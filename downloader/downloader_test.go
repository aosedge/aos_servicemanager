// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2020 Renesas Inc.
// Copyright 2020 EPAM Systems Inc.
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

// Package launcher provides set of API to controls services lifecycle

package downloader_test

import (
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"testing"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/image"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/downloader"
	"aos_servicemanager/fcrypt"
)

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

var downloaderObj *downloader.Downloader
var serverDir string
var workingDir string

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
	var err error

	if err = setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	conf := config.Config{WorkingDir: workingDir}

	downloaderObj, err = downloader.New(&conf, new(fakeFcrypt))
	if err != nil {
		log.Fatalf("Error creation downloader: %s", err)
	}

	ret := m.Run()

	if err = cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

func TestDownload(t *testing.T) {
	sentData := []byte("Hello downloader\n")
	fileName := "package.txt"

	err := ioutil.WriteFile(path.Join(serverDir, fileName), sentData, 0644)
	if err != nil {
		log.Fatalf("Can't create package file: %s", err)
	}
	packageInfo := preparePackageInfo(fileName)
	chains := []amqp.CertificateChain{}
	certs := []amqp.Certificate{}

	_, err = downloaderObj.DownloadAndDecrypt(packageInfo, chains, certs, "")
	if err != nil {
		t.Errorf("Can't DownloadAndDecrypt %s", err)
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

func (sigCont fakeSignContext) AddCertificate(fingerprint string, asn1Bytes []byte) (err error) {
	return nil
}

func (sigCont fakeSignContext) AddCertificateChain(name string, fingerprints []string) (err error) {
	return nil
}

func (sigCont fakeSignContext) VerifySign(f *os.File, chainName string, algName string, signValue []byte) (err error) {
	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	serverDir, err = ioutil.TempDir("", "server_")
	if err != nil {
		return err
	}

	go func() {
		log.Fatal(http.ListenAndServe(":8001", http.FileServer(http.Dir(serverDir))))
	}()

	workingDir, err = ioutil.TempDir("", "wd_")
	if err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	os.RemoveAll(workingDir)
	os.RemoveAll(serverDir)

	return nil
}

func preparePackageInfo(fileName string) (packageInfo amqp.DecryptDataStruct) {
	packageInfo.URLs = []string{"http://localhost:8001/" + fileName}

	filePath := path.Join(serverDir, fileName)
	imageFileInfo, err := image.CreateFileInfo(filePath)
	if err != nil {
		log.Error("error CreateFileInfo", err)
		return packageInfo
	}

	recInfo := struct {
		Serial string `json:"serial"`
		Issuer []byte `json:"issuer"`
	}{
		Serial: "string",
		Issuer: []byte("issuer"),
	}

	packageInfo.Sha256 = imageFileInfo.Sha256
	packageInfo.Sha512 = imageFileInfo.Sha512
	packageInfo.Size = imageFileInfo.Size
	packageInfo.DecryptionInfo = &amqp.DecryptionInfo{
		BlockAlg:     "AES256/CBC/pkcs7",
		BlockIv:      []byte{},
		BlockKey:     []byte{},
		AsymAlg:      "RSA/PKCS1v1_5",
		ReceiverInfo: &recInfo,
	}
	packageInfo.Signs = new(amqp.Signs)

	return packageInfo
}