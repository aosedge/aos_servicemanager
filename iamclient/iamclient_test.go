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
	"math/rand"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/aostypes"
	"github.com/aosedge/aos_common/api/cloudprotocol"
	pb "github.com/aosedge/aos_common/api/iamanager"
	"github.com/aosedge/aos_common/utils/pbconvert"
	"github.com/golang/protobuf/ptypes/empty"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	"github.com/aosedge/aos_servicemanager/config"
	"github.com/aosedge/aos_servicemanager/iamclient"
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

	nodeInfo         cloudprotocol.NodeInfo
	permissionsCache map[string]servicePermissions
}

type servicePermissions struct {
	instanceIdent aostypes.InstanceIdent
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

	tmpDir, err = os.MkdirTemp("", "iam_")
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

func TestGetCurrentNodeInfo(t *testing.T) {
	testServer, err := newTestServer(publicServerURL, protectedServerURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer testServer.close()

	testServer.nodeInfo = cloudprotocol.NodeInfo{
		NodeID:   "nodeID",
		NodeType: "nodeType",
		Name:     "name",
		Status:   "status",
		OSType:   "osType",
		MaxDMIPs: 10000,
		CPUs: []cloudprotocol.CPUInfo{
			{
				ModelName: "modelName1", NumCores: 1, NumThreads: 2, Arch: "arch1",
				ArchFamily: "archFamily1", MaxDMIPs: 1000,
			},
			{
				ModelName: "modelName2", NumCores: 2, NumThreads: 4, Arch: "arch2",
				ArchFamily: "archFamily2", MaxDMIPs: 2000,
			},
		},
		Partitions: []cloudprotocol.PartitionInfo{
			{Name: "name1", Types: []string{"type1", "type2"}, TotalSize: 1000, Path: "/path1"},
			{Name: "name2", Types: []string{"type3", "type4"}, TotalSize: 2000, Path: "/path2"},
			{Name: "name3", Types: []string{"type5", "type6"}, TotalSize: 3000, Path: "/path3"},
		},
		Attrs: map[string]interface{}{
			"attr1": "value1",
			"attr2": "value2",
		},
		ErrorInfo: &cloudprotocol.ErrorInfo{
			AosCode:  1,
			ExitCode: 2,
			Message:  "message",
		},
	}

	client, err := iamclient.New(&config.Config{
		IAMProtectedServerURL: protectedServerURL,
		IAMPublicServerURL:    publicServerURL,
	}, nil, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %v", err)
	}
	defer client.Close()

	nodeInfo, err := client.GetCurrentNodeInfo()
	if err != nil {
		t.Fatalf("Can't get node info: %v", err)
	}

	if !reflect.DeepEqual(testServer.nodeInfo, nodeInfo) {
		t.Errorf("Wrong node info: %v %v", nodeInfo, testServer.nodeInfo)
	}
}

func TestRegisterService(t *testing.T) {
	testServer, err := newTestServer(publicServerURL, protectedServerURL)
	if err != nil {
		t.Fatalf("Can't create test server: %v", err)
	}

	defer testServer.close()

	client, err := iamclient.New(&config.Config{
		IAMProtectedServerURL: protectedServerURL,
		IAMPublicServerURL:    publicServerURL,
	}, nil, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %v", err)
	}

	defer client.Close()

	var (
		permissions      = map[string]map[string]string{"vis": {"*": "rw", "test": "r"}}
		registerInstance = aostypes.InstanceIdent{ServiceID: "serviceID1", SubjectID: "s1", Instance: 1}
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
		IAMProtectedServerURL: protectedServerURL,
		IAMPublicServerURL:    publicServerURL,
	}, nil, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %v", err)
	}

	defer client.Close()

	var (
		permissions      = map[string]map[string]string{"vis": {"*": "rw", "test": "r"}}
		registerInstance = aostypes.InstanceIdent{ServiceID: "serviceID1", SubjectID: "s1", Instance: 1}
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
		permissionsCache: make(map[string]servicePermissions),
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

func (server *testServer) GetNodeInfo(context context.Context, req *empty.Empty) (*pb.NodeInfo, error) {
	pbNodeInfo := &pb.NodeInfo{
		NodeId:   server.nodeInfo.NodeID,
		NodeType: server.nodeInfo.NodeType,
		Name:     server.nodeInfo.Name,
		Status:   server.nodeInfo.Status,
		OsType:   server.nodeInfo.OSType,
		MaxDmips: server.nodeInfo.MaxDMIPs,
		TotalRam: server.nodeInfo.TotalRAM,
	}

	if server.nodeInfo.ErrorInfo != nil {
		pbNodeInfo.Error = pbconvert.ErrorInfoToPB(server.nodeInfo.ErrorInfo)
	}

	if server.nodeInfo.CPUs != nil {
		pbNodeInfo.Cpus = make([]*pb.CPUInfo, 0, len(server.nodeInfo.CPUs))

		for _, cpu := range server.nodeInfo.CPUs {
			pbNodeInfo.Cpus = append(pbNodeInfo.Cpus, &pb.CPUInfo{
				ModelName:  cpu.ModelName,
				NumCores:   cpu.NumCores,
				NumThreads: cpu.NumThreads,
				Arch:       cpu.Arch,
				ArchFamily: cpu.ArchFamily,
				MaxDmips:   cpu.MaxDMIPs,
			})
		}
	}

	if server.nodeInfo.Attrs != nil {
		pbNodeInfo.Attrs = make([]*pb.NodeAttribute, 0, len(server.nodeInfo.Attrs))

		for key, value := range server.nodeInfo.Attrs {
			strValue, ok := value.(string)
			if !ok {
				return nil, aoserrors.New("incorrect attribute value")
			}

			pbNodeInfo.Attrs = append(pbNodeInfo.Attrs, &pb.NodeAttribute{Name: key, Value: strValue})
		}
	}

	if server.nodeInfo.Partitions != nil {
		pbNodeInfo.Partitions = make([]*pb.PartitionInfo, 0, len(server.nodeInfo.Partitions))

		for _, partition := range server.nodeInfo.Partitions {
			pbNodeInfo.Partitions = append(pbNodeInfo.Partitions, &pb.PartitionInfo{
				Name:      partition.Name,
				Types:     partition.Types,
				TotalSize: partition.TotalSize,
				Path:      partition.Path,
			})
		}
	}

	return pbNodeInfo, nil
}

func (server *testServer) RegisterInstance(
	context context.Context, req *pb.RegisterInstanceRequest,
) (rsp *pb.RegisterInstanceResponse, err error) {
	rsp = &pb.RegisterInstanceResponse{}

	instanceIdent := pbconvert.InstanceIdentFromPB(req.GetInstance())

	if secret := server.findSecret(instanceIdent); secret != "" {
		return rsp, aoserrors.New("service is already registered")
	}

	secret := randomString()
	rsp.Secret = secret

	permissions := make(map[string]map[string]string)
	for key, value := range req.GetPermissions() {
		permissions[key] = value.GetPermissions()
	}

	server.permissionsCache[secret] = servicePermissions{instanceIdent: instanceIdent, permissions: permissions}

	return rsp, nil
}

func (server *testServer) UnregisterInstance(
	ctx context.Context, req *pb.UnregisterInstanceRequest,
) (rsp *empty.Empty, err error) {
	rsp = &empty.Empty{}

	secret := server.findSecret(pbconvert.InstanceIdentFromPB(req.GetInstance()))
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

	funcServersPermissions, ok := server.permissionsCache[req.GetSecret()]
	if !ok {
		return rsp, aoserrors.New("secret not found")
	}

	permissions, ok := funcServersPermissions.permissions[req.GetFunctionalServerId()]
	if !ok {
		return rsp, aoserrors.New("permissions for functional server not found")
	}

	rsp.Permissions = &pb.Permissions{Permissions: permissions}
	rsp.Instance = pbconvert.InstanceIdentToPB(funcServersPermissions.instanceIdent)

	return rsp, nil
}

func (server *testServer) findSecret(instance aostypes.InstanceIdent) (secret string) {
	for key, value := range server.permissionsCache {
		if value.instanceIdent == instance {
			return key
		}
	}

	return ""
}

func randomString() string {
	secret := make([]byte, secretLength)

	for i := range secret {
		//nolint:gosec
		secret[i] = secretSymbols[rand.New(rand.NewSource(time.Now().UnixNano())).Intn(len(secretSymbols))]
	}

	return string(secret)
}
