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

package layermanager_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
	"gitpct.epam.com/epmd-aepr/aos_common/image"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/layermanager"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type layerDownloader struct {
}

type testInfoProvider struct {
	sync.Mutex
	layers map[string]layerDesc
}

type layerDesc struct {
	id         string
	aosVersion uint64
	path       string
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
		FullTimestamp:    true})
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

	if err := cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestInstallRemoveLayer(t *testing.T) {
	layerManager, err := layermanager.New(&config.Config{WorkingDir: tmpDir}, new(layerDownloader), newTesInfoProvider())
	if err != nil {
		t.Fatalf("Can't create layer manager: %s", err)
	}

	layerFile, digest, err := createLayer(path.Join(tmpDir, "layerdir1"))
	if err != nil {
		t.Fatalf("Can't create layer: %s", err)
	}

	checkLayerStatuses(t, []<-chan amqp.LayerInfo{
		layerManager.InstallLayer(generateLayerFromCloud(layerFile, "LayerId1", digest, 1), nil, nil)})

	list, err := layerManager.GetLayersInfo()
	if err != nil {
		t.Fatalf("Can't get layer list: %s", err)
	}

	if len(list) != 1 {
		t.Fatal("Count of layers should be 1")
	}

	if list[0].ID != "LayerId1" {
		t.Error("Layer ID should be LayerId1")
	}

	if list[0].AosVersion != 1 {
		t.Error("Layer AosVersion should be 1")
	}

	checkLayerStatuses(t, []<-chan amqp.LayerInfo{
		layerManager.UninstallLayer(digest)})

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

	layerManager, err := layermanager.New(&config.Config{WorkingDir: tmpDir}, new(layerDownloader), infoProvider)
	if err != nil {
		t.Fatalf("Can't create layer manager: %s", err)
	}

	layerFile1, digest1, err := createLayer(path.Join(tmpDir, "layerdir1"))
	if err != nil {
		t.Fatalf("Can't create layer: %s", err)
	}

	layerFile2, digest2, err := createLayer(path.Join(tmpDir, "layerdir2"))
	if err != nil {
		t.Fatalf("Can't create layer: %s", err)
	}

	checkLayerStatuses(t, []<-chan amqp.LayerInfo{
		layerManager.InstallLayer(generateLayerFromCloud(layerFile1, "LayerId1", digest1, 1), nil, nil),
		layerManager.InstallLayer(generateLayerFromCloud(layerFile2, "LayerId2", digest2, 2), nil, nil),
	})

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

func TestLayersStatus(t *testing.T) {
	layerManager, err := layermanager.New(&config.Config{WorkingDir: tmpDir}, new(layerDownloader), newTesInfoProvider())
	if err != nil {
		t.Fatalf("Can't create layer manager: %s", err)
	}

	layerFile, digest, err := createLayer(path.Join(tmpDir, "layerdir1"))
	if err != nil {
		t.Fatalf("Can't create layer: %s", err)
	}

	if status := waitLayerStatus(layerManager.InstallLayer(
		generateLayerFromCloud(layerFile, "LayerId1", digest, 1), nil, nil)); status.Status != amqp.InstalledStatus {
		t.Errorf("Wrong status received: %s", status.Status)
	}

	if status := waitLayerStatus(layerManager.UninstallLayer(digest)); status.Status != amqp.RemovedStatus {
		t.Errorf("Wrong status received: %s", status.Status)
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (downloader *layerDownloader) DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
	chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error) {
	return packageInfo.URLs[0], err
}

func newTesInfoProvider() (infoProvider *testInfoProvider) {
	return &testInfoProvider{layers: make(map[string]layerDesc)}
}

func (infoProvider *testInfoProvider) AddLayer(digest, layerID, path, osVersion,
	vendorVersion, description string, aosVersion uint64) (err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

	infoProvider.layers[digest] = layerDesc{
		id:         layerID,
		aosVersion: aosVersion,
		path:       path}

	return nil
}

func (infoProvider *testInfoProvider) DeleteLayerByDigest(digest string) (err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

	if _, ok := infoProvider.layers[digest]; !ok {
		return aoserrors.New("layer not found")
	}

	delete(infoProvider.layers, digest)

	return nil
}

func (infoProvider *testInfoProvider) GetLayerPathByDigest(digest string) (path string, err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

	layer, ok := infoProvider.layers[digest]
	if !ok {
		return "", aoserrors.New("layer not found")
	}

	return layer.path, nil
}

func (infoProvider *testInfoProvider) GetLayersInfo() (layersList []amqp.LayerInfo, err error) {
	infoProvider.Lock()
	defer infoProvider.Unlock()

	for digest, layer := range infoProvider.layers {
		layersList = append(layersList, amqp.LayerInfo{
			ID: layer.id, AosVersion: layer.aosVersion, Digest: digest, Status: amqp.InstalledStatus})
	}

	return layersList, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	os.RemoveAll(tmpDir)

	return nil
}

func createLayer(dir string) (layerFile string, digest string, err error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)

	tmpLayerFolder := path.Join(tmpDir, "tmpLayerDir")
	if err := os.MkdirAll(tmpLayerFolder, 0755); err != nil {
		return "", "", err
	}

	data := []byte("this is layer data in layer " + dir)

	if err := ioutil.WriteFile(path.Join(tmpLayerFolder, "layer.txt"), data, 0644); err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmpLayerFolder)

	tarFile := path.Join(dir, "layer.tar")

	if output, err := exec.Command("tar", "-C", tmpLayerFolder, "-cf", tarFile, "./").CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("error: %s, code: %s", string(output), err)
	}
	defer os.Remove(tarFile)

	file, err := os.Open(tarFile)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

	byteValue, err := ioutil.ReadAll(file)
	if err != nil {
		return "", "", err
	}

	layerDigest, err := generateAndSaveDigest(dir, byteValue)
	if err != nil {
		return "", "", err
	}

	layerDescriptor := imagespec.Descriptor{
		MediaType: "application/vnd.aos.image.layer.v1.tar+gzip",
		Digest:    layerDigest,
		//TODO fullfill
	}

	dataJSON, err := json.Marshal(layerDescriptor)
	if err != nil {
		return "", "", err
	}

	jsonFile, err := os.Create(path.Join(dir, "layer.json"))
	if err != nil {
		return "", "", err
	}

	if _, err := jsonFile.Write(dataJSON); err != nil {
		return "", "", err
	}

	layerFile = path.Join(tmpDir, layerDigest.Hex()+".tar.gz")
	if output, err := exec.Command("tar", "-C", dir, "-czf", layerFile, "./").CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("error: %s, code: %s", string(output), err)
	}

	return layerFile, string(layerDigest), nil
}

func generateAndSaveDigest(folder string, data []byte) (retDigest digest.Digest, err error) {
	h := sha256.New()
	h.Write(data)

	retDigest = digest.NewDigest("sha256", h)

	file, err := os.Create(path.Join(folder, retDigest.Hex()))
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err = file.Write(data); err != nil {
		return "", err
	}

	return retDigest, nil
}

func generateLayerFromCloud(layerFile, layerID, digest string, aosVersion uint64) (layerInfo amqp.LayerInfoFromCloud) {
	layerInfo.ID = layerID
	layerInfo.Digest = digest
	layerInfo.AosVersion = aosVersion

	layerInfo.URLs = []string{layerFile}

	imageFileInfo, err := image.CreateFileInfo(layerFile)
	if err != nil {
		log.Error("error CreateFileInfo", err)
		return layerInfo
	}

	recInfo := struct {
		Serial string `json:"serial"`
		Issuer []byte `json:"issuer"`
	}{
		Serial: "string",
		Issuer: []byte("issuer"),
	}

	layerInfo.Sha256 = imageFileInfo.Sha256
	layerInfo.Sha512 = imageFileInfo.Sha512
	layerInfo.Size = imageFileInfo.Size
	layerInfo.DecryptionInfo = &amqp.DecryptionInfo{
		BlockAlg:     "AES256/CBC/pkcs7",
		BlockIv:      []byte{},
		BlockKey:     []byte{},
		AsymAlg:      "RSA/PKCS1v1_5",
		ReceiverInfo: &recInfo,
	}
	layerInfo.Signs = new(amqp.Signs)

	return layerInfo
}

func waitLayerStatus(statusChannel <-chan amqp.LayerInfo) (status amqp.LayerInfo) {
	for {
		select {
		case newStatus, ok := <-statusChannel:
			if !ok {
				return status
			}

			log.WithFields(log.Fields{
				"id":         newStatus.ID,
				"aosVersion": newStatus.AosVersion,
				"digest":     newStatus.Digest,
				"status":     newStatus.Status,
				"error":      newStatus.Error,
			}).Debug("Receive layer status")

			status = newStatus
		}
	}
}

func checkLayerStatuses(t *testing.T, statusChannels []<-chan amqp.LayerInfo) {
	t.Helper()

	var wg sync.WaitGroup

	for _, statusChannel := range statusChannels {
		wg.Add(1)

		go func(statusChannel <-chan amqp.LayerInfo) {
			defer wg.Done()

			if status := waitLayerStatus(statusChannel); status.Status == amqp.ErrorStatus {
				t.Errorf("%s, layer ID %s", status.Error, status.ID)
			}
		}(statusChannel)
	}

	wg.Wait()
}
