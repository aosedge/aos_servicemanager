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
	"net/http"
	"os"
	"os/exec"
	"path"
	"testing"

	"github.com/opencontainers/go-digest"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/image"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/database"
	"aos_servicemanager/fcrypt"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const tmpDir = "tmp"
const tmpServerDir = "/tmp/aos/layerserver"

/*******************************************************************************
 * Types
 ******************************************************************************/

type fakeFcrypt struct {
}

type fakeSymmetricContext struct {
}

type fakeSignContext struct {
}

type fakeLayerSender struct {
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

func TestInstalRemovelLayer(t *testing.T) {
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
	layerList := []amqp.LayerInfoFromCloud{generateLayrFromCloud(layerFile, "LayerId1", digest)}

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

	if list[0].LayerID != "LayerId1" {
		t.Error("Layer ID should be LayerId1")
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

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (crypt fakeFcrypt) ImportSessionKey(keyInfo fcrypt.CryptoSessionKeyInfo) (fcrypt.SymmetricContextInterface, error) {
	return new(fakeSymmetricContext), nil
}

func (crypt fakeFcrypt) CreateSignContext() (fcrypt.SignContextInterface, error) {
	return new(fakeSignContext), nil
}

func (symCont fakeSymmetricContext) GenerateKeyAndIV(algString string) (err error) {
	return nil
}

func (symCont fakeSymmetricContext) DecryptFile(encryptedFile, clearFile *os.File) (err error) {
	data, err := ioutil.ReadFile(encryptedFile.Name())
	if err != nil {
		return err
	}

	if _, err = clearFile.Write(data); err != nil {
		return err
	}

	return nil
}

func (symCont fakeSymmetricContext) EncryptFile(clearFile, encryptedFile *os.File) (err error) {
	return nil
}

func (symCont fakeSymmetricContext) IsReady() (ready bool) {
	return true
}

func (sigCont fakeSignContext) AddCertificate(fingerprint string, asn1Bytes []byte) (err error) {
	return nil
}

func (sigCont fakeSignContext) AddCertificateChain(name string, fingerprints []string) (err error) {
	return nil
}

func (sigCont fakeSignContext) VerifySign(f *os.File, chainName string, algName string, signValue []byte) (err error) {
	return nil
}

func (sender fakeLayerSender) SendLayerStatus(serviceStatus amqp.LayerInfo) (err error) {
	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	if err := os.MkdirAll(tmpServerDir, 0755); err != nil {
		return err
	}

	go func() {
		log.Fatal(http.ListenAndServe(":8080", http.FileServer(http.Dir(tmpServerDir))))
	}()

	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return err
	}

	db, err := database.New(path.Join(tmpServerDir, "db.txt"))
	if err != nil {
		log.Fatalf("Can't create database: %s", err)
	}

	layerMgr, err = New(path.Join(tmpDir, "layrStorage"), new(fakeFcrypt), db, new(fakeLayerSender))
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

	data := []byte("this is layer data")
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

func generateLayrFromCloud(layerFile, layerID, digest string) (layerInfo amqp.LayerInfoFromCloud) {
	layerInfo.LayerID = layerID
	layerInfo.URLs = []string{"http://localhost:8080/" + layerFile}
	layerInfo.Digest = digest

	filePath := path.Join(tmpServerDir, layerFile)
	imageFileInfo, err := image.CreateFileInfo(filePath)
	if err != nil {
		log.Error("error CreateFileInfo", err)
		return layerInfo
	}

	layerInfo.Sha256 = imageFileInfo.Sha256
	layerInfo.Sha512 = imageFileInfo.Sha512
	layerInfo.Size = imageFileInfo.Size
	layerInfo.DecryptionInfo = &amqp.DecryptionInfo{
		BlockAlg: "AES256/CBC/pkcs7",
		BlockIv:  []byte{},
		BlockKey: []byte{},
		AsymAlg:  "RSA/PKCS1v1_5",
	}
	layerInfo.Signs = new(amqp.Signs)

	return layerInfo
}
