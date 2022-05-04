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
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/image"
	"github.com/opencontainers/go-digest"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/layermanager"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testLayerStorage struct {
	sync.Mutex
	layers       []layermanager.LayerInfo
	addLayerFail bool
	getLayerFail bool
}

type testSpaceAllocatorProvider struct {
	availableSize         int64
	allocatedSize         int64
	secondAllocateFail    bool
	numberAllocateRequest uint64
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var tmpDir string

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

func TestInstallRemoveLayer(t *testing.T) {
	testLayerStorage := newTestLayerStorage()

	layerManager, err := layermanager.New(
		&config.Config{
			WorkingDir: tmpDir,
			LayersDir:  path.Join(tmpDir, "layers"),
		}, testLayerStorage)
	if err != nil {
		t.Fatalf("Can't create layer manager: %s", err)
	}

	layerManager.SetSpaceAllocator(&testSpaceAllocatorProvider{})

	sizeLayerContent := int64(2 * 1024)

	layerFile, digest, fileInfo, err := createLayer(path.Join(tmpDir, "layerdir1"), sizeLayerContent)
	if err != nil {
		t.Fatalf("Can't create layer: %s", err)
	}

	if err = layerManager.InstallLayer(
		layermanager.LayerInfo{LayerID: "LayerId1", Digest: digest, AosVersion: 1},
		layerFile, fileInfo); err != nil {
		t.Fatalf("Can't install layer: %s", err)
	}

	list, err := layerManager.GetLayersInfo()
	if err != nil {
		t.Fatalf("Can't get layer list: %s", err)
	}

	if len(list) != 1 {
		t.Fatal("Count of layers should be 1")
	}

	if list[0].LayerID != "LayerId1" {
		t.Error("Layer ID should be LayerId1")
	}

	if list[0].AosVersion != 1 {
		t.Error("Layer AosVersion should be 1")
	}

	layerInfo, err := layerManager.GetLayerInfoByDigest(digest)
	if err != nil {
		t.Fatalf("Can't get layer info: %v", err)
	}

	if layerInfo.Digest != digest {
		t.Errorf("Unexpected layer digest: %v", layerInfo.Digest)
	}

	if layerInfo.LayerID != "LayerId1" {
		t.Errorf("Unexpected layer Id: %v", layerInfo.LayerID)
	}

	if layerInfo.Path != path.Join(tmpDir, "layers", strings.Replace(digest, ":", "/", 1)) {
		t.Errorf("Unexpected layer Path: %v", layerInfo.Path)
	}

	size, err := layerManager.UninstallLayer(digest)
	if err != nil {
		t.Fatalf("Can't uninstall layer: %s", err)
	}

	if size != sizeLayerContent {
		t.Errorf("Unexpected uninstall layer size: %v", size)
	}

	list, err = layerManager.GetLayersInfo()
	if err != nil {
		t.Errorf("Can't get layer list %s", err)
	}

	if len(list) != 0 {
		t.Error("Count of layers should be 0")
	}

	testLayerStorage.addLayerFail = true

	if err = layerManager.InstallLayer(
		layermanager.LayerInfo{LayerID: "LayerId1", Digest: digest, AosVersion: 1},
		layerFile, fileInfo); err == nil {
		t.Fatal("Layer should not be installed")
	}

	testLayerStorage.getLayerFail = true

	if _, err = layerManager.GetLayersInfo(); err == nil {
		t.Error("Should be error: can't get layers list")
	}
}

func TestRemoveDemageLayerFolder(t *testing.T) {
	testStorage := newTestLayerStorage()

	layerManager, err := layermanager.New(
		&config.Config{
			WorkingDir: tmpDir,
			LayersDir:  path.Join(tmpDir, "layers"),
		}, testStorage)
	if err != nil {
		t.Fatalf("Can't create layer manager: %v", err)
	}

	layerManager.SetSpaceAllocator(&testSpaceAllocatorProvider{})

	sizeLayerContent := int64(1 * 1024)

	layerFile, digest, fileInfo, err := createLayer(path.Join(tmpDir, "layerdir1"), sizeLayerContent)
	if err != nil {
		t.Fatalf("Can't create layer: %v", err)
	}

	if err = layerManager.InstallLayer(
		layermanager.LayerInfo{LayerID: "LayerId1", Digest: digest, AosVersion: 1},
		layerFile, fileInfo); err != nil {
		t.Fatalf("Can't install layer: %v", err)
	}

	testLayerDir := path.Join(tmpDir, "layers/sha256/test")
	if err := os.MkdirAll(testLayerDir, 0o755); err != nil {
		t.Fatalf("Can't create test layer: %v", err)
	}

	if _, err := os.Stat(testLayerDir); err != nil {
		t.Fatalf("Test layer folder does not exist: %v", err)
	}

	provider := testSpaceAllocatorProvider{}

	layerManager, err = layermanager.New(
		&config.Config{
			WorkingDir: tmpDir,
			LayersDir:  path.Join(tmpDir, "layers"),
		}, testStorage)
	if err != nil {
		t.Fatalf("Can't create layer manager: %v", err)
	}

	layerManager.SetSpaceAllocator(&provider)

	if _, err := os.Stat(testLayerDir); err == nil {
		t.Error("Test layer folder should be deleted")
	}

	layerFile, digest, fileInfo, err = createLayer(path.Join(tmpDir, "layerdir2"), sizeLayerContent)
	if err != nil {
		t.Fatalf("Can't create layer: %v", err)
	}

	if err = layerManager.InstallLayer(
		layermanager.LayerInfo{LayerID: "LayerId2", Digest: digest, AosVersion: 1},
		layerFile, fileInfo); err != nil {
		t.Fatalf("Can't install layer: %v", err)
	}

	layer, err := layerManager.GetLayerInfoByDigest(digest)
	if err != nil {
		t.Fatalf("Can't get layer: %v", err)
	}

	if err = os.RemoveAll(layer.Path); err != nil {
		t.Fatalf("Can't remove layer path: %v", err)
	}

	layerManager, err = layermanager.New(
		&config.Config{
			WorkingDir: tmpDir,
			LayersDir:  path.Join(tmpDir, "layers"),
		}, testStorage)
	if err != nil {
		t.Fatalf("Can't create layer manager: %v", err)
	}

	layerManager.SetSpaceAllocator(&provider)

	if _, err = layerManager.GetLayerInfoByDigest(digest); err == nil {
		t.Fatal("Should be error: layer doesn't exist")
	}
}

func TestRemoteDownloadLayer(t *testing.T) {
	spaceAllocator := &testSpaceAllocatorProvider{
		availableSize: 1 * 1024,
		allocatedSize: 51200,
	}

	layerManager, err := layermanager.New(
		&config.Config{
			WorkingDir: tmpDir,
			LayersDir:  path.Join(tmpDir, "layers"),
		}, newTestLayerStorage())
	if err != nil {
		t.Fatalf("Can't create layer manager: %v", err)
	}

	layerManager.SetSpaceAllocator(spaceAllocator)

	sizeLayerContent := int64(10 * 1024)

	layerFile, digest, fileInfo, err := createLayer(path.Join(tmpDir, "layerdir1"), sizeLayerContent)
	if err != nil {
		t.Fatalf("Can't create layer: %v", err)
	}

	fileServerDir, err := ioutil.TempDir("", "layers_fileserver")
	if err != nil {
		t.Fatalf("Error create temporary dir: %v", err)
	}

	defer os.RemoveAll(fileServerDir)

	urlVal, err := url.Parse(layerFile)
	if err != nil {
		t.Fatalf("Can't parse url: %v", err)
	}

	if err = os.Rename(path.Join(urlVal.Path), path.Join(fileServerDir, "downloadImage")); err != nil {
		t.Fatalf("Can't rename directory: %v", err)
	}

	server := &http.Server{Addr: ":9000", Handler: http.FileServer(http.Dir(fileServerDir))}

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("Can't serve http server: %v", err)
		}
	}()

	time.Sleep(1 * time.Second)

	defer server.Close()

	layermanager.GetAvailableSize = spaceAllocator.getAvailableSize

	if err = layerManager.InstallLayer(
		layermanager.LayerInfo{LayerID: "LayerId1", Digest: digest, AosVersion: 1},
		"http://:9000/downloadImage", fileInfo); err != nil {
		t.Fatalf("Can't install layer: %v", err)
	}
}

func TestAllocateSpace(t *testing.T) {
	spaceAllocator := &testSpaceAllocatorProvider{
		availableSize: 1 * 1024,
		allocatedSize: 51200,
	}

	layerManager, err := layermanager.New(
		&config.Config{
			WorkingDir: tmpDir,
			LayersDir:  path.Join(tmpDir, "layers"),
		}, newTestLayerStorage())
	if err != nil {
		t.Fatalf("Can't create layer manager: %v", err)
	}

	layerManager.SetSpaceAllocator(spaceAllocator)

	sizeLayerContent := int64(10 * 1024)

	layerFile, digest, fileInfo, err := createLayer(path.Join(tmpDir, "layerdir1"), sizeLayerContent)
	if err != nil {
		t.Fatalf("Can't create layer: %v", err)
	}

	layermanager.GetAvailableSize = spaceAllocator.getAvailableSize

	if err = layerManager.InstallLayer(
		layermanager.LayerInfo{LayerID: "LayerId1", Digest: digest, AosVersion: 1},
		layerFile, fileInfo); err != nil {
		t.Fatalf("Can't install layer: %v", err)
	}

	size, err := layerManager.UninstallLayer(digest)
	if err != nil {
		t.Fatalf("Can't uninstall layer: %v", err)
	}

	if size != sizeLayerContent {
		t.Fatalf("Unexpected uninstall layer size: %v", size)
	}
}

func TestAllocateMemoryFailed(t *testing.T) {
	type testData struct {
		spaceAllocator *testSpaceAllocatorProvider
	}

	data := []testData{
		{
			spaceAllocator: &testSpaceAllocatorProvider{
				availableSize: 1 * 1024,
				allocatedSize: 2048,
			},
		},
		{
			spaceAllocator: &testSpaceAllocatorProvider{
				availableSize:      1 * 1024,
				allocatedSize:      51200,
				secondAllocateFail: true,
			},
		},
	}

	sizeLayerContent := int64(10 * 1024)

	layerFile, digest, fileInfo, err := createLayer(path.Join(tmpDir, "layerdir1"), sizeLayerContent)
	if err != nil {
		t.Fatalf("Can't create layer: %v", err)
	}

	for _, testCase := range data {
		layerManager, err := layermanager.New(
			&config.Config{
				WorkingDir: tmpDir,
				LayersDir:  path.Join(tmpDir, "layers"),
			}, newTestLayerStorage())
		if err != nil {
			t.Fatalf("Can't create layer manager: %v", err)
		}

		layerManager.SetSpaceAllocator(testCase.spaceAllocator)

		if err = layerManager.InstallLayer(
			layermanager.LayerInfo{LayerID: "LayerId1", Digest: digest, AosVersion: 1},
			layerFile, fileInfo); err == nil {
			t.Fatal("Layer should not be installed")
		}

		list, err := layerManager.GetLayersInfo()
		if err != nil {
			t.Errorf("Can't get layer list: %v", err)
		}

		if len(list) != 0 {
			t.Error("Count of layers should be 0")
		}

		if _, err := layerManager.UninstallLayer(digest); err == nil {
			t.Fatal("Should be error, layer not installed")
		}

		if _, err := layerManager.GetLayerInfoByDigest(digest); err == nil {
			t.Fatal("Should be error, layer not installed")
		}
	}
}

/***********************************************************************************************************************
 * Interfaces
 **********************************************************************************************************************/

func newTestLayerStorage() (infoProvider *testLayerStorage) {
	return &testLayerStorage{}
}

func (infoProvider *testLayerStorage) AddLayer(layerInfo layermanager.LayerInfo) (err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

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
	infoProvider.Lock()
	defer infoProvider.Unlock()

	for i, layer := range infoProvider.layers {
		if layer.Digest == digest {
			infoProvider.layers = append(infoProvider.layers[:i], infoProvider.layers[i+1:]...)

			return nil
		}
	}

	return aoserrors.New("layer not found")
}

func (infoProvider *testLayerStorage) GetLayersInfo() (layersList []layermanager.LayerInfo, err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

	if infoProvider.getLayerFail {
		return nil, aoserrors.New("can't get layers info")
	}

	layersList = infoProvider.layers

	return layersList, nil
}

func (infoProvider *testLayerStorage) GetLayerInfoByDigest(
	digest string,
) (layerInfo layermanager.LayerInfo, err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

	for _, layer := range infoProvider.layers {
		if layer.Digest == digest {
			return layer, nil
		}
	}

	return layerInfo, aoserrors.New("layer not found")
}

func (servicemanager *testSpaceAllocatorProvider) AllocateLayersSpace(
	extraSpace int64,
) (allocatedLayersSize int64, err error) {
	if servicemanager.numberAllocateRequest++; servicemanager.numberAllocateRequest == 2 {
		if servicemanager.secondAllocateFail {
			return 0, aoserrors.New("can't allocate memory")
		}
	}

	var availableSize int64

	availableSize += servicemanager.allocatedSize

	if availableSize < extraSpace {
		return 0, aoserrors.New("can't allocate memory")
	}

	return availableSize, nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func setup() (err error) {
	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func cleanup() {
	os.RemoveAll(tmpDir)
}

func createLayer(
	dir string, sizeLayerContent int64,
) (layerFile string, digest string, fileInfo image.FileInfo, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}
	defer os.RemoveAll(dir)

	tmpLayerFolder := path.Join(tmpDir, "tmpLayerDir")
	if err := os.MkdirAll(tmpLayerFolder, 0o755); err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}

	file, err := os.Create(path.Join(tmpLayerFolder, "layer.txt"))
	if err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}

	defer os.RemoveAll(tmpLayerFolder)

	if err := file.Truncate(sizeLayerContent); err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}

	tarFile := path.Join(dir, "layer.tar")

	if output, err := exec.Command("tar", "-C", tmpLayerFolder, "-cf", tarFile, "./").CombinedOutput(); err != nil {
		return "", "", fileInfo, aoserrors.New(fmt.Sprintf("error: %s, code: %s", string(output), err))
	}
	defer os.Remove(tarFile)

	file, err = os.Open(tarFile)
	if err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}
	defer file.Close()

	byteValue, err := ioutil.ReadAll(file)
	if err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}

	layerDigest, err := generateAndSaveDigest(dir, byteValue)
	if err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}

	layerDescriptor := imagespec.Descriptor{
		MediaType: "application/vnd.aos.image.layer.v1.tar+gzip",
		Digest:    layerDigest,
		Size:      sizeLayerContent,
	}

	dataJSON, err := json.Marshal(layerDescriptor)
	if err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}

	jsonFile, err := os.Create(path.Join(dir, "layer.json"))
	if err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}

	if _, err := jsonFile.Write(dataJSON); err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}

	layerFile = path.Join(tmpDir, layerDigest.Hex()+".tar.gz")
	if output, err := exec.Command("tar", "-C", dir, "-czf", layerFile, "./").CombinedOutput(); err != nil {
		return "", "", fileInfo, aoserrors.New(fmt.Sprintf("error: %s, code: %s", string(output), err))
	}

	if fileInfo, err = image.CreateFileInfo(context.Background(), layerFile); err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}

	return "file://" + layerFile, string(layerDigest), fileInfo, nil
}

func generateAndSaveDigest(folder string, data []byte) (retDigest digest.Digest, err error) {
	h := sha256.New()
	h.Write(data)

	retDigest = digest.NewDigest("sha256", h)

	file, err := os.Create(path.Join(folder, retDigest.Hex()))
	if err != nil {
		return "", aoserrors.Wrap(err)
	}
	defer file.Close()

	if _, err = file.Write(data); err != nil {
		return "", aoserrors.Wrap(err)
	}

	return retDigest, nil
}

func (servicemanager *testSpaceAllocatorProvider) getAvailableSize(dir string) (availableSize int64, err error) {
	return servicemanager.availableSize, nil
}
