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

package smserver_test

import (
	"context"
	"io/ioutil"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	pb "github.com/aoscloud/aos_common/api/servicemanager/v2"
	"github.com/aoscloud/aos_common/image"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/layermanager"
	"github.com/aoscloud/aos_servicemanager/smserver"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	serverURL = "localhost:8092"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type testLauncher struct {
	stateChannel chan *pb.SMNotifications
	wasRestart   bool
}

type testLayerManager struct {
	currentInstallRequests testLayerInstallRequest
	layers                 []layermanager.LayerInfo
}

type testClient struct {
	connection *grpc.ClientConn
	pbclient   pb.SMServiceClient
}

type testAlertProvider struct {
	alertsChannel chan cloudprotocol.AlertItem
}

type testLogProvider struct {
	currentLogRequest cloudprotocol.RequestServiceLog
	testLogs          []testLogData
	sentIndex         int
	channel           chan cloudprotocol.PushLog
}

type testMonitoringProvider struct {
	monitoringChannel chan cloudprotocol.MonitoringData
}

type testResourceManager struct {
	version             string
	receivedBoardConfig string
}

type testLogData struct {
	intrenalLog   cloudprotocol.PushLog
	expectedPBLog pb.LogData
}

type testLayerInstallRequest struct {
	installInfo layermanager.LayerInfo
	layerURL    string
	fileInfo    image.FileInfo
}

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

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestConnection(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	launcher := &testLauncher{}
	layerMgr := &testLayerManager{}
	resourseManager := &testResourceManager{version: "1.0"}

	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	smServer, err := smserver.New(&smConfig, launcher, layerMgr, nil, nil, resourseManager, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM server: %s", err)
	}

	go func() {
		if err := smServer.Start(); err != nil {
			t.Errorf("Can't start sm server")
		}
	}()
	defer smServer.Stop()

	client, err := newTestClient(serverURL)
	if err != nil {
		t.Fatalf("Can't create test client: %s", err)
	}
	defer client.close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	response, err := client.pbclient.GetAllStatus(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Can't get status: %s", err)
	}

	if len(response.GetServices()) != 1 {
		t.Errorf("incorrect count of services %d", len(response.GetServices()))
	}

	responceBoardCfg, err := client.pbclient.GetBoardConfigStatus(ctx, &emptypb.Empty{})
	if err != nil {
		t.Errorf("Can't get board configuration: %s", err)
	}

	if responceBoardCfg.GetVendorVersion() != "1.0" {
		t.Errorf("incorrect boardConfig version %s", responceBoardCfg.GetVendorVersion())
	}

	service := &pb.InstallServiceRequest{ServiceId: "service1"}

	status, err := client.pbclient.InstallService(ctx, service)
	if err != nil {
		t.Fatalf("Can't install service : %s", err)
	}

	if status.ServiceId != service.ServiceId {
		t.Errorf("Incorrect service id in response")
	}

	_, err = client.pbclient.RemoveService(ctx, &pb.RemoveServiceRequest{ServiceId: "service1"})
	if err != nil {
		t.Fatalf("Can't remove service: %s", err)
	}

	_, err = client.pbclient.InstallLayer(ctx, &pb.InstallLayerRequest{})
	if err != nil {
		t.Fatalf("Can't install layer: %s", err)
	}
}

func TestBoardConfiguration(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	var (
		testLauncher        = testLauncher{}
		testResourceManager = testResourceManager{version: "bcv1"}
		expectedBoardConfig = "{someboardconfig}"
	)

	smServer, err := smserver.New(&smConfig, &testLauncher, nil, nil, nil, &testResourceManager, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	go func() {
		if err := smServer.Start(); err != nil {
			t.Errorf("Can't start sm server")
		}
	}()

	defer smServer.Stop()

	client, err := newTestClient(serverURL)
	if err != nil {
		t.Fatalf("Can't create test client: %s", err)
	}

	defer client.close()

	expectedBCStatus := &pb.BoardConfigStatus{VendorVersion: testResourceManager.version}

	bcStatus, err := client.pbclient.GetBoardConfigStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Can't set board config: %s", err)
	}

	if !proto.Equal(expectedBCStatus, bcStatus) {
		t.Error("Incorrect board config status")
	}

	if _, err := client.pbclient.CheckBoardConfig(
		context.Background(), &pb.BoardConfig{BoardConfig: expectedBoardConfig}); err != nil {
		t.Fatalf("Can't check board config: %s", err)
	}

	if testResourceManager.receivedBoardConfig != expectedBoardConfig {
		t.Error("Incorrect board config in check board config call")
	}

	if _, err := client.pbclient.SetBoardConfig(
		context.Background(), &pb.BoardConfig{BoardConfig: expectedBoardConfig}); err != nil {
		t.Fatalf("Can't set board config: %s", err)
	}

	if !testLauncher.wasRestart {
		t.Error("Should be restart services call")
	}

	if testResourceManager.receivedBoardConfig != expectedBoardConfig {
		t.Error("Incorrect board config in set board config call")
	}
}

func TestLayerMessages(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	testLayerManager := &testLayerManager{}

	smServer, err := smserver.New(&smConfig, nil, testLayerManager, nil, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	go func() {
		if err := smServer.Start(); err != nil {
			t.Errorf("Can't start sm server")
		}
	}()

	defer smServer.Stop()

	client, err := newTestClient(serverURL)
	if err != nil {
		t.Fatalf("Can't create test client: %s", err)
	}

	defer client.close()

	expectedLayerStatuses := &pb.LayersStatus{}

	for i := 0; i < 5; i++ {
		installRequest := pb.InstallLayerRequest{
			Url: "someurl" + strconv.Itoa(i), LayerId: "testLayer" + strconv.Itoa(i), AosVersion: uint64(i),
			VendorVersion: strconv.Itoa(i + 1), Digest: "digest" + strconv.Itoa(i),
			Sha256: []byte{0, 0, 0, byte(i + 100)}, Sha512: []byte{byte(i + 100), 0, 0, 0},
			Description: "desc" + strconv.Itoa(i), Size: uint64(i + 500),
		}

		expectedInstallRequest := testLayerInstallRequest{
			installInfo: layermanager.LayerInfo{
				Digest: "digest" + strconv.Itoa(i), LayerID: "testLayer" + strconv.Itoa(i), AosVersion: uint64(i),
				VendorVersion: strconv.Itoa(i + 1), Description: "desc" + strconv.Itoa(i),
			},
			layerURL: "someurl" + strconv.Itoa(i),
			fileInfo: image.FileInfo{
				Sha256: []byte{0, 0, 0, byte(i + 100)}, Sha512: []byte{byte(i + 100), 0, 0, 0},
				Size: uint64(i + 500),
			},
		}

		expectedStatus := pb.LayerStatus{
			LayerId: "testLayer" + strconv.Itoa(i), AosVersion: uint64(i), VendorVersion: strconv.Itoa(i + 1),
			Digest: "digest" + strconv.Itoa(i),
		}

		if _, err := smServer.InstallLayer(context.Background(), &installRequest); err != nil {
			t.Fatalf("Can't install layer")
		}

		if !reflect.DeepEqual(expectedInstallRequest, testLayerManager.currentInstallRequests) {
			t.Error("Incorrect install request")
		}

		expectedLayerStatuses.Layers = append(expectedLayerStatuses.Layers, &expectedStatus)
	}

	layerStatuses, err := smServer.GetLayersStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Errorf("Can't get layer statuses: %s", err)
	}

	if !proto.Equal(layerStatuses, expectedLayerStatuses) {
		t.Errorf("Incorrect layer statuses")
	}
}

func TestAlertNotifications(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	testAlerts := &testAlertProvider{alertsChannel: make(chan cloudprotocol.AlertItem, 10)}

	smServer, err := smserver.New(&smConfig, nil, nil, testAlerts, nil, nil,
		nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	go func() {
		if err := smServer.Start(); err != nil {
			t.Errorf("Can't start sm server: %s", err)
		}
	}()

	defer smServer.Stop()

	client, err := newTestClient(serverURL)
	if err != nil {
		t.Fatalf("Can't create test client: %s", err)
	}

	defer client.close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	notifications, err := client.pbclient.SubscribeSMNotifications(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Can't subscribe: %s", err)
	}

	type testAlertItem struct {
		sendAlert     cloudprotocol.AlertItem
		expectedAlert pb.Alert
	}

	testAlertItems := []testAlertItem{
		{
			sendAlert: cloudprotocol.AlertItem{
				Tag:     cloudprotocol.AlertTagSystemError,
				Payload: cloudprotocol.SystemAlert{Message: "SystemAlertMessage"},
			},
			expectedAlert: pb.Alert{
				Tag:     cloudprotocol.AlertTagSystemError,
				Payload: &pb.Alert_SystemAlert{SystemAlert: &pb.SystemAlert{Message: "SystemAlertMessage"}},
			},
		},
		{
			sendAlert: cloudprotocol.AlertItem{
				Tag:     cloudprotocol.AlertTagAosCore,
				Payload: cloudprotocol.CoreAlert{CoreComponent: "SM", Message: "CoreAlertMessage"},
			},
			expectedAlert: pb.Alert{
				Tag:     cloudprotocol.AlertTagAosCore,
				Payload: &pb.Alert_CoreAlert{CoreAlert: &pb.CoreAlert{CoreComponent: "SM", Message: "CoreAlertMessage"}},
			},
		},
		{
			sendAlert: cloudprotocol.AlertItem{
				Tag: cloudprotocol.AlertTagResourceValidate,
				Payload: cloudprotocol.ResourceValidateAlert{
					ResourcesErrors: []cloudprotocol.ResourceValidateError{
						{Name: "someName1", Errors: []string{"error1", "error2"}},
						{Name: "someName2", Errors: []string{"error3", "error4"}},
					},
				},
			},
			expectedAlert: pb.Alert{
				Tag: cloudprotocol.AlertTagResourceValidate,
				Payload: &pb.Alert_ResourceValidateAlert{
					ResourceValidateAlert: &pb.ResourceValidateAlert{
						Errors: []*pb.ResourceValidateErrors{
							{Name: "someName1", ErrorMsg: []string{"error1", "error2"}},
							{Name: "someName2", ErrorMsg: []string{"error3", "error4"}},
						},
					},
				},
			},
		},
		{
			sendAlert: cloudprotocol.AlertItem{
				Tag: cloudprotocol.AlertTagDeviceAllocate,
				Payload: cloudprotocol.DeviceAllocateAlert{
					InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
					Device:        "someDevice", Message: "someMessage",
				},
			},
			expectedAlert: pb.Alert{
				Tag: cloudprotocol.AlertTagDeviceAllocate,
				Payload: &pb.Alert_DeviceAllocateAlert{
					DeviceAllocateAlert: &pb.DeviceAllocateAlert{
						Instance: &pb.InstanceIdent{ServiceId: "id1", SubjectId: "s1", Instance: 1},
						Device:   "someDevice", Message: "someMessage",
					},
				},
			},
		},
		{
			sendAlert: cloudprotocol.AlertItem{
				Tag:     cloudprotocol.AlertTagSystemQuota,
				Payload: cloudprotocol.SystemQuotaAlert{Parameter: "param1", Value: 42},
			},
			expectedAlert: pb.Alert{
				Tag: cloudprotocol.AlertTagSystemQuota,
				Payload: &pb.Alert_SystemQuotaAlert{
					SystemQuotaAlert: &pb.SystemQuotaAlert{Parameter: "param1", Value: 42},
				},
			},
		},
		{
			sendAlert: cloudprotocol.AlertItem{
				Tag: cloudprotocol.AlertTagInstanceQuota,
				Payload: cloudprotocol.InstanceQuotaAlert{
					InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
					Parameter:     "param1", Value: 42,
				},
			},
			expectedAlert: pb.Alert{
				Tag: cloudprotocol.AlertTagInstanceQuota,
				Payload: &pb.Alert_InstanceQuotaAlert{
					InstanceQuotaAlert: &pb.InstanceQuotaAlert{
						Instance:  &pb.InstanceIdent{ServiceId: "id1", SubjectId: "s1", Instance: 1},
						Parameter: "param1", Value: 42,
					},
				},
			},
		},
		{
			sendAlert: cloudprotocol.AlertItem{
				Tag: cloudprotocol.AlertTagServiceInstance,
				Payload: cloudprotocol.ServiceInstanceAlert{
					InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
					Message:       "ServiceInstanceAlert", AosVersion: 42,
				},
			},
			expectedAlert: pb.Alert{
				Tag: cloudprotocol.AlertTagServiceInstance,
				Payload: &pb.Alert_InstanceAlert{
					InstanceAlert: &pb.InstanceAlert{
						Instance: &pb.InstanceIdent{ServiceId: "id1", SubjectId: "s1", Instance: 1},
						Message:  "ServiceInstanceAlert", AosVersion: 42,
					},
				},
			},
		},
	}

	for i := range testAlertItems {
		testAlerts.alertsChannel <- testAlertItems[i].sendAlert

		receivedAlert, err := notifications.Recv()
		if err != nil {
			t.Errorf("Can't receive alert: %s", err)
		}

		receivedAlert.GetAlert().Timestamp = nil

		if !proto.Equal(receivedAlert.GetAlert(), &testAlertItems[i].expectedAlert) {
			t.Errorf("Incorrect log item %s", receivedAlert.GetAlert().Tag)
		}
	}

	// test incorrect alerts
	invalidAlerts := []cloudprotocol.AlertItem{
		{
			Tag:     cloudprotocol.AlertTagAosCore,
			Payload: cloudprotocol.SystemAlert{Message: "SystemAlertMessage"},
		},
		{
			Tag:     cloudprotocol.AlertTagDeviceAllocate,
			Payload: cloudprotocol.CoreAlert{CoreComponent: "SM", Message: "CoreAlertMessage"},
		},
		{
			Tag: cloudprotocol.AlertTagSystemError,
			Payload: cloudprotocol.ResourceValidateAlert{
				ResourcesErrors: []cloudprotocol.ResourceValidateError{
					{Name: "someName1", Errors: []string{"error1", "error2"}},
					{Name: "someName2", Errors: []string{"error3", "error4"}},
				},
			},
		},
		{
			Tag: cloudprotocol.AlertTagSystemQuota,
			Payload: cloudprotocol.DeviceAllocateAlert{
				InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
				Device:        "someDevice", Message: "someMessage",
			},
		},

		{
			Tag:     cloudprotocol.AlertTagInstanceQuota,
			Payload: cloudprotocol.SystemQuotaAlert{Parameter: "param1", Value: 42},
		},

		{
			Tag: cloudprotocol.AlertTagServiceInstance,
			Payload: cloudprotocol.InstanceQuotaAlert{
				InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
				Parameter:     "param1", Value: 42,
			},
		},

		{
			Tag: cloudprotocol.AlertTagResourceValidate,
			Payload: cloudprotocol.ServiceInstanceAlert{
				InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
				Message:       "ServiceInstanceAlert", AosVersion: 42,
			},
		},
	}

	for i := range invalidAlerts {
		testAlerts.alertsChannel <- invalidAlerts[i]
	}

	alertChan := make(chan *pb.SMNotifications, 1)

	go func() {
		receivedAlert, err := notifications.Recv()
		if err == nil {
			alertChan <- receivedAlert
		}
	}()

	select {
	case <-alertChan:
		t.Error("Unexpected Alert")

	case <-time.After(2 * time.Second):
	}
}

func TestMonitoringNotifications(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	testMonitoring := &testMonitoringProvider{monitoringChannel: make(chan cloudprotocol.MonitoringData, 10)}

	smServer, err := smserver.New(&smConfig, nil, nil, nil, testMonitoring, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	go func() {
		if err := smServer.Start(); err != nil {
			t.Errorf("Can't start sm server")
		}
	}()

	defer smServer.Stop()

	client, err := newTestClient(serverURL)
	if err != nil {
		t.Fatalf("Can't create test client: %s", err)
	}

	defer client.close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	notifications, err := client.pbclient.SubscribeSMNotifications(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Can't subscribe: %s", err)
	}

	type testMonitoringElement struct {
		sendMonitoring     cloudprotocol.MonitoringData
		expectedMonitoring pb.Monitoring
	}

	testMonitoringData := []testMonitoringElement{
		{
			sendMonitoring: cloudprotocol.MonitoringData{
				Global: cloudprotocol.GlobalMonitoringData{RAM: 10, CPU: 20, UsedDisk: 30, InTraffic: 40, OutTraffic: 50},
			},
			expectedMonitoring: pb.Monitoring{
				SystemMonitoring: &pb.SystemMonitoring{Ram: 10, Cpu: 20, UsedDisk: 30, InTraffic: 40, OutTraffic: 50},
			},
		},
		{
			sendMonitoring: cloudprotocol.MonitoringData{
				Global: cloudprotocol.GlobalMonitoringData{RAM: 10, CPU: 20, UsedDisk: 30, InTraffic: 40, OutTraffic: 50},
				ServiceInstances: []cloudprotocol.InstanceMonitoringData{
					{
						InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "service1", SubjectID: "s1", Instance: 1},
						RAM:           10, CPU: 20, UsedDisk: 30, InTraffic: 40, OutTraffic: 0,
					},
					{
						InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "service2", SubjectID: "s1", Instance: 1},
						RAM:           0, CPU: 20, UsedDisk: 30, InTraffic: 40, OutTraffic: 50,
					},
				},
			},
			expectedMonitoring: pb.Monitoring{
				SystemMonitoring: &pb.SystemMonitoring{Ram: 10, Cpu: 20, UsedDisk: 30, InTraffic: 40, OutTraffic: 50},
				InstanceMonitoring: []*pb.InstanceMonitoring{
					{
						Instance: &pb.InstanceIdent{ServiceId: "service1", SubjectId: "s1", Instance: 1},
						Ram:      10, Cpu: 20, UsedDisk: 30, InTraffic: 40, OutTraffic: 0,
					},
					{
						Instance: &pb.InstanceIdent{ServiceId: "service2", SubjectId: "s1", Instance: 1},
						Ram:      0, Cpu: 20, UsedDisk: 30, InTraffic: 40, OutTraffic: 50,
					},
				},
			},
		},
	}

	for i := range testMonitoringData {
		testMonitoring.monitoringChannel <- testMonitoringData[i].sendMonitoring

		receivedMonitoring, err := notifications.Recv()
		if err != nil {
			t.Fatalf("Can't receive monitoring: %s", err)
		}

		receivedMonitoring.GetMonitoring().Timestamp = nil

		if !proto.Equal(receivedMonitoring.GetMonitoring(), &testMonitoringData[i].expectedMonitoring) {
			t.Errorf("Incorrect monitoring data")
		}
	}
}

func TestServiceStateProcessing(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	launcher := &testLauncher{stateChannel: make(chan *pb.SMNotifications, 10)}

	smServer, err := smserver.New(&smConfig, launcher, nil, nil, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	go func() {
		if err := smServer.Start(); err != nil {
			t.Errorf("Can't start sm server")
		}
	}()
	defer smServer.Stop()

	client, err := newTestClient(serverURL)
	if err != nil {
		t.Fatalf("Can't create test client: %s", err)
	}
	defer client.close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	notifications, err := client.pbclient.SubscribeSMNotifications(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Can't subscribe: %s", err)
	}

	etalonNewStateMsg := &pb.NewServiceState{
		CorrelationId: "corelationID",
		ServiceState:  &pb.ServiceState{ServiceId: "serviecId1", StateChecksum: "someCheckSum", State: []byte("state1")},
	}

	launcher.stateChannel <- &pb.SMNotifications{
		SMNotification: &pb.SMNotifications_NewServiceState{NewServiceState: etalonNewStateMsg},
	}

	receiveNewSate, err := notifications.Recv()
	if err != nil {
		t.Errorf("Can't receive monitoring data: %s", err)
	}

	if !proto.Equal(receiveNewSate.GetNewServiceState(), etalonNewStateMsg) {
		log.Error("received newSate data != sent data")
	}

	etalonStateRequest := &pb.ServiceStateRequest{ServiceId: "serviceId2", Default: false}

	launcher.stateChannel <- &pb.SMNotifications{
		SMNotification: &pb.SMNotifications_ServiceStateRequest{
			ServiceStateRequest: etalonStateRequest,
		},
	}

	receivedSateRequest, err := notifications.Recv()
	if err != nil {
		t.Errorf("Can't receive monitoring data: %s", err)
	}

	if !proto.Equal(receivedSateRequest.GetServiceStateRequest(), etalonStateRequest) {
		log.Error("received state request data != sent data")
	}
}

func TestLogsNotification(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	logProvider := testLogProvider{channel: make(chan cloudprotocol.PushLog)}

	smServer, err := smserver.New(&smConfig, nil, nil, nil, nil, nil, &logProvider, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM server: %s", err)
	}

	go func() {
		if err := smServer.Start(); err != nil {
			t.Errorf("Can't start sm server")
		}
	}()

	defer smServer.Stop()

	client, err := newTestClient(serverURL)
	if err != nil {
		t.Fatalf("Can't create test client: %s", err)
	}

	defer client.close()

	notifications, err := client.pbclient.SubscribeSMNotifications(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Can't subscribe: %s", err)
	}

	logProvider.testLogs = []testLogData{
		{
			intrenalLog:   cloudprotocol.PushLog{LogID: "systemLog", Data: []byte{1, 2, 3}},
			expectedPBLog: pb.LogData{LogId: "systemLog", Data: []byte{1, 2, 3}},
		},
		{
			intrenalLog:   cloudprotocol.PushLog{LogID: "serviceLog1", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
			expectedPBLog: pb.LogData{LogId: "serviceLog1", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			intrenalLog:   cloudprotocol.PushLog{LogID: "serviceLog2", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
			expectedPBLog: pb.LogData{LogId: "serviceLog2", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			intrenalLog:   cloudprotocol.PushLog{LogID: "serviceLog3", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
			expectedPBLog: pb.LogData{LogId: "serviceLog3", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			intrenalLog:   cloudprotocol.PushLog{LogID: "serviceCrashLog", Data: []byte{1, 2, 4}, Error: "some error", Part: 1},
			expectedPBLog: pb.LogData{LogId: "serviceCrashLog", Data: []byte{1, 2, 4}, Error: "some error", Part: 1},
		},
	}

	if _, err := smServer.GetSystemLog(context.Background(), &pb.SystemLogRequest{LogId: "systemlog"}); err != nil {
		t.Fatalf("Can't get system log: %s", err)
	}

	instanceLogRequests := []pb.InstanceLogRequest{
		{
			Instance: &pb.InstanceIdent{ServiceId: "id1", SubjectId: "", Instance: -1},
			LogId:    "serviceLog1",
		},
		{
			Instance: &pb.InstanceIdent{ServiceId: "id2", SubjectId: "s1", Instance: 10},
			LogId:    "serviceLog2",
		},
		{
			Instance: &pb.InstanceIdent{ServiceId: "id3", SubjectId: "s1", Instance: 10},
			From:     timestamppb.Now(), Till: timestamppb.Now(),
			LogId: "serviceLog3",
		},
	}

	for i := range instanceLogRequests {
		if _, err := smServer.GetInstanceLog(context.Background(), &instanceLogRequests[i]); err != nil {
			t.Fatalf("Can't get instance log: %s", err)
		}

		if err := compareServiceLogRequest(&instanceLogRequests[i], logProvider.currentLogRequest); err != nil {
			t.Errorf("Service log requests mismatch: %s", err)
		}
	}

	if _, err := smServer.GetInstanceCrashLog(context.Background(),
		&pb.InstanceLogRequest{LogId: "serviceCrashLog", Instance: &pb.InstanceIdent{ServiceId: "id3"}}); err != nil {
		t.Fatalf("Can't get instance crash log: %s", err)
	}

	if err := waitAndCheckLogs(notifications, logProvider.testLogs); err != nil {
		t.Fatalf("Incorrect logs: %s", err)
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (launcher *testLauncher) SetUsers(users []string) (err error) {
	return nil
}

func (launcher *testLauncher) GetServicesInfo() (currentServices []*pb.ServiceStatus, err error) {
	currentServices = append(currentServices, &pb.ServiceStatus{ServiceId: "123"})

	return currentServices, nil
}

func (launcher *testLauncher) InstallService(serviceInfo *pb.InstallServiceRequest) (status *pb.ServiceStatus, err error) {
	return &pb.ServiceStatus{ServiceId: serviceInfo.ServiceId, StateChecksum: "some state check sum"}, nil
}

func (launcher *testLauncher) UninstallService(removeReq *pb.RemoveServiceRequest) (err error) {
	return nil
}

func (launcher *testLauncher) StateAcceptance(acceptance *pb.StateAcceptance) (err error) {
	return nil
}

func (launcher *testLauncher) SetServiceState(state *pb.ServiceState) (err error) {
	return nil
}

func (launcher *testLauncher) GetStateMessageChannel() (channel <-chan *pb.SMNotifications) {
	return launcher.stateChannel
}

func (launcher *testLauncher) RestartInstances() error {
	launcher.wasRestart = true

	return nil
}

func (launcher *testLauncher) ProcessDesiredEnvVarsList(envVars []*pb.OverrideEnvVar) (status []*pb.EnvVarStatus, err error) {
	return status, nil
}

func (launcher *testLauncher) GetServicesLayersInfoByUsers(users []string) (servicesInfo []*pb.ServiceStatus,
	layersInfo []*pb.LayerStatus, err error,
) {
	return servicesInfo, layersInfo, nil
}

func (layerMgr *testLayerManager) GetLayersInfo() ([]layermanager.LayerInfo, error) {
	return layerMgr.layers, nil
}

func (layerMgr *testLayerManager) InstallLayer(
	installInfo layermanager.LayerInfo, layerURL string, fileInfo image.FileInfo,
) error {
	layerMgr.currentInstallRequests = testLayerInstallRequest{
		installInfo: installInfo,
		layerURL:    layerURL, fileInfo: fileInfo,
	}

	layerMgr.layers = append(layerMgr.layers, layermanager.LayerInfo{
		Digest: installInfo.Digest, LayerID: installInfo.LayerID, AosVersion: installInfo.AosVersion,
		VendorVersion: installInfo.VendorVersion, Description: installInfo.Description,
	})

	return nil
}

func (alerts *testAlertProvider) GetAlertsChannel() (channel <-chan cloudprotocol.AlertItem) {
	return alerts.alertsChannel
}

func (logProvider *testLogProvider) GetInstanceLog(request cloudprotocol.RequestServiceLog) error {
	logProvider.currentLogRequest = request
	logProvider.channel <- logProvider.testLogs[logProvider.sentIndex].intrenalLog
	logProvider.sentIndex++

	return nil
}

func (logProvider *testLogProvider) GetInstanceCrashLog(request cloudprotocol.RequestServiceCrashLog) error {
	logProvider.channel <- logProvider.testLogs[logProvider.sentIndex].intrenalLog
	logProvider.sentIndex++

	return nil
}

func (logProvider *testLogProvider) GetSystemLog(request cloudprotocol.RequestSystemLog) {
	logProvider.channel <- logProvider.testLogs[logProvider.sentIndex].intrenalLog
	logProvider.sentIndex++
}

func (logProvider *testLogProvider) GetLogsDataChannel() (channel <-chan cloudprotocol.PushLog) {
	return logProvider.channel
}

func (monitoring *testMonitoringProvider) GetMonitoringDataChannel() (channel <-chan cloudprotocol.MonitoringData) {
	return monitoring.monitoringChannel
}

func (resMgr *testResourceManager) GetBoardConfigInfo() (version string) {
	return resMgr.version
}

func (resMgr *testResourceManager) CheckBoardConfig(configJSON string) error {
	resMgr.receivedBoardConfig = configJSON

	return nil
}

func (resMgr *testResourceManager) UpdateBoardConfig(configJSON string) error {
	resMgr.receivedBoardConfig = configJSON

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newTestClient(url string) (client *testClient, err error) {
	client = &testClient{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if client.connection, err = grpc.DialContext(ctx, url, grpc.WithInsecure(), grpc.WithBlock()); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	client.pbclient = pb.NewSMServiceClient(client.connection)

	return client, nil
}

func (client *testClient) close() {
	if client.connection != nil {
		client.connection.Close()
	}
}

func waitAndCheckLogs(notification pb.SMService_SubscribeSMNotificationsClient, testLogs []testLogData) (err error) {
	notificationChan := make(chan *pb.SMNotifications, 1)

	go func() {
		for {
			smData, err := notification.Recv()
			if err != nil {
				log.Errorf("Can't receive log: %s", err)
			}

			notificationChan <- smData
		}
	}()

	var currentIndex int

	for {
		select {
		case rawLog := <-notificationChan:
			logData := rawLog.GetLog()

			if logData == nil {
				return aoserrors.New("incorrect notification type")
			}

			if !proto.Equal(logData, &testLogs[currentIndex].expectedPBLog) {
				return aoserrors.New("received log doesn't match sent log")
			}

			currentIndex++

			if currentIndex >= len(testLogs) {
				return nil
			}

		case <-time.After(5 * time.Second):
			return aoserrors.New("timeout")
		}
	}
}

func compareServiceLogRequest(expected *pb.InstanceLogRequest, received cloudprotocol.RequestServiceLog) error {
	switch {
	case received.LogID != expected.LogId:
		return aoserrors.New("incorrect LogID")

	case received.From == nil:
		if expected.From != nil {
			return aoserrors.New("incorrect from timestamp")
		}

	case received.From != nil:
		if expected.From == nil {
			return aoserrors.New("incorrect from timestamp")
		}

		if *received.From != expected.From.AsTime() {
			return aoserrors.New("incorrect from timestamp")
		}

	case received.Till == nil:
		if expected.Till != nil {
			return aoserrors.New("incorrect till timestamp")
		}

	case received.Till != nil:
		if expected.Till == nil {
			return aoserrors.New("incorrect till timestamp")
		}

		if *received.Till != expected.Till.AsTime() {
			return aoserrors.New("incorrect till timestamp")
		}

	case received.ServiceID != expected.Instance.ServiceId:
		return aoserrors.New("incorrect ServiceID")

	case received.SubjectID == nil:
		if expected.Instance.SubjectId != "" {
			return aoserrors.New("incorrect subject ID")
		}

	case received.SubjectID != nil:
		if *received.SubjectID != expected.Instance.SubjectId {
			return aoserrors.New("incorrect subject ID")
		}

	case received.Instance == nil:
		if expected.Instance.Instance != -1 {
			return aoserrors.New("incorrect instance")
		}

	case received.Instance != nil:
		if *received.Instance != uint64(expected.Instance.Instance) {
			return aoserrors.New("incorrect instance")
		}
	}

	return nil
}
