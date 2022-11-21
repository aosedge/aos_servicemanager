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
	"io/ioutil"
	"math/rand"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	pb "github.com/aoscloud/aos_common/api/iamanager/v4"
	"github.com/golang/protobuf/ptypes/empty"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/iamclient"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	protectedServerURL = "localhost:8089"
	publicServerURL    = "localhost:8090"
	secretLength       = 8
	secretSymbols      = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testServer struct {
	pb.UnimplementedIAMPublicServiceServer
	pb.UnimplementedIAMPublicIdentityServiceServer
	pb.UnimplementedIAMPublicPermissionsServiceServer
	pb.UnimplementedIAMPermissionsServiceServer

	publicServer    *grpc.Server
	protectedServer *grpc.Server

	subjects               []string
	subjectsChangedChannel chan []string
	permissionsCache       map[string]servicePermissions
}

type servicePermissions struct {
	instanceIdent cloudprotocol.InstanceIdent
	permissions   map[string]map[string]string
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var tmpDir string

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
 * Main
 **********************************************************************************************************************/

func TestMain(m *testing.M) {
	var err error

	tmpDir, err = ioutil.TempDir("", "iam_")
	if err != nil {
		log.Fatalf("Error create temporary dir: %v", err)
	}

	ret := m.Run()

	if err := os.RemoveAll(tmpDir); err != nil {
		log.Fatalf("Error removing tmp dir: %v", err)
	}

	os.Exit(ret)
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestGetSubjects(t *testing.T) {
	testServer, err := newTestServer(publicServerURL, protectedServerURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer testServer.close()

	testServer.subjects = []string{"subject1", "subject2", "subject3"}

	client, err := iamclient.New(&config.Config{
		IAMServerURL:       protectedServerURL,
		IAMPublicServerURL: publicServerURL,
	}, nil, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %v", err)
	}
	defer client.Close()

	if !reflect.DeepEqual(testServer.subjects, client.GetSubjects()) {
		t.Errorf("Invalid subjects: %s", client.GetSubjects())
	}

	newSubjects := []string{"newSubjects1", "newSubjects2", "newSubjects3"}

	testServer.subjectsChangedChannel <- newSubjects

	select {
	case subjects := <-client.GetSubjectsChangedChannel():
		if !reflect.DeepEqual(subjects, newSubjects) {
			t.Errorf("Invalid subjects: %s", subjects)
		}

	case <-time.After(5 * time.Second):
		t.Error("Wait subjects changed timeout")
	}
}

func TestRegisterService(t *testing.T) {
	testServer, err := newTestServer(publicServerURL, protectedServerURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer testServer.close()

	client, err := iamclient.New(&config.Config{
		IAMServerURL:       protectedServerURL,
		IAMPublicServerURL: publicServerURL,
	}, nil, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %v", err)
	}

	defer client.Close()

	var (
		permissions      = map[string]map[string]string{"vis": {"*": "rw", "test": "r"}}
		registerInstance = cloudprotocol.InstanceIdent{ServiceID: "serviceID1", SubjectID: "s1", Instance: 1}
	)

	secret, err := client.RegisterInstance(registerInstance, permissions)
	if err != nil || secret == "" {
		t.Errorf("Can't send a request: %v", err)
	}

	secret, err = client.RegisterInstance(registerInstance, permissions)
	if err == nil || secret != "" {
		t.Error("Re-registration of the service is prohibited")
	}

	err = client.UnregisterInstance(registerInstance)
	if err != nil {
		t.Errorf("Can't send a request: %v", err)
	}

	secret, err = client.RegisterInstance(registerInstance, permissions)
	if err != nil || secret == "" {
		t.Errorf("Can't send a request: %v", err)
	}
}

func TestGetPermissions(t *testing.T) {
	testServer, err := newTestServer(publicServerURL, protectedServerURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer testServer.close()

	client, err := iamclient.New(&config.Config{
		IAMServerURL:       protectedServerURL,
		IAMPublicServerURL: publicServerURL,
	}, nil, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %v", err)
	}

	defer client.Close()

	var (
		permissions      = map[string]map[string]string{"vis": {"*": "rw", "test": "r"}}
		registerInstance = cloudprotocol.InstanceIdent{ServiceID: "serviceID1", SubjectID: "s1", Instance: 1}
	)

	secret, err := client.RegisterInstance(registerInstance, permissions)
	if err != nil || secret == "" {
		t.Errorf("Can't send a request: %v", err)
	}

	receivedInstance, respPermissions, err := client.GetPermissions(secret, "vis")
	if err != nil {
		t.Errorf("Can't send a request: %v", err)
	}

	if !reflect.DeepEqual(respPermissions, permissions["vis"]) {
		t.Errorf("Wrong permissions: %v", respPermissions)
	}

	if receivedInstance != registerInstance {
		t.Error("Incorrect received instance")
	}

	err = client.UnregisterInstance(registerInstance)
	if err != nil {
		t.Errorf("Can't send a request: %v", err)
	}

	_, _, err = client.GetPermissions(secret, "vis")
	if err == nil {
		t.Error("Getting permissions after removing a service")
	}
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func newTestServer(publicServerURL, protectedServerURL string) (*testServer, error) {
	server := &testServer{
		subjectsChangedChannel: make(chan []string, 1),
		permissionsCache:       make(map[string]servicePermissions),
	}

	publicListener, err := net.Listen("tcp", publicServerURL)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	server.publicServer = grpc.NewServer()

	pb.RegisterIAMPublicServiceServer(server.publicServer, server)
	pb.RegisterIAMPublicIdentityServiceServer(server.publicServer, server)
	pb.RegisterIAMPublicPermissionsServiceServer(server.publicServer, server)

	go func() {
		if err := server.publicServer.Serve(publicListener); err != nil {
			log.Errorf("Can't serve grpc server: %v", err)
		}
	}()

	protectedListener, err := net.Listen("tcp", protectedServerURL)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	server.protectedServer = grpc.NewServer()

	pb.RegisterIAMPermissionsServiceServer(server.protectedServer, server)

	go func() {
		if err := server.protectedServer.Serve(protectedListener); err != nil {
			log.Errorf("Can't serve grpc server: %v", err)
		}
	}()

	return server, nil
}

func (server *testServer) close() {
	if server.publicServer != nil {
		server.publicServer.Stop()
	}

	if server.protectedServer != nil {
		server.protectedServer.Stop()
	}
}

func (server *testServer) RegisterInstance(
	context context.Context, req *pb.RegisterInstanceRequest,
) (rsp *pb.RegisterInstanceResponse, err error) {
	rsp = &pb.RegisterInstanceResponse{}

	instanceIdent := pbToInstanceIdent(req.Instance)

	if secret := server.findSecret(instanceIdent); secret != "" {
		return rsp, aoserrors.New("service is already registered")
	}

	secret := randomString()
	rsp.Secret = secret

	permissions := make(map[string]map[string]string)
	for key, value := range req.Permissions {
		permissions[key] = value.Permissions
	}

	server.permissionsCache[secret] = servicePermissions{instanceIdent: instanceIdent, permissions: permissions}

	return rsp, nil
}

func (server *testServer) UnregisterInstance(
	ctx context.Context, req *pb.UnregisterInstanceRequest,
) (rsp *empty.Empty, err error) {
	rsp = &empty.Empty{}

	secret := server.findSecret(pbToInstanceIdent(req.Instance))
	if secret == "" {
		return rsp, aoserrors.New("service is not registered ")
	}

	delete(server.permissionsCache, secret)

	return rsp, nil
}

func (server *testServer) GetPermissions(
	ctx context.Context, req *pb.PermissionsRequest,
) (rsp *pb.PermissionsResponse, err error) {
	rsp = &pb.PermissionsResponse{}

	funcServersPermissions, ok := server.permissionsCache[req.Secret]
	if !ok {
		return rsp, aoserrors.New("secret not found")
	}

	permissions, ok := funcServersPermissions.permissions[req.FunctionalServerId]
	if !ok {
		return rsp, aoserrors.New("permissions for functional server not found")
	}

	rsp.Permissions = &pb.Permissions{Permissions: permissions}
	rsp.Instance = instanceIdentToPB(funcServersPermissions.instanceIdent)

	return rsp, nil
}

func (server *testServer) GetSubjects(context context.Context, req *empty.Empty) (rsp *pb.Subjects, err error) {
	rsp = &pb.Subjects{Subjects: server.subjects}

	return rsp, nil
}

func (server *testServer) SubscribeSubjectsChanged(req *empty.Empty,
	stream pb.IAMPublicIdentityService_SubscribeSubjectsChangedServer,
) (err error) {
	for {
		select {
		case <-stream.Context().Done():
			return nil

		case subjects := <-server.subjectsChangedChannel:
			if err := stream.Send(&pb.Subjects{Subjects: subjects}); err != nil {
				return aoserrors.Wrap(err)
			}

			return nil
		}
	}
}

func (server *testServer) findSecret(instance cloudprotocol.InstanceIdent) (secret string) {
	for key, value := range server.permissionsCache {
		if value.instanceIdent == instance {
			return key
		}
	}

	return ""
}

func randomString() string {
	secret := make([]byte, secretLength)

	rand.Seed(time.Now().UnixNano())

	for i := range secret {
		secret[i] = secretSymbols[rand.Intn(len(secretSymbols))] // nolint:gosec
	}

	return string(secret)
}

func instanceIdentToPB(ident cloudprotocol.InstanceIdent) *pb.InstanceIdent {
	return &pb.InstanceIdent{ServiceId: ident.ServiceID, SubjectId: ident.SubjectID, Instance: ident.Instance}
}

func pbToInstanceIdent(ident *pb.InstanceIdent) cloudprotocol.InstanceIdent {
	return cloudprotocol.InstanceIdent{
		ServiceID: ident.ServiceId, SubjectID: ident.SubjectId, Instance: uint64(ident.Instance),
	}
}
