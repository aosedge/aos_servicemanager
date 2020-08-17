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
}

// LayerInfoProvider provides API to add, remove or access layer information
type LayerInfoProvider interface {
	AddLayer(digest, layerID, path, osVersion string) (err error)
	DeleteLayerByDigest(digest string) (err error)
	GetLayerPathByDigest(digest string) (path string, err error)
	GetLayersInfo() (layersList []amqp.LayerInfo, err error)
}

//LayerStatusSender provides API to send messages to the cloud
type LayerStatusSender interface {
	SendLayerStatus(serviceStatus amqp.LayerInfo) (err error)
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

	return layermanager, nil
}

// ProcessDesiredLayersList add, remove
func (layermanager *LayerManager) ProcessDesiredLayersList(layerList []amqp.LayerInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (err error) {
	layermanager.layersToRemove = nil
	currentList, err := layermanager.layerInfoProvider.GetLayersInfo()
	if err != nil {
		return err
	}

	var resultError error

	for _, desiredLayer := range layerList {
		layerInstalled := false

		for i, currentLayer := range currentList {
			if currentLayer.Digest == desiredLayer.Digest {
				currentList = append(currentList[:i], currentList[i+1:]...)
				layerInstalled = true
				break
			}
		}

		if !layerInstalled {
			layerInfo, err := layermanager.installLayer(desiredLayer, chains, certs)
			layerStatus := amqp.LayerInfo{Digest: layerInfo.Digest, LayerID: layerInfo.LayerID, Status: layerInfo.Status}
			if err != nil {
				log.Error("Can't install layer ", err)
				layerStatus.Error = err.Error()
				if resultError == nil {
					resultError = err
				}
			}

			if err := layermanager.statusSender.SendLayerStatus(layerStatus); err != nil {
				log.Error("Can't send Layer status")
			}
		}
	}

	//Layers which are nit present in desired configuration will be removed after processing services
	layermanager.layersToRemove = currentList

	return resultError
}

// DeleteUnneededLayers remove all layer which are not present in desired configuration
func (layermanager *LayerManager) DeleteUnneededLayers() (err error) {
	defer func() { layermanager.layersToRemove = nil }()

	for _, layer := range layermanager.layersToRemove {
		layerStatus := amqp.LayerInfo{Digest: layer.Digest, LayerID: layer.LayerID, Status: "removed"}

		layerPath, err := layermanager.layerInfoProvider.GetLayerPathByDigest(layer.Digest)
		if err != nil {
			layerStatus.Status = "error"
			layerStatus.Error = err.Error()
		} else {
			os.RemoveAll(layerPath)
		}

		if err = layermanager.layerInfoProvider.DeleteLayerByDigest(layer.Digest); err != nil {
			layerStatus.Status = "error"
			layerStatus.Error = err.Error()
		}

		if err = layermanager.statusSender.SendLayerStatus(layerStatus); err != nil {
			log.Error("Can't send Layer status")
		}
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
	return layermanager.layerInfoProvider.GetLayersInfo()
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

	layerInfo := amqp.LayerInfo{Digest: desiredLayer.Digest, LayerID: desiredLayer.LayerID, Status: "error"}

	destinationFile, err := layermanager.downloader.DownloadAndDecrypt(decryptData, chains, certs, "")
	if err != nil {
		return layerInfo, err
	}
	defer os.RemoveAll(destinationFile)

	unpackDir := path.Join(layermanager.extractDir, filepath.Base(destinationFile))
	if err = utils.UnpackTarImage(destinationFile, unpackDir); err != nil {
		err = fmt.Errorf("extract layer package from archive error: %s", err.Error())
		layerInfo.Error = err.Error()
		return layerInfo, err
	}
	defer os.RemoveAll(unpackDir)

	byteValue, err := ioutil.ReadFile(path.Join(unpackDir, layerOCIDescriptor))
	if err != nil {
		err = fmt.Errorf("error read layer descriptor: %s", err.Error())
		layerInfo.Error = err.Error()
		return layerInfo, err
	}

	var layerDescriptor imagespec.Descriptor
	if err = json.Unmarshal(byteValue, &layerDescriptor); err != nil {
		err = fmt.Errorf("error parse json descriptor: %s", err.Error())
		layerInfo.Error = err.Error()
		return layerInfo, err
	}

	layerPath, err := getValidLayerPath(layerDescriptor, unpackDir)
	if err != nil {
		err = fmt.Errorf("layer descriptor in incorrect: %s", err.Error())
		layerInfo.Error = err.Error()
		return layerInfo, err
	}

	layerStorageDir := path.Join(layermanager.layersDir, "blobs", (string)(layerDescriptor.Digest.Algorithm()), layerDescriptor.Digest.Hex())
	if err = utils.UnpackTarImage(layerPath, layerStorageDir); err != nil {
		err = fmt.Errorf("extract layer to storage: %s", err.Error())
		layerInfo.Error = err.Error()
		return layerInfo, err
	}

	osVersion := ""
	if layerDescriptor.Platform != nil {
		osVersion = layerDescriptor.Platform.OSVersion
	}

	if err = layermanager.layerInfoProvider.AddLayer(desiredLayer.Digest, desiredLayer.LayerID,
		layerStorageDir, osVersion); err != nil {
		err = fmt.Errorf("can't add layer to DB: %s", err.Error())
		layerInfo.Error = err.Error()
		return layerInfo, err
	}

	layerInfo.Status = "installed"

	return layerStatus, nil
}

func getValidLayerPath(layerDescriptor imagespec.Descriptor, unTarPath string) (layerPath string, err error) {
	//TODO implement Descriptor validation

	layerPath = path.Join(unTarPath, layerDescriptor.Digest.Hex())
	return layerPath, err
}
