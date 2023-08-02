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
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/image"
	"github.com/aoscloud/aos_common/spaceallocator"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/utils/whiteouts"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	layerOCIDescriptor = "layer.json"
)

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var ErrNotExist = errors.New("layer does not exist")

// NewSpaceAllocator space allocator constructor.
//
//nolint:gochecknoglobals // used for unit test mock
var NewSpaceAllocator = spaceallocator.New

// RemoveCachedLayersPeriod global variable is used to be able to mocking the layers TTL functionality in test.
//
//nolint:gochecknoglobals // used for unit test mock
var RemoveCachedLayersPeriod = 24 * time.Hour

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
	layerStorage           LayerStorage
	layersDir              string
	extractDir             string
	downloadDir            string
	layerTTLDays           uint64
	layerAllocator         spaceallocator.Allocator
	downloadAllocator      spaceallocator.Allocator
	extractAllocator       spaceallocator.Allocator
	validateTTLStopChannel chan struct{}
}

// LayerStorage provides API to add, remove or access layer information.
type LayerStorage interface {
	AddLayer(LayerInfo) error
	DeleteLayerByDigest(digest string) error
	GetLayersInfo() ([]LayerInfo, error)
	GetLayerInfoByDigest(digest string) (LayerInfo, error)
	SetLayerCached(digest string, cached bool) error
}

// LayerInfo layer information.
type LayerInfo struct {
	aostypes.VersionInfo
	Digest    string
	LayerID   string
	Path      string
	OSVersion string
	Timestamp time.Time
	Cached    bool
	Size      uint64
}

/**********************************************************************************************************************
 * Public
 **********************************************************************************************************************/
// New creates new layer manager instance.
func New(config *config.Config, layerStorage LayerStorage) (layermanager *LayerManager, err error) {
	layermanager = &LayerManager{
		layersDir:              config.LayersDir,
		layerStorage:           layerStorage,
		extractDir:             config.ExtractDir,
		downloadDir:            config.DownloadDir,
		layerTTLDays:           config.LayerTTLDays,
		validateTTLStopChannel: make(chan struct{}),
	}

	if err := os.RemoveAll(layermanager.downloadDir); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := os.RemoveAll(layermanager.extractDir); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := os.MkdirAll(layermanager.downloadDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := os.MkdirAll(layermanager.extractDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := os.MkdirAll(layermanager.layersDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if layermanager.layerAllocator, err = NewSpaceAllocator(
		layermanager.layersDir, config.LayersPartLimit, layermanager.removeLayer); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if layermanager.downloadAllocator, err = NewSpaceAllocator(
		layermanager.downloadDir, 0, nil); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if layermanager.extractAllocator, err = NewSpaceAllocator(
		layermanager.extractDir, 0, nil); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := layermanager.removeDamagedLayerFolders(); err != nil {
		log.Errorf("Can't remove damaged layer folders: %v", err)
	}

	if err := layermanager.setOutdatedLayers(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := layermanager.removeOutdatedLayers(); err != nil {
		log.Errorf("Can't remove outdated layers: %v", err)
	}

	go layermanager.validateTTLs()

	return layermanager, nil
}

// Close closes layer manager instance.
func (layermanager *LayerManager) Close() {
	if err := layermanager.layerAllocator.Close(); err != nil {
		log.Errorf("Can't close layer allocator: %v", err)
	}

	if err := layermanager.downloadAllocator.Close(); err != nil {
		log.Errorf("Can't close download allocator: %v", err)
	}

	if err := layermanager.extractAllocator.Close(); err != nil {
		log.Errorf("Can't close extract allocator: %v", err)
	}

	close(layermanager.validateTTLStopChannel)
}

// GetLayerInfoByDigest gets layers information by layer digest.
func (layermanager *LayerManager) GetLayerInfoByDigest(digest string) (layer LayerInfo, err error) {
	if layer, err = layermanager.layerStorage.GetLayerInfoByDigest(digest); err != nil {
		return layer, aoserrors.Wrap(err)
	}

	return layer, nil
}

// ProcessDesiredLayers installs, removes, restores desired layers on the system.
func (layermanager *LayerManager) ProcessDesiredLayers(desiredLayers []aostypes.LayerInfo) error {
	layers, err := layermanager.layerStorage.GetLayersInfo()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if desiredLayers, err = layermanager.updateCachedLayers(desiredLayers, layers); err != nil {
		return err
	}

	if err = layermanager.installLayers(desiredLayers); err != nil {
		return err
	}

	return nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (layermanager *LayerManager) installLayers(desiredLayers []aostypes.LayerInfo) error {
	for _, desiredLayer := range desiredLayers {
		if err := layermanager.installLayer(desiredLayer); err != nil {
			return err
		}
	}

	return nil
}

func (layermanager *LayerManager) updateCachedLayers(
	desiredLayers []aostypes.LayerInfo, storeLayers []LayerInfo,
) (installLayers []aostypes.LayerInfo, err error) {
nextLayer:
	for _, storeLayer := range storeLayers {
		for i, desiredLayer := range desiredLayers {
			if desiredLayer.Digest != storeLayer.Digest {
				continue
			}

			if storeLayer.Cached {
				if err := layermanager.setLayerCached(storeLayer, false); err != nil {
					return desiredLayers, aoserrors.Wrap(err)
				}
			}

			desiredLayers = append(desiredLayers[:i], desiredLayers[i+1:]...)

			continue nextLayer
		}

		if !storeLayer.Cached {
			if err := layermanager.setLayerCached(storeLayer, true); err != nil {
				return desiredLayers, aoserrors.Wrap(err)
			}
		}
	}

	return desiredLayers, nil
}

func (layermanager *LayerManager) installLayer(
	layerInfo aostypes.LayerInfo,
) (err error) {
	log.WithFields(log.Fields{
		"id":         layerInfo.ID,
		"aosVersion": layerInfo.AosVersion,
		"digest":     layerInfo.Digest,
	}).Debug("Install layer")

	extractLayerDir := filepath.Join(layermanager.extractDir, layerInfo.Digest)

	if err := os.MkdirAll(extractLayerDir, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}
	defer os.RemoveAll(extractLayerDir)

	layerDescriptor, spaceExtract, err := layermanager.extractPackageByURL(extractLayerDir, &layerInfo)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	defer func() {
		if err := spaceExtract.Release(); err != nil {
			log.Errorf("Can't release memory: %v", err)
		}
	}()

	layerPath, err := getValidLayerPath(layerDescriptor, extractLayerDir)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	spaceLayer, err := layermanager.layerAllocator.AllocateSpace(uint64(layerDescriptor.Size))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	storeLayerPath := filepath.Join(layermanager.layersDir,
		(string)(layerDescriptor.Digest.Algorithm()), layerDescriptor.Digest.Hex())

	defer func() {
		if err != nil {
			releaseAllocatedSpace(storeLayerPath, spaceLayer)

			log.WithFields(log.Fields{
				"id":         layerInfo.ID,
				"aosVersion": layerInfo.AosVersion,
				"digest":     layerInfo.Digest,
			}).Errorf("Can't install layer: %s", err)

			return
		}

		if err := spaceLayer.Accept(); err != nil {
			log.Errorf("Can't accept memory: %v", err)
		}
	}()

	if err = unpackLayer(layerPath, storeLayerPath); err != nil {
		return err
	}

	var osVersion string

	if layerDescriptor.Platform != nil {
		osVersion = layerDescriptor.Platform.OSVersion
	}

	if err = layermanager.layerStorage.AddLayer(LayerInfo{
		LayerID:     layerInfo.ID,
		Digest:      layerInfo.Digest,
		Path:        storeLayerPath,
		OSVersion:   osVersion,
		Size:        uint64(layerDescriptor.Size),
		VersionInfo: layerInfo.VersionInfo,
		Timestamp:   time.Now().UTC(),
	}); err != nil {
		return aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{
		"id":         layerInfo.ID,
		"aosVersion": layerInfo.AosVersion,
		"digest":     layerInfo.Digest,
	}).Info("Layer successfully installed")

	return nil
}

func (layermanager *LayerManager) setLayerCached(layer LayerInfo, cached bool) error {
	if err := layermanager.layerStorage.SetLayerCached(layer.Digest, cached); err != nil {
		return aoserrors.Wrap(err)
	}

	if cached {
		if err := layermanager.layerAllocator.AddOutdatedItem(
			layer.Digest, layer.Size, layer.Timestamp); err != nil {
			return aoserrors.Wrap(err)
		}

		return nil
	}

	layermanager.layerAllocator.RestoreOutdatedItem(layer.Digest)

	return nil
}

func (layermanager *LayerManager) validateTTLs() {
	removeTicker := time.NewTicker(RemoveCachedLayersPeriod)
	defer removeTicker.Stop()

	for {
		select {
		case <-removeTicker.C:
			if err := layermanager.removeOutdatedLayers(); err != nil {
				log.Errorf("Can't remove outdated layers: %v", err)
			}

		case <-layermanager.validateTTLStopChannel:
			return
		}
	}
}

func (layermanager *LayerManager) removeOutdatedLayers() error {
	layers, err := layermanager.layerStorage.GetLayersInfo()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, layer := range layers {
		if layer.Cached &&
			layer.Timestamp.Add(time.Hour*24*time.Duration(layermanager.layerTTLDays)).Before(time.Now()) {
			if err := layermanager.removeLayer(layer.Digest); err != nil {
				return err
			}

			layermanager.layerAllocator.RestoreOutdatedItem(layer.Digest)
		}
	}

	return nil
}

func (layermanager *LayerManager) removeLayer(digest string) error {
	layer, err := layermanager.layerStorage.GetLayerInfoByDigest(digest)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = os.RemoveAll(layer.Path); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = layermanager.layerStorage.DeleteLayerByDigest(digest); err != nil {
		return aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{"digest": digest}).Info("Layer successfully removed")

	return nil
}

func (layermanager *LayerManager) setOutdatedLayers() error {
	layersInfo, err := layermanager.layerStorage.GetLayersInfo()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, layer := range layersInfo {
		if layer.Cached {
			if err = layermanager.layerAllocator.AddOutdatedItem(
				layer.Digest, layer.Size, layer.Timestamp); err != nil {
				return aoserrors.Wrap(err)
			}
		}
	}

	return nil
}

func (layermanager *LayerManager) removeDamagedLayerFolders() error {
	layersInfo, err := layermanager.layerStorage.GetLayersInfo()
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

	algorithms, err := os.ReadDir(layermanager.layersDir)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, algorithm := range algorithms {
		algorithmPath := filepath.Join(layermanager.layersDir, algorithm.Name())

		digests, err := os.ReadDir(algorithmPath)
		if err != nil {
			return aoserrors.Wrap(err)
		}

	digestsLoop:
		for _, digest := range digests {
			digestPath := filepath.Join(algorithmPath, digest.Name())

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
	extractDir string, layerInfo *aostypes.LayerInfo,
) (layerDescriptor imagespec.Descriptor, space spaceallocator.Space, err error) {
	urlVal, err := url.Parse(layerInfo.URL)
	if err != nil {
		return layerDescriptor, nil, aoserrors.Wrap(err)
	}

	var sourceFile string

	if urlVal.Scheme != "file" {
		spaceDownload, err := layermanager.downloadAllocator.AllocateSpace(layerInfo.Size)
		if err != nil {
			return layerDescriptor, nil, aoserrors.Wrap(err)
		}

		defer func() {
			if err := spaceDownload.Release(); err != nil {
				log.Errorf("Can't release memory: %v", err)
			}
		}()

		if sourceFile, err = image.Download(context.Background(), layermanager.downloadDir, layerInfo.URL); err != nil {
			return layerDescriptor, nil, aoserrors.Wrap(err)
		}

		defer os.RemoveAll(sourceFile)
	} else {
		sourceFile = urlVal.Path
	}

	if err = image.CheckFileInfo(context.Background(), sourceFile, image.FileInfo{
		Sha256: layerInfo.Sha256,
		Sha512: layerInfo.Sha512,
		Size:   layerInfo.Size,
	}); err != nil {
		return layerDescriptor, nil, aoserrors.Wrap(err)
	}

	size, err := image.GetUncompressedTarContentSize(sourceFile)
	if err != nil {
		return layerDescriptor, nil, aoserrors.Wrap(err)
	}

	spaceExtract, err := layermanager.extractAllocator.AllocateSpace(uint64(size))
	if err != nil {
		return layerDescriptor, nil, aoserrors.Wrap(err)
	}

	if err = image.UnpackTarImage(sourceFile, extractDir); err != nil {
		return layerDescriptor, nil, aoserrors.Wrap(err)
	}

	var byteValue []byte

	if byteValue, err = os.ReadFile(filepath.Join(extractDir, layerOCIDescriptor)); err != nil {
		return layerDescriptor, nil, aoserrors.Wrap(err)
	}

	if err = json.Unmarshal(byteValue, &layerDescriptor); err != nil {
		return layerDescriptor, nil, aoserrors.Wrap(err)
	}

	return layerDescriptor, spaceExtract, nil
}

func getValidLayerPath(layerDescriptor imagespec.Descriptor, unTarPath string) (layerPath string, err error) {
	return filepath.Join(unTarPath, layerDescriptor.Digest.Hex()), nil
}

func releaseAllocatedSpace(path string, spaceLayer spaceallocator.Space) {
	if err := os.RemoveAll(path); err != nil {
		log.Warnf("Can't remove layer storage dir: %v", err)
	}

	if err := spaceLayer.Release(); err != nil {
		log.Errorf("Can't release memory: %v", err)
	}
}

func unpackLayer(source, destination string) error {
	if err := image.UnpackTarImage(source, destination); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := whiteouts.OCIWhiteoutsToOverlay(destination, 0, 0); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}
