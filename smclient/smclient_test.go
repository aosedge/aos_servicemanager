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

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/aostypes"
	"github.com/aosedge/aos_common/api/cloudprotocol"
	pbcommon "github.com/aosedge/aos_common/api/common"
	pbsm "github.com/aosedge/aos_common/api/servicemanager"
	"github.com/aosedge/aos_common/utils/pbconvert"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aosedge/aos_servicemanager/config"
	"github.com/aosedge/aos_servicemanager/launcher"
	"github.com/aosedge/aos_servicemanager/smclient"
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
	grpcServer               *grpc.Server
	stream                   pbsm.SMService_RegisterSMServer
	nodeConfigStatusChannel  chan *pbsm.NodeConfigStatus
	alertChannel             chan *pbsm.Alert
	instantMonitoringChannel chan *pbsm.SMOutgoingMessages_InstantMonitoring
	averageMonitoringChannel chan *pbsm.SMOutgoingMessages_AverageMonitoring
	logChannel               chan *pbsm.SMOutgoingMessages_Log
	envVarsChannel           chan *pbsm.SMOutgoingMessages_OverrideEnvVarStatus
	pbsm.UnimplementedSMServiceServer
}

type testNodeConfigProcessor struct {
	version string
	err     error
}

type testNodeInfoProvider struct {
	nodeInfo cloudprotocol.NodeInfo
}

type testMonitoringProvider struct {
	averageMonitoring aostypes.NodeMonitoring
	monitoringChannel chan aostypes.NodeMonitoring
}

type testLogProvider struct {
	currentLogRequest cloudprotocol.RequestLog
	testLogs          []testLogData
	sentIndex         int
	channel           chan cloudprotocol.PushLog
}

type testLogData struct {
	internalLog   cloudprotocol.PushLog
	expectedPBLog pbsm.LogData
}

type testAlertProvider struct {
	alertsChannel chan interface{}
}

type testServiceManager struct {
	services []aostypes.ServiceInfo
}

type testNetworkUpdates struct {
	updates     []aostypes.NetworkParameters
	callChannel chan struct{}
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

	nodeInfoProvider := &testNodeInfoProvider{nodeInfo: cloudprotocol.NodeInfo{NodeID: "nodeID", NodeType: "typeType"}}

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, nodeInfoProvider, nil, nil, nil, nil,
		&testNodeConfigProcessor{}, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM client: %v", err)
	}
	defer client.Close()

	if err = server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}
}

func TestDisconnected(t *testing.T) {
	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	nodeInfoProvider := &testNodeInfoProvider{nodeInfo: cloudprotocol.NodeInfo{NodeID: "nodeID", NodeType: "typeType"}}

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, nodeInfoProvider, nil, nil, nil, nil,
		&testNodeConfigProcessor{}, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM client: %v", err)
	}
	defer client.Close()

	if err = server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	server.close()

	server, err = newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer server.close()

	if err = server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}
}

func TestMonitoringNotifications(t *testing.T) {
	currentTime := time.Now()
	pbCurrentTime := timestamppb.New(currentTime)

	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}
	defer server.close()

	nodeInfoProvider := &testNodeInfoProvider{nodeInfo: cloudprotocol.NodeInfo{NodeID: "nodeID", NodeType: "typeType"}}
	monitoringProvider := newTestMonitoringProvider()

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, nodeInfoProvider, nil, nil, nil, nil,
		&testNodeConfigProcessor{}, nil, monitoringProvider, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM client: %v", err)
	}
	defer client.Close()

	if err = server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	type testMonitoringElement struct {
		sendMonitoring     aostypes.NodeMonitoring
		expectedMonitoring pbsm.InstantMonitoring
	}

	testMonitoringData := []testMonitoringElement{
		{
			sendMonitoring: aostypes.NodeMonitoring{
				NodeID: "nodeID",
				NodeData: aostypes.MonitoringData{
					Timestamp: currentTime,
					RAM:       10, CPU: 20, Download: 40, Upload: 50,
					Partitions: []aostypes.PartitionUsage{{Name: "p1", UsedSize: 100}},
				},
			},
			expectedMonitoring: pbsm.InstantMonitoring{
				NodeMonitoring: &pbsm.MonitoringData{
					Timestamp: pbCurrentTime,
					Ram:       10, Cpu: 20, Download: 40, Upload: 50,
					Partitions: []*pbsm.PartitionUsage{{Name: "p1", UsedSize: 100}},
				},
			},
		},
		{
			sendMonitoring: aostypes.NodeMonitoring{
				NodeID: "nodeID",
				NodeData: aostypes.MonitoringData{
					Timestamp: currentTime,
					RAM:       10, CPU: 20, Download: 40, Upload: 50,
					Partitions: []aostypes.PartitionUsage{{Name: "p1", UsedSize: 100}},
				},
				InstancesData: []aostypes.InstanceMonitoring{
					{
						InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "s1", Instance: 1},
						MonitoringData: aostypes.MonitoringData{
							Timestamp: currentTime,
							RAM:       10, CPU: 20, Download: 40, Upload: 50,
							Partitions: []aostypes.PartitionUsage{{Name: "ps1", UsedSize: 100}},
						},
					},
					{
						InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "s1", Instance: 1},
						MonitoringData: aostypes.MonitoringData{
							Timestamp: currentTime,
							RAM:       10, CPU: 20, Download: 40, Upload: 50,
							Partitions: []aostypes.PartitionUsage{{Name: "ps2", UsedSize: 100}},
						},
					},
				},
			},
			expectedMonitoring: pbsm.InstantMonitoring{
				NodeMonitoring: &pbsm.MonitoringData{
					Timestamp: pbCurrentTime,
					Ram:       10, Cpu: 20, Download: 40, Upload: 50,
					Partitions: []*pbsm.PartitionUsage{{Name: "p1", UsedSize: 100}},
				},
				InstancesMonitoring: []*pbsm.InstanceMonitoring{
					{
						Instance: &pbcommon.InstanceIdent{ServiceId: "service1", SubjectId: "s1", Instance: 1},
						MonitoringData: &pbsm.MonitoringData{
							Timestamp: pbCurrentTime,
							Ram:       10, Cpu: 20, Download: 40, Upload: 50,
							Partitions: []*pbsm.PartitionUsage{{Name: "ps1", UsedSize: 100}},
						},
					},
					{
						Instance: &pbcommon.InstanceIdent{ServiceId: "service2", SubjectId: "s1", Instance: 1},
						MonitoringData: &pbsm.MonitoringData{
							Timestamp: pbCurrentTime,
							Ram:       10, Cpu: 20, Download: 40, Upload: 50,
							Partitions: []*pbsm.PartitionUsage{{Name: "ps2", UsedSize: 100}},
						},
					},
				},
			},
		},
	}

	for i := range testMonitoringData {
		monitoringProvider.monitoringChannel <- testMonitoringData[i].sendMonitoring

		receivedMonitoring := <-server.instantMonitoringChannel

		if !proto.Equal(receivedMonitoring.InstantMonitoring, &testMonitoringData[i].expectedMonitoring) {
			log.Debug("Received monitoring: ", receivedMonitoring.InstantMonitoring)
			log.Debug("Expected monitoring: ", &testMonitoringData[i].expectedMonitoring)

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

	nodeInfoProvider := &testNodeInfoProvider{nodeInfo: cloudprotocol.NodeInfo{NodeID: "nodeID", NodeType: "typeType"}}
	logProvider := testLogProvider{channel: make(chan cloudprotocol.PushLog)}

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, nodeInfoProvider,
		nil, nil, nil, nil, &testNodeConfigProcessor{}, nil, nil, &logProvider, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM client: %v", err)
	}
	defer client.Close()

	if err = server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	logProvider.testLogs = []testLogData{
		{
			internalLog:   cloudprotocol.PushLog{LogID: "systemLog", Content: []byte{1, 2, 3}},
			expectedPBLog: pbsm.LogData{LogId: "systemLog", Data: []byte{1, 2, 3}},
		},
		{
			internalLog:   cloudprotocol.PushLog{LogID: "serviceLog1", Content: []byte{1, 2, 4}, PartsCount: 10, Part: 1},
			expectedPBLog: pbsm.LogData{LogId: "serviceLog1", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			internalLog:   cloudprotocol.PushLog{LogID: "serviceLog2", Content: []byte{1, 2, 4}, PartsCount: 10, Part: 1},
			expectedPBLog: pbsm.LogData{LogId: "serviceLog2", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			internalLog:   cloudprotocol.PushLog{LogID: "serviceLog3", Content: []byte{1, 2, 4}, PartsCount: 10, Part: 1},
			expectedPBLog: pbsm.LogData{LogId: "serviceLog3", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			internalLog: cloudprotocol.PushLog{
				LogID: "serviceCrashLog", Content: []byte{1, 2, 4},
				ErrorInfo: &cloudprotocol.ErrorInfo{
					AosCode:  1,
					Message:  "some error",
					ExitCode: 5,
				},
				Part: 1,
			},
			expectedPBLog: pbsm.LogData{LogId: "serviceCrashLog", Part: 1, Data: []byte{1, 2, 4}, Error: &pbcommon.ErrorInfo{
				AosCode:  1,
				Message:  "some error",
				ExitCode: 5,
			}},
		},
	}

	if err := server.stream.Send(&pbsm.SMIncomingMessages{SMIncomingMessage: &pbsm.SMIncomingMessages_SystemLogRequest{
		SystemLogRequest: &pbsm.SystemLogRequest{LogId: "systemlog"},
	}}); err != nil {
		t.Fatalf("Can't get system log: %v", err)
	}

	instanceLogRequests := []pbsm.InstanceLogRequest{
		{
			InstanceFilter: &pbsm.InstanceFilter{ServiceId: "id1", SubjectId: "", Instance: -1},
			LogId:          "serviceLog1",
		},
		{
			InstanceFilter: &pbsm.InstanceFilter{ServiceId: "id2", SubjectId: "s1", Instance: 10},
			LogId:          "serviceLog2",
		},
		{
			InstanceFilter: &pbsm.InstanceFilter{ServiceId: "id3", SubjectId: "s1", Instance: 10},
			From:           timestamppb.Now(), Till: timestamppb.Now(),
			LogId: "serviceLog3",
		},
	}

	for i := range instanceLogRequests {
		if err := server.stream.Send(&pbsm.SMIncomingMessages{
			SMIncomingMessage: &pbsm.SMIncomingMessages_InstanceLogRequest{
				InstanceLogRequest: &instanceLogRequests[i],
			},
		}); err != nil {
			t.Fatalf("Can't get instance log: %v", err)
		}
	}

	if err := server.stream.Send(&pbsm.SMIncomingMessages{
		SMIncomingMessage: &pbsm.SMIncomingMessages_InstanceCrashLogRequest{
			InstanceCrashLogRequest: &pbsm.InstanceCrashLogRequest{
				LogId: "serviceCrashLog", InstanceFilter: &pbsm.InstanceFilter{ServiceId: "id3"},
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

	nodeInfoProvider := &testNodeInfoProvider{nodeInfo: cloudprotocol.NodeInfo{NodeID: "nodeID", NodeType: "typeType"}}
	alertProvider := &testAlertProvider{alertsChannel: make(chan interface{}, 10)}

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, nodeInfoProvider, nil, nil, nil, nil,
		&testNodeConfigProcessor{}, alertProvider, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM client: %v", err)
	}
	defer client.Close()

	if err = server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	type testAlertItem struct {
		sendAlert     interface{}
		expectedAlert pbsm.Alert
	}

	testAlertItems := []testAlertItem{
		{
			sendAlert: cloudprotocol.SystemAlert{
				AlertItem: cloudprotocol.AlertItem{Tag: cloudprotocol.AlertTagSystemError},
				Message:   "SystemAlertMessage",
			},
			expectedAlert: pbsm.Alert{
				Tag:       cloudprotocol.AlertTagSystemError,
				AlertItem: &pbsm.Alert_SystemAlert{SystemAlert: &pbsm.SystemAlert{Message: "SystemAlertMessage"}},
			},
		},
		{
			sendAlert: cloudprotocol.CoreAlert{
				AlertItem:     cloudprotocol.AlertItem{Tag: cloudprotocol.AlertTagAosCore},
				CoreComponent: "SM", Message: "CoreAlertMessage",
			},
			expectedAlert: pbsm.Alert{
				Tag: cloudprotocol.AlertTagAosCore,
				AlertItem: &pbsm.Alert_CoreAlert{
					CoreAlert: &pbsm.CoreAlert{CoreComponent: "SM", Message: "CoreAlertMessage"},
				},
			},
		},
		{
			sendAlert: cloudprotocol.ResourceValidateAlert{
				AlertItem: cloudprotocol.AlertItem{Tag: cloudprotocol.AlertTagResourceValidate},
				Name:      "someName",
				Errors:    []cloudprotocol.ErrorInfo{{Message: "error1"}, {Message: "error2"}},
			},
			expectedAlert: pbsm.Alert{
				Tag: cloudprotocol.AlertTagResourceValidate,
				AlertItem: &pbsm.Alert_ResourceValidateAlert{
					ResourceValidateAlert: &pbsm.ResourceValidateAlert{
						Name: "someName",
						Errors: []*pbcommon.ErrorInfo{
							{Message: "error1"}, {Message: "error2"},
						},
					},
				},
			},
		},
		{
			sendAlert: cloudprotocol.DeviceAllocateAlert{
				AlertItem:     cloudprotocol.AlertItem{Tag: cloudprotocol.AlertTagDeviceAllocate},
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
				Device:        "someDevice", Message: "someMessage",
			},
			expectedAlert: pbsm.Alert{
				Tag: cloudprotocol.AlertTagDeviceAllocate,
				AlertItem: &pbsm.Alert_DeviceAllocateAlert{
					DeviceAllocateAlert: &pbsm.DeviceAllocateAlert{
						Instance: &pbcommon.InstanceIdent{ServiceId: "id1", SubjectId: "s1", Instance: 1},
						Device:   "someDevice", Message: "someMessage",
					},
				},
			},
		},
		{
			sendAlert: cloudprotocol.SystemQuotaAlert{
				AlertItem: cloudprotocol.AlertItem{Tag: cloudprotocol.AlertTagSystemQuota},
				Parameter: "param1", Value: 42,
			},
			expectedAlert: pbsm.Alert{
				Tag: cloudprotocol.AlertTagSystemQuota,
				AlertItem: &pbsm.Alert_SystemQuotaAlert{
					SystemQuotaAlert: &pbsm.SystemQuotaAlert{Parameter: "param1", Value: 42},
				},
			},
		},
		{
			sendAlert: cloudprotocol.InstanceQuotaAlert{
				AlertItem:     cloudprotocol.AlertItem{Tag: cloudprotocol.AlertTagInstanceQuota},
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
				Parameter:     "param1", Value: 42,
			},
			expectedAlert: pbsm.Alert{
				Tag: cloudprotocol.AlertTagInstanceQuota,
				AlertItem: &pbsm.Alert_InstanceQuotaAlert{
					InstanceQuotaAlert: &pbsm.InstanceQuotaAlert{
						Instance:  &pbcommon.InstanceIdent{ServiceId: "id1", SubjectId: "s1", Instance: 1},
						Parameter: "param1", Value: 42,
					},
				},
			},
		},
		{
			sendAlert: cloudprotocol.ServiceInstanceAlert{
				AlertItem:     cloudprotocol.AlertItem{Tag: cloudprotocol.AlertTagServiceInstance},
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "id1", SubjectID: "s1", Instance: 1},
				Message:       "ServiceInstanceAlert", ServiceVersion: "2.0.0",
			},
			expectedAlert: pbsm.Alert{
				Tag: cloudprotocol.AlertTagServiceInstance,
				AlertItem: &pbsm.Alert_InstanceAlert{
					InstanceAlert: &pbsm.InstanceAlert{
						Instance: &pbcommon.InstanceIdent{ServiceId: "id1", SubjectId: "s1", Instance: 1},
						Message:  "ServiceInstanceAlert", ServiceVersion: "2.0.0",
					},
				},
			},
		},
	}

	for i := range testAlertItems {
		alertProvider.alertsChannel <- testAlertItems[i].sendAlert

		receivedAlert := <-server.alertChannel

		receivedAlert.Timestamp = nil

		if !proto.Equal(receivedAlert, &testAlertItems[i].expectedAlert) {
			log.Debug(i)
			log.Debug(receivedAlert)
			log.Debug(&testAlertItems[i].expectedAlert)

			t.Errorf("Incorrect alert item %s", receivedAlert.GetTag())
		}
	}
}

func TestRunInstances(t *testing.T) {
	data := []*pbsm.RunInstances{
		{},
		{ForceRestart: true},
		{
			Services: []*pbsm.ServiceInfo{
				{
					ServiceId:  "service1",
					ProviderId: "provider1",
					Version:    "2.0.0",
					Url:        "url123",
					Gid:        984,
					Sha256:     []byte("fdksj"),
					Size:       789,
				},
				{
					ServiceId:  "service2",
					ProviderId: "provider2",
					Version:    "3.0.0",
					Url:        "url354",
					Gid:        21,
					Sha256:     []byte("sdfafsd"),
					Size:       12900,
				},
			},
			Layers: []*pbsm.LayerInfo{
				{
					LayerId: "layer1",
					Digest:  "digest2329",
					Version: "1.0.0",
					Url:     "url670",
					Sha256:  []byte("sassfdc"),
					Size:    3489,
				},
				{
					LayerId: "layer2",
					Digest:  "digest6509",
					Version: "2.0.0",
					Url:     "url654",
					Sha256:  []byte("asdasdd"),
					Size:    3489,
				},
				{
					LayerId: "layer3",
					Digest:  "digest3209",
					Version: "3.0.0",
					Url:     "url986",
					Sha256:  []byte("dsakjcd"),
					Size:    3489,
				},
			},
			Instances: []*pbsm.InstanceInfo{
				{
					Instance: &pbcommon.InstanceIdent{
						ServiceId: "service1",
						SubjectId: "subject1",
						Instance:  0,
					},
					NetworkParameters: &pbsm.NetworkParameters{
						Ip:         "172.17.0.1",
						Subnet:     "172.17.0.0/16",
						VlanId:     1,
						DnsServers: []string{"10.10.2.1"},
					},
					Uid:         329,
					Priority:    12,
					StoragePath: "storagePath1",
					StatePath:   "statePath1",
				},
				{
					Instance: &pbcommon.InstanceIdent{
						ServiceId: "service1",
						SubjectId: "subject1",
						Instance:  1,
					},
					NetworkParameters: &pbsm.NetworkParameters{
						Ip:         "172.17.0.2",
						Subnet:     "172.17.0.0/16",
						VlanId:     1,
						DnsServers: []string{"10.10.2.1"},
					},
					Uid:         876,
					Priority:    32,
					StoragePath: "storagePath2",
					StatePath:   "statePath2",
				},
				{
					Instance: &pbcommon.InstanceIdent{
						ServiceId: "service2",
						SubjectId: "subject1",
						Instance:  0,
					},
					NetworkParameters: &pbsm.NetworkParameters{
						Ip:         "172.17.0.3",
						Subnet:     "172.17.0.0/16",
						VlanId:     1,
						DnsServers: []string{"10.10.2.1"},
					},
					Uid:         543,
					Priority:    29,
					StoragePath: "storagePath3",
					StatePath:   "statePath3",
				},
				{
					Instance: &pbcommon.InstanceIdent{
						ServiceId: "service2",
						SubjectId: "subject2",
						Instance:  5,
					},
					NetworkParameters: &pbsm.NetworkParameters{
						Ip:         "172.17.0.4",
						Subnet:     "172.17.0.0/16",
						VlanId:     1,
						DnsServers: []string{"10.10.2.1"},
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

	nodeInfoProvider := &testNodeInfoProvider{nodeInfo: cloudprotocol.NodeInfo{NodeID: "nodeID", NodeType: "typeType"}}
	serviceManager := &testServiceManager{}
	layerManager := &testLayerManager{}
	launcher := newTestLauncher()

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, nodeInfoProvider, nil, serviceManager,
		layerManager, launcher, &testNodeConfigProcessor{}, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM client: %v", err)
	}
	defer client.Close()

	if err := server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	for _, req := range data {
		if err := server.stream.Send(&pbsm.SMIncomingMessages{
			SMIncomingMessage: &pbsm.SMIncomingMessages_RunInstances{RunInstances: req},
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

		if !reflect.DeepEqual(launcher.instances, instances) {
			t.Errorf("Wrong instances: %v expected %v", launcher.instances, instances)
		}

		if launcher.forceRestart != forceRestart {
			t.Errorf("Wrong force restart value: %v", launcher.forceRestart)
		}
	}
}

func TestNetworkUpdate(t *testing.T) {
	data := &pbsm.UpdateNetworks{
		Networks: []*pbsm.NetworkParameters{
			{
				Subnet:    "172.17.0.0/16",
				Ip:        "172.17.0.1",
				VlanId:    1,
				NetworkId: "net1",
			},
			{
				Subnet:    "172.18.0.0/16",
				Ip:        "172.18.0.1",
				VlanId:    2,
				NetworkId: "net2",
			},
		},
	}

	expected := []aostypes.NetworkParameters{
		{
			Subnet:    "172.17.0.0/16",
			IP:        "172.17.0.1",
			VlanID:    1,
			NetworkID: "net1",
		},
		{
			Subnet:    "172.18.0.0/16",
			IP:        "172.18.0.1",
			VlanID:    2,
			NetworkID: "net2",
		},
	}

	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}
	defer server.close()

	nodeInfoProvider := &testNodeInfoProvider{nodeInfo: cloudprotocol.NodeInfo{NodeID: "nodeID", NodeType: "typeType"}}
	netManager := &testNetworkUpdates{
		callChannel: make(chan struct{}, 1),
	}

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, nodeInfoProvider, nil, nil, nil, nil,
		&testNodeConfigProcessor{}, nil, nil, nil, netManager, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM client: %v", err)
	}
	defer client.Close()

	if err := server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	if err := server.stream.Send(&pbsm.SMIncomingMessages{
		SMIncomingMessage: &pbsm.SMIncomingMessages_UpdateNetworks{UpdateNetworks: data},
	}); err != nil {
		t.Fatalf("Can't send request: %v", err)
	}

	if err := netManager.waitCall(); err != nil {
		t.Fatalf("Error waiting call: %v", err)
	}

	if !reflect.DeepEqual(netManager.updates, expected) {
		t.Errorf("Wrong networks: %v", netManager.updates)
	}
}

func TestOverrideEnvVars(t *testing.T) {
	type testData struct {
		req    *pbsm.OverrideEnvVars
		status []cloudprotocol.EnvVarsInstanceStatus
	}

	data := []testData{
		{
			req:    &pbsm.OverrideEnvVars{},
			status: []cloudprotocol.EnvVarsInstanceStatus{},
		},
		{
			req: &pbsm.OverrideEnvVars{EnvVars: []*pbsm.OverrideInstanceEnvVar{
				{
					InstanceFilter: &pbsm.InstanceFilter{ServiceId: "service1", SubjectId: "subject1", Instance: 2},
					Variables: []*pbsm.EnvVarInfo{
						{Name: "name1", Value: "value1", Ttl: timestamppb.Now()},
						{Name: "name2", Value: "value2", Ttl: timestamppb.Now()},
						{Name: "name3", Value: "value3", Ttl: timestamppb.Now()},
					},
				},
				{
					InstanceFilter: &pbsm.InstanceFilter{ServiceId: "service2", SubjectId: "subject3", Instance: 0},
					Variables: []*pbsm.EnvVarInfo{
						{Name: "name4", Value: "value4", Ttl: timestamppb.Now()},
						{Name: "name5", Value: "value5", Ttl: timestamppb.Now()},
					},
				},
				{
					InstanceFilter: &pbsm.InstanceFilter{ServiceId: "service3", Instance: -1},
					Variables: []*pbsm.EnvVarInfo{
						{Name: "name6", Value: "value6", Ttl: timestamppb.Now()},
					},
				},
			}},
			status: []cloudprotocol.EnvVarsInstanceStatus{
				{
					InstanceFilter: cloudprotocol.NewInstanceFilter("service1", "subject1", -1),
					Statuses: []cloudprotocol.EnvVarStatus{
						{Name: "name1"},
						{Name: "name2", ErrorInfo: &cloudprotocol.ErrorInfo{Message: "error2"}},
					},
				},
				{
					InstanceFilter: cloudprotocol.NewInstanceFilter("service2", "subject3", 2),
					Statuses: []cloudprotocol.EnvVarStatus{
						{Name: "name3", ErrorInfo: &cloudprotocol.ErrorInfo{Message: "error3"}},
						{Name: "name4", ErrorInfo: &cloudprotocol.ErrorInfo{Message: "error4"}},
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

	nodeInfoProvider := &testNodeInfoProvider{nodeInfo: cloudprotocol.NodeInfo{NodeID: "nodeID", NodeType: "typeType"}}
	launcher := newTestLauncher()

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, nodeInfoProvider, nil, nil, nil, launcher,
		&testNodeConfigProcessor{}, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM client: %v", err)
	}
	defer client.Close()

	if err := server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	for _, item := range data {
		launcher.envVarsStatus = item.status

		if err := server.stream.Send(&pbsm.SMIncomingMessages{
			SMIncomingMessage: &pbsm.SMIncomingMessages_OverrideEnvVars{OverrideEnvVars: item.req},
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

	nodeInfoProvider := &testNodeInfoProvider{nodeInfo: cloudprotocol.NodeInfo{NodeID: "nodeID", NodeType: "typeType"}}
	launcher := newTestLauncher()

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, nodeInfoProvider, nil, nil, nil, launcher,
		&testNodeConfigProcessor{}, nil, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM client: %v", err)
	}
	defer client.Close()

	if err := server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	// Check connected status

	if err := server.stream.Send(&pbsm.SMIncomingMessages{
		SMIncomingMessage: &pbsm.SMIncomingMessages_ConnectionStatus{ConnectionStatus: &pbsm.ConnectionStatus{
			CloudStatus: pbsm.ConnectionEnum_CONNECTED,
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

	if err := server.stream.Send(&pbsm.SMIncomingMessages{
		SMIncomingMessage: &pbsm.SMIncomingMessages_ConnectionStatus{ConnectionStatus: &pbsm.ConnectionStatus{
			CloudStatus: pbsm.ConnectionEnum_DISCONNECTED,
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

func TestAverageMonitoring(t *testing.T) {
	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}
	defer server.close()

	nodeInfoProvider := &testNodeInfoProvider{nodeInfo: cloudprotocol.NodeInfo{NodeID: "nodeID", NodeType: "typeType"}}
	testMonitoring := &testMonitoringProvider{
		averageMonitoring: aostypes.NodeMonitoring{
			NodeData: aostypes.MonitoringData{
				RAM: 10, CPU: 20, Download: 40, Upload: 50,
				Partitions: []aostypes.PartitionUsage{{Name: "p1", UsedSize: 100}},
			},
			InstancesData: []aostypes.InstanceMonitoring{
				{
					InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "s1", Instance: 1},
					MonitoringData: aostypes.MonitoringData{
						RAM: 10, CPU: 20, Download: 40, Upload: 50,
						Partitions: []aostypes.PartitionUsage{{Name: "ps1", UsedSize: 100}},
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "s1", Instance: 1},
					MonitoringData: aostypes.MonitoringData{
						RAM: 10, CPU: 20, Download: 40, Upload: 50,
						Partitions: []aostypes.PartitionUsage{{Name: "ps2", UsedSize: 100}},
					},
				},
			},
		},
	}

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, nodeInfoProvider, nil, nil, nil, nil,
		&testNodeConfigProcessor{}, nil, testMonitoring, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	if err := server.waitNodeConfigStatus(nodeInfoProvider.nodeInfo.NodeID,
		nodeInfoProvider.nodeInfo.NodeType); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	if err := server.stream.Send(&pbsm.SMIncomingMessages{
		SMIncomingMessage: &pbsm.SMIncomingMessages_GetAverageMonitoring{},
	}); err != nil {
		t.Fatalf("Can't get average monitoring: %v", err)
	}

	if err := server.waitAndCheckAverageMonitoring(testMonitoring.averageMonitoring); err != nil {
		t.Errorf("Incorrect monitoring: %v", err)
	}
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func newTestServer(url string) (server *testServer, err error) {
	server = &testServer{
		nodeConfigStatusChannel:  make(chan *pbsm.NodeConfigStatus, 10),
		alertChannel:             make(chan *pbsm.Alert, 10),
		instantMonitoringChannel: make(chan *pbsm.SMOutgoingMessages_InstantMonitoring, 10),
		averageMonitoringChannel: make(chan *pbsm.SMOutgoingMessages_AverageMonitoring, 1),
		logChannel:               make(chan *pbsm.SMOutgoingMessages_Log, 10),
		envVarsChannel:           make(chan *pbsm.SMOutgoingMessages_OverrideEnvVarStatus, 10),
	}

	listener, err := net.Listen("tcp", url)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	server.grpcServer = grpc.NewServer()

	pbsm.RegisterSMServiceServer(server.grpcServer, server)

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

func (server *testServer) waitNodeConfigStatus(nodeID, nodeType string) error {
	select {
	case nodeConfigStatus := <-server.nodeConfigStatusChannel:
		if nodeConfigStatus.GetNodeId() != nodeID {
			return aoserrors.Errorf("incorrect node id: %s", nodeConfigStatus.GetNodeId())
		}

		if nodeConfigStatus.GetNodeType() != nodeType {
			return aoserrors.Errorf("incorrect node type: %s", nodeConfigStatus.GetNodeType())
		}

		return nil

	case <-time.After(waitRegisteredTimeout):
		return aoserrors.New("timeout")
	}
}

func (server *testServer) RegisterSM(stream pbsm.SMService_RegisterSMServer) error {
	server.stream = stream

	for {
		message, err := server.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return aoserrors.Wrap(err)
		}

		switch data := message.GetSMOutgoingMessage().(type) {
		case *pbsm.SMOutgoingMessages_NodeConfigStatus:
			server.nodeConfigStatusChannel <- data.NodeConfigStatus

		case *pbsm.SMOutgoingMessages_InstantMonitoring:
			server.instantMonitoringChannel <- data

		case *pbsm.SMOutgoingMessages_AverageMonitoring:
			server.averageMonitoringChannel <- data

		case *pbsm.SMOutgoingMessages_Log:
			server.logChannel <- data

		case *pbsm.SMOutgoingMessages_Alert:
			server.alertChannel <- data.Alert

		case *pbsm.SMOutgoingMessages_OverrideEnvVarStatus:
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
		receivedStatus := make([]cloudprotocol.EnvVarsInstanceStatus, len(data.OverrideEnvVarStatus.GetEnvVarsStatus()))

		for i, envVarStatus := range data.OverrideEnvVarStatus.GetEnvVarsStatus() {
			receivedStatus[i] = cloudprotocol.EnvVarsInstanceStatus{
				InstanceFilter: cloudprotocol.NewInstanceFilter(
					envVarStatus.GetInstanceFilter().GetServiceId(),
					envVarStatus.GetInstanceFilter().GetSubjectId(),
					envVarStatus.GetInstanceFilter().GetInstance()),
				Statuses: make([]cloudprotocol.EnvVarStatus, len(envVarStatus.GetStatuses())),
			}

			for j, s := range envVarStatus.GetStatuses() {
				receivedStatus[i].Statuses[j] = cloudprotocol.EnvVarStatus{
					Name:      s.GetName(),
					ErrorInfo: pbconvert.ErrorInfoFromPB(s.GetError()),
				}
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

func convertMonitoringData(monitoring *pbsm.MonitoringData) aostypes.MonitoringData {
	data := aostypes.MonitoringData{
		RAM:        monitoring.GetRam(),
		CPU:        monitoring.GetCpu(),
		Download:   monitoring.GetDownload(),
		Upload:     monitoring.GetUpload(),
		Partitions: make([]aostypes.PartitionUsage, 0, len(monitoring.GetPartitions())),
	}

	for _, partition := range monitoring.GetPartitions() {
		data.Partitions = append(data.Partitions, aostypes.PartitionUsage{
			Name:     partition.GetName(),
			UsedSize: partition.GetUsedSize(),
		})
	}

	return data
}

func convertAverageMonitoring(monitoring *pbsm.AverageMonitoring) aostypes.NodeMonitoring {
	data := aostypes.NodeMonitoring{
		NodeData:      convertMonitoringData(monitoring.GetNodeMonitoring()),
		InstancesData: make([]aostypes.InstanceMonitoring, 0, len(monitoring.GetInstancesMonitoring())),
	}

	for _, instanceMonitoring := range monitoring.GetInstancesMonitoring() {
		data.InstancesData = append(data.InstancesData, aostypes.InstanceMonitoring{
			InstanceIdent:  pbconvert.InstanceIdentFromPB(instanceMonitoring.GetInstance()),
			MonitoringData: convertMonitoringData(instanceMonitoring.GetMonitoringData()),
		})
	}

	return data
}

func (server *testServer) waitAndCheckAverageMonitoring(monitoring aostypes.NodeMonitoring) error {
	for {
		select {
		case data := <-server.averageMonitoringChannel:
			if !reflect.DeepEqual(convertAverageMonitoring(data.AverageMonitoring), monitoring) {
				return aoserrors.New("incorrect monitoring data")
			}

			return nil

		case <-time.After(5 * time.Second):
			return aoserrors.New("timeout")
		}
	}
}

func convertRunInstancesReq(req *pbsm.RunInstances) (
	services []aostypes.ServiceInfo, layers []aostypes.LayerInfo, instances []aostypes.InstanceInfo, forceRestart bool,
) {
	services = make([]aostypes.ServiceInfo, len(req.GetServices()))

	for i, service := range req.GetServices() {
		services[i] = aostypes.ServiceInfo{
			ServiceID:  service.GetServiceId(),
			ProviderID: service.GetProviderId(),
			Version:    service.GetVersion(),
			GID:        service.GetGid(),
			URL:        service.GetUrl(),
			Sha256:     service.GetSha256(),
			Size:       service.GetSize(),
		}
	}

	layers = make([]aostypes.LayerInfo, len(req.GetLayers()))

	for i, layer := range req.GetLayers() {
		layers[i] = aostypes.LayerInfo{
			LayerID: layer.GetLayerId(),
			Digest:  layer.GetDigest(),
			Version: layer.GetVersion(),
			URL:     layer.GetUrl(),
			Sha256:  layer.GetSha256(),
			Size:    layer.GetSize(),
		}
	}

	instances = make([]aostypes.InstanceInfo, len(req.GetInstances()))

	for i, instance := range req.GetInstances() {
		instances[i] = aostypes.InstanceInfo{
			InstanceIdent: aostypes.InstanceIdent{
				ServiceID: instance.GetInstance().GetServiceId(),
				SubjectID: instance.GetInstance().GetSubjectId(),
				Instance:  instance.GetInstance().GetInstance(),
			},
			NetworkParameters: aostypes.NetworkParameters{
				IP:            instance.GetNetworkParameters().GetIp(),
				Subnet:        instance.GetNetworkParameters().GetSubnet(),
				VlanID:        instance.GetNetworkParameters().GetVlanId(),
				DNSServers:    instance.GetNetworkParameters().GetDnsServers(),
				FirewallRules: []aostypes.FirewallRule{},
			},
			UID:         instance.GetUid(),
			Priority:    instance.GetPriority(),
			StoragePath: instance.GetStoragePath(),
			StatePath:   instance.GetStatePath(),
		}
	}

	forceRestart = req.GetForceRestart()

	return services, layers, instances, forceRestart
}

func convertEnvVarsReq(req *pbsm.OverrideEnvVars) []cloudprotocol.EnvVarsInstanceInfo {
	envVars := make([]cloudprotocol.EnvVarsInstanceInfo, len(req.GetEnvVars()))

	for i, envVar := range req.GetEnvVars() {
		envVars[i] = cloudprotocol.EnvVarsInstanceInfo{
			InstanceFilter: cloudprotocol.NewInstanceFilter(
				envVar.GetInstanceFilter().GetServiceId(),
				envVar.GetInstanceFilter().GetSubjectId(),
				envVar.GetInstanceFilter().GetInstance()),
			Variables: make([]cloudprotocol.EnvVarInfo, len(envVar.GetVariables())),
		}

		for j, v := range envVar.GetVariables() {
			envVars[i].Variables[j] = cloudprotocol.EnvVarInfo{Name: v.GetName(), Value: v.GetValue()}

			if v.GetTtl() != nil {
				t := v.GetTtl().AsTime()

				envVars[i].Variables[j].TTL = &t
			}
		}
	}

	return envVars
}

/***********************************************************************************************************************
 * Interfaces
 **********************************************************************************************************************/

func (processor *testNodeConfigProcessor) GetNodeConfigStatus() (version string, err error) {
	return processor.version, processor.err
}

func (processor *testNodeConfigProcessor) CheckNodeConfig(configJSON, version string) error {
	return processor.err
}

func (processor *testNodeConfigProcessor) UpdateNodeConfig(configJSON, version string) error {
	return processor.err
}

func (provider *testNodeInfoProvider) GetCurrentNodeInfo() (cloudprotocol.NodeInfo, error) {
	return provider.nodeInfo, nil
}

func newTestMonitoringProvider() *testMonitoringProvider {
	return &testMonitoringProvider{
		monitoringChannel: make(chan aostypes.NodeMonitoring, 10),
	}
}

func (monitoring *testMonitoringProvider) GetNodeMonitoringChannel() <-chan aostypes.NodeMonitoring {
	return monitoring.monitoringChannel
}

func (monitoring *testMonitoringProvider) GetAverageMonitoring() (aostypes.NodeMonitoring, error) {
	return monitoring.averageMonitoring, nil
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

func (alerts *testAlertProvider) GetAlertsChannel() (channel <-chan interface{}) {
	return alerts.alertsChannel
}

func (logProvider *testLogProvider) GetLogsDataChannel() (channel <-chan cloudprotocol.PushLog) {
	return logProvider.channel
}

func (processor *testServiceManager) ProcessDesiredServices(services []aostypes.ServiceInfo) error {
	processor.services = services

	return nil
}

func (processor *testLayerManager) ProcessDesiredLayers(layers []aostypes.LayerInfo) error {
	processor.layers = layers

	return nil
}

func (networkmanager *testNetworkUpdates) UpdateNetworks(networkParameters []aostypes.NetworkParameters) error {
	networkmanager.updates = networkParameters
	networkmanager.callChannel <- struct{}{}

	return nil
}

func (networkmanager *testNetworkUpdates) waitCall() error {
	select {
	case <-networkmanager.callChannel:
		return nil

	case <-time.After(5 * time.Second):
		return aoserrors.New("wait call timeout")
	}
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
