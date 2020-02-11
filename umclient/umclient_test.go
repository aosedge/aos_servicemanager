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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/nunc-ota/aos_common/umprotocol"
	"gitpct.epam.com/nunc-ota/aos_common/wsserver"

	"aos_servicemanager/amqphandler"
	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/database"
	"aos_servicemanager/fcrypt"
	"aos_servicemanager/image"
	"aos_servicemanager/umclient"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const serverURL = "wss://localhost:8089"

const (
	imageFile = "This is image file"
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

/*******************************************************************************
 * Vars
 ******************************************************************************/

var (
	db     *database.Database
	sender *testSender
	server *wsserver.Server
	client *umclient.Client
)

var (
	imageVersion     uint64
	operationVersion uint64
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
	if err := os.MkdirAll("tmp/fileServer", 0755); err != nil {
		log.Fatalf("Can't crate file server dir: %s", err)
	}

	go func() {
		log.Fatal(http.ListenAndServe(":8080", http.FileServer(http.Dir("tmp/fileServer"))))
	}()

	url, err := url.Parse(serverURL)
	if err != nil {
		log.Fatalf("Can't parse url: %s", err)
	}

	server, err = wsserver.New("TestServer", url.Host,
		"../vendor/gitpct.epam.com/nunc-ota/aos_common/wsserver/data/crt.pem",
		"../vendor/gitpct.epam.com/nunc-ota/aos_common/wsserver/data/key.pem", processMessage)
	if err != nil {
		log.Fatalf("Can't create ws server: %s", err)
	}
	defer server.Close()

	sender = newTestSender()

	db, err = database.New("tmp/servicemanager.db")
	if err != nil {
		log.Fatalf("Can't create db: %s", err)
	}

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

	if err := ioutil.WriteFile("tmp/rootCert.pem", rootCert, 0644); err != nil {
		log.Fatalf("Can't create root cert: %s", err)
	}

	crypt, err := fcrypt.CreateContext(config.Crypt{CACert: "tmp/rootCert.pem"})
	if err != nil {
		log.Fatalf("Can't create crypto context: %s", err)
	}

	client, err = umclient.New(&config.Config{UpgradeDir: "tmp/upgrade"}, crypt, sender, db)
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

	if err := os.RemoveAll("tmp"); err != nil {
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
	cert1, _ := base64.StdEncoding.DecodeString("MIIDmTCCAoGgAwIBAgIUPQwLpw4adg7kY7MUGg3/SkGgAbEwDQYJKoZIhvcNAQELBQAwWzEgMB4GA1UEAwwXQU9TIE9FTSBJbnRlcm1lZGlhdGUgQ0ExDTALBgNVBAoMBEVQQU0xDDAKBgNVBAsMA0FPUzENMAsGA1UEBwwES3lpdjELMAkGA1UEBhMCVUEwHhcNMTkwNzMxMDg0MzU5WhcNMjIwNzMwMDg0MzU5WjBBMSIwIAYDVQQDDBlBb1MgVGFyZ2V0IFVwZGF0ZXMgU2lnbmVyMQ0wCwYDVQQKDARFUEFNMQwwCgYDVQQLDANBb1MwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDJlnvb/zEu1N9C3xRnEjLivUr/1LeLeBNgFtb9BTk7SJXDkWFzoTryhzeKhTF0EhoFx5qLbxSpwhcoKHi9ds0AngP2amQ7AhWHRgWIwX/phZVHY+rh3iTTYDsdorReUAVDCPEYpus+XfaZfH9/0E/byNq3kIzyL4u7YvZS2+F+LTV/7nvh8U5d9RCPoUfnfGj7InZF7adVLzh32KlBeDkVLJWGdfWQ6lTdk6uoRgsXtT944Q01TGJfUhbgr9MUeyi4k3L0vfXM7wMEIrzf7horhNcT/UyU5Ftc2BI3FQv+zy3LrbnwFZdJ+0gKV7Ibp9LbVSBJb+fBc4U047EGX117AgMBAAGjbzBtMAkGA1UdEwQCMAAwHQYDVR0OBBYEFNGkZujhIhZqnHHAdptjquk1k2uVMB8GA1UdIwQYMBaAFHGorK9rv0A9+zixyID2S7vfxyfgMAsGA1UdDwQEAwIFoDATBgNVHSUEDDAKBggrBgEFBQcDAzANBgkqhkiG9w0BAQsFAAOCAQEAKjS2CTWEvck1ZFVpNL81Er9MzVvnMNmHDXmkBPO+WcIcQ4RnOZGKfe9z0UX3/bVoAVyBPpVHAkczmxJQTijGC5c6anhXt06uGXyutArlrNFD75ToQOi8JBdBNvB9QfnoYqZ9CHgyYN4sKXGluyGwKMjtwADBBGkV+PR/1KqI+qHvP831Ujylb7WeSOH5RBC6jYB34Mmp5AcnIX4B7HHKFb8NitxX7Kxynua2sUgs5D1eJeOx4v6hTnP8Hto7bBkU9qYaOyJD7H9V6lfyQvxkA8iva5zYvNoQ2zWLnnSK78yrVRphW0J1gB1FW4ZsKvfsbk9fyxpCARyRXhjU8H7SDg==")
	cert2, _ := base64.StdEncoding.DecodeString("MIIDrjCCApagAwIBAgIJAO2BVuwqJLb6MA0GCSqGSIb3DQEBCwUAMFQxGTAXBgNVBAMMEEFvUyBTZWNvbmRhcnkgQ0ExDTALBgNVBAoMBEVQQU0xDDAKBgNVBAsMA0FvUzENMAsGA1UEBwwES3lpdjELMAkGA1UEBhMCVUEwHhcNMTkwMzIxMTMyMjM2WhcNMjUwMzE5MTMyMjM2WjBbMSAwHgYDVQQDDBdBT1MgT0VNIEludGVybWVkaWF0ZSBDQTENMAsGA1UECgwERVBBTTEMMAoGA1UECwwDQU9TMQ0wCwYDVQQHDARLeWl2MQswCQYDVQQGEwJVQTCCASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBAJpf1Zj3yNj/Gl7PRPevf7PYpvMZVgwZRLEuwqfXxvDHYhfb/Kp7xAH7QeZVfB8rINSpJbv1+KcqiCzCZig32H+Iv5cGlyn1xmXCihHySH4/XRyduHGue385dNDyXRExpFGXAru/AhgXGKVaxbfDwE9lnz8yWRFvhNrdPO8/nRNZf1ZOqRaq7YNYC7kRQLgp76Da64/H7OWWy9B82r+fgEKc63ixDWSqaLGcNgIBqHU+Rky/SX/gPUtaCIqJb+CpWZZQlJ2wQ+dv+s+K2AG7O0HSHQkh2BbMcjVDeCcdu477+Mal8+MhhjYzkQmAi1tVOYAzX2H/StCGSYohtpxqT5ECAwEAAaN8MHowDAYDVR0TBAUwAwEB/zAdBgNVHQ4EFgQUcaisr2u/QD37OLHIgPZLu9/HJ+AwHwYDVR0jBBgwFoAUNrDxTEYV6uDVs6xHNU77q9zVmMowCwYDVR0PBAQDAgGmMB0GA1UdJQQWMBQGCCsGAQUFBwMBBggrBgEFBQcDAjANBgkqhkiG9w0BAQsFAAOCAQEAM+yZJvfkSQmXYt554Zsy2Wqm1w/ixUV55T3r0JFRFbf5AQ7RNBp7t2dn1cEUA6O4wSK3U1Y7eyrPn/ECNmbZ5QUlHUAN/QUnuJUIe0UEd9k+lO2VLbLK+XamzDlOxPBn94s9C1jeXrwGdeTRVcq73rH1CvIOMhD7rp/syQKFuBfQBwCgfH0CbSRsHRm9xQii/HQYMfD8TMyqrjMKF7s68r7shQG2OGo1HJqfA6f9Cb+i4A1BfeP97lFeyr3OjQtLcQJ/a6nPdGs1Cg94Zl2PBEPFH9ecuYpKt0UqK8x8HRsYru7Wp8wkzMbvlYShI5mwdIpvksg5aqnIhWWGqhDRqg==")
	cert3, _ := base64.StdEncoding.DecodeString("MIID4TCCAsmgAwIBAgIJAO2BVuwqJLb4MA0GCSqGSIb3DQEBCwUAMIGNMRcwFQYDVQQDDA5GdXNpb24gUm9vdCBDQTEpMCcGCSqGSIb3DQEJARYadm9sb2R5bXlyX2JhYmNodWtAZXBhbS5jb20xDTALBgNVBAoMBEVQQU0xHDAaBgNVBAsME05vdnVzIE9yZG8gU2VjbG9ydW0xDTALBgNVBAcMBEt5aXYxCzAJBgNVBAYTAlVBMB4XDTE5MDMyMTEzMTQyNVoXDTI1MDMxOTEzMTQyNVowVDEZMBcGA1UEAwwQQW9TIFNlY29uZGFyeSBDQTENMAsGA1UECgwERVBBTTEMMAoGA1UECwwDQW9TMQ0wCwYDVQQHDARLeWl2MQswCQYDVQQGEwJVQTCCASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBALyDuKMpBZN/kQFHzKo8N8y1EoPgG5sazSRe0O5xL7lm78hBmp4Vpsm/BYSI8NElkxdOTjqQG6KK0HAyCCfQJ7MnI3G/KnJ9wxD/SWjye0/Wr5ggo1H3kFhSd9HKtuRsZJY6E4BSz4yzburCIILC4ZvS/755OAAFX7g1IEsPeKh8sww1oGLL0xeg8W0CWmWO9PRno5Dl7P5QHR02BKrEwZ/DrpSpsE+ftTczxaPp/tzqp2CDGWYT5NoBfxP3W7zjKmTCECVgM/c29P2/AL4J8xXydDlSujvE9QG5g5UUz/dlBbVXFv0cK0oneADe0D4aRK5sMH2ZsVFaaZAd2laa7+MCAwEAAaN8MHowDAYDVR0TBAUwAwEB/zAdBgNVHQ4EFgQUNrDxTEYV6uDVs6xHNU77q9zVmMowHwYDVR0jBBgwFoAUdEoYczrjPeQYQ9JlsQtY/iqxOlIwCwYDVR0PBAQDAgGmMB0GA1UdJQQWMBQGCCsGAQUFBwMBBggrBgEFBQcDAjANBgkqhkiG9w0BAQsFAAOCAQEAe1IT/RhZ690PIBlkzLDutf0zfs2Ei6jxTyCYxiEmTExrU0qCZECxu/8Up6jpgqHN5upEdL/kDWwtogn0K0NGBqMNiDyc7f18rVvq/5nZBl7P+56h5DcuLJsUb3tCC5pIkV9FYeVCg+Ub5c59b3hlFpqCmxSvDzNnRZZcr+dInAdjcVZWmAisIpoBPrtCrqGydBtP9wy5PPxUW2bwhov4FV58C+WZ7GOLMqF+G0wAlE7RUWvuUcKYVukkDjAg0g2qE01LnPBtpJ4dsYtEJnQknJR4swtnWfCcmlHQrbDoi3MoksAeGSFZePQKpht0vWiimHFQCHV2RS9P8oMqFhZN0g==")

	imageVersion = 3

	if err := client.Connect(serverURL); err != nil {
		log.Fatalf("Error connecting to UM server: %s", err)
	}
	defer client.Disconnect()

	if _, err := client.GetSystemVersion(); err != nil {
		t.Errorf("Can't get system version: %s", err)
	}

	metadata := amqp.UpgradeMetadata{
		Data: createUpgradeFile("target1", "imagefile", []byte(imageFile)),
		CertificateChains: []amqphandler.UpgradeCertificateChain{
			{
				Name: "8D28D60220B8D08826E283B531A0B1D75359C5EE",
				Fingerprints: []string{
					"48FAC66F9994BA0EA0BC71EE6E0CAB79A0A2E6DF",
					"FE232D8F645F9550D2AB559BF3144CAEB6534F69",
					"EE9E93D52D84CF3CBEB2E327635770458457B7C2",
				},
			},
		},
		Certificates: []amqphandler.UpgradeCertificate{
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
		},
	}

	client.SystemUpgrade(4, metadata)

	// wait for upgrade status
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

func processMessage(messageType int, messageIn []byte) (messageOut []byte, err error) {
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

		fileName := path.Join("tmp/upgrade/", upgradeReq.ImageInfo.Path)

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

		if imageFile != string(data) {
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

func createUpgradeFile(target, fileName string, data []byte) (fileInfo amqp.UpgradeFileInfo) {
	filePath := path.Join("tmp/fileServer", fileName)

	if err := ioutil.WriteFile(filePath, data, 0644); err != nil {
		log.Fatalf("Can't write image file: %s", err)
	}

	imageFileInfo, err := image.CreateFileInfo(filePath)
	if err != nil {
		log.Fatalf("Can't create file info: %s", err)
	}

	signValue, _ := base64.StdEncoding.DecodeString("gqrnwcPhxFT7NoTow93HmYUWZKvZCrllSU5CXG+HnaICRTOucIbw36iYZBv63j87dxBmSr7kvk7nep6SioIjnEYOKJJ8yNypyFf9UaIWSNI53Xrb5i6gySJ53IwTaQV8Bd+HsP+Qqd/U42FLFIUD3rW6BYDIAVWdkjDew5PV5VpA32as2l2eDykqwPt08Gidaw6frBN4CkVC/QnTpTFeZv2IJvC927BkP9VdiF2VO5lUK2ZC+kjYCjegsVVHXwNfZWHu1Qi+XVn+72v3LFSw9RzJc0CI7MtDlZi57g48OnFEGeHNHkqxib8LJ9T5DNQlLD92Pj/vu8m2BLWimc11Qw==")
	sign := amqphandler.UpgradeSigns{
		Alg:              "RSA/SHA256",
		ChainName:        "8D28D60220B8D08826E283B531A0B1D75359C5EE",
		TrustedTimestamp: "",
		Value:            signValue,
	}

	fileInfo.Target = target
	fileInfo.URLs = []string{"http://localhost:8080/" + fileName}
	fileInfo.Sha256 = imageFileInfo.Sha256
	fileInfo.Sha512 = imageFileInfo.Sha512
	fileInfo.Size = imageFileInfo.Size
	fileInfo.Signs = &sign

	return fileInfo
}
