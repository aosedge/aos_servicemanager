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
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"

	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/utils"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/
const (
	extractDirName     = "extract"
	layerOCIDescriptor = "layer.json"
)

const (
	layerStatusError     = "error"
	layerStatusInstalled = "installed"
	layerStatusRemoved   = "removed"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// LayerManager instance
type LayerManager struct {
	layersDir         string
	layerInfoProvider LayerInfoProvider
	extractDir        string
	downloader        downloader
	layersToRemove    []amqp.LayerInfo
	statusSender      LayerStatusSender
	currentLayerList  []amqp.LayerInfo
}

// LayerInfoProvider provides API to add, remove or access layer information
type LayerInfoProvider interface {
	AddLayer(digest, layerID, path, osVersion, vendorVersion, description string, aosVersion uint64) (err error)
	DeleteLayerByDigest(digest string) (err error)
	GetLayerPathByDigest(digest string) (path string, err error)
	GetLayersInfo() (layersList []amqp.LayerInfo, err error)
}

//LayerStatusSender provides API to send messages to the cloud
type LayerStatusSender interface {
	SendLayerStatus(serviceStatus []amqp.LayerInfo)
}

type downloader interface {
	DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
		chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New(layersStorageDir string, downloader downloader, infoProvider LayerInfoProvider,
	sender LayerStatusSender) (layermanager *LayerManager, err error) {
	layermanager = &LayerManager{
		layersDir:         layersStorageDir,
		downloader:        downloader,
		layerInfoProvider: infoProvider,
		extractDir:        path.Join(layersStorageDir, extractDirName),
		statusSender:      sender}

	if err := os.MkdirAll(layermanager.extractDir, 0755); err != nil {
		return nil, err
	}

	layermanager.currentLayerList, err = layermanager.layerInfoProvider.GetLayersInfo()
	if err != nil {
		return nil, err
	}

	return layermanager, nil
}

// ProcessDesiredLayersList add, remove
func (layermanager *LayerManager) ProcessDesiredLayersList(layerList []amqp.LayerInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (err error) {
	layermanager.layersToRemove = make([]amqp.LayerInfo, len(layermanager.currentLayerList))
	copy(layermanager.layersToRemove, layermanager.currentLayerList)

	var resultError error

	for _, desiredLayer := range layerList {
		layerInstalled := false

		for i, currentLayer := range layermanager.layersToRemove {
			if currentLayer.Digest == desiredLayer.Digest {
				layermanager.layersToRemove = append(layermanager.layersToRemove[:i], layermanager.layersToRemove[i+1:]...)

				if currentLayer.Status != layerStatusError {
					layerInstalled = true
				}

				break
			}
		}

		if !layerInstalled {
			layerInfo, err := layermanager.installLayer(desiredLayer, chains, certs)
			layerStatus := amqp.LayerInfo{Digest: layerInfo.Digest, ID: layerInfo.ID,
				Status: layerInfo.Status, AosVersion: desiredLayer.AosVersion}
			if err != nil {
				log.Error("Can't install layer ", err)
				layerStatus.Error = err.Error()
				if resultError == nil {
					resultError = err
				}
			}

			layermanager.updateCurrentLayerList(layerStatus)

			layermanager.statusSender.SendLayerStatus(layermanager.currentLayerList)
		}
	}

	return resultError
}

// DeleteUnneededLayers remove all layer which are not present in desired configuration
func (layermanager *LayerManager) DeleteUnneededLayers() (err error) {
	defer func() { layermanager.layersToRemove = []amqp.LayerInfo{} }()

	for _, layer := range layermanager.layersToRemove {
		layerStatus := amqp.LayerInfo{Digest: layer.Digest, ID: layer.ID, Status: layerStatusRemoved,
			AosVersion: layer.AosVersion}

		layerPath, err := layermanager.layerInfoProvider.GetLayerPathByDigest(layer.Digest)
		if err != nil {
			layerStatus.Status = layerStatusError
			layerStatus.Error = err.Error()
		} else {
			os.RemoveAll(layerPath)
		}

		if err = layermanager.layerInfoProvider.DeleteLayerByDigest(layer.Digest); err != nil {
			layerStatus.Status = layerStatusError
			layerStatus.Error = err.Error()
		}

		layermanager.updateCurrentLayerList(layerStatus)
	}

	if len(layermanager.layersToRemove) > 0 {
		layermanager.statusSender.SendLayerStatus(layermanager.currentLayerList)

		layermanager.currentLayerList, err = layermanager.layerInfoProvider.GetLayersInfo()
	}

	return err
}

// CheckLayersConsistency checks layers data to be consistent
func (layermanager *LayerManager) CheckLayersConsistency() (err error) {
	layers, err := layermanager.layerInfoProvider.GetLayersInfo()
	if err != nil {
		log.Error("Can't get layers info")
		return err
	}

	for _, layer := range layers {
		// Checking if Layer path exists
		layerPath, err := layermanager.layerInfoProvider.GetLayerPathByDigest(layer.Digest)
		if err != nil {
			return err
		}

		if fi, err := os.Stat(layerPath); err != nil || !fi.Mode().IsDir() {
			log.Error("Can't find layer data on storage, or data is corrupted")
			return err
		}
	}

	return nil
}

//Clear all Layers
func (layermanager *LayerManager) Cleanup() (err error) {
	//Look like it works, double check with files
	chains := []amqp.CertificateChain{}
	certs := []amqp.Certificate{}
	layerList := []amqp.LayerInfoFromCloud{}
	if err := layermanager.ProcessDesiredLayersList(layerList, chains, certs); err != nil {
		return err
	}

	if err = layermanager.DeleteUnneededLayers(); err != nil {
		return err
	}

	return nil
}

// GetLayersInfo provided list of already installed fs layers
func (layermanager *LayerManager) GetLayersInfo() (layers []amqp.LayerInfo, err error) {
	return layermanager.currentLayerList, nil
}

// GetLayerPathByDigest provied installed layer path by digest
func (layermanager *LayerManager) GetLayerPathByDigest(layerDigest string) (layerPath string, err error) {

	return layermanager.layerInfoProvider.GetLayerPathByDigest(layerDigest)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (layermanager *LayerManager) installLayer(desiredLayer amqp.LayerInfoFromCloud, chains []amqp.CertificateChain,
	certs []amqp.Certificate) (layerStatus amqp.LayerInfo, err error) {

	decryptData := amqp.DecryptDataStruct{URLs: desiredLayer.URLs,
		Sha256:         desiredLayer.Sha256,
		Sha512:         desiredLayer.Sha512,
		Size:           desiredLayer.Size,
		DecryptionInfo: desiredLayer.DecryptionInfo,
		Signs:          desiredLayer.Signs}

	layerStatus = amqp.LayerInfo{Digest: desiredLayer.Digest, ID: desiredLayer.ID, Status: layerStatusError,
		AosVersion: desiredLayer.AosVersion}

	destinationFile, err := layermanager.downloader.DownloadAndDecrypt(decryptData, chains, certs, "")
	if err != nil {
		return layerStatus, err
	}
	defer os.RemoveAll(destinationFile)

	unpackDir := path.Join(layermanager.extractDir, filepath.Base(destinationFile))
	if err = utils.UnpackTarImage(destinationFile, unpackDir); err != nil {
		err = fmt.Errorf("extract layer package from archive error: %s", err.Error())
		layerStatus.Error = err.Error()
		return layerStatus, err
	}
	defer os.RemoveAll(unpackDir)

	byteValue, err := ioutil.ReadFile(path.Join(unpackDir, layerOCIDescriptor))
	if err != nil {
		err = fmt.Errorf("error read layer descriptor: %s", err.Error())
		layerStatus.Error = err.Error()
		return layerStatus, err
	}

	var layerDescriptor imagespec.Descriptor
	if err = json.Unmarshal(byteValue, &layerDescriptor); err != nil {
		err = fmt.Errorf("error parse json descriptor: %s", err.Error())
		layerStatus.Error = err.Error()
		return layerStatus, err
	}

	layerPath, err := getValidLayerPath(layerDescriptor, unpackDir)
	if err != nil {
		err = fmt.Errorf("layer descriptor in incorrect: %s", err.Error())
		layerStatus.Error = err.Error()
		return layerStatus, err
	}

	layerStorageDir := path.Join(layermanager.layersDir, "blobs", (string)(layerDescriptor.Digest.Algorithm()), layerDescriptor.Digest.Hex())
	if err = utils.UnpackTarImage(layerPath, layerStorageDir); err != nil {
		err = fmt.Errorf("extract layer to storage: %s", err.Error())
		layerStatus.Error = err.Error()
		return layerStatus, err
	}

	osVersion := ""
	if layerDescriptor.Platform != nil {
		osVersion = layerDescriptor.Platform.OSVersion
	}

	if err = layermanager.layerInfoProvider.AddLayer(desiredLayer.Digest, desiredLayer.ID,
		layerStorageDir, osVersion, desiredLayer.VendorVersion, desiredLayer.Description,
		desiredLayer.AosVersion); err != nil {
		err = fmt.Errorf("can't add layer to DB: %s", err.Error())
		layerStatus.Error = err.Error()
		return layerStatus, err
	}

	layerStatus.Status = layerStatusInstalled

	return layerStatus, nil
}

func getValidLayerPath(layerDescriptor imagespec.Descriptor, unTarPath string) (layerPath string, err error) {
	//TODO implement Descriptor validation

	layerPath = path.Join(unTarPath, layerDescriptor.Digest.Hex())
	return layerPath, err
}

func (layermanager *LayerManager) updateCurrentLayerList(layerStatus amqp.LayerInfo) {
	for i, value := range layermanager.currentLayerList {
		if value.Digest == layerStatus.Digest {
			layermanager.currentLayerList[i] = layerStatus
			return
		}
	}

	layermanager.currentLayerList = append(layermanager.currentLayerList, layerStatus)
}
