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
	"github.com/aoscloud/aos_servicemanager/launcher"
	"github.com/aoscloud/aos_servicemanager/layermanager"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
	"github.com/aoscloud/aos_servicemanager/smserver"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	serverURL = "localhost:8092"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testLauncher struct {
	runtimeStatusChan chan launcher.RuntimeStatus
	wasRestart        bool
	envVarRequest     []cloudprotocol.EnvVarsInstanceInfo
	runRequest        []cloudprotocol.InstanceInfo
}

type testServiceManager struct {
	currentInstallRequests testServiceInstallRequest
	services               []servicemanager.ServiceInfo
}

type testStateHandler struct {
	newStateChan           chan cloudprotocol.NewState
	stateReqChan           chan cloudprotocol.StateRequest
	currentStateAcceptance cloudprotocol.StateAcceptance
	currentUpdateState     cloudprotocol.UpdateState
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

type testServiceInstallRequest struct {
	installInfo servicemanager.ServiceInfo
	serviceURL  string
	fileInfo    image.FileInfo
}

type testLayerInstallRequest struct {
	installInfo layermanager.LayerInfo
	layerURL    string
	fileInfo    image.FileInfo
}

/***********************************************************************************************************************
 * Init
 **********************************************************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestBoardConfiguration(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	var (
		testLauncher        = testLauncher{}
		testResourceManager = testResourceManager{version: "bcv1"}
		expectedBoardConfig = "{someboardconfig}"
	)

	smServer, err := smserver.New(&smConfig, &testLauncher, nil, nil, nil, nil, nil,
		&testResourceManager, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	defer smServer.Close()

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

func TestInstanceMessages(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	testLauncher := &testLauncher{runtimeStatusChan: make(chan launcher.RuntimeStatus, 10)}

	smServer, err := smserver.New(&smConfig, testLauncher, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	defer smServer.Close()

	client, err := newTestClient(serverURL)
	if err != nil {
		t.Fatalf("Can't create test client: %s", err)
	}

	defer client.close()

	sendEnvVars := &pb.OverrideEnvVarsRequest{EnvVars: []*pb.OverrideInstanceEnvVar{}}
	expectedRequest := []cloudprotocol.EnvVarsInstanceInfo{}
	expectedStatus := &pb.OverrideEnvVarStatus{EnvVarsStatus: []*pb.EnvVarInstanceStatus{}}

	for i := 0; i < 5; i++ {
		curentTime := time.Now()

		sendEnvVars.EnvVars = append(sendEnvVars.EnvVars, &pb.OverrideInstanceEnvVar{
			Instance: &pb.InstanceIdent{ServiceId: "id" + strconv.Itoa(i), SubjectId: "s1", Instance: int64(i - 1)},
			Vars: []*pb.EnvVarInfo{
				{VarId: "varId" + strconv.Itoa(i), Variable: "variable " + strconv.Itoa(i)},
				{VarId: "varId2" + strconv.Itoa(i), Variable: "variable 2" + strconv.Itoa(i), Ttl: timestamppb.New(curentTime)},
			},
		})

		expectedRequest = append(expectedRequest, cloudprotocol.EnvVarsInstanceInfo{
			InstanceFilter: createInstanceFilter("id"+strconv.Itoa(i), "s1", int64(i-1)),
			EnvVars: []cloudprotocol.EnvVarInfo{
				{ID: "varId" + strconv.Itoa(i), Variable: "variable " + strconv.Itoa(i)},
				{ID: "varId2" + strconv.Itoa(i), Variable: "variable 2" + strconv.Itoa(i), TTL: &curentTime},
			},
		})

		expectedStatus.EnvVarsStatus = append(expectedStatus.EnvVarsStatus, &pb.EnvVarInstanceStatus{
			Instance: &pb.InstanceIdent{ServiceId: "id" + strconv.Itoa(i), SubjectId: "s1", Instance: int64(i - 1)},
			VarsStatus: []*pb.EnvVarStatus{
				{VarId: "varId" + strconv.Itoa(i)}, {VarId: "varId2" + strconv.Itoa(i)},
			},
		})
	}

	sendEnvVars.EnvVars[0].Instance.SubjectId = ""
	expectedRequest[0].InstanceFilter.SubjectID = nil
	expectedStatus.EnvVarsStatus[0].Instance.SubjectId = ""

	envVarStatus, err := smServer.OverrideEnvVars(context.Background(), sendEnvVars)
	if err != nil {
		t.Fatalf("Can't override env vars: %s", err)
	}

	if !proto.Equal(envVarStatus, expectedStatus) {
		t.Error("Incorrect override env var status")
	}

	if reflect.DeepEqual(expectedRequest, testLauncher.envVarRequest) {
		t.Error("Incorrect env var request")
	}

	runRequest := &pb.RunInstancesRequest{Instances: []*pb.RunInstanceRequest{}}
	expectedRunRequest := []cloudprotocol.InstanceInfo{}
	expectedRuntimeStatus := &pb.RunInstancesStatus{}
	expectedUpdateStatus := &pb.UpdateInstancesStatus{}

	for i := 0; i < 6; i++ {
		runRequest.Instances = append(runRequest.Instances, &pb.RunInstanceRequest{
			ServiceId: "id" + strconv.Itoa(i), SubjectId: "subj" + strconv.Itoa(i), NumInstances: uint64(i + 1),
		})

		expectedRunRequest = append(expectedRunRequest, cloudprotocol.InstanceInfo{
			ServiceID: "id" + strconv.Itoa(i),
			SubjectID: "subj" + strconv.Itoa(i), NumInstances: uint64(i + 1),
		})

		if i%2 == 0 {
			expectedRuntimeStatus.Instances = append(expectedRuntimeStatus.Instances,
				&pb.InstanceStatus{
					Instance: &pb.InstanceIdent{
						ServiceId: "id" + strconv.Itoa(i), SubjectId: "subj" + strconv.Itoa(i),
						Instance: int64(i + 1),
					},
					AosVersion: uint64(i), StateChecksum: "checkSum", RunState: "running",
				})

			expectedRuntimeStatus.UnitSubjects = append(expectedRuntimeStatus.UnitSubjects, "subj"+strconv.Itoa(i))
		} else {
			expectedUpdateStatus.Instances = append(expectedUpdateStatus.Instances, &pb.InstanceStatus{
				Instance: &pb.InstanceIdent{
					ServiceId: "id" + strconv.Itoa(i), SubjectId: "subj" + strconv.Itoa(i),
					Instance: int64(i + 1),
				},
				AosVersion: uint64(i), StateChecksum: "checkSum", RunState: "running",
			})
		}
	}

	if _, err := smServer.RunInstances(context.Background(), runRequest); err != nil {
		t.Fatalf("Can't run instances")
	}

	if !reflect.DeepEqual(expectedRunRequest, testLauncher.runRequest) {
		t.Errorf("Incorrect run instances request")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	notifications, err := client.pbclient.SubscribeSMNotifications(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Can't subscribe: %s", err)
	}

	receivedRunNtf, err := notifications.Recv()
	if err != nil {
		t.Errorf("Can't receive run notification: %s", err)
	}

	if !proto.Equal(receivedRunNtf, &pb.SMNotifications{
		SMNotification: &pb.SMNotifications_RunInstancesStatus{RunInstancesStatus: expectedRuntimeStatus},
	}) {
		t.Error("Incorrect instance run status notification")
	}

	receivedUpdateNtf, err := notifications.Recv()
	if err != nil {
		t.Errorf("Can't receive instance update notification: %s", err)
	}

	if !proto.Equal(receivedUpdateNtf, &pb.SMNotifications{
		SMNotification: &pb.SMNotifications_UpdateInstancesStatus{UpdateInstancesStatus: expectedUpdateStatus},
	}) {
		t.Error("Incorrect instance update status notification")
	}
}

func TestServicesMessages(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	testServiceManager := testServiceManager{}

	smServer, err := smserver.New(&smConfig, nil, &testServiceManager, nil, nil, nil, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	defer smServer.Close()

	client, err := newTestClient(serverURL)
	if err != nil {
		t.Fatalf("Can't create test client: %s", err)
	}

	defer client.close()

	expectedServiceStatuses := &pb.ServicesStatus{}

	for i := 0; i < 5; i++ {
		installRequest := pb.InstallServiceRequest{
			Url:         "someurl" + strconv.Itoa(i),
			ServiceId:   "testService" + strconv.Itoa(i),
			ProviderId:  "provider" + strconv.Itoa(i),
			AosVersion:  uint64(i),
			AlertRules:  "alertRule" + strconv.Itoa(i),
			Description: "description" + strconv.Itoa(i),
			Sha256:      []byte{0, 0, 0, byte(i + 200)},
			Sha512:      []byte{byte(i + 300), 0, 0, 0},
			Size:        uint64(i + 500),
		}

		expectedInstallRequest := testServiceInstallRequest{
			installInfo: servicemanager.ServiceInfo{
				ServiceID:       "testService" + strconv.Itoa(i),
				AosVersion:      uint64(i),
				ServiceProvider: "provider" + strconv.Itoa(i),
				Description:     "description" + strconv.Itoa(i),
			},
			serviceURL: "someurl" + strconv.Itoa(i),
			fileInfo: image.FileInfo{
				Sha256: []byte{0, 0, 0, byte(i + 200)}, Sha512: []byte{byte(i + 300), 0, 0, 0},
				Size: uint64(i + 500),
			},
		}

		expectedStatus := pb.ServiceStatus{
			ServiceId: "testService" + strconv.Itoa(i), AosVersion: uint64(i),
		}

		if _, err := smServer.InstallService(context.Background(), &installRequest); err != nil {
			t.Fatalf("Can't install service: %s", err)
		}

		if !reflect.DeepEqual(expectedInstallRequest, testServiceManager.currentInstallRequests) {
			t.Error("Incorrect service install request")
		}

		expectedServiceStatuses.Services = append(expectedServiceStatuses.Services, &expectedStatus)
	}

	serviceStatuses, err := smServer.GetServicesStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Errorf("Can't get service statuses: %s", err)
	}

	if !proto.Equal(serviceStatuses, expectedServiceStatuses) {
		t.Errorf("Incorrect service statuses")
	}
}

func TestLayerMessages(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	testLayerManager := &testLayerManager{}

	smServer, err := smserver.New(&smConfig, nil, nil, testLayerManager, nil, nil, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	defer smServer.Close()

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

	smServer, err := smserver.New(&smConfig, nil, nil, nil, nil, testAlerts, nil, nil,
		nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	defer smServer.Close()

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

	smServer, err := smserver.New(&smConfig, nil, nil, nil, nil, nil, testMonitoring, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	defer smServer.Close()

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

func TestInstanceStateProcessing(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	stateHandler := testStateHandler{
		newStateChan: make(chan cloudprotocol.NewState, 10),
		stateReqChan: make(chan cloudprotocol.StateRequest, 10),
	}

	smServer, err := smserver.New(&smConfig, nil, nil, nil, &stateHandler, nil, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	defer smServer.Close()

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

	var (
		stateStr    = "super mega test state"
		checkSumStr = "someCheckSum"
	)

	expectedNewState := &pb.SMNotifications{SMNotification: &pb.SMNotifications_NewInstanceState{
		NewInstanceState: &pb.NewInstanceState{State: &pb.InstanceState{
			Instance:      &pb.InstanceIdent{ServiceId: "service1", SubjectId: "s1", Instance: 2},
			StateChecksum: checkSumStr,
			State:         []byte(stateStr),
		}},
	}}

	stateHandler.newStateChan <- cloudprotocol.NewState{
		InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "service1", SubjectID: "s1", Instance: 2},
		Checksum:      checkSumStr, State: stateStr,
	}

	receivedNewState, err := notifications.Recv()
	if err != nil {
		t.Fatalf("Can't receive notification data: %s", err)
	}

	if !proto.Equal(expectedNewState, receivedNewState) {
		t.Error("received new state doesn't match sent data")
	}

	expectedStateStateAcceptance := cloudprotocol.StateAcceptance{
		InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "service1", SubjectID: "s1", Instance: 2},
		Checksum:      checkSumStr, Result: "result", Reason: "OK",
	}

	if _, err := smServer.InstanceStateAcceptance(context.Background(),
		&pb.StateAcceptance{
			Instance:      &pb.InstanceIdent{ServiceId: "service1", SubjectId: "s1", Instance: 2},
			StateChecksum: checkSumStr, Result: "result", Reason: "OK",
		}); err != nil {
		t.Fatalf("Can't call instance state acceptance: %s", err)
	}

	if !reflect.DeepEqual(expectedStateStateAcceptance, stateHandler.currentStateAcceptance) {
		t.Errorf("Incorrect state acceptance message")
	}

	expectedStateRequest := &pb.SMNotifications{SMNotification: &pb.SMNotifications_InstanceStateRequest{
		InstanceStateRequest: &pb.InstanceStateRequest{
			Instance: &pb.InstanceIdent{ServiceId: "service1", SubjectId: "s1", Instance: 2}, Default: true,
		},
	}}

	stateHandler.stateReqChan <- cloudprotocol.StateRequest{
		InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "service1", SubjectID: "s1", Instance: 2}, Default: true,
	}

	receivedStateRequest, err := notifications.Recv()
	if err != nil {
		t.Fatalf("Can't receive notification data: %s", err)
	}

	if !proto.Equal(expectedStateRequest, receivedStateRequest) {
		t.Error("received state doesn't match sent data")
	}

	expectedUpdateState := cloudprotocol.UpdateState{
		InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "service1", SubjectID: "s1", Instance: 2},
		Checksum:      checkSumStr, State: stateStr,
	}

	if _, err := smServer.SetInstanceState(context.Background(), &pb.InstanceState{
		Instance:      &pb.InstanceIdent{ServiceId: "service1", SubjectId: "s1", Instance: 2},
		StateChecksum: checkSumStr,
		State:         []byte(stateStr),
	}); err != nil {
		t.Fatalf("Can't set instance state: %s", err)
	}

	if !reflect.DeepEqual(expectedUpdateState, stateHandler.currentUpdateState) {
		t.Errorf("Received update state doesn't match expected one")
	}
}

func TestLogsNotification(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	logProvider := testLogProvider{channel: make(chan cloudprotocol.PushLog)}

	smServer, err := smserver.New(&smConfig, nil, nil, nil, nil, nil, nil, nil, &logProvider, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM server: %s", err)
	}

	defer smServer.Close()

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

/***********************************************************************************************************************
 * Interfaces
 **********************************************************************************************************************/

func (handler *testStateHandler) NewStateChannel() <-chan cloudprotocol.NewState {
	return handler.newStateChan
}

func (handler *testStateHandler) StateRequestChannel() <-chan cloudprotocol.StateRequest {
	return handler.stateReqChan
}

func (handler *testStateHandler) UpdateState(updateState cloudprotocol.UpdateState) error {
	handler.currentUpdateState = updateState

	return nil
}

func (handler *testStateHandler) StateAcceptance(stateAcceptance cloudprotocol.StateAcceptance) error {
	handler.currentStateAcceptance = stateAcceptance

	return nil
}

func (testlauncher *testLauncher) RunInstances(instances []cloudprotocol.InstanceInfo) error {
	testlauncher.runRequest = instances

	var (
		runStatus    launcher.RunInstancesStatus
		updateStatus launcher.UpdateInstancesStatus
		notification launcher.RuntimeStatus
	)

	for i, runReq := range instances {
		instanceStatus := cloudprotocol.InstanceStatus{
			InstanceIdent: cloudprotocol.InstanceIdent{
				ServiceID: runReq.ServiceID,
				SubjectID: runReq.SubjectID, Instance: runReq.NumInstances,
			},
			AosVersion: uint64(i), StateChecksum: "checkSum", RunState: "running",
		}

		if i%2 == 0 {
			runStatus.Instances = append(runStatus.Instances, instanceStatus)
			runStatus.UnitSubjects = append(runStatus.UnitSubjects, runReq.SubjectID)
		} else {
			updateStatus.Instances = append(updateStatus.Instances, instanceStatus)
		}
	}

	notification.RunStatus = &runStatus
	testlauncher.runtimeStatusChan <- notification
	notification.RunStatus = nil
	notification.UpdateStatus = &updateStatus
	testlauncher.runtimeStatusChan <- notification

	return nil
}

func (testlauncher *testLauncher) RuntimeStatusChannel() <-chan launcher.RuntimeStatus {
	return testlauncher.runtimeStatusChan
}

func (testlauncher *testLauncher) RestartInstances() error {
	testlauncher.wasRestart = true

	return nil
}

func (testlauncher *testLauncher) OverrideEnvVars(
	envVarsInfo []cloudprotocol.EnvVarsInstanceInfo,
) (status []cloudprotocol.EnvVarsInstanceStatus, err error) {
	testlauncher.envVarRequest = envVarsInfo

	for _, info := range envVarsInfo {
		oneStatus := cloudprotocol.EnvVarsInstanceStatus{
			InstanceFilter: info.InstanceFilter, Statuses: make([]cloudprotocol.EnvVarStatus, len(info.EnvVars)),
		}

		for i, envVar := range info.EnvVars {
			oneStatus.Statuses[i] = cloudprotocol.EnvVarStatus{ID: envVar.ID}
		}

		status = append(status, oneStatus)
	}

	return status, nil
}

func (serviceMgr *testServiceManager) InstallService(
	newService servicemanager.ServiceInfo, imageURL string, fileInfo image.FileInfo,
) error {
	serviceMgr.currentInstallRequests = testServiceInstallRequest{
		installInfo: newService,
		serviceURL:  imageURL, fileInfo: fileInfo,
	}

	serviceMgr.services = append(serviceMgr.services, newService)

	return nil
}

func (serviceMgr *testServiceManager) GetAllServicesStatus() ([]servicemanager.ServiceInfo, error) {
	return serviceMgr.services, nil
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

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

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

func createInstanceFilter(serviceID, subjectID string, instance int64) (filter cloudprotocol.InstanceFilter) {
	filter.ServiceID = serviceID

	if subjectID != "" {
		filter.SubjectID = &subjectID
	}

	if instance != -1 {
		localInstance := (uint64)(instance)

		filter.Instance = &localInstance
	}

	return filter
}
