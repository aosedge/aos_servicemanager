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
	"github.com/aoscloud/aos_common/image"
	"github.com/aoscloud/aos_common/utils/cryptutils"
	"github.com/aoscloud/aos_common/utils/pbconvert"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/golang/protobuf/ptypes/timestamp"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/launcher"
	"github.com/aoscloud/aos_servicemanager/layermanager"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
)

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var errIncorrectAlertType = errors.New("incorrect alert type")

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// ServiceManager service manager intergace.
type ServiceManager interface {
	InstallService(
		newService servicemanager.ServiceInfo, imageURL string, fileInfo image.FileInfo) error
	RemoveService(serviceID string) error
	RestoreService(serviceID string) error
	GetAllServicesStatus() ([]servicemanager.ServiceInfo, error)
}

// InstanceLauncher service instances launcher interface.
type InstanceLauncher interface {
	RunInstances(instances []cloudprotocol.InstanceInfo) error
	RestartInstances() error
	RuntimeStatusChannel() <-chan launcher.RuntimeStatus
	OverrideEnvVars(envVarsInfo []cloudprotocol.EnvVarsInstanceInfo) ([]cloudprotocol.EnvVarsInstanceStatus, error)
}

// StateHandler instance state handler interface.
type StateHandler interface {
	NewStateChannel() <-chan cloudprotocol.NewState
	StateRequestChannel() <-chan cloudprotocol.StateRequest
	UpdateState(updateState cloudprotocol.UpdateState) error
	StateAcceptance(updateState cloudprotocol.StateAcceptance) error
}

// LayerProvider services layer manager interface.
type LayerProvider interface {
	GetLayersInfo() (info []layermanager.LayerInfo, err error)
	InstallLayer(installInfo layermanager.LayerInfo, layerURL string, fileInfo image.FileInfo) error
	RemoveLayer(digest string) error
	RestoreLayer(digest string) error
}

// AlertsProvider alert data provider interface.
type AlertsProvider interface {
	GetAlertsChannel() (channel <-chan cloudprotocol.AlertItem)
}

// MonitoringDataProvider monitoring data provider interface.
type MonitoringDataProvider interface {
	GetMonitoringDataChannel() (monitoringChannel <-chan cloudprotocol.MonitoringData)
}

// BoardConfigProcessor board configuration handler.
type BoardConfigProcessor interface {
	GetBoardConfigInfo() (version string)
	CheckBoardConfig(configJSON string) (err error)
	UpdateBoardConfig(configJSON string) (err error)
}

// LogsProvider logs data provider interface.
type LogsProvider interface {
	GetInstanceLog(request cloudprotocol.RequestServiceLog) error
	GetInstanceCrashLog(request cloudprotocol.RequestServiceCrashLog) error
	GetSystemLog(request cloudprotocol.RequestSystemLog)
	GetLogsDataChannel() (channel <-chan cloudprotocol.PushLog)
}

// CertificateProvider certificate and key provider interface.
type CertificateProvider interface {
	GetCertKeyURL(keyType string) (certURL, keyURL string, err error)
}

// SMServer SM server instance.
type SMServer struct {
	url                  string
	launcher             InstanceLauncher
	serviceManager       ServiceManager
	layerProvider        LayerProvider
	stateHandler         StateHandler
	grpcServer           *grpc.Server
	listener             net.Listener
	boardConfigProcessor BoardConfigProcessor
	logsProvider         LogsProvider
	notificationStream   pb.SMService_SubscribeSMNotificationsServer
	runtimeStatusChannel <-chan launcher.RuntimeStatus
	alertChannel         <-chan cloudprotocol.AlertItem
	monitoringChannel    <-chan cloudprotocol.MonitoringData
	newStateChannel      <-chan cloudprotocol.NewState
	stateReqChannel      <-chan cloudprotocol.StateRequest
	logsChannel          <-chan cloudprotocol.PushLog
	pb.UnimplementedSMServiceServer
}

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new IAM server instance.
func New(cfg *config.Config, launcher InstanceLauncher, serviceManager ServiceManager, layerProvider LayerProvider,
	stateHandler StateHandler, alertsProvider AlertsProvider, monitoringProvider MonitoringDataProvider,
	boardConfigProcessor BoardConfigProcessor, logsProvider LogsProvider, cryptcoxontext *cryptutils.CryptoContext,
	certProvider CertificateProvider, insecure bool,
) (server *SMServer, err error) {
	server = &SMServer{
		launcher: launcher, serviceManager: serviceManager, layerProvider: layerProvider, stateHandler: stateHandler,
		boardConfigProcessor: boardConfigProcessor, logsProvider: logsProvider,
	}

	if server.launcher != nil {
		server.runtimeStatusChannel = launcher.RuntimeStatusChannel()
	}

	if alertsProvider != nil {
		server.alertChannel = alertsProvider.GetAlertsChannel()
	}

	if monitoringProvider != nil {
		server.monitoringChannel = monitoringProvider.GetMonitoringDataChannel()
	}

	if stateHandler != nil {
		server.newStateChannel = stateHandler.NewStateChannel()
		server.stateReqChannel = stateHandler.StateRequestChannel()
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

	go func() {
		if err := server.start(); err != nil {
			log.Errorf("Can't start gRPC server: %v", err)
		}
	}()

	return server, nil
}

// Close stops SM server.
func (server *SMServer) Close() {
	log.Debug("Close grpc server")

	if server.grpcServer != nil {
		server.grpcServer.Stop()
	}

	if server.listener != nil {
		server.listener.Close()
	}
}

// GetBoardConfigStatus gets current board configuration status.
func (server *SMServer) GetBoardConfigStatus(context.Context, *empty.Empty) (*pb.BoardConfigStatus, error) {
	return &pb.BoardConfigStatus{VendorVersion: server.boardConfigProcessor.GetBoardConfigInfo()}, nil
}

// CheckBoardConfig checks new board configuration.
func (server *SMServer) CheckBoardConfig(ctx context.Context, boardConfig *pb.BoardConfig) (*empty.Empty, error) {
	return &emptypb.Empty{}, aoserrors.Wrap(server.boardConfigProcessor.CheckBoardConfig(boardConfig.GetBoardConfig()))
}

// SetBoardConfig sets new board configuration.
func (server *SMServer) SetBoardConfig(ctx context.Context, boardConfig *pb.BoardConfig) (*empty.Empty, error) {
	if err := server.boardConfigProcessor.UpdateBoardConfig(boardConfig.GetBoardConfig()); err != nil {
		return &emptypb.Empty{}, aoserrors.Wrap(err)
	}

	return &emptypb.Empty{}, aoserrors.Wrap(server.launcher.RestartInstances())
}

// InstallService installs aos service.
func (server *SMServer) InstallService(ctx context.Context, service *pb.InstallServiceRequest) (*empty.Empty, error) {
	installInfo := servicemanager.ServiceInfo{
		ServiceID: service.ServiceId, AosVersion: service.AosVersion,
		ServiceProvider: service.ProviderId, Description: service.Description,
	}

	fileInfo := image.FileInfo{Sha256: service.Sha256, Sha512: service.Sha512, Size: service.Size}

	return &emptypb.Empty{}, aoserrors.Wrap(server.serviceManager.InstallService(installInfo, service.Url, fileInfo))
}

// RemoveService removes aos service.
func (server *SMServer) RemoveService(ctx context.Context, req *pb.RemoveServiceRequest) (*empty.Empty, error) {
	return &emptypb.Empty{}, aoserrors.Wrap(server.serviceManager.RemoveService(req.ServiceId))
}

// RestoreService restores Aos service.
func (server *SMServer) RestoreService(ctx context.Context, req *pb.RestoreServiceRequest) (*empty.Empty, error) {
	return &emptypb.Empty{}, aoserrors.Wrap(server.serviceManager.RestoreService(req.ServiceId))
}

// GetServicesStatus gets installed services info.
func (server *SMServer) GetServicesStatus(context.Context, *empty.Empty) (*pb.ServicesStatus, error) {
	statuses, err := server.serviceManager.GetAllServicesStatus()
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	services := &pb.ServicesStatus{Services: make([]*pb.ServiceStatus, len(statuses))}

	for i, serviceStatus := range statuses {
		services.Services[i] = &pb.ServiceStatus{
			ServiceId: serviceStatus.ServiceID, AosVersion: serviceStatus.AosVersion, Cached: serviceStatus.Cached,
		}
	}

	return services, nil
}

// InstanceStateAcceptance accepts new services instance state.
func (server *SMServer) InstanceStateAcceptance(ctx context.Context, accept *pb.StateAcceptance) (*empty.Empty, error) {
	state := cloudprotocol.StateAcceptance{
		InstanceIdent: pbconvert.NewInstanceIdentFromPB(accept.Instance),
		Checksum:      accept.StateChecksum, Result: accept.Result, Reason: accept.Reason,
	}

	return &emptypb.Empty{}, aoserrors.Wrap(server.stateHandler.StateAcceptance(state))
}

// SetInstanceState sets state for service instance.
func (server *SMServer) SetInstanceState(ctx context.Context, state *pb.InstanceState) (*empty.Empty, error) {
	newState := cloudprotocol.UpdateState{
		InstanceIdent: pbconvert.NewInstanceIdentFromPB(state.Instance),
		Checksum:      state.StateChecksum, State: string(state.State),
	}

	return &emptypb.Empty{}, aoserrors.Wrap(server.stateHandler.UpdateState(newState))
}

// RunInstances run service instances.
func (server *SMServer) RunInstances(ctx context.Context, runRequest *pb.RunInstancesRequest) (*empty.Empty, error) {
	instances := make([]cloudprotocol.InstanceInfo, len(runRequest.Instances))

	for i, pbInstance := range runRequest.Instances {
		instances[i] = cloudprotocol.InstanceInfo{
			ServiceID: pbInstance.ServiceId,
			SubjectID: pbInstance.SubjectId, NumInstances: pbInstance.NumInstances,
		}
	}

	return &emptypb.Empty{}, aoserrors.Wrap(server.launcher.RunInstances(instances))
}

// OverrideEnvVars overrides entrainment variables for the service.
func (server *SMServer) OverrideEnvVars(
	ctx context.Context, envVars *pb.OverrideEnvVarsRequest,
) (*pb.OverrideEnvVarStatus, error) {
	envVarsInfo := make([]cloudprotocol.EnvVarsInstanceInfo, len(envVars.EnvVars))

	for i, pbEnvVar := range envVars.EnvVars {
		envVarsInfo[i] = cloudprotocol.EnvVarsInstanceInfo{
			InstanceFilter: getInstanceFilterFromPB(pbEnvVar.Instance),
			EnvVars:        make([]cloudprotocol.EnvVarInfo, len(pbEnvVar.Vars)),
		}

		for j, envVar := range pbEnvVar.Vars {
			envVarsInfo[i].EnvVars[j] = cloudprotocol.EnvVarInfo{ID: envVar.VarId, Variable: envVar.Variable}

			if envVar.Ttl != nil {
				localTime := envVar.Ttl.AsTime()
				envVarsInfo[i].EnvVars[j].TTL = &localTime
			}
		}
	}

	envVarsStatuses, err := server.launcher.OverrideEnvVars(envVarsInfo)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	statuses := &pb.OverrideEnvVarStatus{EnvVarsStatus: make([]*pb.EnvVarInstanceStatus, len(envVarsStatuses))}

	for i, status := range envVarsStatuses {
		statuses.EnvVarsStatus[i] = &pb.EnvVarInstanceStatus{
			Instance:   pbconvert.InstanceFilterToPB(status.InstanceFilter),
			VarsStatus: make([]*pb.EnvVarStatus, len(status.Statuses)),
		}

		for j, varStatus := range status.Statuses {
			statuses.EnvVarsStatus[i].VarsStatus[j] = &pb.EnvVarStatus{VarId: varStatus.ID, Error: varStatus.Error}
		}
	}

	return statuses, nil
}

// InstallLayer installs the layer.
func (server *SMServer) InstallLayer(ctx context.Context, layer *pb.InstallLayerRequest) (*empty.Empty, error) {
	installInfo := layermanager.LayerInfo{
		Digest: layer.Digest, LayerID: layer.LayerId, AosVersion: layer.AosVersion,
		VendorVersion: layer.VendorVersion, Description: layer.Description,
	}

	fileInfo := image.FileInfo{Sha256: layer.Sha256, Sha512: layer.Sha512, Size: layer.Size}

	return &emptypb.Empty{}, aoserrors.Wrap(server.layerProvider.InstallLayer(installInfo, layer.Url, fileInfo))
}

// RemoveLayer removes the layer.
func (server *SMServer) RemoveLayer(ctx context.Context, layer *pb.RemoveLayerRequest) (*empty.Empty, error) {
	return &emptypb.Empty{}, aoserrors.Wrap(server.layerProvider.RemoveLayer(layer.Digest))
}

// RestoreLayer restores the layer.
func (server *SMServer) RestoreLayer(ctx context.Context, layer *pb.RestoreLayerRequest) (*empty.Empty, error) {
	return &emptypb.Empty{}, aoserrors.Wrap(server.layerProvider.RestoreLayer(layer.Digest))
}

// GetLayersStatus gets installed layer info.
func (server *SMServer) GetLayersStatus(context.Context, *empty.Empty) (*pb.LayersStatus, error) {
	info, err := server.layerProvider.GetLayersInfo()
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	layers := &pb.LayersStatus{Layers: make([]*pb.LayerStatus, len(info))}

	for i, layerInfo := range info {
		layers.Layers[i] = &pb.LayerStatus{
			LayerId: layerInfo.LayerID, AosVersion: layerInfo.AosVersion, VendorVersion: layerInfo.VendorVersion,
			Digest: layerInfo.Digest, Cached: layerInfo.Cached,
		}
	}

	return layers, nil
}

// SubscribeSMNotifications subscribes for SM notifications.
func (server *SMServer) SubscribeSMNotifications(
	req *empty.Empty, stream pb.SMService_SubscribeSMNotificationsServer,
) (err error) {
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

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (server *SMServer) start() (err error) {
	server.listener, err = net.Listen("tcp", server.url)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err := server.grpcServer.Serve(server.listener); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (server *SMServer) handleChannels() {
	for {
		select {
		case runtimeStatus := <-server.runtimeStatusChannel:
			if err := server.sendRuntimeInstanceNotifications(runtimeStatus); err != nil {
				log.Errorf("Can't send runtime instance notification: %s", err)

				return
			}

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

		case newState := <-server.newStateChannel:
			if err := server.notificationStream.Send(&pb.SMNotifications{SMNotification: &pb.SMNotifications_NewInstanceState{
				NewInstanceState: &pb.NewInstanceState{State: &pb.InstanceState{
					Instance:      pbconvert.InstanceIdentToPB(newState.InstanceIdent),
					StateChecksum: newState.Checksum,
					State:         []byte(newState.State),
				}},
			}}); err != nil {
				log.Errorf("Can't send new instance state notification :%s ", err)

				return
			}

		case stateRquest := <-server.stateReqChannel:
			if err := server.notificationStream.Send(
				&pb.SMNotifications{SMNotification: &pb.SMNotifications_InstanceStateRequest{
					InstanceStateRequest: &pb.InstanceStateRequest{
						Instance: pbconvert.InstanceIdentToPB(stateRquest.InstanceIdent),
						Default:  stateRquest.Default,
					},
				}}); err != nil {
				log.Errorf("Can't send state request notification :%s ", err)

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

func (server *SMServer) sendRuntimeInstanceNotifications(runtimeStatus launcher.RuntimeStatus) error {
	if runtimeStatus.RunStatus != nil {
		runStatusNtf := &pb.SMNotifications_RunInstancesStatus{
			RunInstancesStatus: runInstanceStatusToPB(runtimeStatus.RunStatus),
		}

		if err := server.notificationStream.Send(&pb.SMNotifications{SMNotification: runStatusNtf}); err != nil {
			return aoserrors.Errorf("Can't send runtime status notification: %s ", err)
		}
	}

	if runtimeStatus.UpdateStatus != nil {
		updateStatusNtf := &pb.SMNotifications_UpdateInstancesStatus{
			UpdateInstancesStatus: updateInstanceStatusToPB(runtimeStatus.UpdateStatus),
		}

		if err := server.notificationStream.Send(&pb.SMNotifications{SMNotification: updateStatusNtf}); err != nil {
			return aoserrors.Errorf("Can't send update status notification: %s ", err)
		}
	}

	return nil
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
		Instance: pbconvert.InstanceIdentToPB(devAlert.InstanceIdent), Message: devAlert.Message,
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
		Instance:  pbconvert.InstanceIdentToPB(instQuotaAlert.InstanceIdent),
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
		Instance:   pbconvert.InstanceIdentToPB(instAlert.InstanceIdent),
		AosVersion: instAlert.AosVersion,
		Message:    instAlert.Message,
	}}, nil
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
			Instance:   pbconvert.InstanceIdentToPB(monitoring.ServiceInstances[i].InstanceIdent),
			Ram:        monitoring.ServiceInstances[i].RAM,
			Cpu:        monitoring.ServiceInstances[i].CPU,
			UsedDisk:   monitoring.ServiceInstances[i].UsedDisk,
			InTraffic:  monitoring.ServiceInstances[i].InTraffic,
			OutTraffic: monitoring.ServiceInstances[i].OutTraffic,
		}
	}

	return pbMonitoring
}

func runInstanceStatusToPB(runStatus *launcher.RunInstancesStatus) *pb.RunInstancesStatus {
	pbStatus := &pb.RunInstancesStatus{
		UnitSubjects:  runStatus.UnitSubjects,
		Instances:     make([]*pb.InstanceStatus, len(runStatus.Instances)),
		ErrorServices: make([]*pb.ServiceError, len(runStatus.ErrorServices)),
	}

	for i, instance := range runStatus.Instances {
		pbStatus.Instances[i] = cloudprotocolInstanceStatusToPB(instance)
	}

	for i, errService := range runStatus.ErrorServices {
		pbStatus.ErrorServices[i] = &pb.ServiceError{
			ServiceId: errService.ID, AosVersion: errService.AosVersion,
			ErrorInfo: cloudprotocolErrorInfoToPB(errService.ErrorInfo),
		}
	}

	return pbStatus
}

func updateInstanceStatusToPB(updateStatus *launcher.UpdateInstancesStatus) *pb.UpdateInstancesStatus {
	pbStatus := &pb.UpdateInstancesStatus{Instances: make([]*pb.InstanceStatus, len(updateStatus.Instances))}

	for i, instance := range updateStatus.Instances {
		pbStatus.Instances[i] = cloudprotocolInstanceStatusToPB(instance)
	}

	return pbStatus
}

func cloudprotocolInstanceStatusToPB(instance cloudprotocol.InstanceStatus) *pb.InstanceStatus {
	return &pb.InstanceStatus{
		Instance:   pbconvert.InstanceIdentToPB(instance.InstanceIdent),
		AosVersion: instance.AosVersion, StateChecksum: instance.StateChecksum,
		RunState: instance.RunState, ErrorInfo: cloudprotocolErrorInfoToPB(instance.ErrorInfo),
	}
}

func cloudprotocolErrorInfoToPB(errorInfo *cloudprotocol.ErrorInfo) *pb.ErrorInfo {
	if errorInfo == nil {
		return nil
	}

	return &pb.ErrorInfo{
		AosCode:  int32(errorInfo.AosCode),
		ExitCode: int32(errorInfo.ExitCode), Message: errorInfo.Message,
	}
}
