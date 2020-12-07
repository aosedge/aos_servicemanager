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

package layermanager

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/opencontainers/go-digest"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/image"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/database"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const tmpDir = "tmp"
const tmpServerDir = "/tmp/aos/layerserver"

/*******************************************************************************
 * Types
 ******************************************************************************/
type fakeLayerSender struct {
	LayerList []amqp.LayerInfo
}

type layerDownloader struct {
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var (
	layerMgr *LayerManager
	db       *database.Database
)

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
	layerDir := path.Join(tmpDir, "layerdir1")
	if err := os.MkdirAll(layerDir, 0755); err != nil {
		log.Fatalf("Can't create folder: %s", err)
	}
	defer os.RemoveAll(layerDir)

	layerFile, digest, err := createLayer(layerDir)
	if err != nil {
		log.Fatalf("Can't layer: %s", err)
	}

	chains := []amqp.CertificateChain{}
	certs := []amqp.Certificate{}
	layerList := []amqp.LayerInfoFromCloud{generateLayerFromCloud(layerFile, "LayerId1", digest, 1)}

	if err := layerMgr.ProcessDesiredLayersList(layerList, chains, certs); err != nil {
		t.Errorf("Can't process layer list %s", err)
	}

	list, err := layerMgr.GetLayersInfo()
	if err != nil {
		t.Errorf("Can't get layer list %s", err)
	}

	if len(list) != 1 {
		t.Error("Count of layers should be 1")
	}

	if list[0].ID != "LayerId1" {
		t.Error("Layer ID should be LayerId1")
	}

	if list[0].AosVersion != 1 {
		t.Error("Layer AosVersion should be 1")
	}

	if err = layerMgr.DeleteUnneededLayers(); err != nil {
		t.Errorf("Error delete unneeded layers %s", err)
	}

	layerList = []amqp.LayerInfoFromCloud{}
	if err := layerMgr.ProcessDesiredLayersList(layerList, chains, certs); err != nil {
		t.Errorf("Can't process layer list %s", err)
	}

	if err = layerMgr.DeleteUnneededLayers(); err != nil {
		t.Errorf("Error delete unneeded layers %s", err)
	}

	list, err = layerMgr.GetLayersInfo()
	if err != nil {
		t.Errorf("Can't get layer list %s", err)
	}

	if len(list) != 0 {
		t.Error("Count of layers should be 0")
	}

}

func TestLayerConsistencyCheck(t *testing.T) {
	layerDir := path.Join(tmpDir, "layerdir1")
	if err := os.MkdirAll(layerDir, 0755); err != nil {
		log.Fatalf("Can't create folder: %s", err)
	}
	defer os.RemoveAll(layerDir)

	layerDir2 := path.Join(tmpDir, "layerdir2")
	if err := os.MkdirAll(layerDir2, 0755); err != nil {
		log.Fatalf("Can't create folder: %s", err)
	}
	defer func() {
		if _, err := os.Stat(layerDir2); !os.IsNotExist(err) {
			os.RemoveAll(layerDir2)
		}
	}()

	layerFile, digest, err := createLayer(layerDir)
	if err != nil {
		log.Fatalf("Can't layer: %s", err)
	}

	layerFile2, digest2, err := createLayer(layerDir2)
	if err != nil {
		log.Fatalf("Can't layer: %s", err)
	}

	chains := []amqp.CertificateChain{}
	certs := []amqp.Certificate{}
	layerList := []amqp.LayerInfoFromCloud{
		generateLayerFromCloud(layerFile, "LayerId1", digest, 1),
		generateLayerFromCloud(layerFile2, "LayerId2", digest2, 2)}

	if err := layerMgr.ProcessDesiredLayersList(layerList, chains, certs); err != nil {
		t.Errorf("Can't process layer list %s", err)
	}

	list, err := layerMgr.GetLayersInfo()
	if err != nil {
		t.Errorf("Can't get layer list %s", err)
	}

	if len(list) != 2 {
		t.Error("Count of layers should be 2")
	}

	if err = layerMgr.CheckLayersConsistency(); err != nil {
		t.Errorf("Layer configuration expected to be consistent. Err: %s", err)
	}

	layer2path, err := layerMgr.layerInfoProvider.GetLayerPathByDigest(digest2)
	if err != nil {
		t.Errorf("Can't get layer path. Err: %s", err)
	}

	if err = os.RemoveAll(layer2path); err != nil {
		t.Errorf("Can't remove dir: %s", err)
	}

	if err = layerMgr.CheckLayersConsistency(); err == nil {
		t.Error("Consistency check error is expected")
	}
}

func TestLayersStatus(t *testing.T) {
	testTmpDir, err := ioutil.TempDir("", "aos_layers")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testTmpDir)

	testDb, err := database.New(path.Join(testTmpDir, "db.txt"), tmpServerDir, tmpServerDir)
	if err != nil {
		log.Fatalf("Can't create database: %s", err)
	}

	sender := new(fakeLayerSender)

	testLayerMgr, err := New(path.Join(testTmpDir, "layerStorage"), new(layerDownloader), testDb, sender)
	if err != nil {
		log.Fatalf("Can't create layer manager: %s", err)
	}

	type testLayerInfo struct {
		layerFile string
		digest    string
	}

	layersInfo := []testLayerInfo{}

	for i := 1; i <= 3; i++ {
		layerDir := "layerdir" + strconv.Itoa(i)
		if err := os.MkdirAll(layerDir, 0755); err != nil {
			log.Fatalf("Can't create folder: %s", err)
		}
		defer os.RemoveAll(layerDir)

		layerFile, digest, err := createLayer(layerDir)
		if err != nil {
			log.Fatalf("Can't layer: %s", err)
		}

		layersInfo = append(layersInfo, testLayerInfo{layerFile: layerFile, digest: digest})
	}

	layer1FomCloud := generateLayerFromCloud(layersInfo[0].layerFile, "LayerId1", layersInfo[0].digest, 1)
	layer2FomCloud := generateLayerFromCloud(layersInfo[1].layerFile, "LayerId2", layersInfo[1].digest, 2)
	layer2UpdateFomCloud := generateLayerFromCloud(layersInfo[2].layerFile, "LayerId2", layersInfo[2].digest, 3)

	chains := []amqp.CertificateChain{}
	certs := []amqp.Certificate{}
	layerList := []amqp.LayerInfoFromCloud{layer1FomCloud, layer2FomCloud}

	if err := testLayerMgr.ProcessDesiredLayersList(layerList, chains, certs); err != nil {
		t.Errorf("Can't process layer list %s", err)
	}
	testLayerMgr.DeleteUnneededLayers()

	referenceList := []amqp.LayerInfo{
		amqp.LayerInfo{ID: "LayerId1", AosVersion: 1, Digest: layersInfo[0].digest, Status: "installed"},
		amqp.LayerInfo{ID: "LayerId2", AosVersion: 2, Digest: layersInfo[1].digest, Status: "installed"}}

	if !reflect.DeepEqual(referenceList, sender.LayerList) {
		t.Error("Incorrect layer status list \n", referenceList, " != \n", sender.LayerList)
	}

	layerList = []amqp.LayerInfoFromCloud{layer1FomCloud, layer2UpdateFomCloud}
	if err := testLayerMgr.ProcessDesiredLayersList(layerList, chains, certs); err != nil {
		t.Errorf("Can't process layer list %s", err)
	}

	referenceList = []amqp.LayerInfo{
		amqp.LayerInfo{ID: "LayerId1", AosVersion: 1, Digest: layersInfo[0].digest, Status: "installed"},
		amqp.LayerInfo{ID: "LayerId2", AosVersion: 2, Digest: layersInfo[1].digest, Status: "installed"},
		amqp.LayerInfo{ID: "LayerId2", AosVersion: 3, Digest: layersInfo[2].digest, Status: "installed"}}

	if !reflect.DeepEqual(referenceList, sender.LayerList) {
		t.Error("Incorrect layer status list \n", referenceList, " != \n", sender.LayerList)
	}

	testLayerMgr.DeleteUnneededLayers()

	referenceList[1].Status = "removed"
	if !reflect.DeepEqual(referenceList, sender.LayerList) {
		t.Error("Incorrect layer status list \n", referenceList, " != \n", sender.LayerList)
	}

	referenceList = []amqp.LayerInfo{
		amqp.LayerInfo{ID: "LayerId1", AosVersion: 1, Digest: layersInfo[0].digest, Status: "installed"},
		amqp.LayerInfo{ID: "LayerId2", AosVersion: 3, Digest: layersInfo[2].digest, Status: "installed"}}

	currentList, err := testLayerMgr.GetLayersInfo()
	if err != nil {
		t.Error("Can't get current layer list")
	}

	if !reflect.DeepEqual(referenceList, currentList) {
		t.Error("Incorrect layer status list \n", referenceList, " != \n", sender.LayerList)
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/
func (sender *fakeLayerSender) SendLayerStatus(layers []amqp.LayerInfo) {
	sender.LayerList = make([]amqp.LayerInfo, len(layers))
	copy(sender.LayerList, layers)
}

func (downloader *layerDownloader) DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
	chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error) {
	srcFile := packageInfo.URLs[0]

	cpCmd := exec.Command("cp", srcFile, tmpDir)
	err = cpCmd.Run()

	return path.Join(tmpDir, filepath.Base(srcFile)), err
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	if err := os.MkdirAll(tmpServerDir, 0755); err != nil {
		return err
	}

	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return err
	}

	db, err := database.New(path.Join(tmpServerDir, "db.txt"), tmpServerDir, tmpServerDir)
	if err != nil {
		log.Fatalf("Can't create database: %s", err)
	}

	layerMgr, err = New(path.Join(tmpDir, "layerStorage"), new(layerDownloader), db, new(fakeLayerSender))
	if err != nil {
		log.Fatalf("Can't layer manager: %s", err)
	}

	return nil
}

func cleanup() (err error) {
	os.RemoveAll(tmpDir)
	os.RemoveAll(tmpServerDir)
	return nil
}

func createLayer(dir string) (layerFile string, digest string, err error) {
	tmpLayerFolder := path.Join(tmpDir, "tmpLayerDir")
	if err := os.MkdirAll(tmpLayerFolder, 0755); err != nil {
		log.Fatalf("Can't create folder: %s", err)
	}

	data := []byte("this is layer data in layer " + dir)
	if err := ioutil.WriteFile(path.Join(tmpLayerFolder, "layer.txt"), data, 0644); err != nil {
		return "", layerFile, err
	}
	defer os.RemoveAll(tmpLayerFolder)

	tarFile := path.Join(dir, "layer.tag")

	if output, err := exec.Command("tar", "-C", tmpLayerFolder, "-cf", tarFile, "./").CombinedOutput(); err != nil {
		return layerFile, digest, fmt.Errorf("error: %s, code: %s", string(output), err)
	}
	defer os.Remove(tarFile)

	file, err := os.Open(tarFile)
	if err != nil {
		return layerFile, digest, err
	}
	defer file.Close()

	byteValue, err := ioutil.ReadAll(file)
	if err != nil {
		return "", layerFile, err
	}

	layerDigest, err := generateAndSaveDigest(dir, byteValue)
	if err != nil {
		return layerFile, digest, err
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

	layerFile = path.Join(tmpServerDir, layerDigest.Hex()+".tar.gz")
	if output, err := exec.Command("tar", "-C", dir, "-czf", layerFile, "./").CombinedOutput(); err != nil {
		return layerFile, digest, fmt.Errorf("error: %s, code: %s", string(output), err)
	}

	digest = (string)(layerDigest)
	layerFile = layerDigest.Hex() + ".tar.gz"

	return layerFile, digest, nil
}

func generateAndSaveDigest(folder string, data []byte) (retDigest digest.Digest, err error) {
	h := sha256.New()
	h.Write(data)
	retDigest = digest.NewDigest("sha256", h)

	file, err := os.Create(path.Join(folder, retDigest.Hex()))
	if err != nil {
		return retDigest, err
	}
	defer file.Close()

	_, err = file.Write(data)
	if err != nil {
		return retDigest, err
	}

	return retDigest, nil
}

func generateLayerFromCloud(layerFile, layerID, digest string, aosVersion uint64) (layerInfo amqp.LayerInfoFromCloud) {
	layerInfo.ID = layerID
	layerInfo.Digest = digest
	layerInfo.AosVersion = aosVersion

	filePath := path.Join(tmpServerDir, layerFile)
	layerInfo.URLs = []string{filePath}

	imageFileInfo, err := image.CreateFileInfo(filePath)
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
