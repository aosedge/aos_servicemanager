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
	decryptDirName     = "decrypt"
	extractDirName     = "extract"
	downloadDirName    = "download"
	layerOCIDescriptor = "layer.json"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// LayerManager instance
type LayerManager struct {
	layersDir         string
	crypt             utils.FcryptInterface
	layerInfoProvider LayerInfoProvider
	downloadDir       string
	layersToRemove    []amqp.LayerInfo
}

// LayerInfoProvider provides API to add, remove or access layer information
type LayerInfoProvider interface {
	AddLayer(digest, layerID, path, osVersion string) (err error)
	DeleteLayerByDigest(digest string) (err error)
	GetLayerPathByDigest(digest string) (path string, err error)
	GetLayersInfo() (layersList []amqp.LayerInfo, err error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New(layersStorageDir string, fcrypt utils.FcryptInterface, infoProvider LayerInfoProvider) (layermanager *LayerManager, err error) {
	layermanager = &LayerManager{layersDir: layersStorageDir, crypt: fcrypt, layerInfoProvider: infoProvider,
		downloadDir: path.Join(layersStorageDir, downloadDirName)}

	if err := os.MkdirAll(layermanager.downloadDir, 0755); err != nil {
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
			if _, err := layermanager.installLayer(desiredLayer, chains, certs); err != nil {
				log.Error("Can't install layer ", err)

				if resultError == nil {
					resultError = err
				}
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
		layerPath, err := layermanager.layerInfoProvider.GetLayerPathByDigest(layer.Digest)
		if err != nil {
			return err
		}

		os.RemoveAll(layerPath)

		if err = layermanager.layerInfoProvider.DeleteLayerByDigest(layer.Digest); err != nil {
			return err
		}
	}

	return nil
}

// GetLayersInfo provied list of already installed fs layers
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

	fileName, err := utils.DownloadImage(decryptData, layermanager.downloadDir)
	if err != nil {
		err = fmt.Errorf("can't download layer: %s", err.Error())
		layerInfo.Error = err.Error()
		return layerInfo, err
	}
	defer os.RemoveAll(fileName)

	if err = utils.CheckFile(fileName, decryptData); err != nil {
		err = fmt.Errorf("check layer checksums error: %s", err.Error())
		layerInfo.Error = err.Error()
		return layerInfo, err
	}

	decryptDir := path.Join(layermanager.downloadDir, decryptDirName)
	if err := os.MkdirAll(decryptDir, 0755); err != nil {
		return layerInfo, err
	}

	destinationFile := path.Join(decryptDir, filepath.Base(fileName))
	if err = utils.DecryptImage(fileName, destinationFile, layermanager.crypt, decryptData.DecryptionInfo); err != nil {
		err = fmt.Errorf("decrypt layer error: %s", err.Error())
		layerInfo.Error = err.Error()
		return layerInfo, err
	}
	defer os.RemoveAll(destinationFile)

	if err = utils.CheckSigns(destinationFile, layermanager.crypt, decryptData.Signs, chains, certs); err != nil {
		err = fmt.Errorf("check layer signature error: %s", err.Error())
		layerInfo.Error = err.Error()
		return layerInfo, err
	}

	unpackDir := path.Join(layermanager.downloadDir, extractDirName, filepath.Base(fileName))
	if err = utils.UnpackTarGzImage(destinationFile, unpackDir); err != nil {
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
