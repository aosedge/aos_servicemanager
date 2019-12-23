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
	"gitpct.epam.com/epmd-aepr/aos_updatemanager/umserver"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/fcrypt"
	"aos_servicemanager/image"
	"aos_servicemanager/wsclient"
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

	imageVersion    uint64
	upgradeState    int
	upgradeVersion  uint64
	upgradeMetadata amqp.UpgradeMetadata
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
	SetUpgradeMetadata(metadata amqp.UpgradeMetadata) (err error)
	GetUpgradeMetadata() (metadata amqp.UpgradeMetadata, err error)
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
		if um.upgradeMetadata, err = um.storage.GetUpgradeMetadata(); err != nil {
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

	var status umserver.StatusMessage

	if err = um.wsClient.SendRequest("Type", &umserver.GetStatusReq{
		MessageHeader: umserver.MessageHeader{Type: umserver.StatusType}}, &status); err != nil {
		return 0, err
	}

	if err = um.handleSystemStatus(status); err != nil {
		return 0, err
	}

	um.imageVersion = status.ImageVersion

	return um.imageVersion, nil
}

// SystemUpgrade send system upgrade request to UM
func (um *Client) SystemUpgrade(imageVersion uint64, metadata amqp.UpgradeMetadata) {
	um.Lock()
	defer um.Unlock()

	log.WithField("version", imageVersion).Info("System upgrade")

	/* TODO: Shall image version be without gaps?
	if um.imageVersion+1 != imageVersion {
		um.sendUpgradeStatus(umserver.FailedStatus, "wrong image version")
		return
	}
	*/

	if um.upgradeState != stateInit && um.upgradeVersion != imageVersion {
		um.sendUpgradeStatus(umserver.FailedStatus, "another upgrade is in progress")
		return
	}

	if um.upgradeState == stateInit {
		um.upgradeVersion = imageVersion
		um.upgradeMetadata = metadata
		um.upgradeState = stateDownloading

		if err := um.clearDirs(); err != nil {
			um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
			return
		}

		if err := um.storage.SetUpgradeVersion(um.upgradeVersion); err != nil {
			um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
			return
		}

		if err := um.storage.SetUpgradeMetadata(um.upgradeMetadata); err != nil {
			um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
			return
		}

		if err := um.storage.SetUpgradeState(um.upgradeState); err != nil {
			um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
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

	/* TODO: Shall image version be without gaps?
	if um.imageVersion-1 != imageVersion {
		um.sendRevertStatus(umserver.FailedStatus, "wrong image version")
		return
	}
	*/

	if um.upgradeState != stateInit && um.upgradeVersion != imageVersion {
		um.sendRevertStatus(umserver.FailedStatus, "another upgrade is in progress")
		return
	}

	if um.upgradeState == stateInit {
		um.upgradeVersion = imageVersion
		um.upgradeState = stateReverting

		if err := um.storage.SetUpgradeVersion(um.upgradeVersion); err != nil {
			um.sendRevertStatus(umserver.FailedStatus, err.Error())
			return
		}

		if err := um.storage.SetUpgradeState(um.upgradeState); err != nil {
			um.sendRevertStatus(umserver.FailedStatus, err.Error())
			return
		}

		if err := um.wsClient.SendMessage(umserver.RevertReq{
			MessageHeader: umserver.MessageHeader{Type: umserver.RevertType},
			ImageVersion:  um.upgradeVersion}); err != nil {
			um.sendRevertStatus(umserver.FailedStatus, err.Error())
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

func (um *Client) messageHandler(message []byte) {
	um.Lock()
	defer um.Unlock()

	var status umserver.StatusMessage

	if err := json.Unmarshal(message, &status); err != nil {
		log.Errorf("Can't parse message: %s", err)
		return
	}

	if status.Type != umserver.StatusType {
		log.Errorf("Wrong message type received: %s", status.Type)
		return
	}

	if err := um.handleSystemStatus(status); err != nil {
		log.Errorf("Can't handle system status: %s", err)
		return
	}
}

func (um *Client) handleSystemStatus(status umserver.StatusMessage) (err error) {
	switch {
	// any update at this moment
	case (um.upgradeState == stateInit || um.upgradeState == stateDownloading) && status.Status != umserver.InProgressStatus:

	// upgrade/revert is in progress
	case (um.upgradeState == stateUpgrading || um.upgradeState == stateReverting) && status.Status == umserver.InProgressStatus:

	// upgrade/revert complete
	case (um.upgradeState == stateUpgrading || um.upgradeState == stateReverting) && status.Status != umserver.InProgressStatus:
		if status.Operation == umserver.RevertType {
			um.sendRevertStatus(status.Status, status.Error)
		}

		if status.Operation == umserver.UpgradeType {
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

	upgradeReq := umserver.UpgradeReq{
		MessageHeader: umserver.MessageHeader{Type: umserver.UpgradeType},
		ImageVersion:  um.upgradeVersion,
		Files:         make([]umserver.UpgradeFileInfo, 0, len(um.upgradeMetadata.Data))}

	for _, data := range um.upgradeMetadata.Data {
		if len(data.URLs) == 0 {
			return errors.New("no file URLs")
		}

		fileInfo, err := image.CreateFileInfo(path.Join(um.upgradeDir, data.URLs[0]))
		if err != nil {
			return err
		}

		upgradeReq.Files = append(upgradeReq.Files, umserver.UpgradeFileInfo{
			Target: data.Target,
			URL:    data.URLs[0],
			Sha256: fileInfo.Sha256,
			Sha512: fileInfo.Sha512,
			Size:   fileInfo.Size})
	}

	if err = um.wsClient.SendMessage(upgradeReq); err != nil {
		return err
	}

	return nil
}

func (um *Client) sendUpgradeStatus(upgradeStatus, upgradeError string) {
	if upgradeStatus == umserver.SuccessStatus {
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
	if revertStatus == umserver.SuccessStatus {
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

func (um *Client) checkFile(fileName string, fileInfo amqp.UpgradeFileInfo) (err error) {
	// This function is called under locked context but we need to unlock for downloads
	um.Unlock()
	defer um.Lock()

	if err = image.CheckFileInfo(fileName, image.FileInfo{
		Sha256: fileInfo.Sha256,
		Sha512: fileInfo.Sha512,
		Size:   fileInfo.Size}); err != nil {
		return err
	}

	return nil
}

func (um *Client) downloadImage() {
	um.Lock()
	defer um.Unlock()

	image, err := image.New()
	if err != nil {
		um.sendUpgradeStatus(umserver.FailedStatus, "can't create image instance")
		return
	}

	if len(um.upgradeMetadata.Data) == 0 {
		um.sendUpgradeStatus(umserver.FailedStatus, "upgrade file list is empty")
		return
	}

	for i, data := range um.upgradeMetadata.Data {

		fileDownloaded := false
		fileName := ""

		for _, rawURL := range data.URLs {
			url, err := url.Parse(rawURL)
			if err != nil {
				um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
				return
			}

			// skip already downloaded and decrypted files
			if !url.IsAbs() {
				break
			}

			var stat syscall.Statfs_t

			syscall.Statfs(um.downloadDir, &stat)

			if data.Size > stat.Bavail*uint64(stat.Bsize) {
				um.sendUpgradeStatus(umserver.FailedStatus, "not enough space")
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
			um.sendUpgradeStatus(umserver.FailedStatus, "can't download file from any source")
			return
		}

		if err = um.checkFile(fileName, data); err != nil {
			um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
			return
		}

		if data.DecryptionInfo != nil {
			if err = um.decryptImage(
				fileName, path.Join(um.upgradeDir, filepath.Base(fileName)), data.DecryptionInfo); err != nil {
				um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
				return
			}
		} else {
			if err = os.Rename(fileName, path.Join(um.upgradeDir, filepath.Base(fileName))); err != nil {
				um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
				return
			}
		}

		um.upgradeMetadata.Data[i].URLs = []string{filepath.Base(fileName)}

		if err = um.storage.SetUpgradeMetadata(um.upgradeMetadata); err != nil {
			um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
			return
		}
	}

	if err = um.checkSigns(); err != nil {
		um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
		return
	}

	if err = um.sendUpgradeRequest(); err != nil {
		um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
		return
	}

	um.upgradeState = stateUpgrading

	if err = um.storage.SetUpgradeState(um.upgradeState); err != nil {
		um.sendUpgradeStatus(umserver.FailedStatus, err.Error())
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

func (um *Client) decryptImage(srcFileName, dstFileName string, decryptionInfo *amqp.UpgradeDecryptionInfo) (err error) {
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

	for _, cert := range um.upgradeMetadata.Certificates {
		if err = context.AddCertificate(cert.Fingerprint, cert.Certificate); err != nil {
			return err
		}
	}

	for _, chain := range um.upgradeMetadata.CertificateChains {
		if err = context.AddCertificateChain(chain.Name, chain.Fingerprints); err != nil {
			return err
		}
	}

	for _, data := range um.upgradeMetadata.Data {
		if data.Signs == nil {
			continue
		}

		file, err := os.Open(path.Join(um.upgradeDir, data.URLs[0]))
		if err != nil {
			return err
		}
		defer file.Close()

		log.WithField("file", file.Name()).Debug("Check signature")

		if err = context.VerifySign(file, data.Signs.ChainName, data.Signs.Alg, data.Signs.Value); err != nil {
			return err
		}
	}

	return nil
}
