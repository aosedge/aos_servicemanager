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
	"io/ioutil"
	"os"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/servicemanager"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

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

	smServer, err := smserver.New(&smConfig, launcher, layerMgr, true)
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

	responce, err := client.pbclient.GetStatus(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Can't get status: %s", err)
	}

	if len(responce.GetServices()) != 1 {
		t.Errorf("incorrect count of services %d", len(responce.GetServices()))
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (launcher *testLauncher) GetServicesInfo() (currentServices []*pb.ServiceStatus, err error) {
	currentServices = append(currentServices, &pb.ServiceStatus{ServiceId: "123"})

	return currentServices, nil
}

func (layerMgr *testLayerManager) GetLayersInfo() (layersList []*pb.LayerStatus, err error) {
	return layersList, nil
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
