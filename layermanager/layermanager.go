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
	"errors"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"sync"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/image"
	"github.com/aoscloud/aos_common/utils/action"
	"github.com/aoscloud/aos_common/utils/fs"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/utils/imageutils"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	extractDirName     = "extract"
	downloadDirName    = "download"
	layerOCIDescriptor = "layer.json"
)

const maxConcurrentActions = 10

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var ErrNotExist = errors.New("layer does not exist")

// GetAvailableSize global variable is used to be able to mocking the functionality in tests.
// nolint:gochecknoglobals
var GetAvailableSize = fs.GetAvailableSize

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// SpaceAllocator space allocator.
type SpaceAllocator interface {
	AllocateLayersSpace(extraSpace int64) (allocatedLayersSize int64, err error)
}

// LayerManager instance.
type LayerManager struct {
	sync.Mutex
	layersDir               string
	layerStorage            LayerStorage
	extractDir              string
	downloadDir             string
	actionHandler           *action.Handler
	availableSize           int64
	numTryAllocateSpace     uint64
	spaceAllocator          SpaceAllocator
	isDownloadSamePartition bool
	isExtractSamePartition  bool
}

// LayerStorage provides API to add, remove or access layer information.
type LayerStorage interface {
	AddLayer(LayerInfo) error
	DeleteLayerByDigest(digest string) error
	GetLayersInfo() ([]LayerInfo, error)
	GetLayerInfoByDigest(digest string) (LayerInfo, error)
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

/**********************************************************************************************************************
 * Public
 **********************************************************************************************************************/
// New creates new launcher object.
func New(config *config.Config, layerStorage LayerStorage) (layermanager *LayerManager, err error) {
	layermanager = &LayerManager{
		layersDir:     config.LayersDir,
		layerStorage:  layerStorage,
		extractDir:    path.Join(config.WorkingDir, extractDirName),
		downloadDir:   path.Join(config.WorkingDir, downloadDirName),
		actionHandler: action.New(maxConcurrentActions),
	}

	if err := os.RemoveAll(layermanager.downloadDir); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := os.RemoveAll(layermanager.extractDir); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := os.MkdirAll(layermanager.extractDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := os.MkdirAll(layermanager.layersDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := layermanager.removeDamagedLayerFolders(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	layersMountPoint, err := fs.GetMountPoint(layermanager.layersDir)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	downloadMountPoint, err := fs.GetMountPoint(layermanager.downloadDir)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	layermanager.isDownloadSamePartition = layersMountPoint == downloadMountPoint

	extractMountPoint, err := fs.GetMountPoint(layermanager.extractDir)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	layermanager.isExtractSamePartition = layersMountPoint == extractMountPoint

	return layermanager, nil
}

// SetSpaceAllocator set space allocator.
func (layermanager *LayerManager) SetSpaceAllocator(spaceAllocator SpaceAllocator) {
	layermanager.spaceAllocator = spaceAllocator
}

// GetLayersInfo provides list of already installed fs layers.
func (layermanager *LayerManager) GetLayersInfo() (info []LayerInfo, err error) {
	if info, err = layermanager.layerStorage.GetLayersInfo(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return info, nil
}

// InstallLayer installs layer.
func (layermanager *LayerManager) InstallLayer(
	installInfo LayerInfo, layerURL string, fileInfo image.FileInfo,
) error {
	return <-layermanager.actionHandler.Execute(installInfo.Digest,
		func(id string) error {
			return layermanager.doInstallLayer(installInfo, layerURL, fileInfo)
		})
}

// UninstallLayer uninstalls layer.
func (layermanager *LayerManager) UninstallLayer(digest string) (deallocatedSize int64, err error) {
	err = <-layermanager.actionHandler.Execute(digest,
		func(id string) error {
			if deallocatedSize, err = layermanager.doUninstallLayer(digest); err != nil {
				return err
			}
			return nil
		})

	return deallocatedSize, err
}

// GetLayerInfoByDigest gets layers information by layer digest.
func (layermanager *LayerManager) GetLayerInfoByDigest(digest string) (layer LayerInfo, err error) {
	if layer, err = layermanager.layerStorage.GetLayerInfoByDigest(digest); err != nil {
		return layer, aoserrors.Wrap(err)
	}

	return layer, nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (layermanager *LayerManager) doInstallLayer(
	installInfo LayerInfo, layerURL string, fileInfo image.FileInfo,
) (err error) {
	log.WithFields(log.Fields{
		"id":         installInfo.LayerID,
		"aosVersion": installInfo.AosVersion,
		"digest":     installInfo.Digest,
	}).Debug("Install layer")

	if _, errNoLayer := layermanager.layerStorage.GetLayerInfoByDigest(installInfo.Digest); errNoLayer == nil {
		// layer already installed
		return nil
	}

	extractLayerDir := path.Join(layermanager.extractDir, installInfo.Digest)

	if err := os.MkdirAll(extractLayerDir, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	defer os.RemoveAll(extractLayerDir)

	if err := layermanager.extractPackageByURL(extractLayerDir, layermanager.downloadDir, layerURL, fileInfo); err != nil {
		return aoserrors.Wrap(err)
	}

	defer layermanager.deallocatedSpaceExtractDirLayer(extractLayerDir)

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

	if err = layermanager.tryAllocateSpace(layerDescriptor.Size); err != nil {
		return aoserrors.Wrap(err)
	}

	layerStorageDir := path.Join(layermanager.layersDir,
		(string)(layerDescriptor.Digest.Algorithm()), layerDescriptor.Digest.Hex())

	defer func() {
		var size int64

		if err != nil {
			size = layerDescriptor.Size

			if err := os.RemoveAll(layerStorageDir); err != nil {
				log.Warnf("Can't remove layer storage dir: %v", err)
			}

			log.WithFields(log.Fields{
				"id":         installInfo.LayerID,
				"aosVersion": installInfo.AosVersion,
				"digest":     installInfo.Digest,
			}).Errorf("Can't install layer: %s", err)
		}

		layermanager.deallocateSpace(size)
	}()

	if err = imageutils.UnpackTarImage(layerPath, layerStorageDir); err != nil {
		return aoserrors.Wrap(err)
	}

	if layerDescriptor.Platform != nil {
		installInfo.OSVersion = layerDescriptor.Platform.OSVersion
	}

	installInfo.Path = layerStorageDir

	if err = layermanager.layerStorage.AddLayer(installInfo); err != nil {
		return aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{
		"id":         installInfo.LayerID,
		"aosVersion": installInfo.AosVersion,
		"digest":     installInfo.Digest,
	}).Info("Layer successfully installed")

	return nil
}

func (layermanager *LayerManager) deallocatedSpaceExtractDirLayer(extractLayerDir string) {
	if layermanager.isExtractSamePartition {
		size, err := fs.GetDirSize(extractLayerDir)
		if err != nil {
			log.Errorf("Can't get directory size: %v", err)
			return
		}

		layermanager.deallocateSpace(size)
	}
}

func (layermanager *LayerManager) doUninstallLayer(digest string) (deallocatedSize int64, err error) {
	log.WithFields(log.Fields{"digest": digest}).Debug("Uninstall layer")

	layer, err := layermanager.layerStorage.GetLayerInfoByDigest(digest)
	if err != nil {
		return 0, aoserrors.Wrap(err)
	}

	size, err := fs.GetDirSize(layer.Path)
	if err != nil {
		return 0, aoserrors.Wrap(err)
	}

	if err = os.RemoveAll(layer.Path); err != nil {
		return 0, aoserrors.Wrap(err)
	}

	if err = layermanager.layerStorage.DeleteLayerByDigest(digest); err != nil {
		return 0, aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{"digest": digest}).Info("Layer successfully uninstalled")

	return size, nil
}

func (layermanager *LayerManager) removeDamagedLayerFolders() error {
	layersInfo, err := layermanager.GetLayersInfo()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, layer := range layersInfo {
		fi, err := os.Stat(layer.Path)
		if err != nil || !fi.Mode().IsDir() {
			log.Warnf("Layer missing: %v", layer.Path)

			if err = layermanager.layerStorage.DeleteLayerByDigest(layer.Digest); err != nil {
				return aoserrors.Wrap(err)
			}
		}
	}

	algorithms, err := ioutil.ReadDir(layermanager.layersDir)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, algorithm := range algorithms {
		algorithmPath := path.Join(layermanager.layersDir, algorithm.Name())

		digests, err := ioutil.ReadDir(algorithmPath)
		if err != nil {
			return aoserrors.Wrap(err)
		}

	digestsLoop:
		for _, digest := range digests {
			digestPath := path.Join(algorithmPath, digest.Name())

			for _, layer := range layersInfo {
				if layer.Path == digestPath {
					continue digestsLoop
				}
			}

			log.Warnf("Layer missing in storage: %v", digestPath)

			if err := os.RemoveAll(digestPath); err != nil {
				return aoserrors.Wrap(err)
			}
		}
	}

	return nil
}

func (layermanager *LayerManager) extractPackageByURL(
	extractDir, downloadDir, packageURL string, fileInfo image.FileInfo,
) error {
	urlVal, err := url.Parse(packageURL)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	var sourceFile string

	if urlVal.Scheme != "file" {
		if layermanager.isDownloadSamePartition {
			if err = layermanager.tryAllocateSpace(int64(fileInfo.Size)); err != nil {
				return aoserrors.Wrap(err)
			}

			defer layermanager.deallocateSpace(int64(fileInfo.Size))
		}

		if sourceFile, err = image.Download(context.Background(), downloadDir, packageURL); err != nil {
			return aoserrors.Wrap(err)
		}

		defer os.RemoveAll(sourceFile)
	} else {
		sourceFile = urlVal.Path
	}

	if err = image.CheckFileInfo(context.Background(), sourceFile, fileInfo); err != nil {
		return aoserrors.Wrap(err)
	}

	if layermanager.isExtractSamePartition {
		size, err := imageutils.GetUncompressedTarContentSize(sourceFile)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		if err = layermanager.tryAllocateSpace(size); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if err = imageutils.UnpackTarImage(sourceFile, extractDir); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (layermanager *LayerManager) tryAllocateSpace(requiredSize int64) (err error) {
	if layermanager.spaceAllocator == nil {
		return aoserrors.New("space allocator not set")
	}

	layermanager.Lock()
	defer layermanager.Unlock()

	if layermanager.numTryAllocateSpace == 0 {
		if layermanager.availableSize, err = GetAvailableSize(layermanager.layersDir); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if layermanager.availableSize < requiredSize {
		allocatedSize, err := layermanager.spaceAllocator.AllocateLayersSpace(
			requiredSize - layermanager.availableSize)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		layermanager.availableSize += allocatedSize
	}

	layermanager.availableSize -= requiredSize
	layermanager.numTryAllocateSpace++

	return nil
}

func (layermanager *LayerManager) deallocateSpace(size int64) {
	layermanager.Lock()
	defer layermanager.Unlock()

	layermanager.availableSize += size
	layermanager.numTryAllocateSpace--
}

func getValidLayerPath(layerDescriptor imagespec.Descriptor, unTarPath string) (layerPath string, err error) {
	return path.Join(unTarPath, layerDescriptor.Digest.Hex()), nil
}
