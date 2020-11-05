// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package umclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/image"
	"gitpct.epam.com/epmd-aepr/aos_common/umprotocol"
	"gitpct.epam.com/epmd-aepr/aos_common/wsclient"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/fcrypt"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

// UM client states
const (
	stateInit = iota
	stateDownloading
	stateUpgrading
	stateReverting
)

const updateMaxDuration = 30 * time.Minute

/*******************************************************************************
 * Types
 ******************************************************************************/

// Client VIS client object
type Client struct {
	ErrorChannel chan error

	sync.Mutex
	crypt      *fcrypt.CryptoContext
	sender     Sender
	wsClient   *wsclient.Client
	downloader downloader

	updateDir   string
	updateState int

	currentComponents []amqp.ComponentInfo
	finishChannel     chan bool
	stopChan          chan bool
}

// Sender provides API to send messages to the cloud
type Sender interface {
	SendComponentStatus(components []amqp.ComponentInfo)
	SendIssueUnitCertificatesRequest(requests []amqp.CertificateRequest) (err error)
	SendInstallCertificatesConfirmation(confirmation []amqp.CertificateConfirmation) (err error)
}

type downloader interface {
	DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
		chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new umclient
func New(config *config.Config, sender Sender) (um *Client, err error) {
	um = &Client{
		sender:        sender,
		updateDir:     config.UpdateDir,
		updateState:   stateInit,
		finishChannel: make(chan bool, 1),
		stopChan:      make(chan bool, 1)}

	if err = os.MkdirAll(config.UpdateDir, 0755); err != nil {
		return nil, err
	}

	if um.wsClient, err = wsclient.New("UM", um.messageHandler); err != nil {
		return nil, err
	}

	um.ErrorChannel = um.wsClient.ErrorChannel

	return um, nil
}

//SetDownloader set downloader for umclient
func (um *Client) SetDownloader(downloader downloader) {
	um.downloader = downloader
}

// Connect connects to UM server
func (um *Client) Connect(url string) (err error) {
	if err = um.wsClient.Connect(url); err != nil {
		return err
	}

	if err = um.getSystemComponents(); err != nil {
		return err
	}

	return nil
}

// Disconnect disconnects from UM server
func (um *Client) Disconnect() (err error) {
	return um.wsClient.Disconnect()
}

// IsConnected returns true if connected to UM server
func (um *Client) IsConnected() (result bool) {
	return um.wsClient.IsConnected()
}

// GetSystemComponents returns list of system components information
func (um *Client) GetSystemComponents() (components []amqp.ComponentInfo, err error) {
	um.Lock()
	defer um.Unlock()

	return um.currentComponents, nil
}

// ProcessDesiredComponents process desred component list
func (um *Client) ProcessDesiredComponents(components []amqp.ComponentInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (err error) {
	if um.updateState == stateInit {
		newComponents := []amqp.ComponentInfoFromCloud{}
		for _, desComponent := range components {
			wasFound := false

			for _, curComponent := range um.currentComponents {
				if curComponent.ID == desComponent.ID {
					if curComponent.VendorVersion == desComponent.VendorVersion &&
						curComponent.Status != umprotocol.StatusError {
						wasFound = true
					}

					break
				}
			}

			if wasFound == false {
				newComponents = append(newComponents, desComponent)
			}
		}

		if len(newComponents) == 0 {
			log.Debug("No update for componenets")
			return nil
		}

		//cleanup finish channel
		for len(um.finishChannel) > 0 {
			<-um.finishChannel
		}

		for _, newElement := range newComponents {
			um.currentComponents = append(um.currentComponents, amqp.ComponentInfo{
				ID:            newElement.ID,
				VendorVersion: newElement.VendorVersion,
				Status:        umprotocol.StatusPending,
				AosVersion:    newElement.AosVersion})
		}

		um.sender.SendComponentStatus(um.currentComponents)

		componentsToUpdate := []umprotocol.ComponentInfo{}

		for _, newElement := range newComponents {
			if err := um.updateCurrentComponentStatus(newElement.ID, newElement.VendorVersion,
				umprotocol.StatusDownloading, ""); err != nil {
				return err
			}

			updateComponent, err := um.downloadComponentUpdate(newElement, chains, certs)
			if err != nil {
				um.updateCurrentComponentStatus(newElement.ID, newElement.VendorVersion, umprotocol.StatusError, err.Error())
				return err
			}

			componentsToUpdate = append(componentsToUpdate, updateComponent)

			if err := um.updateCurrentComponentStatus(newElement.ID, newElement.VendorVersion,
				umprotocol.StatusDownloaded, ""); err != nil {
				return err
			}
		}

		if err = um.sendMessage(umprotocol.UpdateRequestType, componentsToUpdate); err != nil {
			return err
		}

		um.updateState = stateUpgrading

		for i, curElement := range um.currentComponents {
			if curElement.Status == umprotocol.StatusDownloaded {
				um.currentComponents[i].Status = umprotocol.StatusInstalling
			}
		}
	}

	// wait for update complete or reboot of update timer expired
	select {
	case <-um.stopChan:
		err = nil

	case <-um.finishChannel:
		log.Debug("Update finished")
		err = nil

	case <-time.After(updateMaxDuration):
		err = errors.New("update timeout")
		um.updateCurrentComponentsWithError(err)
	}

	return err
}

// RenewCertificatesNotification send notification aboute renew certificates
func (um *Client) RenewCertificatesNotification(systemID, pwd string, certInfo []amqp.CertificateNotification) {
	um.Lock()
	defer um.Unlock()

	var newCerts []amqp.CertificateRequest

	for _, cert := range certInfo {
		request := umprotocol.CreateKeysReq{
			Type:     cert.Type,
			SystemID: systemID,
			Password: pwd}

		response := new(umprotocol.CreateKeysRsp)

		if err := um.sendRequest(umprotocol.CreateKeysRequestType, umprotocol.CreateKeysResponseType,
			&request, response); err != nil {
			log.Error("Can't send createKeysRequest to update manager ", err)
			continue
		}

		if response.Error != "" {
			log.Error("Can't create certificate ", response.Error)
			continue
		}

		newCerts = append(newCerts, amqp.CertificateRequest{Type: response.Type, Csr: response.Csr})
	}

	if len(newCerts) == 0 {
		return
	}

	if err := um.sender.SendIssueUnitCertificatesRequest(newCerts); err != nil {
		log.Error("Can't send issueUnitCertificates ", err)
	}
}

// IssuedUnitCertificates send applyCertRequest to update manager
func (um *Client) IssuedUnitCertificates(certInfo []amqp.IssuedUnitCertificatesInfo) {
	um.Lock()
	defer um.Unlock()

	var confirmations []amqp.CertificateConfirmation

	for _, cert := range certInfo {
		request := umprotocol.ApplyCertReq{
			Type: cert.Type,
			Crt:  cert.CertificateChain}

		response := new(umprotocol.ApplyCertRsp)

		if err := um.sendRequest(umprotocol.ApplyCertRequestType, umprotocol.ApplyCertResponseType,
			&request, response); err != nil {
			log.Error("Can't send applyCertRequest to update manager ", err)
			continue
		}

		if response.Error != "" {
			log.Error("Can't apply certificate ", response.Error)
		}

		certConfirmation := amqp.CertificateConfirmation{Type: response.Type,
			Status:      "installed",
			Description: response.Error}

		serial, err := fcrypt.GetCrtSerialByURL(response.CrtURL)
		if err != nil {
			certConfirmation.Description = err.Error()
			log.Error("Can't get cert serial from ", response.CrtURL, err)
		}

		certConfirmation.Serial = serial

		if certConfirmation.Description != "" {
			certConfirmation.Serial = "error"
		}

		confirmations = append(confirmations, certConfirmation)
	}

	if len(confirmations) == 0 {
		return
	}

	if err := um.sender.SendInstallCertificatesConfirmation(confirmations); err != nil {
		log.Error("Can't send installUnitCertificatesConfirmation ", err)
	}
}

//GetCertificateForSM get sertificate
func (um *Client) GetCertificateForSM(request fcrypt.RetrieveCertificateRequest) (resp fcrypt.RetrieveCertificateResponse,
	err error) {
	requestUm := umprotocol.GetCertReq{
		Type:   request.CertType,
		Issuer: request.Issuer,
		Serial: request.Serial,
	}

	responseUm := new(umprotocol.GetCertRsp)

	if err := um.sendRequest(umprotocol.GetCertRequestType, umprotocol.GetCertResponseType,
		&requestUm, responseUm); err != nil {
		log.Error("Can't send getCertRequest to update manager ", err)
		return resp, err
	}

	if responseUm.Error != "" {
		return resp, errors.New(responseUm.Error)
	}

	if requestUm.Type != responseUm.Type {
		return resp, fmt.Errorf("Cert types missmatch %s!=%s", requestUm.Type, responseUm.Type)
	}

	resp.CrtURL = responseUm.CrtURL
	resp.KeyURL = responseUm.KeyURL

	return resp, nil
}

// Close closes umclient
func (um *Client) Close() (err error) {
	um.stopChan <- true
	return um.wsClient.Close()
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (um *Client) messageHandler(dataJSON []byte) {
	um.Lock()
	defer um.Unlock()

	var message umprotocol.Message

	if err := json.Unmarshal(dataJSON, &message); err != nil {
		log.Errorf("Can't parse message: %s", err)
		return
	}

	if message.Header.MessageType != umprotocol.UpdateStatusType {
		log.Errorf("Wrong message type received: %s", message.Header.MessageType)
		return
	}

	var status []umprotocol.ComponentStatus

	if err := json.Unmarshal(message.Data, &status); err != nil {
		log.Errorf("Can't parse status: %s", err)
		return
	}

	if err := um.handleSystemStatus(status); err != nil {
		log.Errorf("Can't handle system status: %s", err)
		return
	}
}

func (um *Client) handleSystemStatus(status []umprotocol.ComponentStatus) (err error) {
	if um.updateState != stateUpgrading {
		log.Warn("Unexpected updateStatus updateState ", um.updateState)
		return nil
	}

	for _, value := range status {
		if value.Status == umprotocol.StatusInstalled {
			toRemove := []int{}

			for i, curStatus := range um.currentComponents {
				if value.ID == curStatus.ID {
					if curStatus.Status != umprotocol.StatusInstalled {
						continue
					}

					if value.VendorVersion != curStatus.VendorVersion {
						toRemove = append(toRemove, i)
						continue
					}
				}
			}

			sort.Ints(toRemove)

			for i, value := range toRemove {
				um.currentComponents = append(um.currentComponents[:value-i], um.currentComponents[value-i+1:]...)
			}
		}

		um.updateCurrentComponentStatus(value.ID, value.VendorVersion, value.Status, value.Error)
	}

	um.sender.SendComponentStatus(um.currentComponents)

	if um.updateFinished() == true {
		um.updateState = stateInit

		um.finishChannel <- true
	}

	return nil
}

func (um *Client) updateFinished() (updateFinished bool) {
	for _, status := range um.currentComponents {
		if (status.Status != umprotocol.StatusInstalled) && (status.Status != umprotocol.StatusError) {
			return false
		}
	}

	if err := um.clearDirs(); err != nil {
		log.Error("Can't clean update dir ", err)
	}

	return true
}

func (um *Client) getSystemComponents() (err error) {
	um.Lock()
	defer um.Unlock()

	componentsFromUm := []umprotocol.ComponentStatus{}

	if err = um.sendRequest(umprotocol.GetComponentsRequestType, umprotocol.GetComponentsResponseType,
		nil, &componentsFromUm); err != nil {
		return err
	}

	um.currentComponents = []amqp.ComponentInfo{}

	for _, value := range componentsFromUm {
		um.currentComponents = append(um.currentComponents, amqp.ComponentInfo{ID: value.ID, VendorVersion: value.VendorVersion,
			AosVersion: value.AosVersion, Error: value.Error, Status: value.Status})
	}

	if um.updateFinished() == true {
		um.updateState = stateInit
	} else {
		um.updateState = stateUpgrading
	}

	return nil
}

func (um *Client) downloadComponentUpdate(componentUpdate amqp.ComponentInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (infoForUpdate umprotocol.ComponentInfo, err error) {
	decryptData := amqp.DecryptDataStruct{URLs: componentUpdate.URLs,
		Sha256:         componentUpdate.Sha256,
		Sha512:         componentUpdate.Sha512,
		Size:           componentUpdate.Size,
		DecryptionInfo: componentUpdate.DecryptionInfo,
		Signs:          componentUpdate.Signs}

	infoForUpdate = umprotocol.ComponentInfo{ID: componentUpdate.ID,
		VendorVersion: componentUpdate.VendorVersion,
		AosVersion:    componentUpdate.AosVersion,
		Annotations:   componentUpdate.Annotations,
	}

	infoForUpdate.Path, err = um.downloader.DownloadAndDecrypt(decryptData, chains, certs, um.updateDir)
	if err != nil {
		return infoForUpdate, err
	}

	fileInfo, err := image.CreateFileInfo(infoForUpdate.Path)
	if err != nil {
		return infoForUpdate, err
	}

	infoForUpdate.Sha256 = fileInfo.Sha256
	infoForUpdate.Sha512 = fileInfo.Sha512
	infoForUpdate.Size = fileInfo.Size

	return infoForUpdate, err
}

func (um *Client) clearDirs() (err error) {
	if err = os.RemoveAll(um.updateDir); err != nil {
		return err
	}

	if err = os.MkdirAll(um.updateDir, 0755); err != nil {
		return err
	}

	return nil
}

func (um *Client) sendMessage(messageType string, data interface{}) (err error) {
	message := umprotocol.Message{
		Header: umprotocol.Header{
			Version:     umprotocol.Version,
			MessageType: messageType,
		},
	}

	if message.Data, err = json.Marshal(data); err != nil {
		return err
	}

	if err = um.wsClient.SendMessage(&message); err != nil {
		return err
	}

	return nil
}

func (um *Client) sendRequest(messageType, expectedMessageType string, request, response interface{}) (err error) {
	message := umprotocol.Message{
		Header: umprotocol.Header{
			Version:     umprotocol.Version,
			MessageType: messageType,
		},
	}

	if message.Data, err = json.Marshal(request); err != nil {
		return err
	}

	if err = um.wsClient.SendRequest("Header.MessageType", expectedMessageType, &message, &message); err != nil {
		return err
	}

	if err = json.Unmarshal(message.Data, response); err != nil {
		return err
	}

	return nil
}

func (um *Client) updateCurrentComponentStatus(ID, vendorVersion, status, errorStr string) (err error) {
	for i, curElement := range um.currentComponents {
		if curElement.ID == ID && curElement.VendorVersion == vendorVersion {
			um.currentComponents[i].Status = status
			um.currentComponents[i].Error = errorStr
			um.sender.SendComponentStatus(um.currentComponents)
			return nil
		}
	}

	return fmt.Errorf("no element with ID =%s vendorVersion =%s", ID, vendorVersion)
}

func (um *Client) updateCurrentComponentsWithError(err error) {
	for i, curElement := range um.currentComponents {
		if curElement.Status != umprotocol.StatusInstalled && curElement.Status != umprotocol.StatusError {
			um.currentComponents[i].Status = umprotocol.StatusError
			um.currentComponents[i].Error = err.Error()
			um.sender.SendComponentStatus(um.currentComponents)
		}
	}
}
