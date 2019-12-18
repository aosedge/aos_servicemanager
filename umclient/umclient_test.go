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
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"testing"
	"time"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/fcrypt"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_updatemanager/umserver"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/image"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/umclient"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/wsserver"
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
	versionChannel       chan uint64
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

	server, err = wsserver.New("TestServer", url.Host, "../wsserver/data/crt.pem", "../wsserver/data/key.pem", newMessageProcessor)
	if err != nil {
		log.Fatalf("Can't create ws server: %s", err)
	}
	defer server.Close()

	sender = newTestSender()

	db, err = database.New("tmp/servicemanager.db")
	if err != nil {
		log.Fatalf("Can't create db: %s", err)
	}

	crypt, err := fcrypt.CreateContext(config.Crypt{})
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

func TestCheckSystemStatus(t *testing.T) {
	imageVersion = 4

	if err := client.Connect(serverURL); err != nil {
		log.Fatalf("Error connecting to UM server: %s", err)
	}
	defer client.Disconnect()

	// wait for system image version
	select {
	case version := <-sender.versionChannel:
		if version != imageVersion {
			t.Errorf("Wrong image version: %d", version)
		}

	case <-time.After(1 * time.Second):
		t.Error("Waiting for system version timeout")
	}

	client.Disconnect()

	if err := client.Connect(serverURL); err != nil {
		log.Fatalf("Error connecting to UM server: %s", err)
	}

	// wait for system image version
	select {
	case version := <-sender.versionChannel:
		if version != imageVersion {
			t.Errorf("Wrong image version: %d", version)
		}

	case <-time.After(1 * time.Second):
		t.Error("Waiting for system version timeout")
	}
}

func TestSystemUpgrade(t *testing.T) {
	imageVersion = 3

	if err := client.Connect(serverURL); err != nil {
		log.Fatalf("Error connecting to UM server: %s", err)
	}
	defer client.Disconnect()

	// wait for system image version
	select {
	case <-sender.versionChannel:

	case <-time.After(1 * time.Second):
		t.Error("Waiting for system version timeout")
	}

	metadata := amqp.UpgradeMetadata{
		Data: []amqp.UpgradeFileInfo{createUpgradeFile("target1", "imagefile", []byte(imageFile))}}

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

	// wait for system image version
	select {
	case <-sender.versionChannel:

	case <-time.After(1 * time.Second):
		t.Error("Waiting for system version timeout")
	}

	client.SystemRevert(3)

	// wait for revert status
	select {
	case <-sender.revertStatusChannel:

	case <-time.After(1 * time.Second):
		t.Error("Waiting for revert status timeout")
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newTestSender() (sender *testSender) {
	sender = &testSender{}

	sender.versionChannel = make(chan uint64, 1)
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

func (sender *testSender) SendSystemVersion(imageVersion uint64) (err error) {
	sender.versionChannel <- imageVersion

	return nil
}

func newMessageProcessor(sendMessage wsserver.SendMessage) (processor wsserver.MessageProcessor, err error) {
	return &messageProcessor{}, nil
}

func (processor *messageProcessor) ProcessMessage(messageType int, messageIn []byte) (messageOut []byte, err error) {
	var header umserver.MessageHeader
	var rsp interface{}

	if err = json.Unmarshal(messageIn, &header); err != nil {
		return nil, err
	}

	switch header.Type {
	case umserver.StatusType:
		rsp = umserver.StatusMessage{
			MessageHeader:    umserver.MessageHeader{Type: umserver.StatusType},
			Operation:        umserver.UpgradeType,
			Status:           umserver.SuccessStatus,
			OperationVersion: operationVersion,
			ImageVersion:     imageVersion}

	case umserver.UpgradeType:
		var upgradeReq umserver.UpgradeReq

		if err = json.Unmarshal(messageIn, &upgradeReq); err != nil {
			return nil, err
		}

		operationVersion = upgradeReq.ImageVersion
		imageVersion = upgradeReq.ImageVersion

		status := umserver.SuccessStatus
		errStr := ""

		if len(upgradeReq.Files) == 0 {
			status = umserver.FailedStatus
			errStr = "upgrade file list is empty"
		}

		for _, file := range upgradeReq.Files {
			fileName := path.Join("tmp/upgrade/", file.URL)

			if err = image.CheckFileInfo(fileName, image.FileInfo{
				Sha256: file.Sha256,
				Sha512: file.Sha512,
				Size:   file.Size}); err != nil {
				status = umserver.FailedStatus
				errStr = err.Error()
				break
			}

			if errStr == "" {
				data, err := ioutil.ReadFile(fileName)
				if err != nil {
					status = umserver.FailedStatus
					errStr = err.Error()
					break
				}

				if imageFile != string(data) {
					status = umserver.FailedStatus
					errStr = "image file content mismatch"
					break
				}
			}
		}

		rsp = umserver.StatusMessage{
			MessageHeader: umserver.MessageHeader{
				Type:  umserver.StatusType,
				Error: errStr},
			Operation:        umserver.UpgradeType,
			Status:           status,
			OperationVersion: operationVersion,
			ImageVersion:     imageVersion}

	case umserver.RevertType:
		var revertReq umserver.UpgradeReq

		if err = json.Unmarshal(messageIn, &revertReq); err != nil {
			return nil, err
		}

		operationVersion = revertReq.ImageVersion
		imageVersion = revertReq.ImageVersion

		rsp = umserver.StatusMessage{
			MessageHeader:    umserver.MessageHeader{Type: umserver.StatusType},
			Operation:        umserver.RevertType,
			Status:           umserver.SuccessStatus,
			OperationVersion: operationVersion,
			ImageVersion:     imageVersion}

	default:
		header.Error = "Unsupported message type"
		rsp = header
	}

	if rsp != nil {
		if messageOut, err = json.Marshal(rsp); err != nil {
			return nil, err
		}
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

	fileInfo.Target = target
	fileInfo.URLs = []string{"http://localhost:8080/" + fileName}
	fileInfo.Sha256 = imageFileInfo.Sha256
	fileInfo.Sha512 = imageFileInfo.Sha512
	fileInfo.Size = imageFileInfo.Size

	return fileInfo
}
