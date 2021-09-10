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

package smserver

import (
	"context"
	"net"

	"github.com/golang/protobuf/ptypes/empty"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/servicemanager"
	"gitpct.epam.com/epmd-aepr/aos_common/utils/cryptutils"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/emptypb"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

/*******************************************************************************
 * Vars
 ******************************************************************************/

/*******************************************************************************
 * Types
 ******************************************************************************/

// ServiceLauncher services launcher interface
type ServiceLauncher interface {
	SetUsers(users []string) (err error)
	InstallService(serviceInfo *pb.InstallServiceRequest) (status *pb.ServiceStatus, err error)
	UninstallService(removeReq *pb.RemoveServiceRequest) (err error)
	GetServicesInfo() (services []*pb.ServiceStatus, err error)
}

// LayerProvider services layer manager interface
type LayerProvider interface {
	GetLayersInfo() (info []*pb.LayerStatus, err error)
	InstallLayer(installInfo *pb.InstallLayerRequest) (err error)
	UninstallLayer(removeInfo *pb.RemoveLayerRequest) (err error)
}

// SMServer SM server instance
type SMServer struct {
	url           string
	launcher      ServiceLauncher
	layerProvider LayerProvider
	grpcServer    *grpc.Server
	listener      net.Listener
	pb.UnimplementedServiceManagerServer
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new IAM server instance
func New(cfg *config.Config, launcher ServiceLauncher, layerProvider LayerProvider, insecure bool) (server *SMServer, err error) {
	server = &SMServer{launcher: launcher, layerProvider: layerProvider}

	var opts []grpc.ServerOption

	if !insecure {
		tlsConfig, err := cryptutils.GetServerMutualTLSConfig(cfg.Crypt.CACert, cfg.CertStorage)
		if err != nil {
			return nil, aoserrors.Wrap(err)
		}

		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
	} else {
		log.Info("SM GRPC server starts in insecure mode")
	}

	server.url = cfg.SMServerURL

	server.grpcServer = grpc.NewServer(opts...)

	pb.RegisterServiceManagerServer(server.grpcServer, server)

	return server, nil
}

// Start starts SM  server
func (server *SMServer) Start() (err error) {
	server.listener, err = net.Listen("tcp", server.url)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	return server.grpcServer.Serve(server.listener)
}

// Stop stops SM server
func (server *SMServer) Stop() {
	log.Debug("Close grpc server")

	if server.grpcServer != nil {
		server.grpcServer.Stop()
	}

	if server.listener != nil {
		server.listener.Close()
	}
}

// SetUsers sets current user
func (server *SMServer) SetUsers(ctx context.Context, users *pb.Users) (ret *empty.Empty, err error) {
	ret = &emptypb.Empty{}

	return ret, server.launcher.SetUsers(users.GetUsers())
}

// GetStatus gets current SM status
func (server *SMServer) GetStatus(tx context.Context, req *empty.Empty) (status *pb.SMStatus, err error) {
	status = &pb.SMStatus{}

	status.Services, err = server.launcher.GetServicesInfo()
	if err != nil {
		return status, aoserrors.Wrap(err)
	}

	status.Layers, err = server.layerProvider.GetLayersInfo()
	if err != nil {
		return status, aoserrors.Wrap(err)
	}

	return status, nil
}

// SetBoardConfig sets new board configuration
func (server *SMServer) SetBoardConfig(ctx context.Context, boardConfig *pb.BoardConfig) (ret *empty.Empty, err error) {
	return ret, nil
}

// InstallService installs aos service
func (server *SMServer) InstallService(ctx context.Context, service *pb.InstallServiceRequest) (status *pb.ServiceStatus, err error) {
	return server.launcher.InstallService(service)
}

// InstallService removes aos service
func (server *SMServer) RemoveService(ctx context.Context, service *pb.RemoveServiceRequest) (ret *empty.Empty, err error) {
	return &emptypb.Empty{}, server.launcher.UninstallService(service)
}

// SetServiceState sets state for aos service
func (server *SMServer) SetServiceState(ctx context.Context, state *pb.ServiceState) (ret *empty.Empty, err error) {
	return ret, nil
}

// OverrideEnvVars overrides entrainment variables for the service
func (server *SMServer) OverrideEnvVars(ctx context.Context,
	envVars *pb.OverrideEnvVarsRequest) (status *pb.OverrideEnvVarStatus, err error) {
	return status, nil
}

// InstallLayer installs the layer
func (server *SMServer) InstallLayer(ctx context.Context, layer *pb.InstallLayerRequest) (ret *empty.Empty, err error) {
	return &emptypb.Empty{}, server.layerProvider.InstallLayer(layer)
}

// RemoveLayer removes the layer
func (server *SMServer) RemoveLayer(ctx context.Context, layer *pb.RemoveLayerRequest) (ret *empty.Empty, err error) {
	return &emptypb.Empty{}, server.layerProvider.UninstallLayer(layer)
}

// SubscribeSmNotification sunscribes for SM notifications
func (server *SMServer) SubscribeSmNotification(req *empty.Empty, stream pb.ServiceManager_SubscribeSMNotificationsServer) (err error) {
	return nil
}
