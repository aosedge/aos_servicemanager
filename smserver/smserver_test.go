// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2021 Renesas Inc.
// Copyright 2021 EPAM Systems Inc.
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
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/servicemanager"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"aos_servicemanager/alerts"
	"aos_servicemanager/config"
	"aos_servicemanager/smserver"
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
}

type testLayerManager struct {
}

type testClient struct {
	connection *grpc.ClientConn
	pbclient   pb.ServiceManagerClient
}

type testAlertProvider struct {
	alertsChannel chan *pb.Alert
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

	launcher := &testLauncher{}
	layerMgr := &testLayerManager{}

	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	smServer, err := smserver.New(&smConfig, launcher, layerMgr, nil, true)
	if err != nil {
		t.Fatalf("Can't create SM server: %s", err)
	}

	go smServer.Start()
	defer smServer.Stop()

	client, err := newTestClient(serverURL)
	if err != nil {
		t.Fatalf("Can't create test client: %s", err)
	}
	defer client.close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err = client.pbclient.SetUsers(ctx, &pb.Users{Users: []string{"user1"}})
	if err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	response, err := client.pbclient.GetStatus(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Can't get status: %s", err)
	}

	if len(response.GetServices()) != 1 {
		t.Errorf("incorrect count of services %d", len(response.GetServices()))
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

	_, err = client.pbclient.RemoveLayer(ctx, &pb.RemoveLayerRequest{})
	if err != nil {
		t.Fatalf("Can't remove layer: %s", err)
	}
}

func TestAlertNotifications(t *testing.T) {
	smConfig := config.Config{
		SMServerURL: serverURL,
	}

	testAlerts := &testAlertProvider{alertsChannel: make(chan *pb.Alert, 10)}

	smServer, err := smserver.New(&smConfig, nil, nil, testAlerts, true)
	if err != nil {
		t.Fatalf("Can't create: SM Server %s", err)
	}

	go smServer.Start()
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
		t.Fatalf("Can't subscribes: %s", err)
	}

	systemAlert := &pb.Alert{
		Timestamp: timestamppb.Now(),
		Tag:       "core",
		Source:    "system",
		Payload: &pb.Alert_SystemAlert{
			SystemAlert: &pb.SystemAlert{
				Message: "some alert"}}}

	testAlerts.alertsChannel <- systemAlert

	receivedAlert, err := notifications.Recv()
	if err != nil {
		t.Errorf("Can't receive alert: %s", err)
	}

	if !proto.Equal(receivedAlert.GetAlert(), systemAlert) {
		log.Error("received alert != send alert")
	}

	time := time.Now()

	resourceAlert := &pb.Alert{
		Timestamp: timestamppb.New(time),
		Tag:       alerts.AlertTagResource,
		Source:    "test",
		Payload: &pb.Alert_ResourceAlert{ResourceAlert: &pb.ResourceAlert{
			Parameter: "testResource",
			Value:     42}},
	}

	testAlerts.alertsChannel <- resourceAlert

	receivedResAlert, err := notifications.Recv()
	if err != nil {
		t.Errorf("Can't receive alert: %s", err)
	}

	if !proto.Equal(receivedResAlert.GetAlert(), resourceAlert) {
		log.Error("received resource alert != send alert")
	}

	deviceErrors := make(map[string][]error)
	deviceErrors["dev1"] = []error{fmt.Errorf("some error")}

	var convertedErrors []*pb.ResourceValidateErrors

	for name, reason := range deviceErrors {
		var messages []string

		for _, item := range reason {
			messages = append(messages, item.Error())
		}

		resourceError := pb.ResourceValidateErrors{
			Name:     name,
			ErrorMsg: messages}

		convertedErrors = append(convertedErrors, &resourceError)
	}

	validationAlert := &pb.Alert{
		Timestamp: timestamppb.New(time),
		Tag:       alerts.AlertTagAosCore,
		Source:    "test",
		Payload: &pb.Alert_ResourceValidateAlert{
			ResourceValidateAlert: &pb.ResourceValidateAlert{
				Type:   alerts.AlertDeviceErrors,
				Errors: convertedErrors}},
	}

	testAlerts.alertsChannel <- validationAlert

	receivedValidationAlert, err := notifications.Recv()
	if err != nil {
		t.Errorf("Can't receive validation alert: %s", err)
	}

	if !proto.Equal(receivedValidationAlert.GetAlert(), validationAlert) {
		log.Error("received validation alert != send alert")
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

func (layerMgr *testLayerManager) GetLayersInfo() (layersList []*pb.LayerStatus, err error) {
	return layersList, nil
}

func (layerMgr *testLayerManager) InstallLayer(installInfo *pb.InstallLayerRequest) (err error) {
	return nil
}

func (layerMgr *testLayerManager) UninstallLayer(removeInfo *pb.RemoveLayerRequest) (err error) {
	return nil
}

func (alerts *testAlertProvider) GetAlertsChannel() (channel <-chan *pb.Alert) {
	return alerts.alertsChannel
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newTestClient(url string) (client *testClient, err error) {
	client = &testClient{}

	ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)

	if client.connection, err = grpc.DialContext(ctx, url, grpc.WithInsecure(), grpc.WithBlock()); err != nil {
		return nil, err
	}

	client.pbclient = pb.NewServiceManagerClient(client.connection)

	return client, nil
}

func (client *testClient) close() {
	if client.connection != nil {
		client.connection.Close()
	}
}
