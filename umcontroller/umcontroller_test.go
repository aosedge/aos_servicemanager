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

package umcontroller_test

import (
	"context"
	"io"
	"io/ioutil"
	"os"
	"path"
	"reflect"
	"strconv"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	pb "gitpct.epam.com/epmd-aepr/aos_common/api/updatemanager"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/umcontroller"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	serverURL = "localhost:8091"
)

type testStatusSender struct {
	Components []amqp.ComponentInfo
}

type testStorage struct {
}
type testDownloader struct {
}

type testNotifyDownloader struct {
	downloadSyncCh chan bool
	componetCount  int
	curretCount    int
}

type testUmConnection struct {
	stream         pb.UpdateController_RegisterUMClient
	notifyTestChan chan bool
	continueChan   chan bool
	step           string
	test           *testing.T
	umId           string
	components     []*pb.SystemComponent
	conn           *grpc.ClientConn
}

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

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestConnection(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "umID1", Priority: 10},
			config.UmClientConfig{UmID: "umID2", Priority: 0}},
		UpdateDir: tmpDir,
	}
	smConfig := config.Config{UmController: umCtrlConfig}

	umCtrl, err := umcontroller.New(&smConfig, &testStatusSender{}, &testStorage{}, &testDownloader{}, true)
	if err != nil {
		t.Fatalf("Can't create: UM controller %s", err)
	}

	components := []*pb.SystemComponent{&pb.SystemComponent{Id: "component1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "component2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	streamUM1, connUM1, err := createClientConnection("umID1", pb.UmState_IDLE, components)
	if err != nil {
		t.Errorf("Error connect %s", err)
	}

	components2 := []*pb.SystemComponent{&pb.SystemComponent{Id: "component3", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "component4", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	streamUM2, connUM2, err := createClientConnection("umID2", pb.UmState_IDLE, components2)
	if err != nil {
		t.Errorf("Error connect %s", err)
	}

	streamUM1_copy, connUM1_copy, err := createClientConnection("umID1", pb.UmState_IDLE, components)
	if err != nil {
		t.Errorf("Error connect %s", err)
	}

	umCtrl.Close()

	newComponents, err := umCtrl.GetSystemComponents()
	if err != nil {
		t.Errorf("Can't get system components %s", err)
	}

	if len(newComponents) != 4 {
		t.Errorf("Incorrect count of components %d", len(newComponents))
	}

	streamUM1.CloseSend()
	connUM1.Close()

	streamUM2.CloseSend()
	connUM2.Close()

	streamUM1_copy.CloseSend()
	connUM1_copy.Close()

	time.Sleep(1 * time.Second)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func createClientConnection(clientID string, state pb.UmState,
	components []*pb.SystemComponent) (stream pb.UpdateController_RegisterUMClient, conn *grpc.ClientConn, err error) {
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithInsecure())
	opts = append(opts, grpc.WithBlock())

	conn, err = grpc.Dial(serverURL, opts...)
	if err != nil {
		return stream, nil, err
	}

	client := pb.NewUpdateControllerClient(conn)
	stream, err = client.RegisterUM(context.Background())
	if err != nil {
		log.Errorf("Fail call RegisterUM %s", err)
		return stream, nil, err
	}

	umMsg := &pb.UpdateStatus{UmId: clientID, UmState: state, Components: components}

	if err = stream.Send(umMsg); err != nil {
		log.Errorf("Fail send update status message %s", err)
	}
	time.Sleep(1 * time.Second)

	return stream, conn, nil
}

func TestFullUpdate(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "testUM1", Priority: 1},
			config.UmClientConfig{UmID: "testUM2", Priority: 10}},
		UpdateDir: tmpDir,
	}

	smConfig := config.Config{UmController: umCtrlConfig}

	var updateSender testStatusSender
	var updateStorage testStorage

	updateDownloader := testNotifyDownloader{downloadSyncCh: make(chan bool, 1), componetCount: 3}

	umCtrl, err := umcontroller.New(&smConfig, &updateSender, &updateStorage, &updateDownloader, true)
	if err != nil {
		t.Errorf("Can't create: UM controller %s", err)
	}

	um1Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um1C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um1C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um1 := newTestUM("testUM1", pb.UmState_IDLE, "init", um1Components, t)
	go um1.processMessages()

	um2Components := []*pb.SystemComponent{&pb.SystemComponent{Id: "um2C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um2C2", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED}}

	um2 := newTestUM("testUM2", pb.UmState_IDLE, "init", um2Components, t)
	go um2.processMessages()

	desiredStatus := []amqp.ComponentInfoFromCloud{amqp.ComponentInfoFromCloud{ID: "um1C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "1"}},
		amqp.ComponentInfoFromCloud{ID: "um1C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um2C1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
		amqp.ComponentInfoFromCloud{ID: "um2C2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2"}},
	}

	finishChanel := make(chan bool)

	go func(finChan chan bool) {
		umCtrl.ProcessDesiredComponents(desiredStatus, []amqp.CertificateChain{}, []amqp.Certificate{})
		finChan <- true
	}(finishChanel)

	<-updateDownloader.downloadSyncCh

	um1Components = append(um1Components, &pb.SystemComponent{Id: "um1C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um1.setComponents(um1Components)

	um1.step = "prepare"
	um1.continueChan <- true
	<-um1.notifyTestChan // receive prepare
	um1.sendState(pb.UmState_PREPARED)

	um2Components = append(um2Components, &pb.SystemComponent{Id: "um2C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um2Components = append(um2Components, &pb.SystemComponent{Id: "um2C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLING})
	um2.setComponents(um2Components)

	um2.step = "prepare"
	um2.continueChan <- true
	<-um2.notifyTestChan
	um2.sendState(pb.UmState_PREPARED)

	um1.step = "update"
	um1.continueChan <- true
	<-um1.notifyTestChan //um1 updated
	um1.sendState(pb.UmState_UPDATED)

	um2.step = "update"
	um2.continueChan <- true
	<-um2.notifyTestChan //um2 updated
	um2.sendState(pb.UmState_UPDATED)

	um1Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um1C1", VendorVersion: "1", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um1C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED}}
	um1.setComponents(um1Components)

	um1.step = "apply"
	um1.continueChan <- true
	<-um1.notifyTestChan //um1 apply
	um1.sendState(pb.UmState_IDLE)

	um2Components = []*pb.SystemComponent{&pb.SystemComponent{Id: "um2C1", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED},
		&pb.SystemComponent{Id: "um2C2", VendorVersion: "2", Status: pb.ComponentStatus_INSTALLED}}
	um2.setComponents(um2Components)

	um2.step = "apply"
	um2.continueChan <- true
	<-um2.notifyTestChan //um1 apply
	um2.sendState(pb.UmState_IDLE)

	time.Sleep(1 * time.Second)
	um1.step = "finish"
	um2.step = "finish"

	<-finishChanel

	um1.closeConnection()
	um2.closeConnection()

	<-um1.notifyTestChan
	<-um2.notifyTestChan

	umCtrl.Close()

	etalonComponents := []amqp.ComponentInfo{amqp.ComponentInfo{ID: "um1C1", VendorVersion: "1", Status: "installed"},
		amqp.ComponentInfo{ID: "um1C2", VendorVersion: "2", Status: "installed"},
		amqp.ComponentInfo{ID: "um2C1", VendorVersion: "2", Status: "installed"},
		amqp.ComponentInfo{ID: "um2C2", VendorVersion: "2", Status: "installed"}}

	if !reflect.DeepEqual(etalonComponents, updateSender.Components) {
		log.Debug(updateSender.Components)
		t.Error("incorrect result component list")
	}

	time.Sleep(time.Second)
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/
func (sender *testStatusSender) SendComponentStatus(components []amqp.ComponentInfo) {
	sender.Components = make([]amqp.ComponentInfo, len(components))
	copy(sender.Components, components)
}

func (storage *testStorage) GetComponentsUpdateInfo() (updateInfo []umcontroller.SystemComponent, err error) {
	return updateInfo, err
}

func (storage *testStorage) SetComponentsUpdateInfo(updateInfo []umcontroller.SystemComponent) (err error) {
	return err
}

func (downloader *testDownloader) DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
	chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error) {

	return "", nil
}

func (downloader *testNotifyDownloader) DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
	chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error) {
	log.Debug("testNotifyDownloader")

	downloader.curretCount++
	if downloader.curretCount == downloader.componetCount {
		downloader.downloadSyncCh <- true
	}

	resultFile = path.Join(decryptDir, strconv.Itoa(downloader.curretCount))

	if err := ioutil.WriteFile(resultFile, []byte("Some Update"), 0644); err != nil {
		return resultFile, err
	}

	return resultFile, nil
}

func (um *testUmConnection) processMessages() {
	defer func() { um.notifyTestChan <- true }()
	for {
		<-um.continueChan
		msg, err := um.stream.Recv()
		switch um.step {
		case "finish":
			fallthrough

		case "reboot":
			if err == io.EOF {
				log.Debug("[test] End of connection ", um.umId)
				return
			}

			if err != nil {
				log.Debug("[test] End of connection with error ", err, um.umId)
				return
			}

		case "prepare":
			if msg.GetPrepareUpdate() == nil {
				um.test.Error("Expect prepare update request ", um.umId)
			}

		case "update":
			if msg.GetStartUpdate() == nil {
				um.test.Error("Expect start update ", um.umId)
			}

		case "apply":
			if msg.GetApplyUpdate() == nil {
				um.test.Error("Expect apply update ", um.umId)
			}

		case "revert":
			if msg.GetRevertUpdate() == nil {
				um.test.Error("Expect revert update ", um.umId)
			}

		default:
			um.test.Error("unexpected message at step", um.step)
		}
		um.notifyTestChan <- true
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newTestUM(id string, umState pb.UmState, testState string, components []*pb.SystemComponent, t *testing.T) (umTest *testUmConnection) {
	stream, conn, err := createClientConnection(id, umState, components)
	if err != nil {
		t.Errorf("Error connect %s", err)
		return umTest
	}

	umTest = &testUmConnection{
		notifyTestChan: make(chan bool),
		continueChan:   make(chan bool),
		step:           testState,
		test:           t,
		stream:         stream,
		umId:           id,
		conn:           conn,
		components:     components,
	}

	return umTest
}

func (um *testUmConnection) setComponents(components []*pb.SystemComponent) {
	um.components = components
}

func (um *testUmConnection) sendState(state pb.UmState) {
	umMsg := &pb.UpdateStatus{UmId: um.umId, UmState: state, Components: um.components}

	if err := um.stream.Send(umMsg); err != nil {
		um.test.Errorf("Fail send update status message %s", err)
	}
}

func (um *testUmConnection) closeConnection() {
	um.continueChan <- true
	um.conn.Close()
	um.stream.CloseSend()
}
