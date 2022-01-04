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

package smserver

import (
	"context"
	"net"

	"github.com/aoscloud/aos_common/aoserrors"
	pb "github.com/aoscloud/aos_common/api/servicemanager/v1"
	"github.com/aoscloud/aos_common/utils/cryptutils"
	"github.com/golang/protobuf/ptypes/empty"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/aoscloud/aos_servicemanager/config"
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
	GetServicesLayersInfoByUsers(users []string) (servicesInfo []*pb.ServiceStatus, layersInfo []*pb.LayerStatus, err error)
	StateAcceptance(acceptance *pb.StateAcceptance) (err error)
	SetServiceState(state *pb.ServiceState) (err error)
	GetStateMessageChannel() (stateChannel <-chan *pb.SMNotifications)
	RestartServices()
	ProcessDesiredEnvVarsList(envVars []*pb.OverrideEnvVar) (status []*pb.EnvVarStatus, err error)
}

// LayerProvider services layer manager interface
type LayerProvider interface {
	GetLayersInfo() (info []*pb.LayerStatus, err error)
	InstallLayer(installInfo *pb.InstallLayerRequest) (err error)
}

// AlertsProvider alert data provider interface
type AlertsProvider interface {
	GetAlertsChannel() (alertChannel <-chan *pb.Alert)
}

// MonitoringDataProvider monitoring data provider interface
type MonitoringDataProvider interface {
	GetMonitoringDataChannel() (monitoringChannel <-chan *pb.Monitoring)
}

// BoardConfigProcessor board configuration handler
type BoardConfigProcessor interface {
	GetBoardConfigInfo() (version string)
	CheckBoardConfig(configJSON string) (vendorVersion string, err error)
	UpdateBoardConfig(configJSON string) (err error)
}

// LogsProvider logs data provider interface
type LogsProvider interface {
	GetServiceLog(request *pb.ServiceLogRequest)
	GetServiceCrashLog(request *pb.ServiceLogRequest)
	GetSystemLog(request *pb.SystemLogRequest)
	GetLogsDataChannel() (channel <-chan *pb.LogData)
}

// SMServer SM server instance
type SMServer struct {
	url                  string
	launcher             ServiceLauncher
	layerProvider        LayerProvider
	grpcServer           *grpc.Server
	listener             net.Listener
	boardConfigProcessor BoardConfigProcessor
	logsProvider         LogsProvider
	notificationStream   pb.SMService_SubscribeSMNotificationsServer
	alertChannel         <-chan *pb.Alert
	monitoringChannel    <-chan *pb.Monitoring
	stateChannel         <-chan *pb.SMNotifications
	logsChannel          <-chan *pb.LogData
	pb.UnimplementedSMServiceServer
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new IAM server instance.
func New(cfg *config.Config, launcher ServiceLauncher, layerProvider LayerProvider, alertsProvider AlertsProvider,
	monitoringProvider MonitoringDataProvider,
	boardConfigProcessor BoardConfigProcessor, logsProvider LogsProvider,
	insecure bool) (server *SMServer, err error) {
	server = &SMServer{
		launcher: launcher, layerProvider: layerProvider, boardConfigProcessor: boardConfigProcessor,
		logsProvider: logsProvider,
	}

	if alertsProvider != nil {
		server.alertChannel = alertsProvider.GetAlertsChannel()
	}

	if monitoringProvider != nil {
		server.monitoringChannel = monitoringProvider.GetMonitoringDataChannel()
	}

	if launcher != nil {
		server.stateChannel = launcher.GetStateMessageChannel()
	}

	if logsProvider != nil {
		server.logsChannel = logsProvider.GetLogsDataChannel()
	}

	var opts []grpc.ServerOption

	if !insecure {
		tlsConfig, err := cryptutils.GetServerMutualTLSConfig(cfg.CACert, cfg.CertStorage)
		if err != nil {
			return nil, aoserrors.Wrap(err)
		}

		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
	} else {
		log.Info("SM GRPC server starts in insecure mode")
	}

	server.url = cfg.SMServerURL

	server.grpcServer = grpc.NewServer(opts...)

	pb.RegisterSMServiceServer(server.grpcServer, server)

	return server, nil
}

// Start starts SM  server.
func (server *SMServer) Start() (err error) {
	server.listener, err = net.Listen("tcp", server.url)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	return server.grpcServer.Serve(server.listener)
}

// Stop stops SM server.
func (server *SMServer) Stop() {
	log.Debug("Close grpc server")

	if server.grpcServer != nil {
		server.grpcServer.Stop()
	}

	if server.listener != nil {
		server.listener.Close()
	}
}

// GetUsersStatus gets current SM status for user.
func (server *SMServer) GetUsersStatus(ctx context.Context, users *pb.Users) (status *pb.SMStatus, err error) {
	status = &pb.SMStatus{}

	status.Services, status.Layers, err = server.launcher.GetServicesLayersInfoByUsers(users.GetUsers())
	if err != nil {
		return status, aoserrors.Wrap(err)
	}

	return status, nil
}

// GetAllStatus gets current SM status.
func (server *SMServer) GetAllStatus(ctx context.Context, req *empty.Empty) (status *pb.SMStatus, err error) {
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

// GetBoardConfigStatus gets current board configuration status.
func (server *SMServer) GetBoardConfigStatus(context.Context, *empty.Empty) (status *pb.BoardConfigStatus, err error) {
	return &pb.BoardConfigStatus{VendorVersion: server.boardConfigProcessor.GetBoardConfigInfo()}, nil
}

// CheckBoardConfig checks new board configuration.
func (server *SMServer) CheckBoardConfig(ctx context.Context,
	boardConfig *pb.BoardConfig) (status *pb.BoardConfigStatus, err error) {
	version, err := server.boardConfigProcessor.CheckBoardConfig(boardConfig.GetBoardConfig())

	return &pb.BoardConfigStatus{VendorVersion: version}, err
}

// SetBoardConfig sets new board configuration.
func (server *SMServer) SetBoardConfig(ctx context.Context, boardConfig *pb.BoardConfig) (ret *empty.Empty, err error) {
	if err = server.boardConfigProcessor.UpdateBoardConfig(boardConfig.GetBoardConfig()); err != nil {
		return &emptypb.Empty{}, err
	}

	server.launcher.RestartServices()

	return &emptypb.Empty{}, nil
}

// InstallService installs aos service.
func (server *SMServer) InstallService(ctx context.Context, service *pb.InstallServiceRequest) (status *pb.ServiceStatus, err error) {
	return server.launcher.InstallService(service)
}

// InstallService removes aos service.
func (server *SMServer) RemoveService(ctx context.Context, service *pb.RemoveServiceRequest) (ret *empty.Empty, err error) {
	return &emptypb.Empty{}, server.launcher.UninstallService(service)
}

// ServiceStateAcceptance accepts new services state.
func (server *SMServer) ServiceStateAcceptance(ctx context.Context, acceptance *pb.StateAcceptance) (*empty.Empty, error) {
	return &emptypb.Empty{}, server.launcher.StateAcceptance(acceptance)
}

// SetServiceState sets state for aos service.
func (server *SMServer) SetServiceState(ctx context.Context, state *pb.ServiceState) (ret *empty.Empty, err error) {
	return &emptypb.Empty{}, server.launcher.SetServiceState(state)
}

// OverrideEnvVars overrides entrainment variables for the service.
func (server *SMServer) OverrideEnvVars(ctx context.Context,
	envVars *pb.OverrideEnvVarsRequest) (status *pb.OverrideEnvVarStatus, err error) {
	varsStatus, err := server.launcher.ProcessDesiredEnvVarsList(envVars.GetEnvVars())

	return &pb.OverrideEnvVarStatus{EnvVarStatus: varsStatus}, err
}

// InstallLayer installs the layer.
func (server *SMServer) InstallLayer(ctx context.Context, layer *pb.InstallLayerRequest) (ret *empty.Empty, err error) {
	return &emptypb.Empty{}, server.layerProvider.InstallLayer(layer)
}

// SubscribeSMNotifications subscribes for SM notifications.
func (server *SMServer) SubscribeSMNotifications(req *empty.Empty, stream pb.SMService_SubscribeSMNotificationsServer) (err error) {
	server.notificationStream = stream

	server.handleChannels()

	return nil
}

// GetSystemLog gets system logs.
func (server *SMServer) GetSystemLog(ctx context.Context, req *pb.SystemLogRequest) (ret *empty.Empty, err error) {
	server.logsProvider.GetSystemLog(req)

	return &emptypb.Empty{}, nil
}

// GetServiceLog gets the service logs.
func (server *SMServer) GetServiceLog(ctx context.Context, req *pb.ServiceLogRequest) (ret *empty.Empty, err error) {
	server.logsProvider.GetServiceLog(req)

	return &emptypb.Empty{}, nil
}

// GetServiceCrashLog gets the service crash logs.
func (server *SMServer) GetServiceCrashLog(ctx context.Context, req *pb.ServiceLogRequest) (ret *empty.Empty, err error) {
	server.logsProvider.GetServiceCrashLog(req)

	return &emptypb.Empty{}, nil
}

/*******************************************************************************
 * private
 ******************************************************************************/

func (server *SMServer) handleChannels() {
	for {
		select {
		case alert := <-server.alertChannel:
			alertNtf := &pb.SMNotifications_Alert{Alert: alert}

			if err := server.notificationStream.Send(&pb.SMNotifications{SMNotification: alertNtf}); err != nil {
				log.Errorf("Can't send alert: %s ", err)

				return
			}

		case monitoringData := <-server.monitoringChannel:
			if err := server.notificationStream.Send(
				&pb.SMNotifications{
					SMNotification: &pb.SMNotifications_Monitoring{
						Monitoring: monitoringData,
					},
				}); err != nil {
				log.Errorf("Can't send monitoring notification: %s ", err)

				return
			}

		case stateMsg := <-server.stateChannel:
			if err := server.notificationStream.Send(stateMsg); err != nil {
				log.Errorf("Can't send state notification :%s ", err)
				return
			}

		case logs := <-server.logsChannel:
			if err := server.notificationStream.Send(
				&pb.SMNotifications{
					SMNotification: &pb.SMNotifications_Log{Log: logs},
				}); err != nil {
				log.Errorf("Can't send logs: %s ", err)

				return
			}

		case <-server.notificationStream.Context().Done():
			return
		}
	}
}
