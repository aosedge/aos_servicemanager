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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
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
	serverURL      = "wss://localhost:8089"
	tmpDir         = "/tmp/aos"
	imageAes256Key = "7786B273FF34FCE19D6B804EFF5A3F55"
	imageAes256IV  = "66B86B273FF34FCE"
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

var OfflinePrivKeyPem = []byte(`
-----BEGIN PRIVATE KEY-----
MIIEpQIBAAKCAQEA2/gmKbqsk3azfArIxz2GgGnuJj5xWGSyyvAX5cB5oWMlRD8i
bNNQJm2BIBMlPxLfYUygpEInJaeUI0H1iz5RO5sYEABP6sY7pXvX8AzUqVSQ6TY0
0J39kgPsetRvnZUT9hXYLJ43YCis9psiRTBS4fuSuZK+ZhYojsE4E0iPj08yFVqj
KHZFOuEdLQLTPWJ0e3ORHQX7v9aADt4t+ESOxf3gJs+Vxbk57Bzq1MBlYh83tB/b
pf/vy/gzgVzLXBsEalw3o0Lo9aypCnhwNXwGgfqhHq9Cn4sn7cpozpMaWuvoJYdV
6/i2VzVQrHkWP0N3luxSvEMNG7FgXyK4CEupSQIDAQABAoIBAGhADzYvtqKc2yuq
oMVsr1Yk3i1Z4rYV43aym2DT+9E0//B8S4BwFchglZXx/PELrLqcanXutEbwSRD8
rba0biNlud27iCSolpQzQYAPVKp73cHpYtaMSiTtnyIHlG6GvNMgPzfGNFBqdq7Z
j0BjSqS3ai5xEbOoRMiDYmQhO4ibCqQUqsvZJJNmNG9VFKVW0gdacBzfq/RYV1Yj
6P0MVMIiZHulxrg2SBSDNMNpJXFWHUVOJ2HdZeyEM1/eL9PGK+Ys58THLnV6GlAD
icq6okOSBHoS3257BKqtWZTWlemp7na52IYc2Ll4VUVpAZzPRpNyF9IUmL4rCNzc
vE+BEJECgYEA/U2Ml5CQvYWFk3PP8rRQUhqP4yUvtzxFlAPkrZSdYm/KnBSCjXBe
3K6JHzUpZFLO1yXGuMS1z7pITZ6p/8KX5VZvLmgUTANFf+A2omrpGcZFqAfbPGtI
8B8voupOn8/iLRZXziZueGbugSWo/iiC8NiNl4hpgxrLVQqR8EZKHh8CgYEA3k+9
TntpkLRQCIpWjJPr/ri/FsfNwUQyxEuhx5fdOf+Q8M8xBdgp3X9r0766s7IEVJfh
fPMeeg6i8TnjGLrLOeDZ5flaTXRcJqzL/iMoGUnU4tWwG/PAj2Pm7ZFRR95ZvnuM
5zc0NDGcgYq3o7pu3lnxUKWnRZqqUDw4vrCFe5cCgYEA2UTpcSAJZubemoneNpo/
ww0Rmo5NDWjfbYShY9pz3Ply2sok6VkXpUb4SxJ4fJsi3ByFBfuEz7dDSYDs5Hpv
e8HWAAI6VrD/rh4N/uahJwCQwv5qKLsFhyHY5G8CHcZchLwDeMoyO4hez9wTxl3N
YvT9DpttlY0oF7vHTkecT5UCgYEAzsBuGM1h8jgfrrGpqHfxpSYAYZlU3AcnB7Qn
M08jacsq6ypmNz9AQEU+7OCXFoPazympheE9WNq/44SoldkzJBLf06fBugMbqMRP
u3zK0CoAGS4O6RAa58BLhmn9o89Au4yAEJEgteHl4fw2qci7T4NqkExfcrZS6uf3
BjF5EuUCgYEAigRZNLDfltWlVm9ErwWYWaSCGZpXkWCtoPqynvVYHRO2aWxUZg7x
7bkO7pE+LAjRyi3CF+qhVKJKfiKSAQcFWDHGR+xDHh3B82QUjL7qkzqkGf42vx2S
GCeMmVJw+HzT3w19CoTvoDlnW2GHPpNXhOzjl3DD4Y0PUBhNX5sOROM=
-----END PRIVATE KEY-----
`)

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
		log.Fatal(http.ListenAndServe(":8080", http.FileServer(http.Dir(path.Join(tmpDir, "fileServer")))))
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

	crypt, err := fcrypt.CreateContext(config.Crypt{CACert: path.Join(tmpDir, "rootCert.pem")})
	if err != nil {
		log.Fatalf("Cannot create crypto context: %s", err)
	}

	if err := ioutil.WriteFile(path.Join(tmpDir, "offline.key.pem"), OfflinePrivKeyPem, 0644); err != nil {
		log.Fatalf("Cannot create root cert: %s", err)
	}

	if err = crypt.LoadOfflineKeyFile(path.Join(tmpDir, "offline.key.pem")); err != nil {
		log.Fatalf("Cannot load offline key file: %s", err)
	}

	client, err = umclient.New(&config.Config{UpgradeDir: path.Join(tmpDir, "/upgrade")}, crypt, sender, &storage)
	if err != nil {
		log.Fatalf("Error creating UM client: %s", err)
	}

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
	if os.Getenv("CI") != "" {
		log.Debug("Skip TestServiceStorage")
		return
	}

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
	cryptedRaw, err := base64.StdEncoding.DecodeString(imageChipherText)
	if err != nil {
		return data, err
	}

	filePath := path.Join(tmpDir, "fileServer", fileName)

	if err = ioutil.WriteFile(filePath, cryptedRaw, 0644); err != nil {
		return data, err
	}

	data.ImageVersion = version
	data.URLs = []string{"http://localhost:8080/" + fileName}

	imageFileInfo, err := image.CreateFileInfo(filePath)
	if err != nil {
		return data, err
	}

	data.Sha256 = imageFileInfo.Sha256
	data.Sha512 = imageFileInfo.Sha512
	data.Size = imageFileInfo.Size

	signValue, err := base64.StdEncoding.DecodeString("KHwPwgdcu9BbICvhVkOAW79SEB/2GsFwME8xd/r0z+aC7Ne6bpQtXzXLGrjYLsqvWKg5CNn0kN8PbHCM3K18Zvy+vESTuUzvqucQxEt7IvQdF6x0k9lylstwuBgz8hL4xNF3/SJcm7n1GYomyaxr2DKiV7qW9d6ChXemUniaf5pD2BYJXzZ+g7lyv/jrm/MH/pIuW2X5KwT1G4k+q9VvMAHV6mSdf0JncEUe9kkRdgfjzUxhixOCrpA8/sGlUBDYvy0edYgz34pjOi0B8oc3/W3L4ItBKKLK8MZNGr4Oz0UL8cTNLDW885MUKbeWV0RQzilb13hh/lERdXKKBmRjwA==")
	if err != nil {
		return data, err
	}

	sign := amqp.Signs{
		Alg:              "RSA/SHA256",
		ChainName:        "8D28D60220B8D08826E283B531A0B1D75359C5EE",
		TrustedTimestamp: "",
		Value:            signValue,
	}

	data.Signs = &sign
	data.CertificateChains = []amqp.CertificateChain{
		{
			Name: "8D28D60220B8D08826E283B531A0B1D75359C5EE",
			Fingerprints: []string{
				"48FAC66F9994BA0EA0BC71EE6E0CAB79A0A2E6DF",
				"FE232D8F645F9550D2AB559BF3144CAEB6534F69",
				"EE9E93D52D84CF3CBEB2E327635770458457B7C2",
			},
		},
	}

	cert1, _ := base64.StdEncoding.DecodeString("MIIDmTCCAoGgAwIBAgIUPQwLpw4adg7kY7MUGg3/SkGgAbEwDQYJKoZIhvcNAQELBQAwWzEgMB4GA1UEAwwXQU9TIE9FTSBJbnRlcm1lZGlhdGUgQ0ExDTALBgNVBAoMBEVQQU0xDDAKBgNVBAsMA0FPUzENMAsGA1UEBwwES3lpdjELMAkGA1UEBhMCVUEwHhcNMTkwNzMxMDg0MzU5WhcNMjIwNzMwMDg0MzU5WjBBMSIwIAYDVQQDDBlBb1MgVGFyZ2V0IFVwZGF0ZXMgU2lnbmVyMQ0wCwYDVQQKDARFUEFNMQwwCgYDVQQLDANBb1MwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDJlnvb/zEu1N9C3xRnEjLivUr/1LeLeBNgFtb9BTk7SJXDkWFzoTryhzeKhTF0EhoFx5qLbxSpwhcoKHi9ds0AngP2amQ7AhWHRgWIwX/phZVHY+rh3iTTYDsdorReUAVDCPEYpus+XfaZfH9/0E/byNq3kIzyL4u7YvZS2+F+LTV/7nvh8U5d9RCPoUfnfGj7InZF7adVLzh32KlBeDkVLJWGdfWQ6lTdk6uoRgsXtT944Q01TGJfUhbgr9MUeyi4k3L0vfXM7wMEIrzf7horhNcT/UyU5Ftc2BI3FQv+zy3LrbnwFZdJ+0gKV7Ibp9LbVSBJb+fBc4U047EGX117AgMBAAGjbzBtMAkGA1UdEwQCMAAwHQYDVR0OBBYEFNGkZujhIhZqnHHAdptjquk1k2uVMB8GA1UdIwQYMBaAFHGorK9rv0A9+zixyID2S7vfxyfgMAsGA1UdDwQEAwIFoDATBgNVHSUEDDAKBggrBgEFBQcDAzANBgkqhkiG9w0BAQsFAAOCAQEAKjS2CTWEvck1ZFVpNL81Er9MzVvnMNmHDXmkBPO+WcIcQ4RnOZGKfe9z0UX3/bVoAVyBPpVHAkczmxJQTijGC5c6anhXt06uGXyutArlrNFD75ToQOi8JBdBNvB9QfnoYqZ9CHgyYN4sKXGluyGwKMjtwADBBGkV+PR/1KqI+qHvP831Ujylb7WeSOH5RBC6jYB34Mmp5AcnIX4B7HHKFb8NitxX7Kxynua2sUgs5D1eJeOx4v6hTnP8Hto7bBkU9qYaOyJD7H9V6lfyQvxkA8iva5zYvNoQ2zWLnnSK78yrVRphW0J1gB1FW4ZsKvfsbk9fyxpCARyRXhjU8H7SDg==")
	cert2, _ := base64.StdEncoding.DecodeString("MIIDrjCCApagAwIBAgIJAO2BVuwqJLb6MA0GCSqGSIb3DQEBCwUAMFQxGTAXBgNVBAMMEEFvUyBTZWNvbmRhcnkgQ0ExDTALBgNVBAoMBEVQQU0xDDAKBgNVBAsMA0FvUzENMAsGA1UEBwwES3lpdjELMAkGA1UEBhMCVUEwHhcNMTkwMzIxMTMyMjM2WhcNMjUwMzE5MTMyMjM2WjBbMSAwHgYDVQQDDBdBT1MgT0VNIEludGVybWVkaWF0ZSBDQTENMAsGA1UECgwERVBBTTEMMAoGA1UECwwDQU9TMQ0wCwYDVQQHDARLeWl2MQswCQYDVQQGEwJVQTCCASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBAJpf1Zj3yNj/Gl7PRPevf7PYpvMZVgwZRLEuwqfXxvDHYhfb/Kp7xAH7QeZVfB8rINSpJbv1+KcqiCzCZig32H+Iv5cGlyn1xmXCihHySH4/XRyduHGue385dNDyXRExpFGXAru/AhgXGKVaxbfDwE9lnz8yWRFvhNrdPO8/nRNZf1ZOqRaq7YNYC7kRQLgp76Da64/H7OWWy9B82r+fgEKc63ixDWSqaLGcNgIBqHU+Rky/SX/gPUtaCIqJb+CpWZZQlJ2wQ+dv+s+K2AG7O0HSHQkh2BbMcjVDeCcdu477+Mal8+MhhjYzkQmAi1tVOYAzX2H/StCGSYohtpxqT5ECAwEAAaN8MHowDAYDVR0TBAUwAwEB/zAdBgNVHQ4EFgQUcaisr2u/QD37OLHIgPZLu9/HJ+AwHwYDVR0jBBgwFoAUNrDxTEYV6uDVs6xHNU77q9zVmMowCwYDVR0PBAQDAgGmMB0GA1UdJQQWMBQGCCsGAQUFBwMBBggrBgEFBQcDAjANBgkqhkiG9w0BAQsFAAOCAQEAM+yZJvfkSQmXYt554Zsy2Wqm1w/ixUV55T3r0JFRFbf5AQ7RNBp7t2dn1cEUA6O4wSK3U1Y7eyrPn/ECNmbZ5QUlHUAN/QUnuJUIe0UEd9k+lO2VLbLK+XamzDlOxPBn94s9C1jeXrwGdeTRVcq73rH1CvIOMhD7rp/syQKFuBfQBwCgfH0CbSRsHRm9xQii/HQYMfD8TMyqrjMKF7s68r7shQG2OGo1HJqfA6f9Cb+i4A1BfeP97lFeyr3OjQtLcQJ/a6nPdGs1Cg94Zl2PBEPFH9ecuYpKt0UqK8x8HRsYru7Wp8wkzMbvlYShI5mwdIpvksg5aqnIhWWGqhDRqg==")
	cert3, _ := base64.StdEncoding.DecodeString("MIID4TCCAsmgAwIBAgIJAO2BVuwqJLb4MA0GCSqGSIb3DQEBCwUAMIGNMRcwFQYDVQQDDA5GdXNpb24gUm9vdCBDQTEpMCcGCSqGSIb3DQEJARYadm9sb2R5bXlyX2JhYmNodWtAZXBhbS5jb20xDTALBgNVBAoMBEVQQU0xHDAaBgNVBAsME05vdnVzIE9yZG8gU2VjbG9ydW0xDTALBgNVBAcMBEt5aXYxCzAJBgNVBAYTAlVBMB4XDTE5MDMyMTEzMTQyNVoXDTI1MDMxOTEzMTQyNVowVDEZMBcGA1UEAwwQQW9TIFNlY29uZGFyeSBDQTENMAsGA1UECgwERVBBTTEMMAoGA1UECwwDQW9TMQ0wCwYDVQQHDARLeWl2MQswCQYDVQQGEwJVQTCCASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBALyDuKMpBZN/kQFHzKo8N8y1EoPgG5sazSRe0O5xL7lm78hBmp4Vpsm/BYSI8NElkxdOTjqQG6KK0HAyCCfQJ7MnI3G/KnJ9wxD/SWjye0/Wr5ggo1H3kFhSd9HKtuRsZJY6E4BSz4yzburCIILC4ZvS/755OAAFX7g1IEsPeKh8sww1oGLL0xeg8W0CWmWO9PRno5Dl7P5QHR02BKrEwZ/DrpSpsE+ftTczxaPp/tzqp2CDGWYT5NoBfxP3W7zjKmTCECVgM/c29P2/AL4J8xXydDlSujvE9QG5g5UUz/dlBbVXFv0cK0oneADe0D4aRK5sMH2ZsVFaaZAd2laa7+MCAwEAAaN8MHowDAYDVR0TBAUwAwEB/zAdBgNVHQ4EFgQUNrDxTEYV6uDVs6xHNU77q9zVmMowHwYDVR0jBBgwFoAUdEoYczrjPeQYQ9JlsQtY/iqxOlIwCwYDVR0PBAQDAgGmMB0GA1UdJQQWMBQGCCsGAQUFBwMBBggrBgEFBQcDAjANBgkqhkiG9w0BAQsFAAOCAQEAe1IT/RhZ690PIBlkzLDutf0zfs2Ei6jxTyCYxiEmTExrU0qCZECxu/8Up6jpgqHN5upEdL/kDWwtogn0K0NGBqMNiDyc7f18rVvq/5nZBl7P+56h5DcuLJsUb3tCC5pIkV9FYeVCg+Ub5c59b3hlFpqCmxSvDzNnRZZcr+dInAdjcVZWmAisIpoBPrtCrqGydBtP9wy5PPxUW2bwhov4FV58C+WZ7GOLMqF+G0wAlE7RUWvuUcKYVukkDjAg0g2qE01LnPBtpJ4dsYtEJnQknJR4swtnWfCcmlHQrbDoi3MoksAeGSFZePQKpht0vWiimHFQCHV2RS9P8oMqFhZN0g==")

	data.Certificates = []amqp.Certificate{
		{
			Fingerprint: "48FAC66F9994BA0EA0BC71EE6E0CAB79A0A2E6DF",
			Certificate: cert1,
		},
		{
			Fingerprint: "FE232D8F645F9550D2AB559BF3144CAEB6534F69",
			Certificate: cert2,
		},
		{
			Fingerprint: "EE9E93D52D84CF3CBEB2E327635770458457B7C2",
			Certificate: cert3,
		},
	}

	publicKeyRaw, _ := pem.Decode(OfflinePrivKeyPem)
	privKeyImported, err := x509.ParsePKCS1PrivateKey(publicKeyRaw.Bytes)
	if err != nil {
		return data, err
	}

	encryptedAesKey, err := rsa.EncryptPKCS1v15(rand.Reader, &privKeyImported.PublicKey, []byte(imageAes256Key))
	if err != nil {
		return data, err
	}

	data.DecryptionInfo = &amqp.DecryptionInfo{
		BlockAlg: "AES256/CBC/pkcs7",
		BlockIv:  []byte(imageAes256IV),
		BlockKey: encryptedAesKey,
		AsymAlg:  "RSA/PKCS1v1_5",
	}

	return data, nil
}
