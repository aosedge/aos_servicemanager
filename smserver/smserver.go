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
	"errors"
	"net"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	pb "github.com/aoscloud/aos_common/api/servicemanager/v2"
	"github.com/aoscloud/aos_common/utils/cryptutils"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/golang/protobuf/ptypes/timestamp"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aoscloud/aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

/*******************************************************************************
 * Vars
 ******************************************************************************/

var errIncorrectAlertType = errors.New("incorrect alert type")

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

// AlertsProvider alert data provider interface.
type AlertsProvider interface {
	GetAlertsChannel() (channel <-chan cloudprotocol.AlertItem)
}

// MonitoringDataProvider monitoring data provider interface.
type MonitoringDataProvider interface {
	GetMonitoringDataChannel() (monitoringChannel <-chan cloudprotocol.MonitoringData)
}

// BoardConfigProcessor board configuration handler
type BoardConfigProcessor interface {
	GetBoardConfigInfo() (version string)
	CheckBoardConfig(configJSON string) (vendorVersion string, err error)
	UpdateBoardConfig(configJSON string) (err error)
}

// LogsProvider logs data provider interface.
type LogsProvider interface {
	GetInstanceLog(request cloudprotocol.RequestServiceLog) error
	GetInstanceCrashLog(request cloudprotocol.RequestServiceCrashLog) error
	GetSystemLog(request cloudprotocol.RequestSystemLog)
	GetLogsDataChannel() (channel <-chan cloudprotocol.PushLog)
}

// CertificateProvider certificate and key provider interface
type CertificateProvider interface {
	GetCertKeyURL(keyType string) (certURL, keyURL string, err error)
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
	alertChannel         <-chan cloudprotocol.AlertItem
	monitoringChannel    <-chan cloudprotocol.MonitoringData
	stateChannel         <-chan *pb.SMNotifications
	logsChannel          <-chan cloudprotocol.PushLog
	pb.UnimplementedSMServiceServer
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new IAM server instance.
func New(cfg *config.Config, launcher ServiceLauncher, layerProvider LayerProvider, alertsProvider AlertsProvider,
	monitoringProvider MonitoringDataProvider,
	boardConfigProcessor BoardConfigProcessor, logsProvider LogsProvider, cryptcoxontext *cryptutils.CryptoContext,
	certProvider CertificateProvider, insecure bool,
) (server *SMServer, err error) {
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
		certURL, keyURL, err := certProvider.GetCertKeyURL(cfg.CertStorage)
		if err != nil {
			return nil, aoserrors.Wrap(err)
		}

		tlsConfig, err := cryptcoxontext.GetClientMutualTLSConfig(certURL, keyURL)
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
	boardConfig *pb.BoardConfig,
) (status *pb.BoardConfigStatus, err error) {
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
	envVars *pb.OverrideEnvVarsRequest,
) (status *pb.OverrideEnvVarStatus, err error) {
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
func (server *SMServer) GetSystemLog(ctx context.Context, req *pb.SystemLogRequest) (*empty.Empty, error) {
	getSystemLogRequest := cloudprotocol.RequestSystemLog{LogID: req.LogId}

	getSystemLogRequest.From, getSystemLogRequest.Till = getFromTillTimeFromPB(req.From, req.Till)

	server.logsProvider.GetSystemLog(getSystemLogRequest)

	return &emptypb.Empty{}, nil
}

// GetInstanceLog gets instance service logs.
func (server *SMServer) GetInstanceLog(ctx context.Context, req *pb.InstanceLogRequest) (*empty.Empty, error) {
	getInstanceLogRequest := cloudprotocol.RequestServiceLog{LogID: req.LogId}

	getInstanceLogRequest.From, getInstanceLogRequest.Till = getFromTillTimeFromPB(req.From, req.Till)
	getInstanceLogRequest.InstanceFilter = getInstanceFilterFromPB(req.Instance)

	return &emptypb.Empty{}, aoserrors.Wrap(server.logsProvider.GetInstanceLog(getInstanceLogRequest))
}

// GetInstanceCrashLog gets instance service crash logs.
func (server *SMServer) GetInstanceCrashLog(ctx context.Context, req *pb.InstanceLogRequest) (*empty.Empty, error) {
	getInstanceCrashLogRequest := cloudprotocol.RequestServiceCrashLog{LogID: req.LogId}

	getInstanceCrashLogRequest.From, getInstanceCrashLogRequest.Till = getFromTillTimeFromPB(req.From, req.Till)
	getInstanceCrashLogRequest.InstanceFilter = getInstanceFilterFromPB(req.Instance)

	return &emptypb.Empty{}, aoserrors.Wrap(server.logsProvider.GetInstanceCrashLog(getInstanceCrashLogRequest))
}

/*******************************************************************************
 * private
 ******************************************************************************/

func (server *SMServer) handleChannels() {
	for {
		select {
		case alert := <-server.alertChannel:
			pbAlert, err := cloudprotocolAlertToPB(&alert)
			if err != nil {
				log.Errorf("Can't convert alert to pb: %s", err)

				continue
			}

			alertNtf := &pb.SMNotifications_Alert{Alert: pbAlert}

			if err := server.notificationStream.Send(&pb.SMNotifications{SMNotification: alertNtf}); err != nil {
				log.Errorf("Can't send alert: %s ", err)

				return
			}

		case monitoringData := <-server.monitoringChannel:
			if err := server.notificationStream.Send(
				&pb.SMNotifications{
					SMNotification: &pb.SMNotifications_Monitoring{
						Monitoring: cloudprotocolMonitoringToPB(monitoringData),
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
					SMNotification: &pb.SMNotifications_Log{Log: cloudprotocolLogToPB(logs)},
				}); err != nil {
				log.Errorf("Can't send logs: %s ", err)

				return
			}

		case <-server.notificationStream.Context().Done():
			return
		}
	}
}

func getFromTillTimeFromPB(fromPB, tillPB *timestamp.Timestamp) (from, till *time.Time) {
	if fromPB != nil {
		localFrom := fromPB.AsTime()

		from = &localFrom
	}

	if tillPB != nil {
		localTill := tillPB.AsTime()

		till = &localTill
	}

	return from, till
}

func getInstanceFilterFromPB(ident *pb.InstanceIdent) (filter cloudprotocol.InstanceFilter) {
	filter.ServiceID = ident.ServiceId

	if ident.SubjectId != "" {
		filter.SubjectID = &ident.SubjectId
	}

	if ident.Instance != -1 {
		instance := (uint64)(ident.Instance)

		filter.Instance = &instance
	}

	return filter
}

func cloudprotocolLogToPB(log cloudprotocol.PushLog) (pbLog *pb.LogData) {
	return &pb.LogData{LogId: log.LogID, PartCount: log.PartCount, Part: log.Part, Error: log.Error, Data: log.Data}
}

func cloudprotocolAlertToPB(alert *cloudprotocol.AlertItem) (pbAlert *pb.Alert, err error) {
	pbAlert = &pb.Alert{Tag: alert.Tag, Timestamp: timestamppb.New(alert.Timestamp)}

	switch alert.Tag {
	case cloudprotocol.AlertTagSystemError:
		if pbAlert.Payload, err = getPBSystemAlertFromPayload(alert.Payload); err != nil {
			return nil, err
		}

	case cloudprotocol.AlertTagAosCore:
		if pbAlert.Payload, err = getPBCoreAlertFromPayload(alert.Payload); err != nil {
			return nil, err
		}

	case cloudprotocol.AlertTagResourceValidate:
		if pbAlert.Payload, err = getPBResourceValidateAlertFromPayload(alert.Payload); err != nil {
			return nil, err
		}

	case cloudprotocol.AlertTagDeviceAllocate:
		if pbAlert.Payload, err = getPBDeviceAllocateAlertFromPayload(alert.Payload); err != nil {
			return nil, err
		}

	case cloudprotocol.AlertTagSystemQuota:
		if pbAlert.Payload, err = getPBSystemQuotaAlertFromPayload(alert.Payload); err != nil {
			return nil, err
		}

	case cloudprotocol.AlertTagInstanceQuota:
		if pbAlert.Payload, err = getPBInstanceQuotaAlertFromPayload(alert.Payload); err != nil {
			return nil, err
		}

	case cloudprotocol.AlertTagServiceInstance:
		if pbAlert.Payload, err = getPBInstanceAlertFromPayload(alert.Payload); err != nil {
			return nil, err
		}
	}

	return pbAlert, nil
}

func getPBSystemAlertFromPayload(payload interface{}) (*pb.Alert_SystemAlert, error) {
	sysAlert, ok := payload.(cloudprotocol.SystemAlert)
	if !ok {
		return nil, aoserrors.Wrap(errIncorrectAlertType)
	}

	return &pb.Alert_SystemAlert{SystemAlert: &pb.SystemAlert{Message: sysAlert.Message}}, nil
}

func getPBCoreAlertFromPayload(payload interface{}) (*pb.Alert_CoreAlert, error) {
	coreAlert, ok := payload.(cloudprotocol.CoreAlert)
	if !ok {
		return nil, aoserrors.Wrap(errIncorrectAlertType)
	}

	return &pb.Alert_CoreAlert{CoreAlert: &pb.CoreAlert{
		CoreComponent: coreAlert.CoreComponent,
		Message:       coreAlert.Message,
	}}, nil
}

func getPBResourceValidateAlertFromPayload(payload interface{}) (*pb.Alert_ResourceValidateAlert, error) {
	resAlert, ok := payload.(cloudprotocol.ResourceValidateAlert)
	if !ok {
		return nil, aoserrors.Wrap(errIncorrectAlertType)
	}

	pbResAlert := pb.ResourceValidateAlert{}

	for _, resAlertData := range resAlert.ResourcesErrors {
		pbResAlertElement := pb.ResourceValidateErrors{
			Name:     resAlertData.Name,
			ErrorMsg: resAlertData.Errors,
		}

		pbResAlert.Errors = append(pbResAlert.Errors, &pbResAlertElement)
	}

	return &pb.Alert_ResourceValidateAlert{ResourceValidateAlert: &pbResAlert}, nil
}

func getPBDeviceAllocateAlertFromPayload(payload interface{}) (*pb.Alert_DeviceAllocateAlert, error) {
	devAlert, ok := payload.(cloudprotocol.DeviceAllocateAlert)
	if !ok {
		return nil, aoserrors.Wrap(errIncorrectAlertType)
	}

	return &pb.Alert_DeviceAllocateAlert{DeviceAllocateAlert: &pb.DeviceAllocateAlert{
		Instance: cloudprotocolInstanceIdentToPB(devAlert.InstanceIdent), Message: devAlert.Message,
		Device: devAlert.Device,
	}}, nil
}

func getPBSystemQuotaAlertFromPayload(payload interface{}) (*pb.Alert_SystemQuotaAlert, error) {
	sysQuotaAlert, ok := payload.(cloudprotocol.SystemQuotaAlert)
	if !ok {
		return nil, aoserrors.Wrap(errIncorrectAlertType)
	}

	return &pb.Alert_SystemQuotaAlert{SystemQuotaAlert: &pb.SystemQuotaAlert{
		Parameter: sysQuotaAlert.Parameter,
		Value:     sysQuotaAlert.Value,
	}}, nil
}

func getPBInstanceQuotaAlertFromPayload(payload interface{}) (*pb.Alert_InstanceQuotaAlert, error) {
	instQuotaAlert, ok := payload.(cloudprotocol.InstanceQuotaAlert)
	if !ok {
		return nil, aoserrors.Wrap(errIncorrectAlertType)
	}

	return &pb.Alert_InstanceQuotaAlert{InstanceQuotaAlert: &pb.InstanceQuotaAlert{
		Instance:  cloudprotocolInstanceIdentToPB(instQuotaAlert.InstanceIdent),
		Parameter: instQuotaAlert.Parameter,
		Value:     instQuotaAlert.Value,
	}}, nil
}

func getPBInstanceAlertFromPayload(payload interface{}) (*pb.Alert_InstanceAlert, error) {
	instAlert, ok := payload.(cloudprotocol.ServiceInstanceAlert)
	if !ok {
		return nil, aoserrors.Wrap(errIncorrectAlertType)
	}

	return &pb.Alert_InstanceAlert{InstanceAlert: &pb.InstanceAlert{
		Instance:   cloudprotocolInstanceIdentToPB(instAlert.InstanceIdent),
		AosVersion: instAlert.AosVersion,
		Message:    instAlert.Message,
	}}, nil
}

func cloudprotocolInstanceIdentToPB(ident cloudprotocol.InstanceIdent) *pb.InstanceIdent {
	return &pb.InstanceIdent{ServiceId: ident.ServiceID, SubjectId: ident.SubjectID, Instance: int64(ident.Instance)}
}

func cloudprotocolMonitoringToPB(monitoring cloudprotocol.MonitoringData) *pb.Monitoring {
	pbMonitoring := &pb.Monitoring{
		Timestamp: timestamppb.New(monitoring.Timestamp),
		SystemMonitoring: &pb.SystemMonitoring{
			Ram:        monitoring.Global.RAM,
			Cpu:        monitoring.Global.CPU,
			UsedDisk:   monitoring.Global.UsedDisk,
			InTraffic:  monitoring.Global.InTraffic,
			OutTraffic: monitoring.Global.OutTraffic,
		},
		InstanceMonitoring: make([]*pb.InstanceMonitoring, len(monitoring.ServiceInstances)),
	}

	for i := range monitoring.ServiceInstances {
		pbMonitoring.InstanceMonitoring[i] = &pb.InstanceMonitoring{
			Instance:   cloudprotocolInstanceIdentToPB(monitoring.ServiceInstances[i].InstanceIdent),
			Ram:        monitoring.ServiceInstances[i].RAM,
			Cpu:        monitoring.ServiceInstances[i].CPU,
			UsedDisk:   monitoring.ServiceInstances[i].UsedDisk,
			InTraffic:  monitoring.ServiceInstances[i].InTraffic,
			OutTraffic: monitoring.ServiceInstances[i].OutTraffic,
		}
	}

	return pbMonitoring
}
