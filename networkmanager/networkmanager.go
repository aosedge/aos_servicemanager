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

// Package networkmanager provides set of API to configure network
package networkmanager

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/aostypes"
	cni "github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/plugins/ipam/host-local/backend/allocator"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netns"

	"github.com/aosedge/aos_servicemanager/config"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	bridgePrefix                 = "br-"
	vlanPrefix                   = "vlan-"
	instanceIfName               = "eth0"
	pathToNetNs                  = "/run/netns"
	cniBinPath                   = "/opt/cni/bin"
	cniVersion                   = "0.4.0"
	adminChainPrefix             = "INSTANCE_"
	burstLen                     = uint64(12800)
	exposePortConfigExpectedLen  = 2
	countRetryVlanNameGeneration = 10
)

/***********************************************************************************************************************
* Types
**********************************************************************************************************************/

// Storage interface for network manager.
type Storage interface {
	// storage for network info
	RemoveNetworkInfo(networkID string) error
	AddNetworkInfo(info NetworkParameters) error
	GetNetworksInfo() ([]NetworkParameters, error)

	// storage for network traffic monitoring
	SetTrafficMonitorData(chain string, timestamp time.Time, value uint64) (err error)
	GetTrafficMonitorData(chain string) (timestamp time.Time, value uint64, err error)
	RemoveTrafficMonitorData(chain string) (err error)
}

type netInstanceData struct {
	instanceIP string
	hosts      []string
}

// NetworkManager network manager instance.
type NetworkManager struct {
	sync.RWMutex
	cniInterface      cni.CNI
	hosts             []aostypes.Host
	networkDir        string
	trafficMonitoring *trafficMonitoring
	instancesData     map[string]map[string]netInstanceData
	providerNetworks  map[string]NetworkParameters
	vlanIfNames       map[string]string

	storage Storage
}

// NetworkParameters network parameters set for service provider.
type NetworkParameters struct {
	NetworkID  string
	Subnet     string
	IP         string
	VlanID     uint64
	VlanIfName string
}

// NetworkParams network parameters set for instance.
type NetworkParams struct {
	aostypes.InstanceIdent
	aostypes.NetworkParameters
	Hostname           string
	Aliases            []string
	IngressKbit        uint64
	EgressKbit         uint64
	ExposedPorts       []string
	Hosts              []aostypes.Host
	DNSSevers          []string
	HostsFilePath      string
	ResolvConfFilePath string
	UploadLimit        uint64
	DownloadLimit      uint64
}

type cniNetwork struct {
	Name       string            `json:"name"`
	CNIVersion string            `json:"cniVersion"`
	Plugins    []json.RawMessage `json:"plugins"`
}

type bridgeNetConf struct {
	Type             string               `json:"type"`
	Bridge           string               `json:"bridge"`
	IsGateway        bool                 `json:"isGateway"`
	IsDefaultGateway bool                 `json:"isDefaultGateway,omitempty"`
	ForceAddress     bool                 `json:"forceAddress,omitempty"`
	IPMasq           bool                 `json:"ipMasq"`
	MTU              int                  `json:"mtu,omitempty"`
	HairpinMode      bool                 `json:"hairpinMode"`
	PromiscMode      bool                 `json:"promiscMode,omitempty"`
	Vlan             int                  `json:"vlan,omitempty"`
	IPAM             allocator.IPAMConfig `json:"ipam"`
}

type bandwidthNetConf struct {
	Type         string `json:"type,omitempty"`
	IngressRate  uint64 `json:"ingressRate,omitempty"`
	IngressBurst uint64 `json:"ingressBurst,omitempty"`
	EgressRate   uint64 `json:"egressRate,omitempty"`
	EgressBurst  uint64 `json:"egressBurst,omitempty"`
}

type aosFirewallNetConf struct {
	Type                   string               `json:"type"`
	UUID                   string               `json:"uuid"`
	IptablesAdminChainName string               `json:"iptablesAdminChainName"`
	AllowPublicConnections bool                 `json:"allowPublicConnections"`
	InputAccess            []inputAccessConfig  `json:"inputAccess,omitempty"`
	OutputAccess           []outputAccessConfig `json:"outputAccess,omitempty"`
}

type aosDNSNetConf struct {
	Type          string          `json:"type"`
	MultiDomain   bool            `json:"multiDomain,omitempty"`
	DomainName    string          `json:"domainName,omitempty"`
	RemoteServers []string        `json:"remoteServers,omitempty"`
	Capabilities  map[string]bool `json:"capabilities,omitempty"`
}

type inputAccessConfig struct {
	Port     string `json:"port"`
	Protocol string `json:"protocol"`
}

type outputAccessConfig struct {
	DstIP   string `json:"dstIp"`
	DstPort string `json:"dstPort"`
	Proto   string `json:"proto"`
	SrcIP   string `json:"srcIp"`
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var errTrafficMonitorDisable = errors.New("traffic monitoring is disabled")

// CreateVlan this global variable is used to be able to mocking the functionality of networking in tests.
//
//nolint:gochecknoglobals
var CreateVlan = createVlan

// CNIPlugins this global variable is used to be able to mocking the functionality of networking in tests.
//
//nolint:gochecknoglobals
var CNIPlugins cni.CNI

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates network manager instance.
func New(cfg *config.Config, storage Storage) (manager *NetworkManager, err error) {
	log.Debug("Create network manager")

	cniDir := path.Join(cfg.WorkingDir, "cni")

	manager = &NetworkManager{
		hosts:            cfg.Hosts,
		networkDir:       path.Join(cniDir, "networks"),
		instancesData:    make(map[string]map[string]netInstanceData),
		providerNetworks: make(map[string]NetworkParameters),
		vlanIfNames:      make(map[string]string),
		storage:          storage,
	}

	if manager.cniInterface = CNIPlugins; manager.cniInterface == nil {
		manager.cniInterface = cni.NewCNIConfigWithCacheDir([]string{cniBinPath}, cniDir, nil)
	}

	networksInfo, err := storage.GetNetworksInfo()
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if len(networksInfo) > 0 {
		if _, err := manager.createNetwork(networksInfo); err != nil {
			log.Errorf("Can't create networks: %v", err)
		}
	}

	if err := os.RemoveAll(manager.networkDir); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err = os.RemoveAll(cniDir); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	manager.trafficMonitoring, err = newTrafficMonitor(storage)
	if err != nil {
		return manager, err
	}

	manager.trafficMonitoring.runUpdateIptables()

	return manager, nil
}

// Close closes network manager instance.
func (manager *NetworkManager) Close() error {
	log.Debug("Close network manager")

	if manager.trafficMonitoring != nil {
		manager.trafficMonitoring.close()
	}

	return nil
}

// GetNetnsPath get path to instance network namespace.
func (manager *NetworkManager) GetNetnsPath(instanceID string) (pathToNetNS string) {
	return path.Join(pathToNetNs, instanceID)
}

// UpdateNetworks updates networks.
func (manager *NetworkManager) UpdateNetworks(networkParameters []aostypes.NetworkParameters) error {
	log.Debug("Update networks")

	if err := manager.removeNetworks(networkParameters); err != nil {
		return aoserrors.Wrap(err)
	}

	netParameters := make([]NetworkParameters, len(networkParameters))

	for i, networkParameter := range networkParameters {
		netParameters[i] = NetworkParameters{
			NetworkID: networkParameter.NetworkID,
			Subnet:    networkParameter.Subnet,
			IP:        networkParameter.IP,
			VlanID:    networkParameter.VlanID,
		}
	}

	newNetworkParameters, err := manager.createNetwork(netParameters)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, network := range newNetworkParameters {
		if err := manager.storage.AddNetworkInfo(network); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

// AddInstanceToNetwork adds instance to network.
func (manager *NetworkManager) AddInstanceToNetwork(instanceID, networkID string, params NetworkParams) (err error) {
	log.WithFields(log.Fields{"instanceID": instanceID, "networkID": networkID}).Debug("Add instance to network")

	if manager.isInstanceInNetwork(instanceID, networkID) {
		return aoserrors.Errorf("Instance %s already in the network %s", instanceID, networkID)
	}

	manager.addInstanceNetworkToCache(instanceID, networkID)

	defer func() {
		if err != nil {
			if err := manager.deleteInstanceNetworkFromCache(instanceID, networkID); err != nil {
				log.Errorf("Can't delete network instance: %v", err)
			}
		}
	}()

	if err = createNetNS(instanceID); err != nil {
		return aoserrors.Wrap(err)
	}

	defer func() {
		if err != nil {
			if delErr := netns.DeleteNamed(instanceID); delErr != nil {
				log.Errorf("Can't delete named network namespace: %s", delErr)
			}
		}
	}()

	netConfig, runtimeConfig, hosts, err := manager.prepareCNIConfig(instanceID, networkID, params)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			if err := manager.cniInterface.DelNetworkList(context.Background(), netConfig, runtimeConfig); err != nil {
				log.Errorf("Can't delete network list: %s", err)
			}
		}
	}()

	nameservers, instanceIP, err := manager.addNetwork(instanceID, netConfig, runtimeConfig)
	if err != nil {
		return err
	}

	if err = createResolvConfAndHostFile(networkID, instanceIP, nameservers, params); err != nil {
		return err
	}

	if manager.trafficMonitoring != nil {
		if err = manager.trafficMonitoring.startInstanceTrafficMonitor(
			instanceID, instanceIP, params.DownloadLimit, params.UploadLimit); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if err = manager.updateInstanceNetworkCache(instanceID, networkID, instanceIP, hosts); err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"instanceID": instanceID,
		"IP":         instanceIP,
	}).Debug("Instance has been added to the network")

	return nil
}

// RemoveInstanceFromNetwork removes instance from network.
func (manager *NetworkManager) RemoveInstanceFromNetwork(instanceID, networkID string) error {
	log.WithFields(log.Fields{"instanceID": instanceID}).Debug("Remove instance from network")

	if !manager.isInstanceInNetwork(instanceID, networkID) {
		return nil
	}

	if manager.trafficMonitoring != nil {
		if err := manager.trafficMonitoring.stopInstanceTrafficMonitor(instanceID); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if err := manager.removeInstanceFromNetwork(instanceID, networkID); err != nil {
		return aoserrors.Wrap(err)
	}

	return manager.deleteInstanceNetworkFromCache(instanceID, networkID)
}

// GetInstanceIP return instance IP address.
func (manager *NetworkManager) GetInstanceIP(instanceID, networkID string) (ip string, err error) {
	log.WithFields(log.Fields{"instanceID": instanceID, "networkID": networkID}).Debug("Get instance IP")

	if !manager.isInstanceInNetwork(instanceID, networkID) {
		log.WithFields(log.Fields{"instanceID": instanceID}).Warn("Instance is not in network")

		return "", aoserrors.New("Instance is not in network")
	}

	manager.RLock()
	defer manager.RUnlock()

	return manager.instancesData[networkID][instanceID].instanceIP, nil
}

func (manager *NetworkManager) GetSystemTraffic() (inputTraffic, outputTraffic uint64, err error) {
	if manager.trafficMonitoring == nil {
		return 0, 0, errTrafficMonitorDisable
	}

	inputTrafficData, outputTrafficData, err := manager.trafficMonitoring.getInputOutputTrafficData(
		manager.trafficMonitoring.inChain, manager.trafficMonitoring.outChain)
	if err != nil {
		return 0, 0, err
	}

	return inputTrafficData.currentValue, outputTrafficData.currentValue, nil
}

func (manager *NetworkManager) GetInstanceTraffic(instanceID string) (inputTraffic, outputTraffic uint64, err error) {
	if manager.trafficMonitoring == nil {
		return 0, 0, errTrafficMonitorDisable
	}

	instanceChains := manager.trafficMonitoring.getInstanceChains(instanceID)
	if instanceChains == nil {
		return 0, 0, errors.Errorf("chain for instance %s is not found", instanceID)
	}

	inTrafficData, outTrafficData, err := manager.trafficMonitoring.getInputOutputTrafficData(
		instanceChains.inChain, instanceChains.outChain)
	if err != nil {
		return 0, 0, err
	}

	return inTrafficData.currentValue, outTrafficData.currentValue, nil
}

func (manager *NetworkManager) SetTrafficPeriod(period int) error {
	if manager.trafficMonitoring == nil {
		return errTrafficMonitorDisable
	}

	if period < MinutePeriod || period > YearPeriod {
		return errors.New("failed to set traffic period, unexpected value")
	}

	manager.trafficMonitoring.trafficPeriod = period

	return nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (manager *NetworkManager) removeNetworks(networkParameters []aostypes.NetworkParameters) error {
	manager.Lock()
	defer manager.Unlock()

next:
	for _, existNetworkParameter := range manager.providerNetworks {
		for _, networkParameter := range networkParameters {
			if existNetworkParameter.NetworkID == networkParameter.NetworkID {
				continue next
			}
		}

		log.Infof("Removing network: %s", existNetworkParameter.NetworkID)

		instances, ok := manager.instancesData[existNetworkParameter.NetworkID]
		if !ok || len(instances) == 0 {
			log.Infof("Network %s is empty", existNetworkParameter.NetworkID)

			if err := manager.clearNetwork(existNetworkParameter.NetworkID); err != nil {
				return err
			}
		}

		delete(manager.providerNetworks, existNetworkParameter.NetworkID)

		if err := manager.storage.RemoveNetworkInfo(existNetworkParameter.NetworkID); err != nil {
			return aoserrors.Wrap(err)
		}

		log.WithFields(log.Fields{
			"networkID": existNetworkParameter.NetworkID,
		}).Debug("Network has been removed")
	}

	return nil
}

func (manager *NetworkManager) createNetwork(
	networkParameters []NetworkParameters,
) (newNetworkParameters []NetworkParameters, err error) {
	manager.Lock()
	defer manager.Unlock()

	for _, networkParameter := range networkParameters {
		if existParameters, ok := manager.providerNetworks[networkParameter.NetworkID]; ok &&
			existParameters.IP == networkParameter.IP {
			continue
		}

		vlanIfname := networkParameter.VlanIfName

		if vlanIfname == "" {
			if vlanIfname, err = manager.generateVlanName(); err != nil {
				return nil, err
			}

			networkParameter.VlanIfName = vlanIfname
		}

		manager.vlanIfNames[networkParameter.NetworkID] = vlanIfname

		if err := CreateVlan(Vlan{
			vlanID: int(networkParameter.VlanID),
			bridge: bridgePrefix + networkParameter.NetworkID,
			ifName: vlanIfname,
			ip:     networkParameter.IP,
			subnet: networkParameter.Subnet,
		}); err != nil {
			return nil, err
		}

		manager.providerNetworks[networkParameter.NetworkID] = networkParameter

		newNetworkParameters = append(newNetworkParameters, networkParameter)

		log.WithFields(log.Fields{
			"networkID": networkParameter.NetworkID,
			"IP":        networkParameter.IP,
		}).Debug("Network has been created")
	}

	return newNetworkParameters, nil
}

func (manager *NetworkManager) generateVlanName() (string, error) {
next:
	for i := 0; i < countRetryVlanNameGeneration; i++ {
		vlanName, err := randomVlanName()
		if err != nil {
			return "", err
		}

		for _, existingVlanName := range manager.vlanIfNames {
			if existingVlanName == vlanName {
				continue next
			}
		}

		return vlanName, nil
	}

	return "", errors.New("failed to generate vlan name")
}

func (manager *NetworkManager) updateInstanceNetworkCache(
	instanceID, networkID, instanceIP string, hosts []string,
) error {
	manager.Lock()
	defer manager.Unlock()

	networkInstanceData, ok := manager.instancesData[networkID][instanceID]
	if !ok {
		return aoserrors.Errorf("can't find network instanceID: %s", instanceID)
	}

	networkInstanceData.hosts = hosts
	networkInstanceData.instanceIP = instanceIP

	manager.instancesData[networkID][instanceID] = networkInstanceData

	return nil
}

func (manager *NetworkManager) addInstanceNetworkToCache(instanceID, networkID string) {
	manager.Lock()
	defer manager.Unlock()

	if _, ok := manager.instancesData[networkID]; !ok {
		manager.instancesData[networkID] = make(map[string]netInstanceData)
	}

	manager.instancesData[networkID][instanceID] = netInstanceData{}
}

func (manager *NetworkManager) deleteInstanceNetworkFromCache(instanceID, networkID string) error {
	manager.Lock()
	defer manager.Unlock()

	delete(manager.instancesData[networkID], instanceID)
	networkEmpty := len(manager.instancesData[networkID]) == 0

	if _, ok := manager.providerNetworks[networkID]; networkEmpty && !ok {
		return manager.clearNetwork(networkID)
	}

	return nil
}

func createResolvConfAndHostFile(networkID, instanceIP string, nameservers []string, params NetworkParams) error {
	if params.HostsFilePath != "" {
		if err := writeHostToHostsFile(params.HostsFilePath, instanceIP,
			networkID, params.Hostname, params.Hosts); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if params.ResolvConfFilePath != "" {
		mainServers := []string{"8.8.8.8"}

		if len(nameservers) != 0 {
			mainServers = nameservers
		}

		if err := writeResolveConfFile(params.ResolvConfFilePath, mainServers, params.DNSSevers); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (manager *NetworkManager) addNetwork(
	instanceID string, netConfig *cni.NetworkConfigList, runtimeConfig *cni.RuntimeConf) (
	nameservers []string, instanceIP string, err error,
) {
	resAdd, err := manager.cniInterface.AddNetworkList(context.Background(), netConfig, runtimeConfig)
	if err != nil {
		return nil, "", aoserrors.Wrap(err)
	}

	result, err := current.GetResult(resAdd)
	if err != nil {
		return nil, "", aoserrors.Wrap(err)
	}

	if len(result.IPs) == 0 {
		return nil, "", aoserrors.Errorf("error getting IP address for instance %s", instanceID)
	}

	return result.DNS.Nameservers, result.IPs[0].Address.IP.String(), nil
}

func (manager *NetworkManager) prepareCNIConfig(
	instanceID, networkID string, params NetworkParams) (
	netConfig *cni.NetworkConfigList, runtimeConfig *cni.RuntimeConf, hosts []string, err error,
) {
	if hosts, err = manager.prepareHostnameList(networkID, params); err != nil {
		return nil, nil, nil, err
	}

	if netConfig, err = prepareNetworkConfigList(manager.networkDir, instanceID, networkID, params); err != nil {
		return nil, nil, nil, aoserrors.Wrap(err)
	}

	if _, err = manager.cniInterface.ValidateNetworkList(context.Background(), netConfig); err != nil {
		return nil, nil, nil, aoserrors.Wrap(err)
	}

	return netConfig, manager.prepareRuntimeConfig(instanceID, networkID, hosts), hosts, nil
}

func (manager *NetworkManager) isInstanceInNetwork(instanceID, networkID string) (status bool) {
	manager.RLock()
	defer manager.RUnlock()

	if instances, ok := manager.instancesData[networkID]; ok {
		if _, ok := instances[instanceID]; ok {
			return true
		}
	}

	return false
}

func (manager *NetworkManager) clearNetwork(networkID string) error {
	log.WithFields(log.Fields{"networkID": networkID}).Debug("Clear network")

	delete(manager.instancesData, networkID)

	if err := removeInterface(bridgePrefix + networkID); err != nil {
		return err
	}

	vlanIfname, ok := manager.vlanIfNames[networkID]
	if ok {
		if err := removeInterface(vlanIfname); err != nil {
			return err
		}

		delete(manager.vlanIfNames, networkID)
	}

	os.RemoveAll(path.Join(manager.networkDir, networkID))

	return nil
}

func (manager *NetworkManager) removeInstanceFromNetwork(instanceID, networkID string) (err error) {
	defer func() {
		if delErr := netns.DeleteNamed(instanceID); delErr != nil {
			log.Errorf("Can't delete named network namespace: %s", delErr)

			if err == nil {
				err = aoserrors.Wrap(delErr)
			}
		}
	}()

	networkConfig, runtimeConfig := getRuntimeNetConfig(instanceID, networkID)

	confBytes, runtimeConfig, err := manager.cniInterface.GetNetworkListCachedConfig(networkConfig, runtimeConfig)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if confBytes == nil {
		return aoserrors.Errorf("instance %s not found in network %s", instanceID, networkID)
	}

	if networkConfig, err = cni.ConfListFromBytes(confBytes); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = manager.cniInterface.DelNetworkList(context.Background(), networkConfig, runtimeConfig); err != nil {
		return aoserrors.Wrap(err)
	}

	if manager.trafficMonitoring != nil {
		if err = manager.trafficMonitoring.stopInstanceTrafficMonitor(instanceID); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	log.WithFields(log.Fields{"instanceID": instanceID}).Debug("Instance successfully removed from network")

	return nil
}

func (manager *NetworkManager) prepareRuntimeConfig(instanceID, networkID string, hosts []string) (
	runtimeConfig *cni.RuntimeConf,
) {
	runtimeConfig = &cni.RuntimeConf{
		ContainerID: instanceID,
		NetNS:       manager.GetNetnsPath(instanceID),
		IfName:      instanceIfName,
		Args: [][2]string{
			{"IgnoreUnknown", "1"},
			{"K8S_POD_NAME", instanceID},
		},
		CapabilityArgs: make(map[string]interface{}),
	}

	if len(hosts) != 0 {
		runtimeConfig.CapabilityArgs["aliases"] = map[string][]string{networkID: hosts}
	}

	return runtimeConfig
}

func (manager *NetworkManager) isHostnameExists(networkID string, hosts []string) error {
	manager.RLock()
	defer manager.RUnlock()

	instances, ok := manager.instancesData[networkID]
	if !ok {
		return nil
	}

	for _, networkInstanceData := range instances {
		for _, existHostname := range networkInstanceData.hosts {
			for _, newHostname := range hosts {
				if existHostname == newHostname {
					return aoserrors.Errorf("hostname %s already exists", newHostname)
				}
			}
		}
	}

	return nil
}

func (manager *NetworkManager) prepareHostnameList(networkID string, params NetworkParams) (hosts []string, err error) {
	hosts = append(hosts, params.Aliases...)

	if params.Hostname != "" {
		hosts = append(hosts, params.Hostname)
	}

	if params.ServiceID != "" && params.SubjectID != "" {
		hosts = append(hosts, fmt.Sprintf("%d.%s.%s", params.Instance, params.SubjectID, params.ServiceID))

		if params.Instance == 0 {
			hosts = append(hosts, fmt.Sprintf("%s.%s", params.SubjectID, params.ServiceID))
		}
	}

	if len(hosts) != 0 {
		hosts = tryAppendDomainNameToHostname(hosts, networkID)

		if err = manager.isHostnameExists(networkID, hosts); err != nil {
			return nil, aoserrors.Wrap(err)
		}
	}

	return hosts, nil
}

func tryAppendDomainNameToHostname(hosts []string, networkID string) []string {
	for _, hostname := range hosts {
		if strings.ContainsAny(hostname, ".") {
			hosts = append(hosts, strings.Join([]string{hostname, networkID}, "."))
		}
	}

	return hosts
}

func getBridgePluginConfig(networkDir, networkID string, subnet string, ip string) (config json.RawMessage, err error) {
	_, ipSubnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	_, defaultRoute, _ := net.ParseCIDR("0.0.0.0/0")

	configBridge := &bridgeNetConf{
		Type:        "bridge",
		Bridge:      bridgePrefix + networkID,
		IsGateway:   true,
		IPMasq:      true,
		HairpinMode: true,
		IPAM: allocator.IPAMConfig{
			DataDir: networkDir,
			Type:    "host-local",
			Range: &allocator.Range{
				RangeStart: net.ParseIP(ip),
				RangeEnd:   net.ParseIP(ip),
				Subnet:     types.IPNet(*ipSubnet),
			},
			Routes: []*types.Route{
				{
					Dst: *defaultRoute,
				},
			},
		},
	}

	if config, err = json.Marshal(configBridge); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return config, nil
}

func getFirewallPluginConfig(instanceID string, exposedPorts []string, firewallRules []aostypes.FirewallRule) (
	config json.RawMessage, err error,
) {
	aosFirewall := &aosFirewallNetConf{
		Type:                   "aos-firewall",
		UUID:                   instanceID,
		IptablesAdminChainName: adminChainPrefix + instanceID,
		AllowPublicConnections: true,
	}

	// ExposedPorts format port/protocol
	for _, exposePort := range exposedPorts {
		portConfig := strings.Split(exposePort, "/")
		if len(portConfig) > exposePortConfigExpectedLen || len(portConfig) == 0 {
			return nil, aoserrors.Errorf("unsupported ExposedPorts format %s", exposePort)
		}

		input := inputAccessConfig{Port: portConfig[0], Protocol: "tcp"}
		if len(portConfig) == exposePortConfigExpectedLen {
			input.Protocol = portConfig[1]
		}

		aosFirewall.InputAccess = append(aosFirewall.InputAccess, input)
	}

	for _, rule := range firewallRules {
		output := outputAccessConfig{
			DstIP:   rule.DstIP,
			DstPort: rule.DstPort,
			Proto:   rule.Proto,
			SrcIP:   rule.SrcIP,
		}

		aosFirewall.OutputAccess = append(aosFirewall.OutputAccess, output)
	}

	if config, err = json.Marshal(aosFirewall); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return config, nil
}

func getBandwidthPluginConfig(ingressKbit, egressKbit uint64) (config json.RawMessage, err error) {
	bandwidth := &bandwidthNetConf{
		Type: "bandwidth",
	}

	// the burst argument was selected relative to the mtu network interface

	if ingressKbit > 0 {
		bandwidth.IngressRate = ingressKbit * 1000
		bandwidth.IngressBurst = burstLen
	}

	if egressKbit > 0 {
		bandwidth.EgressRate = egressKbit * 1000
		bandwidth.EgressBurst = burstLen
	}

	if config, err = json.Marshal(bandwidth); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return config, nil
}

func getDNSPluginConfig(networkID string, dnsServers []string) (config json.RawMessage, err error) {
	configDNS := &aosDNSNetConf{
		Type:          "dnsname",
		MultiDomain:   true,
		DomainName:    networkID,
		RemoteServers: dnsServers,
		Capabilities:  map[string]bool{"aliases": true},
	}

	if config, err = json.Marshal(configDNS); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return config, nil
}

func getRuntimeNetConfig(instanceID, networkID string) (
	networkingConfig *cni.NetworkConfigList, runtimeConfig *cni.RuntimeConf,
) {
	networkingConfig = &cni.NetworkConfigList{
		Name:       networkID,
		CNIVersion: cniVersion,
	}

	runtimeConfig = &cni.RuntimeConf{
		ContainerID: instanceID,
		NetNS:       path.Join(pathToNetNs, instanceID),
		IfName:      instanceIfName,
	}

	return networkingConfig, runtimeConfig
}

func prepareNetworkConfigList(networkDir, instanceID, networkID string, params NetworkParams,
) (cniNetworkConfig *cni.NetworkConfigList, err error) {
	networkConfig := cniNetwork{Name: networkID, CNIVersion: cniVersion}

	// Bridge

	bridgeConfig, err := getBridgePluginConfig(networkDir, networkID, params.Subnet, params.IP)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	networkConfig.Plugins = append(networkConfig.Plugins, bridgeConfig)

	// Firewall

	firewallConfig, err := getFirewallPluginConfig(instanceID, params.ExposedPorts, params.NetworkParameters.FirewallRules)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	networkConfig.Plugins = append(networkConfig.Plugins, firewallConfig)

	// Bandwidth

	if params.IngressKbit > 0 || params.EgressKbit > 0 {
		bandwidthConfig, err := getBandwidthPluginConfig(params.IngressKbit, params.EgressKbit)
		if err != nil {
			return nil, aoserrors.Wrap(err)
		}

		networkConfig.Plugins = append(networkConfig.Plugins, bandwidthConfig)
	}

	// DNS

	dnsConfig, err := getDNSPluginConfig(networkID, params.DNSServers)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	networkConfig.Plugins = append(networkConfig.Plugins, dnsConfig)

	networkConfigBytes, err := json.Marshal(networkConfig)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if cniNetworkConfig, err = cni.ConfListFromBytes(networkConfigBytes); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return cniNetworkConfig, nil
}

func randomVlanName() (string, error) {
	b := make([]byte, 4) //nolint:gomnd
	if _, err := rand.Read(b); err != nil {
		return "", aoserrors.Wrap(err)
	}

	return fmt.Sprintf("%s%x", vlanPrefix, b), nil
}
