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

// Package layermanager provides set of API to controls service fs layers
package layermanager

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"

	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/utils/action"
	"aos_servicemanager/utils/imageutils"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/
const (
	layerDirName       = "layers"
	extractDirName     = "extract"
	decryptDirName     = "decrypt"
	layerOCIDescriptor = "layer.json"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// LayerManager instance
type LayerManager struct {
	layersDir         string
	layerInfoProvider LayerInfoProvider
	extractDir        string
	decryptDir        string
	downloader        downloader
	actionHandler     *action.Handler
}

// LayerInfoProvider provides API to add, remove or access layer information
type LayerInfoProvider interface {
	AddLayer(digest, layerID, path, osVersion, vendorVersion, description string, aosVersion uint64) (err error)
	DeleteLayerByDigest(digest string) (err error)
	GetLayerPathByDigest(digest string) (path string, err error)
	GetLayersInfo() (layersList []amqp.LayerInfo, err error)
}

type downloader interface {
	DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
		chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error)
}

type installLayerInfo struct {
	layerInfo    amqp.LayerInfoFromCloud
	chains       []amqp.CertificateChain
	certs        []amqp.Certificate
	statusSender statusSender
}

type statusSender chan amqp.LayerInfo

/*******************************************************************************
 * Public
 ******************************************************************************/
// New creates new launcher object
func New(config *config.Config, downloader downloader,
	infoProvider LayerInfoProvider) (layermanager *LayerManager, err error) {
	layermanager = &LayerManager{
		layersDir:         config.LayersDir,
		downloader:        downloader,
		layerInfoProvider: infoProvider,
		extractDir:        path.Join(config.WorkingDir, extractDirName),
		decryptDir:        path.Join(config.WorkingDir, decryptDirName),
	}

	if layermanager.layersDir == "" {
		layermanager.layersDir = path.Join(config.WorkingDir, layerDirName)
	}

	if err := os.MkdirAll(layermanager.extractDir, 0755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if layermanager.actionHandler, err = action.New(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	os.RemoveAll(layermanager.decryptDir)

	return layermanager, nil
}

// GetLayersInfo provides list of already installed fs layers
func (layermanager *LayerManager) GetLayersInfo() (info []amqp.LayerInfo, err error) {
	if info, err = layermanager.layerInfoProvider.GetLayersInfo(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return info, nil
}

// InstallLayer installs layer
func (layermanager *LayerManager) InstallLayer(layerInfo amqp.LayerInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (statusChannel <-chan amqp.LayerInfo) {
	statusSender := make(statusSender, 1)

	info := installLayerInfo{
		layerInfo:    layerInfo,
		chains:       chains,
		certs:        certs,
		statusSender: statusSender,
	}

	info.statusSender.sendStatus(info.layerInfo.ID, info.layerInfo.AosVersion,
		info.layerInfo.Digest, amqp.PendingStatus, "")

	layermanager.actionHandler.PutInQueue(layerInfo.Digest, info, layermanager.doActionInstall)

	return statusSender
}

// UninstallLayer uninstalls layer
func (layermanager *LayerManager) UninstallLayer(digest string) (statusChannel <-chan amqp.LayerInfo) {
	statusSender := make(statusSender, 1)

	layermanager.actionHandler.PutInQueue(digest, statusSender, layermanager.doActionUninstall)

	return statusSender
}

// CheckLayersConsistency checks layers data to be consistent
func (layermanager *LayerManager) CheckLayersConsistency() (err error) {
	layers, err := layermanager.layerInfoProvider.GetLayersInfo()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, layer := range layers {
		// Checking if Layer path exists
		layerPath, err := layermanager.layerInfoProvider.GetLayerPathByDigest(layer.Digest)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		if fi, err := os.Stat(layerPath); err != nil || !fi.Mode().IsDir() {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

// Cleanup clears all Layers
func (layermanager *LayerManager) Cleanup() (err error) {
	layersInfo, err := layermanager.layerInfoProvider.GetLayersInfo()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, layerInfo := range layersInfo {
		if curErr := layermanager.uninstallLayer(layerInfo.Digest); curErr != nil {
			if err == nil {
				err = aoserrors.Wrap(curErr)
			}
		}
	}

	return err
}

// GetLayerPathByDigest provied installed layer path by digest
func (layermanager *LayerManager) GetLayerPathByDigest(layerDigest string) (layerPath string, err error) {
	layerPath, err = layermanager.layerInfoProvider.GetLayerPathByDigest(layerDigest)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	return layerPath, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (sender *statusSender) sendStatus(id string, aosVersion uint64, digest, status, errStr string) {
	*sender <- amqp.LayerInfo{
		ID:         id,
		AosVersion: aosVersion,
		Digest:     digest,
		Status:     status,
		Error:      errStr,
	}
}

func (layermanager *LayerManager) doActionInstall(id string, data interface{}) {
	var err error

	installInfo := data.(installLayerInfo)

	log.WithFields(log.Fields{
		"id":         installInfo.layerInfo.ID,
		"aosVersion": installInfo.layerInfo.AosVersion,
		"digest":     installInfo.layerInfo.Digest}).Debug("Install layer")

	defer func() {
		if err != nil {
			log.WithFields(log.Fields{
				"id":         installInfo.layerInfo.ID,
				"aosVersion": installInfo.layerInfo.AosVersion,
				"digest":     installInfo.layerInfo.Digest}).Errorf("Can't install layer: %s", err)

			installInfo.statusSender.sendStatus(installInfo.layerInfo.ID, installInfo.layerInfo.AosVersion,
				installInfo.layerInfo.Digest, amqp.ErrorStatus, err.Error())
		}

		close(installInfo.statusSender)
	}()

	installInfo.statusSender.sendStatus(installInfo.layerInfo.ID, installInfo.layerInfo.AosVersion,
		installInfo.layerInfo.Digest, amqp.DownloadingStatus, "")

	decryptData := amqp.DecryptDataStruct{URLs: installInfo.layerInfo.URLs,
		Sha256:         installInfo.layerInfo.Sha256,
		Sha512:         installInfo.layerInfo.Sha512,
		Size:           installInfo.layerInfo.Size,
		DecryptionInfo: installInfo.layerInfo.DecryptionInfo,
		Signs:          installInfo.layerInfo.Signs}

	var destinationFile string

	if destinationFile, err = layermanager.downloader.DownloadAndDecrypt(
		decryptData, installInfo.chains, installInfo.certs, layermanager.decryptDir); err != nil {
		err = aoserrors.Wrap(err)
		return
	}
	defer os.RemoveAll(destinationFile)

	installInfo.statusSender.sendStatus(installInfo.layerInfo.ID, installInfo.layerInfo.AosVersion,
		installInfo.layerInfo.Digest, amqp.InstallingStatus, "")

	unpackDir := path.Join(layermanager.extractDir, filepath.Base(destinationFile))

	if err = imageutils.UnpackTarImage(destinationFile, unpackDir); err != nil {
		err = aoserrors.Wrap(err)
		return
	}
	defer os.RemoveAll(unpackDir)

	var byteValue []byte

	if byteValue, err = ioutil.ReadFile(path.Join(unpackDir, layerOCIDescriptor)); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	var layerDescriptor imagespec.Descriptor

	if err = json.Unmarshal(byteValue, &layerDescriptor); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	var layerPath string

	if layerPath, err = getValidLayerPath(layerDescriptor, unpackDir); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	layerStorageDir := path.Join(layermanager.layersDir, "blobs", (string)(layerDescriptor.Digest.Algorithm()), layerDescriptor.Digest.Hex())

	if err = imageutils.UnpackTarImage(layerPath, layerStorageDir); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	osVersion := ""

	if layerDescriptor.Platform != nil {
		osVersion = layerDescriptor.Platform.OSVersion
	}

	if err = layermanager.layerInfoProvider.AddLayer(installInfo.layerInfo.Digest, installInfo.layerInfo.ID,
		layerStorageDir, osVersion, installInfo.layerInfo.VendorVersion, installInfo.layerInfo.Description,
		installInfo.layerInfo.AosVersion); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	installInfo.statusSender.sendStatus(installInfo.layerInfo.ID, installInfo.layerInfo.AosVersion,
		installInfo.layerInfo.Digest, amqp.InstalledStatus, "")

	log.WithFields(log.Fields{
		"id":         installInfo.layerInfo.ID,
		"aosVersion": installInfo.layerInfo.AosVersion,
		"digest":     installInfo.layerInfo.Digest}).Info("Layer successfully installed")
}

func (layermanager *LayerManager) doActionUninstall(id string, data interface{}) {
	var (
		err       error
		layerInfo amqp.LayerInfo
	)

	statusSender := data.(statusSender)

	log.WithFields(log.Fields{"digest": id}).Debug("Uninstall layer")

	defer func() {
		if err != nil {
			log.WithFields(log.Fields{"digest": id}).Errorf("Can't uninstall layer: %s", err)

			statusSender.sendStatus(layerInfo.ID, layerInfo.AosVersion, id, amqp.ErrorStatus, err.Error())
		}

		close(statusSender)
	}()

	var layersInfo []amqp.LayerInfo

	if layersInfo, err = layermanager.layerInfoProvider.GetLayersInfo(); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	found := false

	for _, info := range layersInfo {
		if info.Digest == id {
			layerInfo = info
			found = true

			break
		}
	}

	if !found {
		err = aoserrors.New("layer not found")
		return
	}

	statusSender.sendStatus(layerInfo.ID, layerInfo.AosVersion, layerInfo.Digest, amqp.RemovingStatus, "")

	if err = layermanager.uninstallLayer(id); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	statusSender.sendStatus(layerInfo.ID, layerInfo.AosVersion, layerInfo.Digest, amqp.RemovedStatus, "")

	log.WithFields(log.Fields{"digest": id}).Info("Layer successfully uninstalled")

}

func (layermanager *LayerManager) uninstallLayer(digest string) (err error) {
	layerPath, err := layermanager.layerInfoProvider.GetLayerPathByDigest(digest)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = os.RemoveAll(layerPath); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = layermanager.layerInfoProvider.DeleteLayerByDigest(digest); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func getValidLayerPath(layerDescriptor imagespec.Descriptor, unTarPath string) (layerPath string, err error) {
	// TODO implement descriptor validation
	return path.Join(unTarPath, layerDescriptor.Digest.Hex()), nil
}
