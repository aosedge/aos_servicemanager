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
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sync"
	"syscall"
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

const (
	updateDownloadsTime = 10 * time.Second
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Client VIS client object
type Client struct {
	ErrorChannel chan error

	sync.Mutex
	crypt    *fcrypt.CryptoContext
	sender   Sender
	storage  Storage
	wsClient *wsclient.Client

	downloadDir string
	upgradeDir  string

	imageVersion   uint64
	upgradeState   int
	upgradeVersion uint64
	upgradeData    amqp.SystemUpgrade
}

// Sender provides API to send messages to the cloud
type Sender interface {
	SendSystemRevertStatus(revertStatus, revertError string, imageVersion uint64) (err error)
	SendSystemUpgradeStatus(upgradeStatus, upgradeError string, imageVersion uint64) (err error)
}

// Storage provides API to store/retreive persistent data
type Storage interface {
	SetUpgradeState(state int) (err error)
	GetUpgradeState() (state int, err error)
	SetUpgradeData(data amqp.SystemUpgrade) (err error)
	GetUpgradeData() (data amqp.SystemUpgrade, err error)
	SetUpgradeVersion(version uint64) (err error)
	GetUpgradeVersion() (version uint64, err error)
}

// Image provides API to download, decrypt images
type Image interface {
	Download(dstDir, url string) (fileName string, err error)
	Decrypt(srcFileName, dstFileName string, asymAlg, blockAlg string, blockKey, blockIv []byte) (err error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new umclient
func New(config *config.Config, crypt *fcrypt.CryptoContext, sender Sender, storage Storage) (um *Client, err error) {
	um = &Client{
		crypt:       crypt,
		sender:      sender,
		storage:     storage,
		downloadDir: path.Join(config.UpgradeDir, "downloads"),
		upgradeDir:  config.UpgradeDir}

	if um.wsClient, err = wsclient.New("UM", um.messageHandler); err != nil {
		return nil, err
	}

	um.ErrorChannel = um.wsClient.ErrorChannel

	if um.upgradeState, err = um.storage.GetUpgradeState(); err != nil {
		return nil, err
	}

	if um.upgradeVersion, err = um.storage.GetUpgradeVersion(); err != nil {
		return nil, err
	}

	if um.upgradeState == stateDownloading {
		if um.upgradeData, err = um.storage.GetUpgradeData(); err != nil {
			return nil, err
		}

		go um.downloadImage()
	}

	return um, nil
}

// Connect connects to UM server
func (um *Client) Connect(url string) (err error) {
	return um.wsClient.Connect(url)
}

// Disconnect disconnects from UM server
func (um *Client) Disconnect() (err error) {
	return um.wsClient.Disconnect()
}

// IsConnected returns true if connected to UM server
func (um *Client) IsConnected() (result bool) {
	return um.wsClient.IsConnected()
}

// GetSystemVersion return system version
func (um *Client) GetSystemVersion() (version uint64, err error) {
	um.Lock()
	defer um.Unlock()

	var status umprotocol.StatusRsp

	if err = um.sendRequest(umprotocol.StatusRequestType, umprotocol.StatusResponseType, nil, &status); err != nil {
		return 0, err
	}

	if err = um.handleSystemStatus(status); err != nil {
		return 0, err
	}

	return um.imageVersion, nil
}

// SystemUpgrade send system upgrade request to UM
func (um *Client) SystemUpgrade(upgradeData amqp.SystemUpgrade) {
	um.Lock()
	defer um.Unlock()

	log.WithField("version", upgradeData.ImageVersion).Info("System upgrade")

	if um.imageVersion == upgradeData.ImageVersion {
		um.sendUpgradeStatus(umprotocol.SuccessStatus, "")
		return
	}

	/* TODO: Shall image version be without gaps?
	if um.imageVersion+1 != imageVersion {
		um.sendUpgradeStatus(umprotocol.FailedStatus, "wrong image version")
		return
	}
	*/

	if um.imageVersion > upgradeData.ImageVersion {
		um.sendUpgradeStatus(umprotocol.FailedStatus, "wrong image version")
		return
	}

	if um.upgradeState != stateInit && um.upgradeVersion != upgradeData.ImageVersion {
		um.sendUpgradeStatus(umprotocol.FailedStatus, "another upgrade is in progress")
		return
	}

	if um.upgradeState == stateInit {
		um.upgradeVersion = upgradeData.ImageVersion
		um.upgradeData = upgradeData
		um.upgradeState = stateDownloading

		if err := um.clearDirs(); err != nil {
			um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
			return
		}

		if err := um.storage.SetUpgradeVersion(um.upgradeVersion); err != nil {
			um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
			return
		}

		if err := um.storage.SetUpgradeData(um.upgradeData); err != nil {
			um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
			return
		}

		if err := um.storage.SetUpgradeState(um.upgradeState); err != nil {
			um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
			return
		}

		go um.downloadImage()
	}
}

// SystemRevert send system revert request to UM
func (um *Client) SystemRevert(imageVersion uint64) {
	um.Lock()
	defer um.Unlock()

	log.WithField("version", imageVersion).Info("System revert")

	if um.imageVersion == imageVersion {
		um.sendRevertStatus(umprotocol.SuccessStatus, "")
		return
	}

	/* TODO: Shall image version be without gaps?
	if um.imageVersion-1 != imageVersion {
		um.sendRevertStatus(umprotocol.FailedStatus, "wrong image version")
		return
	}
	*/

	if um.imageVersion < imageVersion {
		um.sendRevertStatus(umprotocol.FailedStatus, "wrong image version")
		return
	}

	if um.upgradeState != stateInit && um.upgradeVersion != imageVersion {
		um.sendRevertStatus(umprotocol.FailedStatus, "another upgrade is in progress")
		return
	}

	if um.upgradeState == stateInit {
		um.upgradeVersion = imageVersion
		um.upgradeState = stateReverting

		if err := um.storage.SetUpgradeVersion(um.upgradeVersion); err != nil {
			um.sendRevertStatus(umprotocol.FailedStatus, err.Error())
			return
		}

		if err := um.storage.SetUpgradeState(um.upgradeState); err != nil {
			um.sendRevertStatus(umprotocol.FailedStatus, err.Error())
			return
		}

		if err := um.sendMessage(umprotocol.RevertRequestType, umprotocol.RevertReq{
			ImageVersion: um.upgradeVersion}); err != nil {
			um.sendRevertStatus(umprotocol.FailedStatus, err.Error())
			return
		}
	}
}

// Close closes umclient
func (um *Client) Close() (err error) {
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

	if message.Header.MessageType != umprotocol.StatusResponseType {
		log.Errorf("Wrong message type received: %s", message.Header.MessageType)
		return
	}

	var status umprotocol.StatusRsp

	if err := json.Unmarshal(message.Data, &status); err != nil {
		log.Errorf("Can't parse status: %s", err)
		return
	}

	if err := um.handleSystemStatus(status); err != nil {
		log.Errorf("Can't handle system status: %s", err)
		return
	}
}

func (um *Client) handleSystemStatus(status umprotocol.StatusRsp) (err error) {
	um.imageVersion = status.CurrentVersion

	switch {
	// any update at this moment
	case (um.upgradeState == stateInit || um.upgradeState == stateDownloading) && status.Status != umprotocol.InProgressStatus:

	// upgrade/revert is in progress
	case (um.upgradeState == stateUpgrading || um.upgradeState == stateReverting) && status.Status == umprotocol.InProgressStatus:

	// upgrade/revert complete
	case (um.upgradeState == stateUpgrading || um.upgradeState == stateReverting) && status.Status != umprotocol.InProgressStatus:
		if status.Operation == umprotocol.RevertOperation {
			um.sendRevertStatus(status.Status, status.Error)
		}

		if status.Operation == umprotocol.UpgradeOperation {
			um.sendUpgradeStatus(status.Status, status.Error)
		}

	default:
		log.Error("Unexpected status received")
	}

	return nil
}

func (um *Client) sendUpgradeRequest() (err error) {
	// This function is called under locked context but we need to unlock for downloads
	um.Unlock()
	defer um.Lock()

	if len(um.upgradeData.URLs) == 0 {
		return errors.New("metadata doesn't contain URL for download")
	}

	fileInfo, err := image.CreateFileInfo(path.Join(um.upgradeDir, um.upgradeData.URLs[0]))
	if err != nil {
		return err
	}

	upgradeReq := umprotocol.UpgradeReq{
		ImageVersion: um.upgradeVersion,
		ImageInfo: umprotocol.ImageInfo{
			Path:   um.upgradeData.URLs[0],
			Sha256: fileInfo.Sha256,
			Sha512: fileInfo.Sha512,
			Size:   fileInfo.Size,
		},
	}

	if err = um.sendMessage(umprotocol.UpgradeRequestType, upgradeReq); err != nil {
		return err
	}

	return nil
}

func (um *Client) sendUpgradeStatus(upgradeStatus, upgradeError string) {
	if upgradeStatus == umprotocol.SuccessStatus {
		log.WithFields(log.Fields{"version": um.upgradeVersion}).Info("Upgrade success")
	} else {
		log.WithFields(log.Fields{"version": um.upgradeVersion}).Errorf("Upgrade failed: %s", upgradeError)
	}

	um.upgradeState = stateInit

	if err := um.storage.SetUpgradeState(um.upgradeState); err != nil {
		log.Errorf("Can't set upgrade state: %s", err)
	}

	if err := um.sender.SendSystemUpgradeStatus(upgradeStatus, upgradeError, um.upgradeVersion); err != nil {
		log.Errorf("Can't send system upgrade status: %s", err)
	}
}

func (um *Client) sendRevertStatus(revertStatus, revertError string) {
	if revertStatus == umprotocol.SuccessStatus {
		log.WithFields(log.Fields{"version": um.upgradeVersion}).Info("Revert success")
	} else {
		log.WithFields(log.Fields{"version": um.upgradeVersion}).Errorf("Revert failed: %s", revertError)
	}

	um.upgradeState = stateInit

	if err := um.storage.SetUpgradeState(um.upgradeState); err != nil {
		log.Errorf("Can't set upgrade state: %s", err)
	}

	if err := um.sender.SendSystemRevertStatus(revertStatus, revertError, um.upgradeVersion); err != nil {
		log.Errorf("Can't send system revert status: %s", err)
	}
}

func (um *Client) checkFile(fileName string, data amqp.SystemUpgrade) (err error) {
	// This function is called under locked context but we need to unlock for downloads
	um.Unlock()
	defer um.Lock()

	if err = image.CheckFileInfo(fileName, image.FileInfo{
		Sha256: data.Sha256,
		Sha512: data.Sha512,
		Size:   data.Size}); err != nil {
		return err
	}

	return nil
}

func (um *Client) downloadImage() {
	um.Lock()
	defer um.Unlock()

	image, err := image.New()
	if err != nil {
		um.sendUpgradeStatus(umprotocol.FailedStatus, "can't create image instance")
		return
	}

	if len(um.upgradeData.URLs) == 0 {
		um.sendUpgradeStatus(umprotocol.FailedStatus, "upgrade file list URLs is empty")
		return
	}

	fileDownloaded := false
	fileName := ""

	for _, rawURL := range um.upgradeData.URLs {
		url, err := url.Parse(rawURL)
		if err != nil {
			um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
			return
		}

		// skip already downloaded and decrypted files
		if !url.IsAbs() {
			break
		}

		var stat syscall.Statfs_t

		syscall.Statfs(um.downloadDir, &stat)

		if um.upgradeData.Size > stat.Bavail*uint64(stat.Bsize) {
			um.sendUpgradeStatus(umprotocol.FailedStatus, "not enough space")
			return
		}

		if fileName, err = image.Download(um.downloadDir, rawURL); err != nil {
			log.WithField("url", rawURL).Warningf("Can't download file: %s", err)
			continue
		}

		fileDownloaded = true

		break
	}

	if !fileDownloaded {
		um.sendUpgradeStatus(umprotocol.FailedStatus, "can't download file from any source")
		return
	}

	if err = um.checkFile(fileName, um.upgradeData); err != nil {
		um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
		return
	}

	if um.upgradeData.DecryptionInfo == nil {
		um.sendUpgradeStatus(umprotocol.FailedStatus, "no decryption info provided")
		return
	}

	if err = um.decryptImage(
		fileName, path.Join(um.upgradeDir, filepath.Base(fileName)), um.upgradeData.DecryptionInfo); err != nil {
		um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
		return
	}

	um.upgradeData.URLs = []string{filepath.Base(fileName)}

	if err = um.storage.SetUpgradeData(um.upgradeData); err != nil {
		um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
		return
	}

	if err = um.checkSigns(); err != nil {
		um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
		return
	}

	if err = um.sendUpgradeRequest(); err != nil {
		um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
		return
	}

	um.upgradeState = stateUpgrading

	if err = um.storage.SetUpgradeState(um.upgradeState); err != nil {
		um.sendUpgradeStatus(umprotocol.FailedStatus, err.Error())
		return
	}
}

func (um *Client) clearDirs() (err error) {
	if err = os.RemoveAll(um.upgradeDir); err != nil {
		return err
	}

	if err = os.MkdirAll(um.downloadDir, 0755); err != nil {
		return err
	}

	return nil
}

func (um *Client) decryptImage(srcFileName, dstFileName string, decryptionInfo *amqp.DecryptionInfo) (err error) {
	context, err := um.crypt.ImportSessionKey(fcrypt.CryptoSessionKeyInfo{
		SymmetricAlgName:  decryptionInfo.BlockAlg,
		SessionKey:        decryptionInfo.BlockKey,
		SessionIV:         decryptionInfo.BlockIv,
		AsymmetricAlgName: decryptionInfo.AsymAlg})
	if err != nil {
		return err
	}

	srcFile, err := os.Open(srcFileName)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dstFileName, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	log.WithFields(log.Fields{"srcFile": srcFile.Name(), "dstFile": dstFile.Name()}).Debug("Decrypt image")

	if err = context.DecryptFile(srcFile, dstFile); err != nil {
		return err
	}

	return nil
}

func (um *Client) checkSigns() (err error) {
	context, err := um.crypt.CreateSignContext()
	if err != nil {
		return err
	}

	for _, cert := range um.upgradeData.Certificates {
		if err = context.AddCertificate(cert.Fingerprint, cert.Certificate); err != nil {
			return err
		}
	}

	for _, chain := range um.upgradeData.CertificateChains {
		if err = context.AddCertificateChain(chain.Name, chain.Fingerprints); err != nil {
			return err
		}
	}

	if um.upgradeData.Signs == nil {
		return errors.New("upgradeData does not have signature")
	}

	file, err := os.Open(path.Join(um.upgradeDir, um.upgradeData.URLs[0]))
	if err != nil {
		return err
	}
	defer file.Close()

	log.WithField("file", file.Name()).Debug("Check signature")

	if err = context.VerifySign(file, um.upgradeData.Signs.ChainName, um.upgradeData.Signs.Alg,
		um.upgradeData.Signs.Value); err != nil {
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
