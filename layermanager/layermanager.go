// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2021 Renesas Electronics Corporation.
// Copyright (C) 2021 EPAM Systems, Inc.
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
	"context"
	"encoding/json"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"path/filepath"

	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/servicemanager/v1"
	"gitpct.epam.com/epmd-aepr/aos_common/image"

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
	downloadDirName    = "download"
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
	downloadDir       string
	actionHandler     *action.Handler
}

// LayerInfoProvider provides API to add, remove or access layer information
type LayerInfoProvider interface {
	AddLayer(digest, layerID, path, osVersion, vendorVersion, description string, aosVersion uint64) (err error)
	DeleteLayerByDigest(digest string) (err error)
	GetLayerPathByDigest(digest string) (path string, err error)
	GetLayersInfo() (layersList []*pb.LayerStatus, err error)
	GetLayerInfoByDigest(digest string) (layer pb.LayerStatus, err error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/
// New creates new launcher object
func New(config *config.Config,
	infoProvider LayerInfoProvider) (layermanager *LayerManager, err error) {
	layermanager = &LayerManager{
		layersDir:         config.LayersDir,
		layerInfoProvider: infoProvider,
		extractDir:        path.Join(config.WorkingDir, extractDirName),
		downloadDir:       path.Join(config.WorkingDir, downloadDirName),
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

	os.RemoveAll(layermanager.downloadDir)

	return layermanager, nil
}

// GetLayersInfo provides list of already installed fs layers
func (layermanager *LayerManager) GetLayersInfo() (info []*pb.LayerStatus, err error) {
	if info, err = layermanager.layerInfoProvider.GetLayersInfo(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return info, nil
}

// InstallLayer installs layer
func (layermanager *LayerManager) InstallLayer(installInfo *pb.InstallLayerRequest) (err error) {
	log.WithFields(log.Fields{
		"id":         installInfo.GetLayerId(),
		"aosVersion": installInfo.GetAosVersion(),
		"digest":     installInfo.GetDigest()}).Debug("Install layer")

	defer func() {
		if err != nil {
			log.WithFields(log.Fields{
				"id":         installInfo.LayerId,
				"aosVersion": installInfo.AosVersion,
				"digest":     installInfo.Digest}).Errorf("Can't install layer: %s", err)
		}
	}()

	urlVal, err := url.Parse(installInfo.Url)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	var destinationFile string

	if urlVal.Scheme != "file" {
		if destinationFile, err = image.Download(context.Background(), layermanager.downloadDir, installInfo.Url); err != nil {
			return aoserrors.Wrap(err)
		}

		defer os.RemoveAll(destinationFile)
	} else {
		destinationFile = urlVal.Path
	}

	if err = image.CheckFileInfo(context.Background(), destinationFile, image.FileInfo{Sha256: installInfo.Sha256,
		Sha512: installInfo.Sha512, Size: installInfo.Size}); err != nil {
		return aoserrors.Wrap(err)
	}

	unpackDir := path.Join(layermanager.extractDir, filepath.Base(destinationFile))

	if err = imageutils.UnpackTarImage(destinationFile, unpackDir); err != nil {
		return aoserrors.Wrap(err)
	}
	defer os.RemoveAll(unpackDir)

	var byteValue []byte

	if byteValue, err = ioutil.ReadFile(path.Join(unpackDir, layerOCIDescriptor)); err != nil {
		return aoserrors.Wrap(err)
	}

	var layerDescriptor imagespec.Descriptor

	if err = json.Unmarshal(byteValue, &layerDescriptor); err != nil {
		return aoserrors.Wrap(err)
	}

	var layerPath string

	if layerPath, err = getValidLayerPath(layerDescriptor, unpackDir); err != nil {
		return aoserrors.Wrap(err)
	}

	layerStorageDir := path.Join(layermanager.layersDir, "blobs", (string)(layerDescriptor.Digest.Algorithm()), layerDescriptor.Digest.Hex())

	if err = imageutils.UnpackTarImage(layerPath, layerStorageDir); err != nil {
		return aoserrors.Wrap(err)
	}

	osVersion := ""

	if layerDescriptor.Platform != nil {
		osVersion = layerDescriptor.Platform.OSVersion
	}

	if err = layermanager.layerInfoProvider.AddLayer(installInfo.Digest, installInfo.LayerId,
		layerStorageDir, osVersion, installInfo.VendorVersion, installInfo.Description,
		installInfo.AosVersion); err != nil {
		return aoserrors.Wrap(err)

	}

	log.WithFields(log.Fields{
		"id":         installInfo.LayerId,
		"aosVersion": installInfo.AosVersion,
		"digest":     installInfo.Digest}).Info("Layer successfully installed")

	return nil
}

// UninstallLayer uninstalls layer
func (layermanager *LayerManager) UninstallLayer(digest string) (err error) {
	log.WithFields(log.Fields{"digest": digest}).Debug("Uninstall layer")

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

	log.WithFields(log.Fields{"digest": digest}).Info("Layer successfully uninstalled")

	return nil
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

		fi, err := os.Stat(layerPath)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		if !fi.Mode().IsDir() {
			return aoserrors.New("layer is not a dir")
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
		if curErr := layermanager.UninstallLayer(layerInfo.Digest); curErr != nil {
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

//GetLayerInfoByDigest get layers information by layer digest
func (layermanager *LayerManager) GetLayerInfoByDigest(digest string) (layer pb.LayerStatus, err error) {
	return layermanager.layerInfoProvider.GetLayerInfoByDigest(digest)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func getValidLayerPath(layerDescriptor imagespec.Descriptor, unTarPath string) (layerPath string, err error) {
	// TODO implement descriptor validation
	return path.Join(unTarPath, layerDescriptor.Digest.Hex()), nil
}
