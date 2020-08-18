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

package umclient_test

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/image"
	"gitpct.epam.com/epmd-aepr/aos_common/umprotocol"
	"gitpct.epam.com/epmd-aepr/aos_common/wsserver"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/fcrypt"
	"aos_servicemanager/umclient"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	serverURL = "wss://localhost:8089"
	tmpDir    = "/tmp/aos"

	imagePlainText = "This is a wonderful crypto update"
	// Chipher was decoded as base64 string
	// openssl enc -aes-256-cbc -nosalt -e -in file.data -out file.data.crypted -iv '66B86B273FF34FCE' -K '7786B273FF34FCE19D6B804EFF5A3F55'
	// cat file.data.crypted | base64
	imageChipherText = "GJlYICHYJ1dgXLDHZ+WGzjePWZ1lMBd+tMlBJ3n9Nx3epyWzqegBNamUfemIl45L"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type operationStatus struct {
	status       string
	err          string
	imageVersion uint64
}

// Test sender
type testSender struct {
	upgradeStatusChannel chan operationStatus
	revertStatusChannel  chan operationStatus
}

type messageProcessor struct {
}

type testStorage struct {
	state   int
	data    amqp.SystemUpgrade
	version uint64
}

type clientHandler struct {
}

type testCertProvider struct {
	keyPath string
}

type updateDownloader struct {
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var (
	sender *testSender
	server *wsserver.Server
	client *umclient.Client
)

var (
	imageVersion     uint64
	operationVersion uint64
)

var storage testStorage

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
	if err := os.MkdirAll(path.Join(tmpDir, "fileServer"), 0755); err != nil {
		log.Fatalf("Can't crate file server dir: %s", err)
	}

	go func() {
		log.Fatal(http.ListenAndServe(":8081", http.FileServer(http.Dir(path.Join(tmpDir, "fileServer")))))
	}()

	url, err := url.Parse(serverURL)
	if err != nil {
		log.Fatalf("Can't parse url: %s", err)
	}

	server, err = wsserver.New("TestServer", url.Host,
		"../ci/crt.pem",
		"../ci/key.pem", new(clientHandler))
	if err != nil {
		log.Fatalf("Can't create ws server: %s", err)
	}
	defer server.Close()

	// Wait for server becomes ready
	time.Sleep(1 * time.Second)

	sender = newTestSender()

	rootCert := []byte(`
-----BEGIN CERTIFICATE-----
MIIEAjCCAuqgAwIBAgIJAPwk2NFfSDPjMA0GCSqGSIb3DQEBCwUAMIGNMRcwFQYD
VQQDDA5GdXNpb24gUm9vdCBDQTEpMCcGCSqGSIb3DQEJARYadm9sb2R5bXlyX2Jh
YmNodWtAZXBhbS5jb20xDTALBgNVBAoMBEVQQU0xHDAaBgNVBAsME05vdnVzIE9y
ZG8gU2VjbG9ydW0xDTALBgNVBAcMBEt5aXYxCzAJBgNVBAYTAlVBMB4XDTE4MDQx
MDExMzMwMFoXDTI2MDYyNzExMzMwMFowgY0xFzAVBgNVBAMMDkZ1c2lvbiBSb290
IENBMSkwJwYJKoZIhvcNAQkBFhp2b2xvZHlteXJfYmFiY2h1a0BlcGFtLmNvbTEN
MAsGA1UECgwERVBBTTEcMBoGA1UECwwTTm92dXMgT3JkbyBTZWNsb3J1bTENMAsG
A1UEBwwES3lpdjELMAkGA1UEBhMCVUEwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAw
ggEKAoIBAQC+K2ow2HO7+SUVfOq5tTtmHj4LQijHJ803mLk9pkPef+Glmeyp9HXe
jDlQC04MeovMBeNTaq0wibf7qas9niXbeXRVzheZIFziMXqRuwLqc0KXdDxIDPTb
TW3K0HE6M/eAtTfn9+Z/LnkWt4zMXasc02hvufsmIVEuNbc1VhrsJJg5uk88ldPM
LSF7nff9eYZTHYgCyBkt9aL+fwoXO6eSDSAhjopX3lhdidkM+ni7EOhlN7STmgDM
WKh9nMjXD5f28PGhtW/dZvn4SzasRE5MeaExIlBmhkWEUgVCyP7LvuQGRUPK+NYz
FE2CLRuirLCWy1HIt9lLziPjlZ4361mNAgMBAAGjYzBhMB0GA1UdDgQWBBR0Shhz
OuM95BhD0mWxC1j+KrE6UjAMBgNVHRMEBTADAQH/MAsGA1UdDwQEAwIBBjAlBgNV
HREEHjAcgRp2b2xvZHlteXJfYmFiY2h1a0BlcGFtLmNvbTANBgkqhkiG9w0BAQsF
AAOCAQEAl8bv1HTYe3l4Y+g0TVZR7bYL5BNsnGgqy0qS5fu991khXWf+Zwa2MLVn
YakMnLkjvdHqUpWMJ/S82o2zWGmmuxca56ehjxCiP/nkm4M74yXz2R8cu52WxYnF
yMvgawzQ6c1yhvZiv/gEE7KdbYRVKLHPgBzfyup21i5ngSlTcMRRS7oOBmoye4qc
6adq6HtY6X/OnZ9I5xoRN1GcvaLUgUE6igTiVa1pF8kedWhHY7wzTXBxzSvIZkCU
VHEOzvaGk9miP6nBrDfNv7mIkgEKARrjjSpmJasIEU+mNtzeOIEiMtW1EMRc457o
0PdFI3jseyLVPVhEzUkuC7mwjb7CeQ==
-----END CERTIFICATE-----
`)

	if err := ioutil.WriteFile(path.Join(tmpDir, "rootCert.pem"), rootCert, 0644); err != nil {
		log.Fatalf("Cannot create root cert: %s", err)
	}

	client, err = umclient.New(&config.Config{UpgradeDir: path.Join(tmpDir, "/upgrade")}, sender, &storage)
	if err != nil {
		log.Fatalf("Error creating UM client: %s", err)
	}

	client.SetDownloader(new(updateDownloader))

	go func() {
		<-client.ErrorChannel
	}()

	ret := m.Run()

	if err = client.Close(); err != nil {
		log.Fatalf("Error closing UM: %s", err)
	}

	if err := os.RemoveAll(tmpDir); err != nil {
		log.Fatalf("Error removing tmp dir: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestGetSystemVersion(t *testing.T) {
	imageVersion = 4

	if err := client.Connect(serverURL); err != nil {
		log.Fatalf("Error connecting to UM server: %s", err)
	}
	defer client.Disconnect()

	version, err := client.GetSystemVersion()
	if err != nil {
		t.Fatalf("Can't get system version: %s", err)
	}

	if version != imageVersion {
		t.Errorf("Wrong image version: %d", version)
	}

	client.Disconnect()
}

func TestSystemUpgrade(t *testing.T) {
	if err := client.Connect(serverURL); err != nil {
		log.Fatalf("Error connecting to UM server: %s", err)
	}
	defer client.Disconnect()

	if _, err := client.GetSystemVersion(); err != nil {
		t.Errorf("Can't get system version: %s", err)
	}

	data, err := createUpgradeData(5, "imagefile", []byte(imagePlainText))
	if err != nil {
		t.Fatalf("Can't create upgrade data: %s", err)
	}

	client.SystemUpgrade(data)

	// wait for upgrade status
	select {
	case status := <-sender.upgradeStatusChannel:
		if status.err != "" {
			t.Errorf("Upgrade failed: %s", status.err)
		}

	case <-time.After(1 * time.Second):
		t.Error("Waiting for upgrade status timeout")
	}

	// Try to upgrade the same version
	client.SystemUpgrade(data)

	select {
	case status := <-sender.upgradeStatusChannel:
		if status.err != "" {
			t.Errorf("Upgrade failed: %s", status.err)
		}

	case <-time.After(1 * time.Second):
		t.Error("Waiting for upgrade status timeout")
	}
}

func TestRevertUpgrade(t *testing.T) {
	imageVersion = 4

	if err := client.Connect(serverURL); err != nil {
		log.Fatalf("Error connecting to UM server: %s", err)
	}
	defer client.Disconnect()

	if _, err := client.GetSystemVersion(); err != nil {
		t.Errorf("Can't get system version: %s", err)
	}

	client.SystemRevert(3)

	// wait for revert status
	select {
	case status := <-sender.revertStatusChannel:
		if status.err != "" {
			t.Errorf("Revert failed: %s", status.err)
		}

	case <-time.After(1 * time.Second):
		t.Error("Waiting for revert status timeout")
	}

	// Try to revert the same version
	client.SystemRevert(3)

	select {
	case status := <-sender.revertStatusChannel:
		if status.err != "" {
			t.Errorf("Revert failed: %s", status.err)
		}

	case <-time.After(1 * time.Second):
		t.Error("Waiting for upgrade status timeout")
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (storage *testStorage) SetUpgradeState(state int) (err error) {
	storage.state = state

	return nil
}

func (storage *testStorage) GetUpgradeState() (state int, err error) {
	return storage.state, nil
}

func (storage *testStorage) SetUpgradeData(data amqp.SystemUpgrade) (err error) {
	storage.data = data

	return nil
}

func (storage *testStorage) GetUpgradeData() (data amqp.SystemUpgrade, err error) {
	return storage.data, nil
}

func (storage *testStorage) SetUpgradeVersion(version uint64) (err error) {
	storage.version = version

	return nil
}

func (storage *testStorage) GetUpgradeVersion() (version uint64, err error) {
	return storage.version, nil
}

func (downloader *updateDownloader) DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
	chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error) {
	srcFile := packageInfo.URLs[0]

	cpCmd := exec.Command("cp", srcFile, decryptDir)
	err = cpCmd.Run()

	return path.Join(decryptDir, filepath.Base(srcFile)), err
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newTestSender() (sender *testSender) {
	sender = &testSender{}

	sender.revertStatusChannel = make(chan operationStatus, 1)
	sender.upgradeStatusChannel = make(chan operationStatus, 1)

	return sender
}

func (sender *testSender) SendSystemRevertStatus(revertStatus, revertError string, imageVersion uint64) (err error) {
	sender.revertStatusChannel <- operationStatus{revertStatus, revertError, imageVersion}

	return nil
}

func (sender *testSender) SendSystemUpgradeStatus(upgradeStatus, upgradeError string, imageVersion uint64) (err error) {
	sender.upgradeStatusChannel <- operationStatus{upgradeStatus, upgradeError, imageVersion}

	return nil
}

func (sender *testSender) SendIssueUnitCertificatesRequest(requests []amqp.CertificateRequest) (err error) {
	return nil
}

func (sender *testSender) SendInstallCertificatesConfirmation(confirmation []amqp.CertificateConfirmation) (err error) {
	return nil
}

func (handler clientHandler) ProcessMessage(client *wsserver.Client, messageType int, messageIn []byte) (messageOut []byte, err error) {
	var message umprotocol.Message
	var response interface{}

	log.Debug(string(messageIn))

	if err = json.Unmarshal(messageIn, &message); err != nil {
		return nil, err
	}

	switch message.Header.MessageType {
	case umprotocol.StatusRequestType:
		response = umprotocol.StatusRsp{
			Operation:        umprotocol.UpgradeOperation,
			Status:           umprotocol.SuccessStatus,
			RequestedVersion: operationVersion,
			CurrentVersion:   imageVersion}

	case umprotocol.UpgradeRequestType:
		var upgradeReq umprotocol.UpgradeReq

		if err = json.Unmarshal(message.Data, &upgradeReq); err != nil {
			return nil, err
		}

		operationVersion = upgradeReq.ImageVersion
		imageVersion = upgradeReq.ImageVersion

		status := umprotocol.SuccessStatus
		errStr := ""

		fileName := path.Join(tmpDir, "upgrade/", upgradeReq.ImageInfo.Path)

		if err = image.CheckFileInfo(fileName, image.FileInfo{
			Sha256: upgradeReq.ImageInfo.Sha256,
			Sha512: upgradeReq.ImageInfo.Sha512,
			Size:   upgradeReq.ImageInfo.Size}); err != nil {
			status = umprotocol.FailedStatus
			errStr = err.Error()
			break
		}

		data, err := ioutil.ReadFile(fileName)
		if err != nil {
			status = umprotocol.FailedStatus
			errStr = err.Error()
			break
		}

		if imagePlainText != string(data) {
			status = umprotocol.FailedStatus
			errStr = "image file content mismatch"
			break
		}

		response = umprotocol.StatusRsp{
			Operation:        umprotocol.UpgradeOperation,
			Status:           status,
			Error:            errStr,
			RequestedVersion: operationVersion,
			CurrentVersion:   imageVersion}

	case umprotocol.RevertRequestType:
		var revertReq umprotocol.UpgradeReq

		if err = json.Unmarshal(message.Data, &revertReq); err != nil {
			return nil, err
		}

		operationVersion = revertReq.ImageVersion
		imageVersion = revertReq.ImageVersion

		response = umprotocol.StatusRsp{
			Operation:        umprotocol.RevertOperation,
			Status:           umprotocol.SuccessStatus,
			RequestedVersion: operationVersion,
			CurrentVersion:   imageVersion}

	default:
		return nil, fmt.Errorf("unsupported message type: %s", message.Header.MessageType)
	}

	message.Header.MessageType = umprotocol.StatusResponseType

	if message.Data, err = json.Marshal(response); err != nil {
		return nil, err
	}

	if messageOut, err = json.Marshal(message); err != nil {
		return nil, err
	}

	return messageOut, nil
}

func (handler clientHandler) ClientConnected(client *wsserver.Client) {

}

func (handler clientHandler) ClientDisconnected(client *wsserver.Client) {

}

func createUpgradeData(version uint64, fileName string, content []byte) (data amqp.SystemUpgrade, err error) {
	filePath := path.Join(tmpDir, "fileServer", fileName)

	if err = ioutil.WriteFile(filePath, content, 0644); err != nil {
		return data, err
	}

	data.ImageVersion = version
	data.URLs = []string{filePath}

	imageFileInfo, err := image.CreateFileInfo(filePath)
	if err != nil {
		return data, err
	}

	data.Sha256 = imageFileInfo.Sha256
	data.Sha512 = imageFileInfo.Sha512
	data.Size = imageFileInfo.Size

	recInfo := struct {
		Serial string `json:"serial"`
		Issuer []byte `json:"issuer"`
	}{
		Serial: "string",
		Issuer: []byte("issuer"),
	}

	data.DecryptionInfo = &amqp.DecryptionInfo{
		ReceiverInfo: &recInfo,
	}

	return data, nil
}

func (provider testCertProvider) GetCertificateForSM(request fcrypt.RetrieveCertificateRequest) (
	resp fcrypt.RetrieveCertificateResponse, err error) {

	absPath, _ := filepath.Abs(provider.keyPath)
	resp.KeyURL = "file://" + absPath

	return resp, err
}
