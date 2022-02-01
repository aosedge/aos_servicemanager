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
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"sync"
	"testing"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/image"
	"github.com/opencontainers/go-digest"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/layermanager"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type testInfoProvider struct {
	sync.Mutex
	layers []layermanager.LayerInfo
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var tmpDir string

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	ret := m.Run()

	cleanup()

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestInstallRemoveLayer(t *testing.T) {
	layerManager, err := layermanager.New(&config.Config{WorkingDir: tmpDir}, newTesInfoProvider())
	if err != nil {
		t.Fatalf("Can't create layer manager: %s", err)
	}

	layerFile, digest, fileInfo, err := createLayer(path.Join(tmpDir, "layerdir1"))
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

	if err = layerManager.UninstallLayer(digest); err != nil {
		t.Fatalf("Can't uninstall layer: %s", err)
	}

	list, err = layerManager.GetLayersInfo()
	if err != nil {
		t.Errorf("Can't get layer list %s", err)
	}

	if len(list) != 0 {
		t.Error("Count of layers should be 0")
	}
}

func TestLayerConsistencyCheck(t *testing.T) {
	infoProvider := newTesInfoProvider()

	layerManager, err := layermanager.New(&config.Config{WorkingDir: tmpDir}, infoProvider)
	if err != nil {
		t.Fatalf("Can't create layer manager: %s", err)
	}

	layerFile1, digest1, fileInfo1, err := createLayer(path.Join(tmpDir, "layerdir1"))
	if err != nil {
		t.Fatalf("Can't create layer: %s", err)
	}

	layerFile2, digest2, fileInfo2, err := createLayer(path.Join(tmpDir, "layerdir2"))
	if err != nil {
		t.Fatalf("Can't create layer: %s", err)
	}

	if err = layerManager.InstallLayer(
		layermanager.LayerInfo{LayerID: "LayerId1", Digest: digest1, AosVersion: 1},
		layerFile1, fileInfo1); err != nil {
		t.Fatalf("Can't install layer: %s", err)
	}

	if err = layerManager.InstallLayer(
		layermanager.LayerInfo{LayerID: "LayerId2", Digest: digest2, AosVersion: 1},
		layerFile2, fileInfo2); err != nil {
		t.Fatalf("Can't install layer: %s", err)
	}

	list, err := layerManager.GetLayersInfo()
	if err != nil {
		t.Fatalf("Can't get layer list: %s", err)
	}

	if len(list) != 2 {
		t.Error("Count of layers should be 2")
	}

	if err = layerManager.CheckLayersConsistency(); err != nil {
		t.Errorf("Error checking layer consistency: %s", err)
	}

	layer2path, err := infoProvider.GetLayerPathByDigest(digest2)
	if err != nil {
		t.Errorf("Can't get layer path: %s", err)
	}

	if err = os.RemoveAll(layer2path); err != nil {
		t.Errorf("Can't remove dir: %s", err)
	}

	if err = layerManager.CheckLayersConsistency(); err == nil {
		t.Error("Consistency check error is expected")
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func newTesInfoProvider() (infoProvider *testInfoProvider) {
	return &testInfoProvider{}
}

func (infoProvider *testInfoProvider) AddLayer(layerInfo layermanager.LayerInfo) (err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

	for _, layer := range infoProvider.layers {
		if layer.Digest == layerInfo.Digest {
			return aoserrors.New("Layer exist")
		}
	}

	infoProvider.layers = append(infoProvider.layers, layerInfo)

	return nil
}

func (infoProvider *testInfoProvider) DeleteLayerByDigest(digest string) (err error) {
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

func (infoProvider *testInfoProvider) GetLayerPathByDigest(digest string) (path string, err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

	for _, layer := range infoProvider.layers {
		if layer.Digest == digest {
			return layer.Path, nil
		}
	}

	return "", aoserrors.New("layer not found")
}

func (infoProvider *testInfoProvider) GetLayersInfo() (layersList []layermanager.LayerInfo, err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

	layersList = infoProvider.layers

	return layersList, nil
}

func (infoProvider *testInfoProvider) GetLayerInfoByDigest(
	digest string) (layerInfo layermanager.LayerInfo, err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

	for _, layer := range infoProvider.layers {
		if layer.Digest == digest {
			return layer, nil
		}
	}

	return layerInfo, aoserrors.New("layer not found")
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func cleanup() {
	os.RemoveAll(tmpDir)
}

func createLayer(dir string) (layerFile string, digest string, fileInfo image.FileInfo, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}
	defer os.RemoveAll(dir)

	tmpLayerFolder := path.Join(tmpDir, "tmpLayerDir")
	if err := os.MkdirAll(tmpLayerFolder, 0o755); err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}

	data := []byte("this is layer data in layer " + dir)

	if err := ioutil.WriteFile(path.Join(tmpLayerFolder, "layer.txt"), data, 0o600); err != nil {
		return "", "", fileInfo, aoserrors.Wrap(err)
	}
	defer os.RemoveAll(tmpLayerFolder)

	tarFile := path.Join(dir, "layer.tar")

	if output, err := exec.Command("tar", "-C", tmpLayerFolder, "-cf", tarFile, "./").CombinedOutput(); err != nil {
		return "", "", fileInfo, aoserrors.New(fmt.Sprintf("error: %s, code: %s", string(output), err))
	}
	defer os.Remove(tarFile)

	file, err := os.Open(tarFile)
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
