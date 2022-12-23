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

package smclient

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	pb "github.com/aoscloud/aos_common/api/servicemanager/v3"
	"github.com/aoscloud/aos_common/utils/cryptutils"
	"github.com/aoscloud/aos_common/utils/pbconvert"
	"github.com/golang/protobuf/ptypes/timestamp"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/launcher"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	cmRequestTimeout   = 30 * time.Second
	cmReconnectTimeout = 10 * time.Second
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// SMClient SM client instance.
type SMClient struct {
	sync.Mutex
	config               *config.Config
	connection           *grpc.ClientConn
	stream               pb.SMService_RegisterSMClient
	closeChannel         chan struct{}
	servicesProcessor    ServicesProcessor
	layersProcessor      LayersProcessor
	launcher             InstanceLauncher
	unitConfigProcessor  UnitConfigProcessor
	monitoringProvider   MonitoringDataProvider
	logsProvider         LogsProvider
	runtimeStatusChannel <-chan launcher.RuntimeStatus
	alertChannel         <-chan cloudprotocol.AlertItem
	monitoringChannel    <-chan cloudprotocol.NodeMonitoringData
	logsChannel          <-chan cloudprotocol.PushLog
	nodeID               string
	nodeType             string
	systemInfo           cloudprotocol.SystemInfo
}

// CertificateProvider interface to get certificate.
type CertificateProvider interface {
	GetCertificate(certType string, cryptocontext *cryptutils.CryptoContext) (certURL, ketURL string, err error)
}

// UnitConfigProcessor unit configuration handler.
type UnitConfigProcessor interface {
	GetUnitConfigInfo() (version string)
	CheckUnitConfig(configJSON, version string) error
	UpdateUnitConfig(configJSON, version string) error
}

// ServicesProcessor process desired services list.
type ServicesProcessor interface {
	ProcessDesiredServices(services []aostypes.ServiceInfo) error
}

// LayersProcessor process desired layer list.
type LayersProcessor interface {
	ProcessDesiredLayers(layers []aostypes.LayerInfo) error
}

// InstanceLauncher service instances launcher interface.
type InstanceLauncher interface {
	RunInstances(instances []aostypes.InstanceInfo, forceRestart bool)
	RuntimeStatusChannel() <-chan launcher.RuntimeStatus
	OverrideEnvVars(envVarsInfo []cloudprotocol.EnvVarsInstanceInfo) ([]cloudprotocol.EnvVarsInstanceStatus, error)
}

// AlertsProvider alert data provider interface.
type AlertsProvider interface {
	GetAlertsChannel() (channel <-chan cloudprotocol.AlertItem)
}

// MonitoringDataProvider monitoring data provider interface.
type MonitoringDataProvider interface {
	GetMonitoringDataChannel() (monitoringChannel <-chan cloudprotocol.NodeMonitoringData)
	GetNodeMonitoringData() cloudprotocol.NodeMonitoringData
	GetSystemInfo() cloudprotocol.SystemInfo
}

// LogsProvider logs data provider interface.
type LogsProvider interface {
	GetInstanceLog(request cloudprotocol.RequestServiceLog)
	GetInstanceCrashLog(request cloudprotocol.RequestServiceCrashLog)
	GetSystemLog(request cloudprotocol.RequestSystemLog)
	GetLogsDataChannel() (channel <-chan cloudprotocol.PushLog)
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var errIncorrectAlertType = errors.New("incorrect alert type")

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates SM client fo communication with CM.
func New(config *config.Config, nodeID, nodeType string, provider CertificateProvider,
	servicesProcessor ServicesProcessor, layersProcessor LayersProcessor, launcher InstanceLauncher,
	unitConfigProcessor UnitConfigProcessor, alertsProvider AlertsProvider, monitoringProvider MonitoringDataProvider,
	logsProvider LogsProvider, cryptcoxontext *cryptutils.CryptoContext, insecure bool,
) (*SMClient, error) {
	cmClient := &SMClient{
		closeChannel: make(chan struct{}, 1), servicesProcessor: servicesProcessor, layersProcessor: layersProcessor,
		launcher: launcher, unitConfigProcessor: unitConfigProcessor, monitoringProvider: monitoringProvider,
		logsProvider: logsProvider, nodeID: nodeID, nodeType: nodeType, config: config,
	}

	if err := cmClient.createConnection(config, provider, cryptcoxontext, insecure); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if cmClient.launcher != nil {
		cmClient.runtimeStatusChannel = launcher.RuntimeStatusChannel()
	}

	if alertsProvider != nil {
		cmClient.alertChannel = alertsProvider.GetAlertsChannel()
	}

	if monitoringProvider != nil {
		cmClient.monitoringChannel = monitoringProvider.GetMonitoringDataChannel()
		cmClient.systemInfo = monitoringProvider.GetSystemInfo()
	}

	if logsProvider != nil {
		cmClient.logsChannel = logsProvider.GetLogsDataChannel()
	}

	return cmClient, nil
}

// Close closes SM client.
func (client *SMClient) Close() (err error) {
	log.Debug("Close SM client")

	if client.stream != nil {
		err = client.stream.CloseSend()
	}

	if client.connection != nil {
		errCloseconn := client.connection.Close()

		if err != nil {
			err = errCloseconn
		}
	}

	close(client.closeChannel)

	return aoserrors.Wrap(err)
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (client *SMClient) createConnection(
	config *config.Config, provider CertificateProvider,
	cryptcoxontext *cryptutils.CryptoContext, insecureConn bool,
) (err error) {
	log.Debug("Connecting to CM...")

	var secureOpt grpc.DialOption

	if insecureConn {
		secureOpt = grpc.WithTransportCredentials(insecure.NewCredentials())
	} else {
		certURL, keyURL, err := provider.GetCertificate(config.CertStorage, cryptcoxontext)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		tlsConfig, err := cryptcoxontext.GetClientMutualTLSConfig(certURL, keyURL)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		secureOpt = grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	}

	ctx, cancel := context.WithTimeout(context.Background(), cmRequestTimeout)
	defer cancel()

	if client.connection, err = grpc.DialContext(ctx, config.CMServerURL, secureOpt, grpc.WithBlock()); err != nil {
		return aoserrors.Wrap(err)
	}

	log.Debug("Connected to CM")

	go func() {
		err := client.register(config)

		for {
			if err != nil && len(client.closeChannel) == 0 {
				log.Errorf("Error register to CM: %v", aoserrors.Wrap(err))
			} else {
				if err = client.processMessages(); err != nil {
					if errors.Is(err, io.EOF) {
						log.Debug("Connection is closed")
					} else {
						log.Errorf("Connection error: %v", aoserrors.Wrap(err))
					}
				}
			}

			log.Debugf("Reconnect to CM in %v...", cmReconnectTimeout)

			select {
			case <-client.closeChannel:
				log.Debugf("Disconnected from CM")

				return

			case <-time.After(cmReconnectTimeout):
				err = client.register(config)
			}
		}
	}()

	return nil
}

func (client *SMClient) register(config *config.Config) (err error) {
	client.Lock()
	defer client.Unlock()

	log.Debug("Registering to CM...")

	if client.stream, err = pb.NewSMServiceClient(client.connection).RegisterSM(context.Background()); err != nil {
		return aoserrors.Wrap(err)
	}

	nodeCfg := pb.NodeConfiguration{
		NodeId: client.nodeID, NodeType: client.nodeType, RemoteNode: config.RemoteNode,
		RunnerFeatures: config.RunnerFeatures,
		NumCpus:        client.systemInfo.NumCPUs, TotalRam: client.systemInfo.TotalRAM,
		Partitions: make([]*pb.Partition, len(client.systemInfo.Partitions)),
	}

	for i, partition := range client.systemInfo.Partitions {
		nodeCfg.Partitions[i] = &pb.Partition{
			Name: partition.Name, Type: partition.Type, TotalSize: partition.TotalSize,
		}
	}

	if err := client.stream.Send(
		&pb.SMOutgoingMessages{
			SMOutgoingMessage: &pb.SMOutgoingMessages_NodeConfiguration{NodeConfiguration: &nodeCfg},
		}); err != nil {
		return aoserrors.Wrap(err)
	}

	log.Debug("Registered to CM")

	go client.handleChannels()

	return nil
}

func (client *SMClient) processMessages() (err error) {
	for {
		message, err := client.stream.Recv()
		if err != nil {
			if code, ok := status.FromError(err); ok {
				if code.Code() == codes.Canceled {
					log.Debug("SM client connection closed")
					return nil
				}
			}

			return aoserrors.Wrap(err)
		}

		switch data := message.SMIncomingMessage.(type) {
		case *pb.SMIncomingMessages_GetUnitConfigStatus:
			client.processGetUnitConfigStatus()

		case *pb.SMIncomingMessages_CheckUnitConfig:
			client.processCheckUnitConfig(data.CheckUnitConfig)

		case *pb.SMIncomingMessages_SetUnitConfig:
			client.processSetUnitConfig(data.SetUnitConfig)

		case *pb.SMIncomingMessages_RunInstances:
			client.processRunInstances(data.RunInstances)

		case *pb.SMIncomingMessages_SystemLogRequest:
			client.processGetSystemLogRequest(data.SystemLogRequest)

		case *pb.SMIncomingMessages_InstanceLogRequest:
			client.processGetInstanceLogRequest(data.InstanceLogRequest)

		case *pb.SMIncomingMessages_InstanceCrashLogRequest:
			client.processGetInstanceCrashLogRequest(data.InstanceCrashLogRequest)

		case *pb.SMIncomingMessages_OverrideEnvVars:
			client.processOverrideEnvVars(data.OverrideEnvVars)

		case *pb.SMIncomingMessages_GetNodeMonitoring:
			client.processNodeMonitoringData()
		}
	}
}

func (client *SMClient) processGetUnitConfigStatus() {
	status := &pb.UnitConfigStatus{VendorVersion: client.unitConfigProcessor.GetUnitConfigInfo()}

	if err := client.stream.Send(&pb.SMOutgoingMessages{
		SMOutgoingMessage: &pb.SMOutgoingMessages_UnitConfigStatus{UnitConfigStatus: status},
	}); err != nil {
		log.Errorf("Can't send unit config status: %v", err)
	}
}

func (client *SMClient) processCheckUnitConfig(check *pb.CheckUnitConfig) {
	status := &pb.UnitConfigStatus{}

	if err := client.unitConfigProcessor.CheckUnitConfig(check.UnitConfig, check.VendorVersion); err != nil {
		status.Error = err.Error()
	}

	if err := client.stream.Send(&pb.SMOutgoingMessages{
		SMOutgoingMessage: &pb.SMOutgoingMessages_UnitConfigStatus{UnitConfigStatus: status},
	}); err != nil {
		log.Errorf("Can't send unit config status: %v", err)
	}
}

func (client *SMClient) processSetUnitConfig(config *pb.SetUnitConfig) {
	status := &pb.UnitConfigStatus{}

	if err := client.unitConfigProcessor.UpdateUnitConfig(config.UnitConfig, config.VendorVersion); err != nil {
		status.Error = err.Error()
	}

	if err := client.stream.Send(&pb.SMOutgoingMessages{
		SMOutgoingMessage: &pb.SMOutgoingMessages_UnitConfigStatus{UnitConfigStatus: status},
	}); err != nil {
		log.Errorf("Can't send unit config status: %v", err)
	}
}

func (client *SMClient) processRunInstances(runInstances *pb.RunInstances) {
	services := make([]aostypes.ServiceInfo, len(runInstances.Services))

	for i, pbService := range runInstances.Services {
		services[i] = aostypes.ServiceInfo{
			VersionInfo: aostypes.VersionInfo{
				AosVersion:    pbService.VersionInfo.AosVersion,
				VendorVersion: pbService.VersionInfo.VendorVersion, Description: pbService.VersionInfo.Description,
			},
			ID: pbService.ServiceId, ProviderID: pbService.ProviderId, URL: pbService.Url, GID: pbService.Gid,
			Sha256: pbService.Sha256, Sha512: pbService.Sha512, Size: pbService.Size,
		}
	}

	if err := client.servicesProcessor.ProcessDesiredServices(services); err != nil {
		log.Errorf("Can't process desired services list %v", err)
	}

	layers := make([]aostypes.LayerInfo, len(runInstances.Layers))

	for i, pbLayer := range runInstances.Layers {
		layers[i] = aostypes.LayerInfo{
			VersionInfo: aostypes.VersionInfo{
				AosVersion:    pbLayer.VersionInfo.AosVersion,
				VendorVersion: pbLayer.VersionInfo.VendorVersion, Description: pbLayer.VersionInfo.Description,
			},
			Digest: pbLayer.Digest, URL: pbLayer.Url, Sha256: pbLayer.Sha256, Sha512: pbLayer.Sha512,
			Size: pbLayer.Size,
		}
	}

	if err := client.layersProcessor.ProcessDesiredLayers(layers); err != nil {
		log.Errorf("Can't process desired layer list %v", err)
	}

	instances := make([]aostypes.InstanceInfo, len(runInstances.Instances))

	for i, pbInstance := range runInstances.Instances {
		instances[i] = aostypes.InstanceInfo{
			InstanceIdent: pbconvert.NewInstanceIdentFromPB(pbInstance.Instance),
			UID:           pbInstance.Uid, Priority: pbInstance.Priority,
			StoragePath: pbInstance.StoragePath, StatePath: pbInstance.StatePath,
		}
	}

	client.launcher.RunInstances(instances, runInstances.ForceRestart)
}

func (client *SMClient) processGetSystemLogRequest(logRequest *pb.SystemLogRequest) {
	getSystemLogRequest := cloudprotocol.RequestSystemLog{LogID: logRequest.LogId}

	getSystemLogRequest.From, getSystemLogRequest.Till = getFromTillTimeFromPB(
		logRequest.From, logRequest.Till)

	client.logsProvider.GetSystemLog(getSystemLogRequest)
}

func (client *SMClient) processGetInstanceLogRequest(instanceLogRequest *pb.InstanceLogRequest) {
	getInstanceLogRequest := cloudprotocol.RequestServiceLog{LogID: instanceLogRequest.LogId}

	getInstanceLogRequest.From, getInstanceLogRequest.Till = getFromTillTimeFromPB(
		instanceLogRequest.From, instanceLogRequest.Till)
	getInstanceLogRequest.InstanceFilter = getInstanceFilterFromPB(instanceLogRequest.Instance)

	client.logsProvider.GetInstanceLog(getInstanceLogRequest)
}

func (client *SMClient) processGetInstanceCrashLogRequest(logrequest *pb.InstanceCrashLogRequest) {
	getInstanceCrashLogRequest := cloudprotocol.RequestServiceCrashLog{LogID: logrequest.LogId}

	getInstanceCrashLogRequest.From, getInstanceCrashLogRequest.Till = getFromTillTimeFromPB(
		logrequest.From, logrequest.Till)
	getInstanceCrashLogRequest.InstanceFilter = getInstanceFilterFromPB(logrequest.Instance)

	client.logsProvider.GetInstanceCrashLog(getInstanceCrashLogRequest)
}

func (client *SMClient) processOverrideEnvVars(envVars *pb.OverrideEnvVars) {
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

	statuses := &pb.OverrideEnvVarStatus{}

	envVarsStatuses, err := client.launcher.OverrideEnvVars(envVarsInfo)
	if err != nil {
		statuses.Error = err.Error()
	} else {
		statuses.EnvVarsStatus = make([]*pb.EnvVarInstanceStatus, len(envVarsStatuses))

		for i, status := range envVarsStatuses {
			statuses.EnvVarsStatus[i] = &pb.EnvVarInstanceStatus{
				Instance:   pbconvert.InstanceFilterToPB(status.InstanceFilter),
				VarsStatus: make([]*pb.EnvVarStatus, len(status.Statuses)),
			}

			for j, varStatus := range status.Statuses {
				statuses.EnvVarsStatus[i].VarsStatus[j] = &pb.EnvVarStatus{VarId: varStatus.ID, Error: varStatus.Error}
			}
		}
	}

	if err := client.stream.Send(&pb.SMOutgoingMessages{
		SMOutgoingMessage: &pb.SMOutgoingMessages_OverrideEnvVarStatus{OverrideEnvVarStatus: statuses},
	}); err != nil {
		log.Errorf("Can't send roverride env vars status: %v", err)
	}
}

func (client *SMClient) processNodeMonitoringData() {
	if err := client.stream.Send(
		&pb.SMOutgoingMessages{
			SMOutgoingMessage: &pb.SMOutgoingMessages_NodeMonitoring{
				NodeMonitoring: cloudprotocolMonitoringToPB(client.monitoringProvider.GetNodeMonitoringData()),
			},
		}); err != nil {
		log.Errorf("Can't send monitoring notification: %v ", err)
	}
}

func (client *SMClient) handleChannels() {
	for {
		select {
		case runtimeStatus := <-client.runtimeStatusChannel:
			if err := client.sendRuntimeInstanceNotifications(runtimeStatus); err != nil {
				log.Errorf("Can't send runtime instance notification: %v", err)

				return
			}

		case alert := <-client.alertChannel:
			pbAlert, err := cloudprotocolAlertToPB(&alert)
			if err != nil {
				log.Errorf("Can't convert alert to pb: %v", err)

				continue
			}

			alertNtf := &pb.SMOutgoingMessages_Alert{Alert: pbAlert}

			if err := client.stream.Send(&pb.SMOutgoingMessages{SMOutgoingMessage: alertNtf}); err != nil {
				log.Errorf("Can't send alert: %v", err)

				return
			}

		case monitoringData := <-client.monitoringChannel:
			if err := client.stream.Send(
				&pb.SMOutgoingMessages{
					SMOutgoingMessage: &pb.SMOutgoingMessages_NodeMonitoring{
						NodeMonitoring: cloudprotocolMonitoringToPB(monitoringData),
					},
				}); err != nil {
				log.Errorf("Can't send monitoring notification: %v", err)

				return
			}

		case logs := <-client.logsChannel:
			if err := client.stream.Send(
				&pb.SMOutgoingMessages{
					SMOutgoingMessage: &pb.SMOutgoingMessages_Log{Log: cloudprotocolLogToPB(logs)},
				}); err != nil {
				log.Errorf("Can't send logs: %v", err)

				return
			}

		case <-client.stream.Context().Done():
			return
		}
	}
}

func (client *SMClient) sendRuntimeInstanceNotifications(runtimeStatus launcher.RuntimeStatus) error {
	if runtimeStatus.RunStatus != nil {
		runStatusNtf := &pb.SMOutgoingMessages_RunInstancesStatus{
			RunInstancesStatus: runInstanceStatusToPB(runtimeStatus.RunStatus),
		}

		if err := client.stream.Send(&pb.SMOutgoingMessages{SMOutgoingMessage: runStatusNtf}); err != nil {
			return aoserrors.Errorf("Can't send runtime status notification: %v", err)
		}
	}

	if runtimeStatus.UpdateStatus != nil {
		updateStatusNtf := &pb.SMOutgoingMessages_UpdateInstancesStatus{
			UpdateInstancesStatus: updateInstanceStatusToPB(runtimeStatus.UpdateStatus),
		}

		if err := client.stream.Send(&pb.SMOutgoingMessages{SMOutgoingMessage: updateStatusNtf}); err != nil {
			return aoserrors.Errorf("Can't send update status notification: %v", err)
		}
	}

	return nil
}

func runInstanceStatusToPB(runStatus *launcher.InstancesStatus) *pb.RunInstancesStatus {
	pbStatus := &pb.RunInstancesStatus{Instances: make([]*pb.InstanceStatus, len(runStatus.Instances))}

	for i, instance := range runStatus.Instances {
		pbStatus.Instances[i] = cloudprotocolInstanceStatusToPB(instance)
	}

	return pbStatus
}

func updateInstanceStatusToPB(updateStatus *launcher.InstancesStatus) *pb.UpdateInstancesStatus {
	pbStatus := &pb.UpdateInstancesStatus{Instances: make([]*pb.InstanceStatus, len(updateStatus.Instances))}

	for i, instance := range updateStatus.Instances {
		pbStatus.Instances[i] = cloudprotocolInstanceStatusToPB(instance)
	}

	return pbStatus
}

func cloudprotocolInstanceStatusToPB(instance cloudprotocol.InstanceStatus) *pb.InstanceStatus {
	return &pb.InstanceStatus{
		Instance: pbconvert.InstanceIdentToPB(instance.InstanceIdent), AosVersion: instance.AosVersion,
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

func cloudprotocolMonitoringToPB(monitoring cloudprotocol.NodeMonitoringData) *pb.NodeMonitoring {
	pbMonitoring := &pb.NodeMonitoring{
		Timestamp: timestamppb.New(monitoring.Timestamp),
		MonitoringData: &pb.MonitoringData{
			Ram:        monitoring.RAM,
			Cpu:        monitoring.CPU,
			InTraffic:  monitoring.InTraffic,
			OutTraffic: monitoring.OutTraffic,
			Disk:       make([]*pb.PartitionUsage, len(monitoring.Disk)),
		},
		InstanceMonitoring: make([]*pb.InstanceMonitoring, len(monitoring.ServiceInstances)),
	}

	for i, disk := range monitoring.Disk {
		pbMonitoring.MonitoringData.Disk[i] = &pb.PartitionUsage{Name: disk.Name, UsedSize: disk.UsedSize}
	}

	for i, serviceMonitoring := range monitoring.ServiceInstances {
		pbMonitoring.InstanceMonitoring[i] = &pb.InstanceMonitoring{
			Instance: pbconvert.InstanceIdentToPB(monitoring.ServiceInstances[i].InstanceIdent),
			MonitoringData: &pb.MonitoringData{
				Ram:        serviceMonitoring.RAM,
				Cpu:        serviceMonitoring.CPU,
				InTraffic:  serviceMonitoring.InTraffic,
				OutTraffic: serviceMonitoring.OutTraffic,
				Disk:       make([]*pb.PartitionUsage, len(serviceMonitoring.Disk)),
			},
		}

		for j, serviceDisk := range serviceMonitoring.Disk {
			pbMonitoring.InstanceMonitoring[i].MonitoringData.Disk[j] = &pb.PartitionUsage{
				Name: serviceDisk.Name, UsedSize: serviceDisk.UsedSize,
			}
		}
	}

	return pbMonitoring
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

func cloudprotocolLogToPB(log cloudprotocol.PushLog) (pbLog *pb.LogData) {
	return &pb.LogData{LogId: log.LogID, PartCount: log.PartCount, Part: log.Part, Error: log.Error, Data: log.Data}
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
