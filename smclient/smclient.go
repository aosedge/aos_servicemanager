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

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/aostypes"
	"github.com/aosedge/aos_common/api/cloudprotocol"
	"github.com/aosedge/aos_common/api/iamanager"
	pb "github.com/aosedge/aos_common/api/servicemanager"
	"github.com/aosedge/aos_common/utils/cryptutils"
	"github.com/aosedge/aos_common/utils/grpchelpers"
	"github.com/aosedge/aos_common/utils/pbconvert"
	"github.com/golang/protobuf/ptypes/timestamp"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aosedge/aos_servicemanager/config"
	"github.com/aosedge/aos_servicemanager/launcher"
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
	nodeID               string
	nodeType             string
	stream               pb.SMService_RegisterSMClient
	closeChannel         chan struct{}
	servicesProcessor    ServicesProcessor
	layersProcessor      LayersProcessor
	launcher             InstanceLauncher
	nodeConfigProcessor  NodeConfigProcessor
	monitoringProvider   MonitoringDataProvider
	logsProvider         LogsProvider
	networkManager       NetworkProvider
	runtimeStatusChannel <-chan launcher.RuntimeStatus
	alertChannel         <-chan interface{}
	monitoringChannel    <-chan aostypes.NodeMonitoring
	logsChannel          <-chan cloudprotocol.PushLog
	runStatus            *launcher.InstancesStatus
}

// NodeInfoProvider interface to get node information.
type NodeInfoProvider interface {
	GetCurrentNodeInfo() (cloudprotocol.NodeInfo, error)
}

// CertificateProvider interface to get certificate.
type CertificateProvider interface {
	GetCertificate(certType string, issuer []byte, serial string) (certURL, ketURL string, err error)
	SubscribeCertChanged(certType string) (<-chan *iamanager.CertInfo, error)
}

// NodeConfigProcessor node configuration handler.
type NodeConfigProcessor interface {
	GetNodeConfigStatus() (version string, err error)
	CheckNodeConfig(configJSON, version string) error
	UpdateNodeConfig(configJSON, version string) error
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
	RunInstances(instances []aostypes.InstanceInfo, forceRestart bool) error
	RuntimeStatusChannel() <-chan launcher.RuntimeStatus
	OverrideEnvVars(envVarsInfo []cloudprotocol.EnvVarsInstanceInfo) ([]cloudprotocol.EnvVarsInstanceStatus, error)
	CloudConnection(connected bool) error
}

// AlertsProvider alert data provider interface.
type AlertsProvider interface {
	GetAlertsChannel() (channel <-chan interface{})
}

// NetworkProvider network provider interface.
type NetworkProvider interface {
	UpdateNetworks(networkParameters []aostypes.NetworkParameters) error
}

// MonitoringDataProvider monitoring data provider interface.
type MonitoringDataProvider interface {
	GetAverageMonitoring() (aostypes.NodeMonitoring, error)
	GetNodeMonitoringChannel() <-chan aostypes.NodeMonitoring
}

// LogsProvider logs data provider interface.
type LogsProvider interface {
	GetInstanceLog(request cloudprotocol.RequestLog) error
	GetInstanceCrashLog(request cloudprotocol.RequestLog) error
	GetSystemLog(request cloudprotocol.RequestLog)
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
func New(config *config.Config, nodeInfoProvider NodeInfoProvider, certificateProvider CertificateProvider,
	servicesProcessor ServicesProcessor, layersProcessor LayersProcessor, launcher InstanceLauncher,
	nodeConfigProcessor NodeConfigProcessor, alertsProvider AlertsProvider, monitoringProvider MonitoringDataProvider,
	logsProvider LogsProvider, networkManager NetworkProvider, cryptcoxontext *cryptutils.CryptoContext, insecure bool,
) (*SMClient, error) {
	cmClient := &SMClient{
		config: config, servicesProcessor: servicesProcessor,
		layersProcessor: layersProcessor, launcher: launcher, nodeConfigProcessor: nodeConfigProcessor,
		monitoringProvider: monitoringProvider, logsProvider: logsProvider, networkManager: networkManager,
		closeChannel: make(chan struct{}, 1),
	}

	nodeInfo, err := nodeInfoProvider.GetCurrentNodeInfo()
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	cmClient.nodeID = nodeInfo.NodeID
	cmClient.nodeType = nodeInfo.NodeType

	if err := cmClient.createConnection(config, certificateProvider, cryptcoxontext, insecure); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if !insecure {
		var ch <-chan *iamanager.CertInfo

		if ch, err = certificateProvider.SubscribeCertChanged(config.CertStorage); err != nil {
			return nil, aoserrors.Wrap(err)
		}

		go cmClient.processTLSCertChanged(ch)
	}

	if cmClient.launcher != nil {
		cmClient.runtimeStatusChannel = launcher.RuntimeStatusChannel()
	}

	if alertsProvider != nil {
		cmClient.alertChannel = alertsProvider.GetAlertsChannel()
	}

	if monitoringProvider != nil {
		cmClient.monitoringChannel = monitoringProvider.GetNodeMonitoringChannel()
	}

	if logsProvider != nil {
		cmClient.logsChannel = logsProvider.GetLogsDataChannel()
	}

	return cmClient, nil
}

// Close closes SM client.
func (client *SMClient) Close() (err error) {
	log.Debug("Close SM client")

	close(client.closeChannel)

	client.closeGRPCConnection()

	return nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (client *SMClient) createConnection(
	config *config.Config, provider CertificateProvider,
	cryptcoxontext *cryptutils.CryptoContext, insecureConn bool,
) (err error) {
	if err := client.register(config, provider, cryptcoxontext, insecureConn); err != nil {
		return err
	}

	go func() {
		for {
			if err = client.processMessages(); err != nil {
				if errors.Is(err, io.EOF) {
					log.Debug("Connection is closed")
				} else {
					log.Errorf("Connection error: %v", aoserrors.Wrap(err))
				}
			}

			log.Debugf("Reconnect to CM in %v...", cmReconnectTimeout)

			client.closeGRPCConnection()

		reconnectionLoop:
			for {
				select {
				case <-client.closeChannel:
					log.Debug("Disconnected from CM")

					return

				case <-time.After(cmReconnectTimeout):
					if err := client.register(config, provider, cryptcoxontext, insecureConn); err != nil {
						log.WithField("err", err).Debug("Reconnection failed")
					} else {
						break reconnectionLoop
					}
				}
			}
		}
	}()

	return nil
}

func (client *SMClient) processTLSCertChanged(certChannel <-chan *iamanager.CertInfo) {
	for {
		select {
		case <-certChannel:
			log.Debug("TLS certificate changed")

			client.closeGRPCConnection()

		case <-client.closeChannel:
			return
		}
	}
}

func (client *SMClient) openGRPCConnection(config *config.Config, provider CertificateProvider,
	cryptcoxontext *cryptutils.CryptoContext, insecureConn bool,
) (err error) {
	log.Debug("Connecting to CM...")

	if client.connection, err = grpchelpers.CreateProtectedConnection(
		config.CertStorage, config.CMServerURL, cmRequestTimeout, cryptcoxontext, provider, insecureConn); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (client *SMClient) closeGRPCConnection() {
	log.Debug("Closing CM connection...")

	if client.stream != nil {
		if err := client.stream.CloseSend(); err != nil {
			log.WithField("err", err).Error("SM client failed send close")
		}
	}

	if client.connection != nil {
		client.connection.Close()
		client.connection = nil
	}
}

func (client *SMClient) register(config *config.Config, provider CertificateProvider,
	cryptcoxontext *cryptutils.CryptoContext, insecureConn bool,
) (err error) {
	client.Lock()
	defer client.Unlock()

	if err := client.openGRPCConnection(config, provider, cryptcoxontext, insecureConn); err != nil {
		return err
	}

	log.Debug("Registering to CM...")

	if client.stream, err = pb.NewSMServiceClient(client.connection).RegisterSM(context.Background()); err != nil {
		return aoserrors.Wrap(err)
	}

	var configErrorInfo *cloudprotocol.ErrorInfo

	configVersion, configError := client.nodeConfigProcessor.GetNodeConfigStatus()
	if configError != nil {
		configErrorInfo = &cloudprotocol.ErrorInfo{Message: configError.Error()}
	}

	if err := client.stream.Send(
		&pb.SMOutgoingMessages{
			SMOutgoingMessage: &pb.SMOutgoingMessages_NodeConfigStatus{NodeConfigStatus: &pb.NodeConfigStatus{
				NodeId: client.nodeID, NodeType: client.nodeType, Version: configVersion,
				Error: pbconvert.ErrorInfoToPB(configErrorInfo),
			}},
		}); err != nil {
		return aoserrors.Wrap(err)
	}

	if client.runStatus != nil {
		if err := client.sendRuntimeInstanceNotifications(launcher.RuntimeStatus{
			RunStatus: client.runStatus,
		}); err != nil {
			return err
		}
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

		switch data := message.GetSMIncomingMessage().(type) {
		case *pb.SMIncomingMessages_GetNodeConfigStatus:
			client.processGetNodeConfigStatus()

		case *pb.SMIncomingMessages_CheckNodeConfig:
			client.processCheckNodeConfig(data.CheckNodeConfig)

		case *pb.SMIncomingMessages_SetNodeConfig:
			client.processSetNodeConfig(data.SetNodeConfig)

		case *pb.SMIncomingMessages_RunInstances:
			client.processRunInstances(data.RunInstances)

		case *pb.SMIncomingMessages_UpdateNetworks:
			client.processUpdateNetworks(data.UpdateNetworks)

		case *pb.SMIncomingMessages_SystemLogRequest:
			client.processGetSystemLogRequest(data.SystemLogRequest)

		case *pb.SMIncomingMessages_InstanceLogRequest:
			client.processGetInstanceLogRequest(data.InstanceLogRequest)

		case *pb.SMIncomingMessages_InstanceCrashLogRequest:
			client.processGetInstanceCrashLogRequest(data.InstanceCrashLogRequest)

		case *pb.SMIncomingMessages_OverrideEnvVars:
			client.processOverrideEnvVars(data.OverrideEnvVars)

		case *pb.SMIncomingMessages_GetAverageMonitoring:
			client.processAverageMonitoringData()

		case *pb.SMIncomingMessages_ConnectionStatus:
			client.processConnectionStatus(data.ConnectionStatus)
		}
	}
}

func (client *SMClient) processGetNodeConfigStatus() {
	version, err := client.nodeConfigProcessor.GetNodeConfigStatus()

	status := &pb.NodeConfigStatus{Version: version, NodeId: client.nodeID, NodeType: client.nodeType}

	if err != nil {
		status.Error = pbconvert.ErrorInfoToPB(&cloudprotocol.ErrorInfo{Message: err.Error()})
	}

	if err := client.stream.Send(&pb.SMOutgoingMessages{
		SMOutgoingMessage: &pb.SMOutgoingMessages_NodeConfigStatus{NodeConfigStatus: status},
	}); err != nil {
		log.Errorf("Can't send node config status: %v", err)
	}
}

func (client *SMClient) processCheckNodeConfig(check *pb.CheckNodeConfig) {
	status := &pb.NodeConfigStatus{}

	if err := client.nodeConfigProcessor.CheckNodeConfig(check.GetNodeConfig(), check.GetVersion()); err != nil {
		status.Error = pbconvert.ErrorInfoToPB(&cloudprotocol.ErrorInfo{Message: err.Error()})
	}

	if err := client.stream.Send(&pb.SMOutgoingMessages{
		SMOutgoingMessage: &pb.SMOutgoingMessages_NodeConfigStatus{NodeConfigStatus: status},
	}); err != nil {
		log.Errorf("Can't send node config status: %v", err)
	}
}

func (client *SMClient) processSetNodeConfig(config *pb.SetNodeConfig) {
	status := &pb.NodeConfigStatus{}

	if err := client.nodeConfigProcessor.UpdateNodeConfig(config.GetNodeConfig(), config.GetVersion()); err != nil {
		status.Error = pbconvert.ErrorInfoToPB(&cloudprotocol.ErrorInfo{Message: err.Error()})
	}

	if err := client.stream.Send(&pb.SMOutgoingMessages{
		SMOutgoingMessage: &pb.SMOutgoingMessages_NodeConfigStatus{NodeConfigStatus: status},
	}); err != nil {
		log.Errorf("Can't send node config status: %v", err)
	}
}

func (client *SMClient) processUpdateNetworks(updateNetworks *pb.UpdateNetworks) {
	networkParameters := make([]aostypes.NetworkParameters, len(updateNetworks.GetNetworks()))

	for i, network := range updateNetworks.GetNetworks() {
		networkParameters[i] = aostypes.NetworkParameters{
			Subnet:    network.GetSubnet(),
			IP:        network.GetIp(),
			VlanID:    network.GetVlanId(),
			NetworkID: network.GetNetworkId(),
		}
	}

	if err := client.networkManager.UpdateNetworks(networkParameters); err != nil {
		log.Errorf("Can't update networks: %v", err)
	}
}

func (client *SMClient) processRunInstances(runInstances *pb.RunInstances) {
	services := make([]aostypes.ServiceInfo, len(runInstances.GetServices()))

	for i, pbService := range runInstances.GetServices() {
		services[i] = aostypes.ServiceInfo{
			ServiceID:  pbService.GetServiceId(),
			ProviderID: pbService.GetProviderId(),
			Version:    pbService.GetVersion(),
			URL:        pbService.GetUrl(),
			GID:        pbService.GetGid(),
			Sha256:     pbService.GetSha256(),
			Size:       pbService.GetSize(),
		}
	}

	if err := client.servicesProcessor.ProcessDesiredServices(services); err != nil {
		log.Errorf("Can't process desired services list %v", err)
	}

	layers := make([]aostypes.LayerInfo, len(runInstances.GetLayers()))

	for i, pbLayer := range runInstances.GetLayers() {
		layers[i] = aostypes.LayerInfo{
			LayerID: pbLayer.GetLayerId(),
			Digest:  pbLayer.GetDigest(),
			Version: pbLayer.GetVersion(),
			URL:     pbLayer.GetUrl(),
			Sha256:  pbLayer.GetSha256(),
			Size:    pbLayer.GetSize(),
		}
	}

	if err := client.layersProcessor.ProcessDesiredLayers(layers); err != nil {
		log.Errorf("Can't process desired layer list %v", err)
	}

	instances := make([]aostypes.InstanceInfo, len(runInstances.GetInstances()))

	for i, pbInstance := range runInstances.GetInstances() {
		instances[i] = aostypes.InstanceInfo{
			InstanceIdent:     pbconvert.InstanceIdentFromPB(pbInstance.GetInstance()),
			NetworkParameters: pbconvert.NewNetworkParametersFromPB(pbInstance.GetNetworkParameters()),
			UID:               pbInstance.GetUid(), Priority: pbInstance.GetPriority(),
			StoragePath: pbInstance.GetStoragePath(), StatePath: pbInstance.GetStatePath(),
		}
	}

	if err := client.launcher.RunInstances(instances, runInstances.GetForceRestart()); err != nil {
		log.Errorf("Can't run instances: %v", err)
	}
}

func (client *SMClient) processGetSystemLogRequest(logRequest *pb.SystemLogRequest) {
	getSystemLogRequest := cloudprotocol.RequestLog{
		LogID: logRequest.GetLogId(),
		Filter: cloudprotocol.LogFilter{
			From: getTimeFromPB(logRequest.GetFrom()),
			Till: getTimeFromPB(logRequest.GetTill()),
		},
	}

	client.logsProvider.GetSystemLog(getSystemLogRequest)
}

func (client *SMClient) processGetInstanceLogRequest(instanceLogRequest *pb.InstanceLogRequest) {
	getInstanceLogRequest := cloudprotocol.RequestLog{
		LogID: instanceLogRequest.GetLogId(),
		Filter: cloudprotocol.LogFilter{
			From: getTimeFromPB(instanceLogRequest.GetFrom()),
			Till: getTimeFromPB(instanceLogRequest.GetTill()),
			InstanceFilter: pbconvert.InstanceFilterFromPB(
				instanceLogRequest.GetInstanceFilter()),
		},
	}

	if err := client.logsProvider.GetInstanceLog(getInstanceLogRequest); err != nil {
		log.Errorf("Can't get instance log: %v", err)
	}
}

func (client *SMClient) processGetInstanceCrashLogRequest(crashLogRequest *pb.InstanceCrashLogRequest) {
	getInstanceCrashLogRequest := cloudprotocol.RequestLog{
		LogID: crashLogRequest.GetLogId(),
		Filter: cloudprotocol.LogFilter{
			From:           getTimeFromPB(crashLogRequest.GetFrom()),
			Till:           getTimeFromPB(crashLogRequest.GetTill()),
			InstanceFilter: pbconvert.InstanceFilterFromPB(crashLogRequest.GetInstanceFilter()),
		},
	}

	if err := client.logsProvider.GetInstanceCrashLog(getInstanceCrashLogRequest); err != nil {
		log.Errorf("Can't get instance crash log: %v", err)
	}
}

func (client *SMClient) processOverrideEnvVars(envVars *pb.OverrideEnvVars) {
	envVarsInfo := make([]cloudprotocol.EnvVarsInstanceInfo, len(envVars.GetEnvVars()))

	for i, pbEnvVar := range envVars.GetEnvVars() {
		envVarsInfo[i] = cloudprotocol.EnvVarsInstanceInfo{
			InstanceFilter: pbconvert.InstanceFilterFromPB(pbEnvVar.GetInstanceFilter()),
			Variables:      make([]cloudprotocol.EnvVarInfo, len(pbEnvVar.GetVariables())),
		}

		for j, envVar := range pbEnvVar.GetVariables() {
			envVarsInfo[i].Variables[j] = cloudprotocol.EnvVarInfo{Name: envVar.GetName(), Value: envVar.GetValue()}

			if envVar.GetTtl() != nil {
				localTime := envVar.GetTtl().AsTime()
				envVarsInfo[i].Variables[j].TTL = &localTime
			}
		}
	}

	statuses := &pb.OverrideEnvVarStatus{}

	envVarsStatuses, err := client.launcher.OverrideEnvVars(envVarsInfo)
	if err != nil {
		statuses.Error = pbconvert.ErrorInfoToPB(&cloudprotocol.ErrorInfo{Message: err.Error()})
	} else {
		statuses.EnvVarsStatus = make([]*pb.EnvVarInstanceStatus, len(envVarsStatuses))

		for i, status := range envVarsStatuses {
			statuses.EnvVarsStatus[i] = &pb.EnvVarInstanceStatus{
				InstanceFilter: pbconvert.InstanceFilterToPB(status.InstanceFilter),
				Statuses:       make([]*pb.EnvVarStatus, len(status.Statuses)),
			}

			for j, varStatus := range status.Statuses {
				statuses.EnvVarsStatus[i].Statuses[j] = &pb.EnvVarStatus{
					Name: varStatus.Name, Error: pbconvert.ErrorInfoToPB(varStatus.ErrorInfo),
				}
			}
		}
	}

	if err := client.stream.Send(&pb.SMOutgoingMessages{
		SMOutgoingMessage: &pb.SMOutgoingMessages_OverrideEnvVarStatus{OverrideEnvVarStatus: statuses},
	}); err != nil {
		log.Errorf("Can't send override env vars status: %v", err)
	}
}

func (client *SMClient) processAverageMonitoringData() {
	averageMonitoring, err := client.monitoringProvider.GetAverageMonitoring()
	if err != nil {
		log.Errorf("Can't get average monitoring: %v", err)

		return
	}

	if err := client.stream.Send(
		&pb.SMOutgoingMessages{
			SMOutgoingMessage: &pb.SMOutgoingMessages_AverageMonitoring{
				AverageMonitoring: averageMonitoringToPB(averageMonitoring),
			},
		}); err != nil {
		log.Errorf("Can't send average monitoring: %v ", err)
	}
}

func (client *SMClient) processConnectionStatus(status *pb.ConnectionStatus) {
	if err := client.launcher.CloudConnection(status.GetCloudStatus() == pb.ConnectionEnum_CONNECTED); err != nil {
		log.Errorf("Can't set cloud connection: %v", err)
	}
}

func (client *SMClient) handleChannels() {
	for {
		select {
		case runtimeStatus := <-client.runtimeStatusChannel:
			if runtimeStatus.RunStatus != nil {
				client.runStatus = runtimeStatus.RunStatus
			}

			if err := client.sendRuntimeInstanceNotifications(runtimeStatus); err != nil {
				log.Errorf("Can't send runtime instance notification: %v", err)

				return
			}

		case alert := <-client.alertChannel:
			pbAlert, err := cloudprotocolAlertToPB(alert)
			if err != nil {
				log.Errorf("Can't convert alert to pb: %v", err)

				continue
			}

			alertNtf := &pb.SMOutgoingMessages_Alert{Alert: pbAlert}

			if err := client.stream.Send(&pb.SMOutgoingMessages{SMOutgoingMessage: alertNtf}); err != nil {
				log.Errorf("Can't send alert: %v", err)

				return
			}

		case monitoringData, ok := <-client.monitoringChannel:
			if !ok {
				break
			}

			if err := client.stream.Send(
				&pb.SMOutgoingMessages{
					SMOutgoingMessage: &pb.SMOutgoingMessages_InstantMonitoring{
						InstantMonitoring: instantMonitoringToPB(monitoringData),
					},
				}); err != nil {
				log.Errorf("Can't send instant monitoring: %v", err)

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
		pbStatus.Instances[i] = instanceStatusToPB(instance)
	}

	return pbStatus
}

func updateInstanceStatusToPB(updateStatus *launcher.InstancesStatus) *pb.UpdateInstancesStatus {
	pbStatus := &pb.UpdateInstancesStatus{Instances: make([]*pb.InstanceStatus, len(updateStatus.Instances))}

	for i, instance := range updateStatus.Instances {
		pbStatus.Instances[i] = instanceStatusToPB(instance)
	}

	return pbStatus
}

func instanceStatusToPB(instance cloudprotocol.InstanceStatus) *pb.InstanceStatus {
	return &pb.InstanceStatus{
		Instance:       pbconvert.InstanceIdentToPB(instance.InstanceIdent),
		ServiceVersion: instance.ServiceVersion, RunState: instance.Status,
		ErrorInfo: pbconvert.ErrorInfoToPB(instance.ErrorInfo),
	}
}

func monitoringDataToPB(monitoring aostypes.MonitoringData) *pb.MonitoringData {
	pbMonitoringData := &pb.MonitoringData{
		Timestamp: timestamppb.New(monitoring.Timestamp),
		Ram:       monitoring.RAM, Cpu: monitoring.CPU, Download: monitoring.Download,
		Upload: monitoring.Upload, Partitions: make([]*pb.PartitionUsage, len(monitoring.Partitions)),
	}

	for i, partition := range monitoring.Partitions {
		pbMonitoringData.Partitions[i] = &pb.PartitionUsage{Name: partition.Name, UsedSize: partition.UsedSize}
	}

	return pbMonitoringData
}

func averageMonitoringToPB(monitoring aostypes.NodeMonitoring) *pb.AverageMonitoring {
	pbMonitoring := &pb.AverageMonitoring{
		NodeMonitoring:      monitoringDataToPB(monitoring.NodeData),
		InstancesMonitoring: make([]*pb.InstanceMonitoring, len(monitoring.InstancesData)),
	}

	for i, instanceMonitoring := range monitoring.InstancesData {
		pbMonitoring.InstancesMonitoring[i] = &pb.InstanceMonitoring{
			Instance:       pbconvert.InstanceIdentToPB(monitoring.InstancesData[i].InstanceIdent),
			MonitoringData: monitoringDataToPB(instanceMonitoring.MonitoringData),
		}
	}

	return pbMonitoring
}

func instantMonitoringToPB(monitoring aostypes.NodeMonitoring) *pb.InstantMonitoring {
	pbMonitoring := &pb.InstantMonitoring{
		NodeMonitoring:      monitoringDataToPB(monitoring.NodeData),
		InstancesMonitoring: make([]*pb.InstanceMonitoring, len(monitoring.InstancesData)),
	}

	for i, instanceMonitoring := range monitoring.InstancesData {
		pbMonitoring.InstancesMonitoring[i] = &pb.InstanceMonitoring{
			Instance:       pbconvert.InstanceIdentToPB(monitoring.InstancesData[i].InstanceIdent),
			MonitoringData: monitoringDataToPB(instanceMonitoring.MonitoringData),
		}
	}

	return pbMonitoring
}

func getTimeFromPB(timePB *timestamp.Timestamp) *time.Time {
	if timePB != nil {
		time := timePB.AsTime()

		return &time
	}

	return nil
}

func cloudprotocolLogToPB(log cloudprotocol.PushLog) (pbLog *pb.LogData) {
	return &pb.LogData{
		LogId: log.LogID, PartCount: log.PartsCount, Part: log.Part, Data: log.Content,
		Error:  pbconvert.ErrorInfoToPB(log.ErrorInfo),
		Status: log.Status,
	}
}

func cloudprotocolAlertToPB(alert interface{}) (*pb.Alert, error) {
	switch alertItem := alert.(type) {
	case cloudprotocol.SystemAlert:
		return &pb.Alert{
			Tag:       cloudprotocol.AlertTagSystemError,
			Timestamp: timestamppb.New(alertItem.Timestamp),
			AlertItem: &pb.Alert_SystemAlert{
				SystemAlert: &pb.SystemAlert{Message: alertItem.Message},
			},
		}, nil

	case cloudprotocol.CoreAlert:
		return &pb.Alert{
			Tag:       cloudprotocol.AlertTagAosCore,
			Timestamp: timestamppb.New(alertItem.Timestamp),
			AlertItem: &pb.Alert_CoreAlert{
				CoreAlert: &pb.CoreAlert{
					CoreComponent: alertItem.CoreComponent,
					Message:       alertItem.Message,
				},
			},
		}, nil

	case cloudprotocol.ResourceValidateAlert:
		pbResourceValidateAlert := &pb.ResourceValidateAlert{
			Name: alertItem.Name,
		}

		for i := range alertItem.Errors {
			pbResourceValidateAlert.Errors = append(pbResourceValidateAlert.Errors,
				pbconvert.ErrorInfoToPB(&alertItem.Errors[i]))
		}

		return &pb.Alert{
			Tag:       cloudprotocol.AlertTagResourceValidate,
			Timestamp: timestamppb.New(alertItem.Timestamp),
			AlertItem: &pb.Alert_ResourceValidateAlert{
				ResourceValidateAlert: pbResourceValidateAlert,
			},
		}, nil

	case cloudprotocol.DeviceAllocateAlert:
		return &pb.Alert{
			Tag:       cloudprotocol.AlertTagDeviceAllocate,
			Timestamp: timestamppb.New(alertItem.Timestamp),
			AlertItem: &pb.Alert_DeviceAllocateAlert{
				DeviceAllocateAlert: &pb.DeviceAllocateAlert{
					Instance: pbconvert.InstanceIdentToPB(alertItem.InstanceIdent), Message: alertItem.Message,
					Device: alertItem.Device,
				},
			},
		}, nil

	case cloudprotocol.SystemQuotaAlert:
		return &pb.Alert{
			Tag:       cloudprotocol.AlertTagSystemQuota,
			Timestamp: timestamppb.New(alertItem.Timestamp),
			AlertItem: &pb.Alert_SystemQuotaAlert{
				SystemQuotaAlert: &pb.SystemQuotaAlert{
					Parameter: alertItem.Parameter,
					Value:     alertItem.Value,
					Status:    alertItem.Status,
				},
			},
		}, nil

	case cloudprotocol.InstanceQuotaAlert:
		return &pb.Alert{
			Tag:       cloudprotocol.AlertTagInstanceQuota,
			Timestamp: timestamppb.New(alertItem.Timestamp),
			AlertItem: &pb.Alert_InstanceQuotaAlert{InstanceQuotaAlert: &pb.InstanceQuotaAlert{
				Instance:  pbconvert.InstanceIdentToPB(alertItem.InstanceIdent),
				Parameter: alertItem.Parameter,
				Value:     alertItem.Value,
				Status:    alertItem.Status,
			}},
		}, nil

	case cloudprotocol.ServiceInstanceAlert:
		return &pb.Alert{
			Tag:       cloudprotocol.AlertTagServiceInstance,
			Timestamp: timestamppb.New(alertItem.Timestamp),
			AlertItem: &pb.Alert_InstanceAlert{InstanceAlert: &pb.InstanceAlert{
				Instance:       pbconvert.InstanceIdentToPB(alertItem.InstanceIdent),
				ServiceVersion: alertItem.ServiceVersion,
				Message:        alertItem.Message,
			}},
		}, nil
	}

	return nil, errIncorrectAlertType
}
