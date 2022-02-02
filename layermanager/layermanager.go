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
	"encoding/json"
	"io/ioutil"
	"os"
	"path"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/image"
	"github.com/aoscloud/aos_common/utils/action"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/utils/imageutils"
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

const maxConcurrentActions = 10

/*******************************************************************************
 * Types
 ******************************************************************************/

// LayerManager instance.
type LayerManager struct {
	layersDir         string
	layerInfoProvider LayerInfoProvider
	extractDir        string
	downloadDir       string
	actionHandler     *action.Handler
}

// LayerInfoProvider provides API to add, remove or access layer information.
type LayerInfoProvider interface {
	AddLayer(layerInfo LayerInfo) (err error)
	DeleteLayerByDigest(digest string) (err error)
	GetLayerPathByDigest(digest string) (path string, err error)
	GetLayersInfo() (layersList []LayerInfo, err error)
	GetLayerInfoByDigest(digest string) (layer LayerInfo, err error)
}

// LayerInfo layer information.
type LayerInfo struct {
	Digest        string
	LayerID       string
	Path          string
	OSVersion     string
	AosVersion    uint64
	VendorVersion string
	Description   string
}

/*******************************************************************************
 * Public
 ******************************************************************************/
// New creates new launcher object.
func New(config *config.Config,
	infoProvider LayerInfoProvider) (layermanager *LayerManager, err error) {
	layermanager = &LayerManager{
		layersDir:         config.LayersDir,
		layerInfoProvider: infoProvider,
		extractDir:        path.Join(config.WorkingDir, extractDirName),
		downloadDir:       path.Join(config.WorkingDir, downloadDirName),
		actionHandler:     action.New(maxConcurrentActions),
	}

	if layermanager.layersDir == "" {
		layermanager.layersDir = path.Join(config.WorkingDir, layerDirName)
	}

	if err := os.MkdirAll(layermanager.extractDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	os.RemoveAll(layermanager.downloadDir)

	return layermanager, nil
}

// GetLayersInfo provides list of already installed fs layers.
func (layermanager *LayerManager) GetLayersInfo() (info []LayerInfo, err error) {
	if info, err = layermanager.layerInfoProvider.GetLayersInfo(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return info, nil
}

// InstallLayer installs layer.
func (layermanager *LayerManager) InstallLayer(
	installInfo LayerInfo, layerURL string, fileInfo image.FileInfo) (err error) {
	return <-layermanager.actionHandler.Execute(installInfo.Digest,
		func(id string) error {
			return layermanager.doInstallLayer(installInfo, layerURL, fileInfo)
		})
}

// UninstallLayer uninstalls layer.
func (layermanager *LayerManager) UninstallLayer(digest string) (err error) {
	return <-layermanager.actionHandler.Execute(digest,
		func(id string) error {
			return layermanager.doUninstallLayer(digest)
		})
}

// CheckLayersConsistency checks layers data to be consistent.
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

// Cleanup clears all Layers.
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

	return aoserrors.Wrap(err)
}

// GetLayerPathByDigest provies installed layer path by digest.
func (layermanager *LayerManager) GetLayerPathByDigest(layerDigest string) (layerPath string, err error) {
	layerPath, err = layermanager.layerInfoProvider.GetLayerPathByDigest(layerDigest)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	return layerPath, nil
}

// GetLayerInfoByDigest gets layers information by layer digest.
func (layermanager *LayerManager) GetLayerInfoByDigest(digest string) (layer LayerInfo, err error) {
	if layer, err = layermanager.layerInfoProvider.GetLayerInfoByDigest(digest); err != nil {
		return layer, aoserrors.Wrap(err)
	}

	return layer, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (layermanager *LayerManager) doInstallLayer(
	installInfo LayerInfo, layerURL string, fileInfo image.FileInfo) (err error) {
	log.WithFields(log.Fields{
		"id":         installInfo.LayerID,
		"aosVersion": installInfo.AosVersion,
		"digest":     installInfo.Digest,
	}).Debug("Install layer")

	defer func() {
		if err != nil {
			log.WithFields(log.Fields{
				"id":         installInfo.LayerID,
				"aosVersion": installInfo.AosVersion,
				"digest":     installInfo.Digest,
			}).Errorf("Can't install layer: %s", err)
		}
	}()

	if _, errNoLayer := layermanager.layerInfoProvider.GetLayerInfoByDigest(installInfo.Digest); errNoLayer == nil {
		// layer already installed
		return nil
	}

	extractLayerDir := path.Join(layermanager.extractDir, installInfo.Digest)

	if err := os.MkdirAll(extractLayerDir, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	defer os.RemoveAll(extractLayerDir)

	if err := imageutils.ExtractPackageByURL(extractLayerDir, layermanager.downloadDir, layerURL, fileInfo); err != nil {
		return aoserrors.Wrap(err)
	}

	var byteValue []byte

	if byteValue, err = ioutil.ReadFile(path.Join(extractLayerDir, layerOCIDescriptor)); err != nil {
		return aoserrors.Wrap(err)
	}

	var layerDescriptor imagespec.Descriptor

	if err = json.Unmarshal(byteValue, &layerDescriptor); err != nil {
		return aoserrors.Wrap(err)
	}

	var layerPath string

	if layerPath, err = getValidLayerPath(layerDescriptor, extractLayerDir); err != nil {
		return aoserrors.Wrap(err)
	}

	layerStorageDir := path.Join(layermanager.layersDir, "blobs",
		(string)(layerDescriptor.Digest.Algorithm()), layerDescriptor.Digest.Hex())

	if err = imageutils.UnpackTarImage(layerPath, layerStorageDir); err != nil {
		return aoserrors.Wrap(err)
	}

	if layerDescriptor.Platform != nil {
		installInfo.OSVersion = layerDescriptor.Platform.OSVersion
	}

	installInfo.Path = layerStorageDir

	if err = layermanager.layerInfoProvider.AddLayer(installInfo); err != nil {
		return aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{
		"id":         installInfo.LayerID,
		"aosVersion": installInfo.AosVersion,
		"digest":     installInfo.Digest,
	}).Info("Layer successfully installed")

	return nil
}

func (layermanager *LayerManager) doUninstallLayer(digest string) (err error) {
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

func getValidLayerPath(
	layerDescriptor imagespec.Descriptor, unTarPath string) (layerPath string, err error) { // nolint:unparam
	return path.Join(unTarPath, layerDescriptor.Digest.Hex()), nil
}
