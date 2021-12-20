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

package iamclient_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	pb "github.com/aoscloud/aos_common/api/iamanager/v1"
	"github.com/golang/protobuf/ptypes/empty"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	"aos_servicemanager/config"
	"aos_servicemanager/iamclient"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	serverURL     = "localhost:8089"
	secretLength  = 8
	secretSymbols = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type testServer struct {
	grpcServer          *grpc.Server
	users               []string
	usersChangedChannel chan []string
	permissionsCache    map[string]servicePermissions
	pb.UnimplementedIAMProtectedServiceServer
	pb.UnimplementedIAMPublicServiceServer
}

type servicePermissions struct {
	serviceID   string
	permissions map[string]map[string]string
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var tmpDir string

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
	var err error

	tmpDir, err = ioutil.TempDir("", "iam_")
	if err != nil {
		log.Fatalf("Error create temporary dir: %s", err)
	}

	ret := m.Run()

	if err := os.RemoveAll(tmpDir); err != nil {
		log.Fatalf("Error removing tmp dir: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestGetUsers(t *testing.T) {
	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %s", err)
	}
	defer server.close()

	server.users = []string{"user1", "user2", "user3"}

	client, err := iamclient.New(&config.Config{IAMServerURL: serverURL}, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %s", err)
	}
	defer client.Close()

	if !reflect.DeepEqual(server.users, client.GetUsers()) {
		t.Errorf("Invalid users: %s", client.GetUsers())
	}

	newUsers := []string{"newUser1", "newUser2", "newUser3"}

	server.usersChangedChannel <- newUsers

	select {
	case users := <-client.GetUsersChangedChannel():
		if !reflect.DeepEqual(users, newUsers) {
			t.Errorf("Invalid users: %s", users)
		}

	case <-time.After(5 * time.Second):
		t.Error("Wait users changed timeout")
	}
}

func TestRegisterService(t *testing.T) {
	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %s", err)
	}
	defer server.close()

	client, err := iamclient.New(&config.Config{IAMServerURL: serverURL}, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %s", err)
	}
	defer client.Close()

	permissions := map[string]map[string]string{"vis": {"*": "rw", "test": "r"}}

	secret, err := client.RegisterService("serviceID", permissions)
	if err != nil || secret == "" {
		t.Errorf("Can't send a request: %s", err)
	}

	secret, err = client.RegisterService("serviceID", permissions)
	if err == nil || secret != "" {
		t.Error("Re-registration of the service is prohibited")
	}

	err = client.UnregisterService("serviceID")
	if err != nil {
		t.Errorf("Can't send a request: %s", err)
	}

	secret, err = client.RegisterService("serviceID", permissions)
	if err != nil || secret == "" {
		t.Errorf("Can't send a request: %s", err)
	}
}

func TestGetPermissions(t *testing.T) {
	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %s", err)
	}
	defer server.close()

	client, err := iamclient.New(&config.Config{IAMServerURL: serverURL}, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %s", err)
	}
	defer client.Close()

	permissions := map[string]map[string]string{"vis": {"*": "rw", "test": "r"}}

	registerServiceID := "serviceID"
	secret, err := client.RegisterService(registerServiceID, permissions)
	if err != nil || secret == "" {
		t.Errorf("Can't send a request: %s", err)
	}

	serviceID, respPermissions, err := client.GetPermissions(secret, "vis")
	if err != nil {
		t.Errorf("Can't send a request: %s", err)
	}

	if !reflect.DeepEqual(respPermissions, permissions["vis"]) {
		t.Errorf("Wrong permissions: %v", respPermissions)
	}

	if registerServiceID != serviceID {
		t.Errorf("Wrong service id: %v", serviceID)
	}

	err = client.UnregisterService(registerServiceID)
	if err != nil {
		t.Errorf("Can't send a request: %s", err)
	}

	_, _, err = client.GetPermissions(secret, "vis")
	if err == nil {
		t.Error("Getting permissions after removing a service")
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newTestServer(url string) (server *testServer, err error) {
	server = &testServer{usersChangedChannel: make(chan []string, 1)}

	listener, err := net.Listen("tcp", url)
	if err != nil {
		return nil, err
	}
	server.grpcServer = grpc.NewServer()

	pb.RegisterIAMProtectedServiceServer(server.grpcServer, server)
	pb.RegisterIAMPublicServiceServer(server.grpcServer, server)

	server.permissionsCache = make(map[string]servicePermissions)

	go server.grpcServer.Serve(listener)

	return server, nil
}

func (server *testServer) close() (err error) {
	if server.grpcServer != nil {
		server.grpcServer.Stop()
	}

	return nil
}

func (server *testServer) RegisterService(context context.Context,
	req *pb.RegisterServiceRequest) (rsp *pb.RegisterServiceResponse, err error) {
	rsp = &pb.RegisterServiceResponse{}

	secret := server.findServiceID(req.ServiceId)
	if secret != "" {
		return rsp, fmt.Errorf("service %s is already registered", req.ServiceId)
	}

	secret = randomString()
	rsp.Secret = secret

	permissions := make(map[string]map[string]string)
	for key, value := range req.Permissions {
		permissions[key] = value.Permissions
	}

	server.permissionsCache[secret] = servicePermissions{serviceID: req.ServiceId, permissions: permissions}

	return rsp, nil
}

func (server *testServer) UnregisterService(ctx context.Context, req *pb.UnregisterServiceRequest) (rsp *empty.Empty, err error) {
	rsp = &empty.Empty{}

	secret := server.findServiceID(req.ServiceId)
	if secret == "" {
		return rsp, fmt.Errorf("service %s is not registered ", req.ServiceId)
	}

	delete(server.permissionsCache, secret)

	return rsp, nil
}

func (server *testServer) GetPermissions(ctx context.Context, req *pb.PermissionsRequest) (rsp *pb.PermissionsResponse, err error) {
	rsp = &pb.PermissionsResponse{}

	funcServersPermissions, ok := server.permissionsCache[req.Secret]
	if !ok {
		return rsp, fmt.Errorf("secret not found")
	}

	permissions, ok := funcServersPermissions.permissions[req.FunctionalServerId]
	if !ok {
		return rsp, fmt.Errorf("permissions for functional server not found")
	}

	rsp.Permissions = &pb.Permissions{Permissions: permissions}
	rsp.ServiceId = funcServersPermissions.serviceID

	return rsp, nil
}

func (server *testServer) GetUsers(context context.Context, req *empty.Empty) (rsp *pb.Users, err error) {
	rsp = &pb.Users{Users: server.users}

	return rsp, nil
}

func (server *testServer) SubscribeUsersChanged(req *empty.Empty, stream pb.IAMPublicService_SubscribeUsersChangedServer) (err error) {
	for {
		select {
		case <-stream.Context().Done():
			return nil

		case users := <-server.usersChangedChannel:
			stream.Send(&pb.Users{Users: users})
		}
	}
}

func (server *testServer) findServiceID(serviceID string) (secret string) {
	for key, value := range server.permissionsCache {
		if value.serviceID == serviceID {
			return key
		}
	}

	return ""
}

func randomString() string {
	secret := make([]byte, secretLength)

	rand.Seed(time.Now().UnixNano())

	for i := range secret {
		secret[i] = secretSymbols[rand.Intn(len(secretSymbols))]
	}

	return string(secret)
}
