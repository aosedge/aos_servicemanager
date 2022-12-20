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
	intrenalLog   cloudprotocol.PushLog
	expectedPBLog pb.LogData
}
type testAlertProvider struct {
	alertsChannel chan cloudprotocol.AlertItem
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

func TestSmRegistration(t *testing.T) {
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
		"mainSM", "model1", nil, nil, nil, nil, nil,
		nil, testMonitoring, nil, nil, systemInfo, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	expectedNodeCpnfiguration := &pb.NodeConfiguration{
		NodeId: "mainSM", NodeType: "model1", RemoteNode: false, RunnerFeatures: []string{"crun"},
		NumCpus: 1, TotalRam: 100,
		Partitions: []*pb.Partition{{Name: "p1", Types: []string{"t1"}, TotalSize: 200}},
	}

	if err = server.waitClientRegistered(expectedNodeCpnfiguration); err != nil {
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
		CMServerURL: serverURL, RemoteNode: true,
		RunnerFeatures: []string{"crun"},
	}, "mainSM", "model1", nil, nil, nil, nil, nil, nil, testMonitoring, nil, nil, systemInfo, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	expectedNodeCpnfiguration := &pb.NodeConfiguration{
		NodeId: "mainSM", NodeType: "model1", RemoteNode: true, RunnerFeatures: []string{"crun"},
		NumCpus: 1, TotalRam: 100,
		Partitions: []*pb.Partition{{Name: "p1", Types: []string{"t1"}, TotalSize: 200}},
	}

	if err = server.waitClientRegistered(expectedNodeCpnfiguration); err != nil {
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

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, "mainSM", "model1", nil, nil, nil, nil, nil,
		nil, nil, &logProvider, nil, cloudprotocol.SystemInfo{}, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	expectedNodeCpnfiguration := &pb.NodeConfiguration{
		NodeId: "mainSM", NodeType: "model1",
	}

	if err = server.waitClientRegistered(expectedNodeCpnfiguration); err != nil {
		t.Fatalf("SM registration error: %v", err)
	}

	logProvider.testLogs = []testLogData{
		{
			intrenalLog:   cloudprotocol.PushLog{LogID: "systemLog", Content: []byte{1, 2, 3}},
			expectedPBLog: pb.LogData{LogId: "systemLog", Data: []byte{1, 2, 3}},
		},
		{
			intrenalLog:   cloudprotocol.PushLog{LogID: "serviceLog1", Content: []byte{1, 2, 4}, PartsCount: 10, Part: 1},
			expectedPBLog: pb.LogData{LogId: "serviceLog1", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			intrenalLog:   cloudprotocol.PushLog{LogID: "serviceLog2", Content: []byte{1, 2, 4}, PartsCount: 10, Part: 1},
			expectedPBLog: pb.LogData{LogId: "serviceLog2", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			intrenalLog:   cloudprotocol.PushLog{LogID: "serviceLog3", Content: []byte{1, 2, 4}, PartsCount: 10, Part: 1},
			expectedPBLog: pb.LogData{LogId: "serviceLog3", Data: []byte{1, 2, 4}, PartCount: 10, Part: 1},
		},
		{
			intrenalLog: cloudprotocol.PushLog{
				LogID: "serviceCrashLog", Content: []byte{1, 2, 4},
				ErrorInfo: cloudprotocol.ErrorInfo{
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

	if err := waitAndCheckLogs(server.logChannel, logProvider.testLogs); err != nil {
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

	client, err := smclient.New(&config.Config{CMServerURL: serverURL}, "mainSM", "model1", nil, nil, nil, nil, nil,
		testAlerts, nil, nil, nil, cloudprotocol.SystemInfo{}, true)
	if err != nil {
		t.Fatalf("Can't create UM client: %v", err)
	}
	defer client.Close()

	expectedNodeCpnfiguration := &pb.NodeConfiguration{
		NodeId: "mainSM", NodeType: "model1",
	}

	if err = server.waitClientRegistered(expectedNodeCpnfiguration); err != nil {
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

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func newTestServer(url string) (server *testServer, err error) {
	server = &testServer{
		registerChannel:   make(chan *pb.NodeConfiguration, 10),
		alertChannel:      make(chan *pb.Alert, 10),
		monitoringChannel: make(chan *pb.SMOutgoingMessages_NodeMonitoring, 10),
		logChannel:        make(chan *pb.SMOutgoingMessages_Log, 10),
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

func (server *testServer) waitClientRegistered(expectedNodeConfig *pb.NodeConfiguration) (err error) {
	select {
	case nodeConfig := <-server.registerChannel:
		if !proto.Equal(nodeConfig, expectedNodeConfig) {
			log.Debugf("EXP :%v", expectedNodeConfig)
			log.Debugf("REC :%v", nodeConfig)

			return aoserrors.New("Incorrect node configuration")
		}

		return nil

	case <-time.After(waitRegisteredTimeout):
		return aoserrors.New("timeout")
	}
}

func (server *testServer) RegisterSM(stream pb.SMService_RegisterSMServer) (err error) {
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
			log.Debug("nodeID ", data.NodeConfiguration.NodeId)

			server.registerChannel <- data.NodeConfiguration

		case *pb.SMOutgoingMessages_NodeMonitoring:
			server.monitoringChannel <- data

		case *pb.SMOutgoingMessages_Log:
			server.logChannel <- data

		case *pb.SMOutgoingMessages_Alert:
			server.alertChannel <- data.Alert
		}
	}
}

func waitAndCheckLogs(receivedLogs <-chan *pb.SMOutgoingMessages_Log, testLogs []testLogData) error {
	var currentIndex int

	for {
		select {
		case logData := <-receivedLogs:
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
	logProvider.channel <- logProvider.testLogs[logProvider.sentIndex].intrenalLog
	logProvider.sentIndex++

	return nil
}

func (logProvider *testLogProvider) GetInstanceCrashLog(request cloudprotocol.RequestLog) error {
	logProvider.channel <- logProvider.testLogs[logProvider.sentIndex].intrenalLog
	logProvider.sentIndex++

	return nil
}

func (logProvider *testLogProvider) GetSystemLog(request cloudprotocol.RequestLog) {
	logProvider.channel <- logProvider.testLogs[logProvider.sentIndex].intrenalLog
	logProvider.sentIndex++
}

func (logProvider *testLogProvider) GetLogsDataChannel() (channel <-chan cloudprotocol.PushLog) {
	return logProvider.channel
}

func (alerts *testAlertProvider) GetAlertsChannel() (channel <-chan cloudprotocol.AlertItem) {
	return alerts.alertsChannel
}
