// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2022 Renesas Electronics Corporation.
// Copyright (C) 2022 EPAM Systems, Inc.
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

package smclient_test

import (
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	pb "github.com/aoscloud/aos_common/api/servicemanager/v3"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/launcher"
	"github.com/aoscloud/aos_servicemanager/smclient"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const serverURL = "localhost:8093"

const waitRegisteredTimeout = 30 * time.Second

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testServer struct {
	grpcServer        *grpc.Server
	stream            pb.SMService_RegisterSMServer
	registerChannel   chan *pb.NodeConfiguration
	alertChannel      chan *pb.Alert
	monitoringChannel chan *pb.SMOutgoingMessages_NodeMonitoring
	logChannel        chan *pb.SMOutgoingMessages_Log
	envVarsChannel    chan *pb.SMOutgoingMessages_OverrideEnvVarStatus
	pb.UnimplementedSMServiceServer
}

type testMonitoringProvider struct {
	monitoringChannel chan cloudprotocol.NodeMonitoringData
}

type testLogProvider struct {
	currentLogRequest cloudprotocol.RequestLog
	testLogs          []testLogData
	sentIndex         int
	channel           chan cloudprotocol.PushLog
}

type testLogData struct {
	internalLog   cloudprotocol.PushLog
	expectedPBLog pb.LogData
}
type testAlertProvider struct {
	alertsChannel chan cloudprotocol.AlertItem
}

type testServiceManager struct {
	services []aostypes.ServiceInfo
}

type testLayerManager struct {
	layers []aostypes.LayerInfo
}

type testLauncher struct {
	instances     []aostypes.InstanceInfo
	forceRestart  bool
	envVarsInfo   []cloudprotocol.EnvVarsInstanceInfo
	envVarsStatus []cloudprotocol.EnvVarsInstanceStatus

	callChannel       chan struct{}
	connectionChannel chan bool
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

func TestSMRegistration(t *testing.T) {
	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer server.close()

	testMonitoring := &testMonitoringProvider{
		monitoringChannel: make(chan cloudprotocol.NodeMonitoringData, 10),
	}

	systemInfo := cloudprotocol.SystemInfo{
		NumCPUs: 1, TotalRAM: 100,
		Partitions: []cloudprotocol.PartitionInfo{{Name: "p1", Types: []string{"t1"}, TotalSize: 200}},
	}

	client, err := smclient.New(&config.Config{CMServerURL: serverURL, RunnerFeatures: []string{"crun"}},
		smclient.NodeDescription{NodeID: "mainSM", NodeType: "model1", SystemInfo: systemInfo},
		nil, nil, nil, nil, nil, nil, testMonitoring, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	expectedNodeConfiguration := &pb.NodeConfiguration{
		NodeId: "mainSM", NodeType: "model1", RemoteNode: false, RunnerFeatures: []string{"crun"},
		NumCpus: 1, TotalRam: 100,
		Partitions: []*pb.Partition{{Name: "p1", Types: []string{"t1"}, TotalSize: 200}},
	}

	if err = server.waitClientRegistered(expectedNodeConfiguration); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}
}

func TestMonitoringNotifications(t *testing.T) {
	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer server.close()

	testMonitoring := &testMonitoringProvider{
		monitoringChannel: make(chan cloudprotocol.NodeMonitoringData, 10),
	}

	systemInfo := cloudprotocol.SystemInfo{
		NumCPUs: 1, TotalRAM: 100,
		Partitions: []cloudprotocol.PartitionInfo{{Name: "p1", Types: []string{"t1"}, TotalSize: 200}},
	}

	client, err := smclient.New(&config.Config{
		CMServerURL: serverURL, RemoteNode: true, RunnerFeatures: []string{"crun"},
	}, smclient.NodeDescription{NodeID: "mainSM", NodeType: "model1", SystemInfo: systemInfo},
		nil, nil, nil, nil, nil, nil, testMonitoring, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	expectedNodeConfiguration := &pb.NodeConfiguration{
		NodeId: "mainSM", NodeType: "model1", RemoteNode: true, RunnerFeatures: []string{"crun"},
		NumCpus: 1, TotalRam: 100,
		Partitions: []*pb.Partition{{Name: "p1", Types: []string{"t1"}, TotalSize: 200}},
	}

	if err = server.waitClientRegistered(expectedNodeConfiguration); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	type testMonitoringElement struct {
		sendMonitoring     cloudprotocol.NodeMonitoringData
		expectedMonitoring pb.NodeMonitoring
	}

	testMonitoringData := []testMonitoringElement{
		{
			sendMonitoring: cloudprotocol.NodeMonitoringData{
				MonitoringData: cloudprotocol.MonitoringData{
					RAM: 10, CPU: 20, InTraffic: 40, OutTraffic: 50,
					Disk: []cloudprotocol.PartitionUsage{{Name: "p1", UsedSize: 100}},
				},
			},
			expectedMonitoring: pb.NodeMonitoring{
				MonitoringData: &pb.MonitoringData{
					Ram: 10, Cpu: 20, InTraffic: 40, OutTraffic: 50,
					Disk: []*pb.PartitionUsage{{Name: "p1", UsedSize: 100}},
				},
			},
		},
		{
			sendMonitoring: cloudprotocol.NodeMonitoringData{
				MonitoringData: cloudprotocol.MonitoringData{
					RAM: 10, CPU: 20, InTraffic: 40, OutTraffic: 50,
					Disk: []cloudprotocol.PartitionUsage{{Name: "p1", UsedSize: 100}},
				},
				ServiceInstances: []cloudprotocol.InstanceMonitoringData{
					{
						InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "s1", Instance: 1},
						MonitoringData: cloudprotocol.MonitoringData{
							RAM: 10, CPU: 20, InTraffic: 40, OutTraffic: 50,
							Disk: []cloudprotocol.PartitionUsage{{Name: "ps1", UsedSize: 100}},
						},
					},
					{
						InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "s1", Instance: 1},
						MonitoringData: cloudprotocol.MonitoringData{
							RAM: 10, CPU: 20, InTraffic: 40, OutTraffic: 50,
							Disk: []cloudprotocol.PartitionUsage{{Name: "ps2", UsedSize: 100}},
						},
					},
				},
			},
			expectedMonitoring: pb.NodeMonitoring{
				MonitoringData: &pb.MonitoringData{
					Ram: 10, Cpu: 20, InTraffic: 40, OutTraffic: 50,
					Disk: []*pb.PartitionUsage{{Name: "p1", UsedSize: 100}},
				},
				InstanceMonitoring: []*pb.InstanceMonitoring{
					{
						Instance: &pb.InstanceIdent{ServiceId: "service1", SubjectId: "s1", Instance: 1},
						MonitoringData: &pb.MonitoringData{
							Ram: 10, Cpu: 20, InTraffic: 40, OutTraffic: 50,
							Disk: []*pb.PartitionUsage{{Name: "ps1", UsedSize: 100}},
						},
					},
					{
						Instance: &pb.InstanceIdent{ServiceId: "service2", SubjectId: "s1", Instance: 1},
						MonitoringData: &pb.MonitoringData{
							Ram: 10, Cpu: 20, InTraffic: 40, OutTraffic: 50,
							Disk: []*pb.PartitionUsage{{Name: "ps2", UsedSize: 100}},
						},
					},
				},
			},
		},
	}

	for i := range testMonitoringData {
		testMonitoring.monitoringChannel <- testMonitoringData[i].sendMonitoring

		receivedMonitoring := <-server.monitoringChannel

		receivedMonitoring.NodeMonitoring.Timestamp = nil

		if !proto.Equal(receivedMonitoring.NodeMonitoring, &testMonitoringData[i].expectedMonitoring) {
			log.Debug("R :", receivedMonitoring.NodeMonitoring)
			log.Debug("E :", &testMonitoringData[i].expectedMonitoring)
			t.Errorf("Incorrect monitoring data")
		}
	}
}

func TestLogsNotification(t *testing.T) {
	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer server.close()

	logProvider := testLogProvider{channel: make(chan cloudprotocol.PushLog)}

	client, err := smclient.New(&config.Config{CMServerURL: serverURL},
		smclient.NodeDescription{NodeID: "mainSM", NodeType: "model1", SystemInfo: cloudprotocol.SystemInfo{}},
		nil, nil, nil, nil, nil, nil, nil, &logProvider, nil, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	expectedNodeConfiguration := &pb.NodeConfiguration{
		NodeId: "mainSM", NodeType: "model1",
	}

	if err = server.waitClientRegistered(expectedNodeConfiguration); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	logProvider.testLogs = []testLogData{
		{
			internalLog:   cloudprotocol.PushLog{LogID: "systemLog", Content: []byte{1, 2, 3}},
			expectedPBLog: pb.LogData{LogId: "systemLog", Data: []byte{1, 2, 3}},
		},
		{
			internalLog:   cloudprotocol.PushLog{LogID: "serviceLog1", Content: []byte{1, 2, 4}, PartsCount: 10, Part: 1},
			expectedPBLog: pb.LogData{LogId: "serviceLog1", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			internalLog:   cloudprotocol.PushLog{LogID: "serviceLog2", Content: []byte{1, 2, 4}, PartsCount: 10, Part: 1},
			expectedPBLog: pb.LogData{LogId: "serviceLog2", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			internalLog:   cloudprotocol.PushLog{LogID: "serviceLog3", Content: []byte{1, 2, 4}, PartsCount: 10, Part: 1},
			expectedPBLog: pb.LogData{LogId: "serviceLog3", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			internalLog: cloudprotocol.PushLog{
				LogID: "serviceCrashLog", Content: []byte{1, 2, 4},
				ErrorInfo: &cloudprotocol.ErrorInfo{
					Message: "some error",
				},
				Part: 1,
			},
			expectedPBLog: pb.LogData{LogId: "serviceCrashLog", Data: []byte{1, 2, 4}, Error: "some error", Part: 1},
		},
	}

	if err := server.stream.Send(&pb.SMIncomingMessages{SMIncomingMessage: &pb.SMIncomingMessages_SystemLogRequest{
		SystemLogRequest: &pb.SystemLogRequest{LogId: "systemlog"},
	}}); err != nil {
		t.Fatalf("Can't get system log: %v", err)
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
		if err := server.stream.Send(&pb.SMIncomingMessages{
			SMIncomingMessage: &pb.SMIncomingMessages_InstanceLogRequest{
				InstanceLogRequest: &instanceLogRequests[i],
			},
		}); err != nil {
			t.Fatalf("Can't get instance log: %v", err)
		}
	}

	if err := server.stream.Send(&pb.SMIncomingMessages{
		SMIncomingMessage: &pb.SMIncomingMessages_InstanceCrashLogRequest{
			InstanceCrashLogRequest: &pb.InstanceCrashLogRequest{
				LogId: "serviceCrashLog", Instance: &pb.InstanceIdent{ServiceId: "id3"},
			},
		},
	}); err != nil {
		t.Fatalf("Can't get instance crash log: %v", err)
	}

	if err := server.waitAndCheckLogs(logProvider.testLogs); err != nil {
		t.Fatalf("Incorrect logs: %v", err)
	}
}

func TestAlertNotifications(t *testing.T) {
	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer server.close()

	testAlerts := &testAlertProvider{alertsChannel: make(chan cloudprotocol.AlertItem, 10)}

	client, err := smclient.New(&config.Config{CMServerURL: serverURL},
		smclient.NodeDescription{NodeID: "mainSM", NodeType: "model1", SystemInfo: cloudprotocol.SystemInfo{}},
		nil, nil, nil, nil, nil, testAlerts, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	expectedNodeConfiguration := &pb.NodeConfiguration{
		NodeId: "mainSM", NodeType: "model1",
	}

	if err = server.waitClientRegistered(expectedNodeConfiguration); err != nil {
		t.Fatalf("SM registration error: %v", err)
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
				Tag: cloudprotocol.AlertTagAosCore,
				Payload: &pb.Alert_CoreAlert{
					CoreAlert: &pb.CoreAlert{CoreComponent: "SM", Message: "CoreAlertMessage"},
				},
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
					InstanceIdent: aostypes.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
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
					InstanceIdent: aostypes.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
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
					InstanceIdent: aostypes.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
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

		receivedAlert := <-server.alertChannel

		receivedAlert.Timestamp = nil

		if !proto.Equal(receivedAlert, &testAlertItems[i].expectedAlert) {
			t.Errorf("Incorrect log item %s", receivedAlert.Tag)
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
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
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
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
				Parameter:     "param1", Value: 42,
			},
		},

		{
			Tag: cloudprotocol.AlertTagResourceValidate,
			Payload: cloudprotocol.ServiceInstanceAlert{
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
				Message:       "ServiceInstanceAlert", AosVersion: 42,
			},
		},
	}

	for i := range invalidAlerts {
		testAlerts.alertsChannel <- invalidAlerts[i]
	}

	select {
	case <-server.alertChannel:
		t.Error("Unexpected Alert")

	case <-time.After(1 * time.Second):
	}
}

func TestRunInstances(t *testing.T) {
	data := []*pb.RunInstances{
		{},
		{ForceRestart: true},
		{
			Services: []*pb.ServiceInfo{
				{
					VersionInfo: &pb.VersionInfo{AosVersion: 4, VendorVersion: "32", Description: "this is service 1"},
					Url:         "url123",
					ServiceId:   "service1",
					ProviderId:  "provider1",
					Gid:         984,
					Sha256:      []byte("fdksj"),
					Sha512:      []byte("popk"),
					Size:        789,
				},
				{
					VersionInfo: &pb.VersionInfo{AosVersion: 6, VendorVersion: "17", Description: "this is service 2"},
					Url:         "url354",
					ServiceId:   "service2",
					ProviderId:  "provider2",
					Gid:         21,
					Sha256:      []byte("sdfafsd"),
					Sha512:      []byte("dsklddf"),
					Size:        12900,
				},
			},
			Layers: []*pb.LayerInfo{
				{
					VersionInfo: &pb.VersionInfo{AosVersion: 1, VendorVersion: "7", Description: "this is layer 1"},
					Url:         "url670",
					LayerId:     "layer1",
					Digest:      "digest2329",
					Sha256:      []byte("sassfdc"),
					Sha512:      []byte("dsdsjkk"),
					Size:        3489,
				},
				{
					VersionInfo: &pb.VersionInfo{AosVersion: 3, VendorVersion: "9", Description: "this is layer 2"},
					Url:         "url654",
					LayerId:     "layer2",
					Digest:      "digest6509",
					Sha256:      []byte("asdasdd"),
					Sha512:      []byte("pcxalks"),
					Size:        3489,
				},
				{
					VersionInfo: &pb.VersionInfo{AosVersion: 5, VendorVersion: "5", Description: "this is layer 3"},
					Url:         "url986",
					LayerId:     "layer3",
					Digest:      "digest3209",
					Sha256:      []byte("dsakjcd"),
					Sha512:      []byte("cszxdfa"),
					Size:        3489,
				},
			},
			Instances: []*pb.InstanceInfo{
				{
					Instance: &pb.InstanceIdent{
						ServiceId: "service1",
						SubjectId: "subject1",
						Instance:  0,
					},
					NetworkParameters: &pb.NetworkParameters{
						Ip:     "172.17.0.1",
						Subnet: "172.17.0.0/16",
						VlanId: 1,
					},
					Uid:         329,
					Priority:    12,
					StoragePath: "storagePath1",
					StatePath:   "statePath1",
				},
				{
					Instance: &pb.InstanceIdent{
						ServiceId: "service1",
						SubjectId: "subject1",
						Instance:  1,
					},
					NetworkParameters: &pb.NetworkParameters{
						Ip:     "172.17.0.2",
						Subnet: "172.17.0.0/16",
						VlanId: 1,
					},
					Uid:         876,
					Priority:    32,
					StoragePath: "storagePath2",
					StatePath:   "statePath2",
				},
				{
					Instance: &pb.InstanceIdent{
						ServiceId: "service2",
						SubjectId: "subject1",
						Instance:  0,
					},
					NetworkParameters: &pb.NetworkParameters{
						Ip:     "172.17.0.3",
						Subnet: "172.17.0.0/16",
						VlanId: 1,
					},
					Uid:         543,
					Priority:    29,
					StoragePath: "storagePath3",
					StatePath:   "statePath3",
				},
				{
					Instance: &pb.InstanceIdent{
						ServiceId: "service2",
						SubjectId: "subject2",
						Instance:  5,
					},
					NetworkParameters: &pb.NetworkParameters{
						Ip:     "172.17.0.4",
						Subnet: "172.17.0.0/16",
						VlanId: 1,
					},
					Uid:         765,
					Priority:    37,
					StoragePath: "storagePath4",
					StatePath:   "statePath4",
				},
			},
		},
	}

	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer server.close()

	serviceManager := &testServiceManager{}
	layerManager := &testLayerManager{}
	launcher := newTestLauncher()

	client, err := smclient.New(&config.Config{CMServerURL: serverURL},
		smclient.NodeDescription{NodeID: "mainSM", NodeType: "model1", SystemInfo: cloudprotocol.SystemInfo{}},
		nil, serviceManager, layerManager, launcher, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	if err := server.waitClientRegistered(&pb.NodeConfiguration{NodeId: "mainSM", NodeType: "model1"}); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	for _, req := range data {
		if err := server.stream.Send(&pb.SMIncomingMessages{
			SMIncomingMessage: &pb.SMIncomingMessages_RunInstances{RunInstances: req},
		}); err != nil {
			t.Fatalf("Can't send request: %v", err)
		}

		if err := launcher.waitCall(); err != nil {
			t.Fatalf("Error waiting call: %v", err)
		}

		services, layers, instances, forceRestart := convertRunInstancesReq(req)

		if !reflect.DeepEqual(serviceManager.services, services) {
			t.Errorf("Wrong services: %v", serviceManager.services)
		}

		if !reflect.DeepEqual(layerManager.layers, layers) {
			t.Errorf("Wrong layers: %v", layerManager.layers)
		}

		if !reflect.DeepEqual(launcher.instances, instances) {
			t.Errorf("Wrong instances: %v expected %v", launcher.instances, instances)
		}

		if launcher.forceRestart != forceRestart {
			t.Errorf("Wrong force restart value: %v", launcher.forceRestart)
		}
	}
}

func TestOverrideEnvVars(t *testing.T) {
	type testData struct {
		req    *pb.OverrideEnvVars
		status []cloudprotocol.EnvVarsInstanceStatus
	}

	data := []testData{
		{
			req:    &pb.OverrideEnvVars{},
			status: []cloudprotocol.EnvVarsInstanceStatus{},
		},
		{
			req: &pb.OverrideEnvVars{EnvVars: []*pb.OverrideInstanceEnvVar{
				{
					Instance: &pb.InstanceIdent{ServiceId: "service1", SubjectId: "subject1", Instance: 2},
					Vars: []*pb.EnvVarInfo{
						{VarId: "varID1", Variable: "var1", Ttl: timestamppb.Now()},
						{VarId: "varID2", Variable: "var2", Ttl: timestamppb.Now()},
						{VarId: "varID3", Variable: "var3", Ttl: timestamppb.Now()},
					},
				},
				{
					Instance: &pb.InstanceIdent{ServiceId: "service2", SubjectId: "subject3", Instance: 0},
					Vars: []*pb.EnvVarInfo{
						{VarId: "varID4", Variable: "var4", Ttl: timestamppb.Now()},
						{VarId: "varID5", Variable: "var5", Ttl: timestamppb.Now()},
					},
				},
				{
					Instance: &pb.InstanceIdent{ServiceId: "service3", Instance: -1},
					Vars: []*pb.EnvVarInfo{
						{VarId: "varID6", Variable: "var6", Ttl: timestamppb.Now()},
					},
				},
			}},
			status: []cloudprotocol.EnvVarsInstanceStatus{
				{
					InstanceFilter: cloudprotocol.NewInstanceFilter("service1", "subject1", -1),
					Statuses: []cloudprotocol.EnvVarStatus{
						{ID: "varID1"},
						{ID: "varID2", Error: "error2"},
					},
				},
				{
					InstanceFilter: cloudprotocol.NewInstanceFilter("service2", "subject3", 2),
					Statuses: []cloudprotocol.EnvVarStatus{
						{ID: "varID3", Error: "error3"},
						{ID: "varID4", Error: "error4"},
					},
				},
			},
		},
	}

	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer server.close()

	launcher := newTestLauncher()

	client, err := smclient.New(&config.Config{CMServerURL: serverURL},
		smclient.NodeDescription{NodeID: "mainSM", NodeType: "model1", SystemInfo: cloudprotocol.SystemInfo{}},
		nil, nil, nil, launcher, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	if err := server.waitClientRegistered(&pb.NodeConfiguration{NodeId: "mainSM", NodeType: "model1"}); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	for _, item := range data {
		launcher.envVarsStatus = item.status

		if err := server.stream.Send(&pb.SMIncomingMessages{
			SMIncomingMessage: &pb.SMIncomingMessages_OverrideEnvVars{OverrideEnvVars: item.req},
		}); err != nil {
			t.Fatalf("Can't send request: %v", err)
		}

		if err := launcher.waitCall(); err != nil {
			t.Fatalf("Error waiting call: %v", err)
		}

		envVars := convertEnvVarsReq(item.req)

		if !reflect.DeepEqual(launcher.envVarsInfo, envVars) {
			t.Errorf("Wrong env vars: %v", launcher.envVarsInfo)
		}

		if err := server.waitEnvVarsStatus(item.status); err != nil {
			t.Errorf("Wait override env status error: %v", err)
		}
	}
}

func TestCloudConnection(t *testing.T) {
	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer server.close()

	launcher := newTestLauncher()

	client, err := smclient.New(&config.Config{CMServerURL: serverURL},
		smclient.NodeDescription{NodeID: "mainSM", NodeType: "model1", SystemInfo: cloudprotocol.SystemInfo{}},
		nil, nil, nil, launcher, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	if err := server.waitClientRegistered(&pb.NodeConfiguration{NodeId: "mainSM", NodeType: "model1"}); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	// Check connected status

	if err := server.stream.Send(&pb.SMIncomingMessages{
		SMIncomingMessage: &pb.SMIncomingMessages_ConnectionStatus{ConnectionStatus: &pb.ConnectionStatus{
			CloudStatus: pb.ConnectionEnum_CONNECTED,
		}},
	}); err != nil {
		t.Fatalf("Can't send request: %v", err)
	}

	connected, err := launcher.waitCloudConnection()
	if err != nil {
		t.Fatalf("Cloud connection error: %v", err)
	}

	if !connected {
		t.Error("Cloud should be connected")
	}

	// Check disconnected status

	if err := server.stream.Send(&pb.SMIncomingMessages{
		SMIncomingMessage: &pb.SMIncomingMessages_ConnectionStatus{ConnectionStatus: &pb.ConnectionStatus{
			CloudStatus: pb.ConnectionEnum_DISCONNECTED,
		}},
	}); err != nil {
		t.Fatalf("Can't send request: %v", err)
	}

	if connected, err = launcher.waitCloudConnection(); err != nil {
		t.Fatalf("Cloud connection error: %v", err)
	}

	if connected {
		t.Error("Cloud should be disconnected")
	}
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func newTestServer(url string) (server *testServer, err error) {
	server = &testServer{
		registerChannel:   make(chan *pb.NodeConfiguration, 10),
		alertChannel:      make(chan *pb.Alert, 10),
		monitoringChannel: make(chan *pb.SMOutgoingMessages_NodeMonitoring, 10),
		logChannel:        make(chan *pb.SMOutgoingMessages_Log, 10),
		envVarsChannel:    make(chan *pb.SMOutgoingMessages_OverrideEnvVarStatus, 10),
	}

	listener, err := net.Listen("tcp", url)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	server.grpcServer = grpc.NewServer()

	pb.RegisterSMServiceServer(server.grpcServer, server)

	go func() {
		if err := server.grpcServer.Serve(listener); err != nil {
			log.Errorf("Can't serve grpc server: %v", err)
		}
	}()

	return server, nil
}

func (server *testServer) close() {
	if server.grpcServer != nil {
		server.grpcServer.Stop()
	}
}

func (server *testServer) waitClientRegistered(expectedNodeConfig *pb.NodeConfiguration) error {
	select {
	case nodeConfig := <-server.registerChannel:
		if !proto.Equal(nodeConfig, expectedNodeConfig) {
			return aoserrors.New("incorrect node configuration")
		}

		return nil

	case <-time.After(waitRegisteredTimeout):
		return aoserrors.New("timeout")
	}
}

func (server *testServer) RegisterSM(stream pb.SMService_RegisterSMServer) error {
	server.stream = stream

	for {
		message, err := server.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return aoserrors.Wrap(err)
		}

		switch data := message.SMOutgoingMessage.(type) {
		case *pb.SMOutgoingMessages_NodeConfiguration:
			server.registerChannel <- data.NodeConfiguration

		case *pb.SMOutgoingMessages_NodeMonitoring:
			server.monitoringChannel <- data

		case *pb.SMOutgoingMessages_Log:
			server.logChannel <- data

		case *pb.SMOutgoingMessages_Alert:
			server.alertChannel <- data.Alert

		case *pb.SMOutgoingMessages_OverrideEnvVarStatus:
			server.envVarsChannel <- data
		}
	}
}

func (server *testServer) waitAndCheckLogs(testLogs []testLogData) error {
	var currentIndex int

	for {
		select {
		case logData := <-server.logChannel:
			if !proto.Equal(logData.Log, &testLogs[currentIndex].expectedPBLog) {
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

func (server *testServer) waitEnvVarsStatus(status []cloudprotocol.EnvVarsInstanceStatus) error {
	select {
	case data := <-server.envVarsChannel:
		receivedStatus := make([]cloudprotocol.EnvVarsInstanceStatus, len(data.OverrideEnvVarStatus.EnvVarsStatus))

		for i, envVarStatus := range data.OverrideEnvVarStatus.EnvVarsStatus {
			receivedStatus[i] = cloudprotocol.EnvVarsInstanceStatus{
				InstanceFilter: cloudprotocol.NewInstanceFilter(
					envVarStatus.Instance.ServiceId, envVarStatus.Instance.SubjectId, envVarStatus.Instance.Instance),
				Statuses: make([]cloudprotocol.EnvVarStatus, len(envVarStatus.VarsStatus)),
			}

			for j, s := range envVarStatus.VarsStatus {
				receivedStatus[i].Statuses[j] = cloudprotocol.EnvVarStatus{ID: s.VarId, Error: s.Error}
			}
		}

		if !reflect.DeepEqual(receivedStatus, status) {
			return aoserrors.New("wrong env vars status")
		}

		return nil

	case <-time.After(5 * time.Second):
		return aoserrors.New("wait env vars status timeout")
	}
}

func convertRunInstancesReq(req *pb.RunInstances) (
	services []aostypes.ServiceInfo, layers []aostypes.LayerInfo, instances []aostypes.InstanceInfo, forceRestart bool,
) {
	services = make([]aostypes.ServiceInfo, len(req.Services))

	for i, service := range req.Services {
		services[i] = aostypes.ServiceInfo{
			VersionInfo: aostypes.VersionInfo{
				AosVersion:    service.VersionInfo.AosVersion,
				VendorVersion: service.VersionInfo.VendorVersion,
				Description:   service.VersionInfo.Description,
			},
			ID:         service.ServiceId,
			ProviderID: service.ProviderId,
			GID:        service.Gid,
			URL:        service.Url,
			Sha256:     service.Sha256,
			Sha512:     service.Sha512,
			Size:       service.Size,
		}
	}

	layers = make([]aostypes.LayerInfo, len(req.Layers))

	for i, layer := range req.Layers {
		layers[i] = aostypes.LayerInfo{
			VersionInfo: aostypes.VersionInfo{
				AosVersion:    layer.VersionInfo.AosVersion,
				VendorVersion: layer.VersionInfo.VendorVersion,
				Description:   layer.VersionInfo.Description,
			},
			ID:     layer.LayerId,
			Digest: layer.Digest,
			URL:    layer.Url,
			Sha256: layer.Sha256,
			Sha512: layer.Sha512,
			Size:   layer.Size,
		}
	}

	instances = make([]aostypes.InstanceInfo, len(req.Instances))

	for i, instance := range req.Instances {
		instances[i] = aostypes.InstanceInfo{
			InstanceIdent: aostypes.InstanceIdent{
				ServiceID: instance.Instance.ServiceId,
				SubjectID: instance.Instance.SubjectId,
				Instance:  uint64(instance.Instance.Instance),
			},
			NetworkParameters: aostypes.NetworkParameters{
				IP:     instance.NetworkParameters.Ip,
				Subnet: instance.NetworkParameters.Subnet,
				VlanID: instance.NetworkParameters.VlanId,
			},
			UID:         instance.Uid,
			Priority:    instance.Priority,
			StoragePath: instance.StoragePath,
			StatePath:   instance.StatePath,
		}
	}

	forceRestart = req.ForceRestart

	return services, layers, instances, forceRestart
}

func convertEnvVarsReq(req *pb.OverrideEnvVars) []cloudprotocol.EnvVarsInstanceInfo {
	envVars := make([]cloudprotocol.EnvVarsInstanceInfo, len(req.EnvVars))

	for i, envVar := range req.EnvVars {
		envVars[i] = cloudprotocol.EnvVarsInstanceInfo{
			InstanceFilter: cloudprotocol.NewInstanceFilter(
				envVar.Instance.ServiceId, envVar.Instance.SubjectId, envVar.Instance.Instance),
			EnvVars: make([]cloudprotocol.EnvVarInfo, len(envVar.Vars)),
		}

		for j, v := range envVar.Vars {
			envVars[i].EnvVars[j] = cloudprotocol.EnvVarInfo{ID: v.VarId, Variable: v.Variable}

			if v.Ttl != nil {
				t := v.Ttl.AsTime()

				envVars[i].EnvVars[j].TTL = &t
			}
		}
	}

	return envVars
}

/***********************************************************************************************************************
 * Interfaces
 **********************************************************************************************************************/

func (monitoring *testMonitoringProvider) GetMonitoringDataChannel() (
	channel <-chan cloudprotocol.NodeMonitoringData,
) {
	return monitoring.monitoringChannel
}

func (logProvider *testLogProvider) GetInstanceLog(request cloudprotocol.RequestLog) error {
	logProvider.currentLogRequest = request
	logProvider.channel <- logProvider.testLogs[logProvider.sentIndex].internalLog
	logProvider.sentIndex++

	return nil
}

func (logProvider *testLogProvider) GetInstanceCrashLog(request cloudprotocol.RequestLog) error {
	logProvider.channel <- logProvider.testLogs[logProvider.sentIndex].internalLog
	logProvider.sentIndex++

	return nil
}

func (logProvider *testLogProvider) GetSystemLog(request cloudprotocol.RequestLog) {
	logProvider.channel <- logProvider.testLogs[logProvider.sentIndex].internalLog
	logProvider.sentIndex++
}

func (logProvider *testLogProvider) GetLogsDataChannel() (channel <-chan cloudprotocol.PushLog) {
	return logProvider.channel
}

func (alerts *testAlertProvider) GetAlertsChannel() (channel <-chan cloudprotocol.AlertItem) {
	return alerts.alertsChannel
}

func (processor *testServiceManager) ProcessDesiredServices(services []aostypes.ServiceInfo) error {
	processor.services = services

	return nil
}

func (processor *testLayerManager) ProcessDesiredLayers(layers []aostypes.LayerInfo) error {
	processor.layers = layers

	return nil
}

func newTestLauncher() *testLauncher {
	return &testLauncher{callChannel: make(chan struct{}, 1), connectionChannel: make(chan bool, 1)}
}

func (launcher *testLauncher) RunInstances(instances []aostypes.InstanceInfo, forceRestart bool) error {
	launcher.instances = instances
	launcher.forceRestart = forceRestart

	launcher.callChannel <- struct{}{}

	return nil
}

func (launcher *testLauncher) RuntimeStatusChannel() <-chan launcher.RuntimeStatus {
	return nil
}

func (launcher *testLauncher) OverrideEnvVars(
	envVarsInfo []cloudprotocol.EnvVarsInstanceInfo,
) ([]cloudprotocol.EnvVarsInstanceStatus, error) {
	launcher.envVarsInfo = envVarsInfo

	launcher.callChannel <- struct{}{}

	return launcher.envVarsStatus, nil
}

func (launcher *testLauncher) CloudConnection(connected bool) error {
	launcher.connectionChannel <- connected

	return nil
}

func (launcher *testLauncher) waitCall() error {
	select {
	case <-launcher.callChannel:
		return nil

	case <-time.After(5 * time.Second):
		return aoserrors.New("wait call timeout")
	}
}

func (launcher *testLauncher) waitCloudConnection() (bool, error) {
	select {
	case connected := <-launcher.connectionChannel:
		return connected, nil

	case <-time.After(5 * time.Second):
		return false, aoserrors.New("wait cloud connection timeout")
	}
}
