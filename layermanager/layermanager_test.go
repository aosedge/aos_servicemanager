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

package layermanager_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/aostypes"
	"github.com/aosedge/aos_common/image"
	"github.com/aosedge/aos_common/spaceallocator"
	"github.com/opencontainers/go-digest"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"

	"github.com/aosedge/aos_servicemanager/config"
	"github.com/aosedge/aos_servicemanager/layermanager"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	kilobyte = uint64(1 << 10)
	megabyte = uint64(1 << 20)
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testLayerStorage struct {
	layers       []layermanager.LayerInfo
	addLayerFail bool
	getLayerFail bool
}

type testAllocator struct {
	sync.Mutex

	totalSize     uint64
	allocatedSize uint64
	remover       spaceallocator.ItemRemover
	outdatedItems []testOutdatedItem
}

type testSpace struct {
	allocator *testAllocator
	size      uint64
}

type testOutdatedItem struct {
	id   string
	size uint64
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var (
	tmpDir         string
	layersDir      string
	layerAllocator = &testAllocator{}
)

/***********************************************************************************************************************
 * Init
 **********************************************************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/***********************************************************************************************************************
 * Main
 **********************************************************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	ret := m.Run()

	cleanup()

	os.Exit(ret)
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestProcessDesiredLayers(t *testing.T) {
	testLayerStorage := &testLayerStorage{}

	layerAllocator = &testAllocator{}

	defer func() {
		if err := os.RemoveAll(layersDir); err != nil {
			t.Errorf("Can't remove layers dir: %v", err)
		}
	}()

	layermanager.RemoveCachedLayersPeriod = 1 * time.Second

	layerManager, err := layermanager.New(
		&config.Config{
			LayersDir:    layersDir,
			ExtractDir:   filepath.Join(tmpDir, "extract"),
			DownloadDir:  filepath.Join(tmpDir, "download"),
			LayerTTLDays: 0,
		}, testLayerStorage)
	if err != nil {
		t.Fatalf("Can't create layer manager: %s", err)
	}
	defer layerManager.Close()

	layers := make(map[string]aostypes.LayerInfo)

	generateLayer := []struct {
		layerID          string
		layerContentSize uint64
	}{
		{layerID: "layer1", layerContentSize: 2 * kilobyte},
		{layerID: "layer2", layerContentSize: 1 * kilobyte},
		{layerID: "layer3", layerContentSize: 3 * kilobyte},
	}

	for _, layer := range generateLayer {
		layerInfo, err := createLayer(filepath.Join(tmpDir, "layerdir1"), int64(layer.layerContentSize), layer.layerID)
		if err != nil {
			t.Fatalf("Can't prepare layer: %v", err)
		}

		layers[layer.layerID] = layerInfo
	}

	cases := []struct {
		desiredLayers   []aostypes.LayerInfo
		removedLayers   []string
		restoreLayers   []string
		installedLayers []string
	}{
		{
			desiredLayers:   getDesiredLayers(layers, []string{"layer1", "layer2", "layer3"}),
			installedLayers: []string{"layer1", "layer2", "layer3"},
		},
		{
			desiredLayers:   getDesiredLayers(layers, []string{"layer1", "layer3"}),
			installedLayers: []string{"layer1", "layer3"},
			removedLayers:   []string{"layer2"},
		},
		{
			desiredLayers:   getDesiredLayers(layers, []string{"layer1", "layer2"}),
			installedLayers: []string{"layer1"},
			removedLayers:   []string{"layer3"},
			restoreLayers:   []string{"layer2"},
		},
	}

	for _, tCase := range cases {
		if err := layerManager.ProcessDesiredLayers(tCase.desiredLayers); err != nil {
			t.Errorf("Can't process desired layers: %v", err)
		}

	nextInstallLayer:
		for _, installLayer := range tCase.installedLayers {
			for _, storeLayer := range testLayerStorage.layers {
				if installLayer == storeLayer.LayerID {
					continue nextInstallLayer
				}
			}

			t.Errorf("Layer %s should be installed", installLayer)
		}

	nextRemoveLayer:
		for _, removeLayer := range tCase.removedLayers {
			for _, storeLayer := range testLayerStorage.layers {
				if removeLayer == storeLayer.LayerID && storeLayer.Cached {
					continue nextRemoveLayer
				}
			}

			t.Errorf("Layer %s should be cached", removeLayer)
		}

	nextRestoreLayer:
		for _, restoreLayer := range tCase.restoreLayers {
			for _, storeLayer := range testLayerStorage.layers {
				if restoreLayer == storeLayer.LayerID && !storeLayer.Cached {
					continue nextRestoreLayer
				}
			}

			t.Errorf("Layer %s should not be cached", restoreLayer)
		}
	}
}

func TestRemoveDemageLayerFolder(t *testing.T) {
	testStorage := &testLayerStorage{}

	layerAllocator = &testAllocator{}

	defer func() {
		if err := os.RemoveAll(layersDir); err != nil {
			t.Errorf("Can't remove layers dir: %v", err)
		}
	}()

	layerManager, err := layermanager.New(
		&config.Config{
			LayersDir:   layersDir,
			ExtractDir:  filepath.Join(tmpDir, "extract"),
			DownloadDir: filepath.Join(tmpDir, "download"),
		}, testStorage)
	if err != nil {
		t.Fatalf("Can't create layer manager: %v", err)
	}

	sizeLayerContent := int64(1 * kilobyte)

	layerInfo, err := createLayer(filepath.Join(tmpDir, "layerdir1"), sizeLayerContent, "layer1")
	if err != nil {
		t.Fatalf("Can't create layer: %v", err)
	}

	if err = layerManager.ProcessDesiredLayers([]aostypes.LayerInfo{layerInfo}); err != nil {
		t.Fatalf("Can't process desired layers: %v", err)
	}

	testLayerDir := filepath.Join(tmpDir, "layers/sha256/test")
	if err := os.MkdirAll(testLayerDir, 0o755); err != nil {
		t.Fatalf("Can't create test layer: %v", err)
	}

	if _, err := os.Stat(testLayerDir); err != nil {
		t.Fatalf("Test layer folder does not exist: %v", err)
	}

	layerAllocator = &testAllocator{}

	layerManager, err = layermanager.New(
		&config.Config{
			LayersDir:   layersDir,
			ExtractDir:  filepath.Join(tmpDir, "extract"),
			DownloadDir: filepath.Join(tmpDir, "download"),
		}, testStorage)
	if err != nil {
		t.Fatalf("Can't create layer manager: %v", err)
	}

	if _, err := os.Stat(testLayerDir); err == nil {
		t.Error("Test layer folder should be deleted")
	}

	layerInfo, err = createLayer(filepath.Join(tmpDir, "layerdir2"), sizeLayerContent, "layer2")
	if err != nil {
		t.Fatalf("Can't create layer: %v", err)
	}

	if err = layerManager.ProcessDesiredLayers([]aostypes.LayerInfo{layerInfo}); err != nil {
		t.Fatalf("Can't install layer: %v", err)
	}

	layer, err := layerManager.GetLayerInfoByDigest(layerInfo.Digest)
	if err != nil {
		t.Fatalf("Can't get layer: %v", err)
	}

	if err = os.RemoveAll(layer.Path); err != nil {
		t.Fatalf("Can't remove layer path: %v", err)
	}

	layerManager.Close()

	layerAllocator = &testAllocator{}

	layerManager, err = layermanager.New(
		&config.Config{
			LayersDir:   layersDir,
			ExtractDir:  filepath.Join(tmpDir, "extract"),
			DownloadDir: filepath.Join(tmpDir, "download"),
		}, testStorage)
	if err != nil {
		t.Fatalf("Can't create layer manager: %v", err)
	}
	defer layerManager.Close()

	if _, err = layerManager.GetLayerInfoByDigest(layerInfo.Digest); err == nil {
		t.Fatal("Should be error: layer doesn't exist")
	}
}

func TestRemoteDownloadLayer(t *testing.T) {
	layerAllocator = &testAllocator{}

	defer func() {
		if err := os.RemoveAll(layersDir); err != nil {
			t.Errorf("Can't remove layers dir: %v", err)
		}
	}()

	layerManager, err := layermanager.New(
		&config.Config{
			LayersDir:   layersDir,
			ExtractDir:  filepath.Join(tmpDir, "extract"),
			DownloadDir: filepath.Join(tmpDir, "download"),
		}, &testLayerStorage{})
	if err != nil {
		t.Fatalf("Can't create layer manager: %v", err)
	}
	defer layerManager.Close()

	sizeLayerContent := int64(10 * kilobyte)

	layerInfo, err := createLayer(filepath.Join(tmpDir, "layerdir1"), sizeLayerContent, "layer1")
	if err != nil {
		t.Fatalf("Can't create layer: %v", err)
	}

	fileServerDir, err := os.MkdirTemp("", "layers_fileserver")
	if err != nil {
		t.Fatalf("Error create temporary dir: %v", err)
	}

	defer os.RemoveAll(fileServerDir)

	urlVal, err := url.Parse(layerInfo.URL)
	if err != nil {
		t.Fatalf("Can't parse url: %v", err)
	}

	if err = os.Rename(urlVal.Path, filepath.Join(fileServerDir, "downloadImage")); err != nil {
		t.Fatalf("Can't rename directory: %v", err)
	}

	server := &http.Server{
		Addr:              ":9000",
		Handler:           http.FileServer(http.Dir(fileServerDir)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("Can't serve http server: %v", err)
		}
	}()

	time.Sleep(1 * time.Second)

	defer server.Close()

	layerInfo.URL = "http://:9000/downloadImage"

	if err = layerManager.ProcessDesiredLayers([]aostypes.LayerInfo{layerInfo}); err != nil {
		t.Fatalf("Can't install layer: %v", err)
	}
}

func TestInstallLayerNotEnoughSpace(t *testing.T) {
	layerAllocator = &testAllocator{
		totalSize: 1 * megabyte,
	}

	defer func() {
		if err := os.RemoveAll(layersDir); err != nil {
			t.Errorf("Can't remove layers dir: %v", err)
		}
	}()

	layerManager, err := layermanager.New(
		&config.Config{
			LayersDir:    layersDir,
			ExtractDir:   filepath.Join(tmpDir, "extract"),
			DownloadDir:  filepath.Join(tmpDir, "download"),
			LayerTTLDays: 2,
		}, &testLayerStorage{})
	if err != nil {
		t.Fatalf("Can't create layer manager: %v", err)
	}
	defer layerManager.Close()

	layers := make(map[string]aostypes.LayerInfo)

	generateLayer := []struct {
		layerID          string
		layerContentSize uint64
	}{
		{layerID: "layer1", layerContentSize: 512 * kilobyte},
		{layerID: "layer2", layerContentSize: 520 * kilobyte},
		{layerID: "layer3", layerContentSize: 600 * kilobyte},
	}

	for i, layer := range generateLayer {
		layerInfo, err := createLayer(
			filepath.Join(tmpDir, fmt.Sprintf("layerdir%d", i)), int64(layer.layerContentSize), layer.layerID)
		if err != nil {
			t.Fatalf("Can't prepare layer: %v", err)
		}

		layers[layer.layerID] = layerInfo
	}

	cases := []struct {
		desiredLayers       []aostypes.LayerInfo
		processDesiredError error
	}{
		{
			desiredLayers: getDesiredLayers(layers, []string{"layer1"}),
		},
		{
			desiredLayers: getDesiredLayers(layers, []string{"layer2"}),
		},
		{
			desiredLayers:       getDesiredLayers(layers, []string{"layer2", "layer3"}),
			processDesiredError: spaceallocator.ErrNoSpace,
		},
		{
			desiredLayers: getDesiredLayers(layers, []string{"layer3"}),
		},
	}

	for _, tCase := range cases {
		if err := layerManager.ProcessDesiredLayers(tCase.desiredLayers); !errors.Is(err, tCase.processDesiredError) {
			t.Errorf("Can't process desired layers: %v", err)
		}
	}
}

/***********************************************************************************************************************
 * Interfaces
 **********************************************************************************************************************/

func newSpaceAllocator(
	path string, partLimit uint, remover spaceallocator.ItemRemover,
) (spaceallocator.Allocator, error) {
	switch path {
	case layersDir:
		layerAllocator.remover = remover
		return layerAllocator, nil

	default:
		return &testAllocator{remover: remover}, nil
	}
}

func (allocator *testAllocator) AllocateSpace(size uint64) (spaceallocator.Space, error) {
	allocator.Lock()
	defer allocator.Unlock()

	if allocator.totalSize != 0 && allocator.allocatedSize+size > allocator.totalSize {
		for allocator.allocatedSize+size > allocator.totalSize {
			if len(allocator.outdatedItems) == 0 {
				return nil, spaceallocator.ErrNoSpace
			}

			if err := allocator.remover(allocator.outdatedItems[0].id); err != nil {
				return nil, err
			}

			if allocator.outdatedItems[0].size < allocator.allocatedSize {
				allocator.allocatedSize -= allocator.outdatedItems[0].size
			} else {
				allocator.allocatedSize = 0
			}

			allocator.outdatedItems = allocator.outdatedItems[1:]
		}
	}

	allocator.allocatedSize += size

	return &testSpace{allocator: allocator, size: size}, nil
}

func (allocator *testAllocator) FreeSpace(size uint64) {
	allocator.Lock()
	defer allocator.Unlock()

	if size > allocator.allocatedSize {
		allocator.allocatedSize = 0
	} else {
		allocator.allocatedSize -= size
	}
}

func (allocator *testAllocator) AddOutdatedItem(id string, size uint64, timestamp time.Time) error {
	allocator.outdatedItems = append(allocator.outdatedItems, testOutdatedItem{id: id, size: size})

	return nil
}

func (allocator *testAllocator) RestoreOutdatedItem(id string) {
	for i, item := range allocator.outdatedItems {
		if item.id == id {
			allocator.outdatedItems = append(allocator.outdatedItems[:i], allocator.outdatedItems[i+1:]...)

			break
		}
	}
}

func (allocator *testAllocator) Close() error {
	return nil
}

func (space *testSpace) Accept() error {
	return nil
}

func (space *testSpace) Release() error {
	space.allocator.FreeSpace(space.size)

	return nil
}

func (infoProvider *testLayerStorage) AddLayer(layerInfo layermanager.LayerInfo) (err error) {
	if infoProvider.addLayerFail {
		return aoserrors.New("can't add layer")
	}

	for _, layer := range infoProvider.layers {
		if layer.Digest == layerInfo.Digest {
			return aoserrors.New("layer exist")
		}
	}

	infoProvider.layers = append(infoProvider.layers, layerInfo)

	return nil
}

func (infoProvider *testLayerStorage) DeleteLayerByDigest(digest string) (err error) {
	for i, layer := range infoProvider.layers {
		if layer.Digest == digest {
			infoProvider.layers = append(infoProvider.layers[:i], infoProvider.layers[i+1:]...)

			return nil
		}
	}

	return aoserrors.New("layer not found")
}

func (infoProvider *testLayerStorage) GetLayersInfo() (layersList []layermanager.LayerInfo, err error) {
	if infoProvider.getLayerFail {
		return nil, aoserrors.New("can't get layers info")
	}

	layersList = infoProvider.layers

	return layersList, nil
}

func (infoProvider *testLayerStorage) GetLayerInfoByDigest(
	digest string,
) (layerInfo layermanager.LayerInfo, err error) {
	for _, layer := range infoProvider.layers {
		if layer.Digest == digest {
			return layer, nil
		}
	}

	return layerInfo, aoserrors.New("layer not found")
}

func (infoProvider *testLayerStorage) SetLayerCached(digest string, cached bool) error {
	for i, layer := range infoProvider.layers {
		if layer.Digest == digest {
			infoProvider.layers[i].Cached = cached

			return nil
		}
	}

	return aoserrors.New("layer not found")
}

func (infoProvider *testLayerStorage) SetLayerTimestamp(digest string, timestamp time.Time) error {
	for i, layer := range infoProvider.layers {
		if layer.Digest == digest {
			infoProvider.layers[i].Timestamp = timestamp

			return nil
		}
	}

	return aoserrors.New("layer not found")
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func setup() (err error) {
	if tmpDir, err = os.MkdirTemp("", "aos_"); err != nil {
		return aoserrors.Wrap(err)
	}

	layersDir = filepath.Join(tmpDir, "layers")

	layermanager.NewSpaceAllocator = newSpaceAllocator

	return nil
}

func cleanup() {
	os.RemoveAll(tmpDir)
}

func createLayer(
	dir string, sizeLayerContent int64, layerID string,
) (layerInfo aostypes.LayerInfo, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}
	defer os.RemoveAll(dir)

	tmpLayerFolder := filepath.Join(tmpDir, "tmpLayerDir")
	if err := os.MkdirAll(tmpLayerFolder, 0o755); err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}

	file, err := os.Create(filepath.Join(tmpLayerFolder, "layer.txt"))
	if err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}

	defer os.RemoveAll(tmpLayerFolder)

	if err := file.Truncate(sizeLayerContent); err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}

	tarFile := filepath.Join(dir, "layer.tar")

	if output, err := exec.Command("tar", "-C", tmpLayerFolder, "-cf", tarFile, "./").CombinedOutput(); err != nil {
		return layerInfo, aoserrors.New(fmt.Sprintf("error: %s, code: %s", string(output), err))
	}
	defer os.Remove(tarFile)

	file, err = os.Open(tarFile)
	if err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}
	defer file.Close()

	byteValue, err := io.ReadAll(file)
	if err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}

	layerDigest, err := generateAndSaveDigest(dir, byteValue)
	if err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}

	layerDescriptor := imagespec.Descriptor{
		MediaType: "application/vnd.aos.image.layer.v1.tar+gzip",
		Digest:    layerDigest,
		Size:      sizeLayerContent,
	}

	dataJSON, err := json.Marshal(layerDescriptor)
	if err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}

	jsonFile, err := os.Create(filepath.Join(dir, "layer.json"))
	if err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}

	if _, err := jsonFile.Write(dataJSON); err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}

	layerFile := filepath.Join(tmpDir, layerDigest.Hex()+".tar.gz")
	if output, err := exec.Command("tar", "-C", dir, "-czf", layerFile, "./").CombinedOutput(); err != nil {
		return layerInfo, aoserrors.New(fmt.Sprintf("error: %s, code: %s", string(output), err))
	}

	fileInfo, err := image.CreateFileInfo(context.Background(), layerFile)
	if err != nil {
		return layerInfo, aoserrors.Wrap(err)
	}

	return aostypes.LayerInfo{
		URL:     "file://" + layerFile,
		Digest:  string(layerDigest),
		LayerID: layerID,
		Version: "1.0.0",
		Sha256:  fileInfo.Sha256,
		Size:    fileInfo.Size,
	}, nil
}

func generateAndSaveDigest(folder string, data []byte) (retDigest digest.Digest, err error) {
	h := sha256.New()
	h.Write(data)

	retDigest = digest.NewDigest("sha256", h)

	file, err := os.Create(filepath.Join(folder, retDigest.Hex()))
	if err != nil {
		return "", aoserrors.Wrap(err)
	}
	defer file.Close()

	if _, err = file.Write(data); err != nil {
		return "", aoserrors.Wrap(err)
	}

	return retDigest, nil
}

func getDesiredLayers(layers map[string]aostypes.LayerInfo, layersID []string) (desiredLayers []aostypes.LayerInfo) {
	for _, layerID := range layersID {
		layer, ok := layers[layerID]
		if ok {
			desiredLayers = append(desiredLayers, layer)
		}
	}

	return
}
