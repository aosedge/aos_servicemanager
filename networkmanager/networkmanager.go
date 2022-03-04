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
	"bufio"
	"context"
	"encoding/json"
	"io/ioutil"
	"net"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/aoscloud/aos_common/aoserrors"
	cni "github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/plugins/ipam/host-local/backend/allocator"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netns"

	"github.com/aoscloud/aos_servicemanager/config"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	bridgePrefix                  = "br-"
	instanceIfName                = "eth0"
	pathToNetNs                   = "/run/netns"
	cniBinPath                    = "/opt/cni/bin"
	cniVersion                    = "0.4.0"
	adminChainPrefix              = "INSTANCE_"
	burstLen                      = uint64(12800)
	exposePortConfigExpectedLen   = 2
	allowedConnectionsExpectedLen = 3
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// NetworkManager network manager instance.
type NetworkManager struct {
	sync.Mutex
	cniInterface      cni.CNI
	ipamSubnetwork    *ipSubnetwork
	hosts             []config.Host
	networkDir        string
	trafficMonitoring *trafficMonitoring
	networkInstanceIP map[string]map[string]string
}

// NetworkParams network parameters set for instance.
type NetworkParams struct {
	Hostname           string
	Aliases            []string
	IngressKbit        uint64
	EgressKbit         uint64
	ExposedPorts       []string
	AllowedConnections []string
	Hosts              []config.Host
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
	Type         string          `json:"type"`
	MultiDomain  bool            `json:"multiDomain,omitempty"`
	DomainName   string          `json:"domainName,omitempty"`
	Capabilities map[string]bool `json:"capabilities,omitempty"`
}

type inputAccessConfig struct {
	Port     string `json:"port"`
	Protocol string `json:"protocol"`
}

type outputAccessConfig struct {
	UUID     string `json:"uuid"`
	Port     string `json:"port"`
	Protocol string `json:"protocol"`
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

// nolint:gochecknoglobals
var skipNetworkFileNames = []string{"lock", "last_reserved_ip.0"}

// These global variables are used to be able to mocking the functionality of networking in tests.
// nolint:gochecknoglobals
var (
	CNIPlugins        cni.CNI
	GetIPSubnet       func(networkID string) (allocIPNet *net.IPNet, err error)
	GetIPAddressRange = getIPAddressRange
)

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates network manager instance.
func New(cfg *config.Config, trafficStorage TrafficStorage) (manager *NetworkManager, err error) {
	log.Debug("Create network manager")

	cniDir := path.Join(cfg.WorkingDir, "cni")

	manager = &NetworkManager{
		hosts:             cfg.Hosts,
		networkDir:        path.Join(cniDir, "networks"),
		networkInstanceIP: make(map[string]map[string]string),
	}

	if manager.cniInterface = CNIPlugins; manager.cniInterface == nil {
		manager.cniInterface = cni.NewCNIConfigWithCacheDir([]string{cniBinPath}, cniDir, nil)
	}

	if GetIPSubnet == nil {
		GetIPSubnet = manager.getIPSubnet
	}

	if manager.ipamSubnetwork, err = newIPam(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err = manager.deleteAllNetworks(); err != nil {
		log.Errorf("Can't delete all networks: %s", err)
	}

	if err = os.RemoveAll(cniDir); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if trafficStorage != nil {
		manager.trafficMonitoring, err = newTrafficMonitor(trafficStorage)
		if err != nil {
			return manager, err
		}
	} else {
		log.Warn("Can't initialize traffic monitoring: storage is nil")
	}

	return manager, nil
}

// Close closes network manager instance.
func (manager *NetworkManager) Close() error {
	log.Debug("Close network manager")

	if manager.trafficMonitoring != nil {
		if err := manager.trafficMonitoring.deleteAllTrafficChains(); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

// GetNetnsPath get path to instance network namespace.
func (manager *NetworkManager) GetNetnsPath(instanceID string) (pathToNetNS string) {
	return path.Join(pathToNetNs, instanceID)
}

// AddInstanceToNetwork adds instance to network.
func (manager *NetworkManager) AddInstanceToNetwork(instanceID, networkID string, params NetworkParams) error {
	manager.Lock()
	defer manager.Unlock()

	log.WithFields(log.Fields{"instanceID": instanceID, "networkID": networkID}).Debug("Add instance to network")

	if manager.isInstanceInNetwork(instanceID, networkID) {
		return aoserrors.Errorf("Instance %s already in the network %s", instanceID, networkID)
	}

	ipSubnet, err := GetIPSubnet(networkID)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			manager.ipamSubnetwork.releaseIPNetPool(networkID)
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

	netConfig, runtimeConfig, err := manager.prepareNetRuntimeConfig(instanceID, networkID, ipSubnet, params)
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

	if _, ok := manager.networkInstanceIP[networkID]; !ok {
		manager.networkInstanceIP[networkID] = make(map[string]string)
	}

	manager.networkInstanceIP[networkID][instanceID] = instanceIP

	log.WithFields(log.Fields{
		"instanceID": instanceID,
		"IP":         instanceIP,
	}).Debug("Instance has been added to the network")

	return nil
}

// RemoveInstanceFromNetwork removes instance from network.
func (manager *NetworkManager) RemoveInstanceFromNetwork(instanceID, networkID string) error {
	manager.Lock()
	defer manager.Unlock()

	log.WithFields(log.Fields{"instanceID": instanceID}).Debug("Remove instance from network")

	if !manager.isInstanceInNetwork(instanceID, networkID) {
		return aoserrors.Errorf("Instance %s is not in network", instanceID)
	}

	if manager.trafficMonitoring != nil {
		if err := manager.trafficMonitoring.stopInstanceTrafficMonitor(instanceID); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if err := manager.removeInstanceFromNetwork(instanceID, networkID); err != nil {
		return aoserrors.Wrap(err)
	}

	delete(manager.networkInstanceIP[networkID], instanceID)

	if len(manager.networkInstanceIP[networkID]) == 0 {
		return aoserrors.Wrap(manager.clearNetwork(networkID))
	}

	return nil
}

// GetInstanceIP return instance IP address.
func (manager *NetworkManager) GetInstanceIP(instanceID, networkID string) (ip string, err error) {
	manager.Lock()
	defer manager.Unlock()

	log.WithFields(log.Fields{"instanceID": instanceID, "networkID": networkID}).Debug("Get instance IP")

	if !manager.isInstanceInNetwork(instanceID, networkID) {
		log.WithFields(log.Fields{"instanceID": instanceID}).Warn("Instance is not in network")

		return "", aoserrors.New("Instance is not in network")
	}

	return manager.networkInstanceIP[networkID][instanceID], nil
}

func (manager *NetworkManager) GetSystemTraffic() (inputTraffic, outputTraffic uint64, err error) {
	if manager.trafficMonitoring == nil {
		return 0, 0, errors.New("traffic monitoring is disabled")
	}

	if err = manager.trafficMonitoring.processTrafficMonitor(); err != nil {
		return 0, 0, aoserrors.Wrap(err)
	}

	inputTrafficData, ok := manager.trafficMonitoring.trafficMap[manager.trafficMonitoring.inChain]
	if !ok {
		return 0, 0, errors.New("chain for input system traffic is not found")
	}

	outputTrafficData, ok := manager.trafficMonitoring.trafficMap[manager.trafficMonitoring.outChain]
	if !ok {
		return inputTrafficData.currentValue, 0, errors.New("chain for output system traffic is not found")
	}

	return inputTrafficData.currentValue, outputTrafficData.currentValue, nil
}

func (manager *NetworkManager) GetInstanceTraffic(instanceID string) (inputTraffic, outputTraffic uint64, err error) {
	if manager.trafficMonitoring == nil {
		return 0, 0, errors.New("traffic monitoring is disabled")
	}

	instanceChains, ok := manager.trafficMonitoring.instanceChainsMap[instanceID]
	if !ok {
		return 0, 0, errors.Errorf("chain for instance %s is not found", instanceID)
	}

	if err = manager.trafficMonitoring.processTrafficMonitor(); err != nil {
		return 0, 0, aoserrors.Wrap(err)
	}

	inTrafficData, ok := manager.trafficMonitoring.trafficMap[instanceChains.inChain]
	if !ok {
		return 0, 0, errors.Errorf("input chain %s for instance %s is not found", instanceChains.inChain, instanceID)
	}

	outTrafficData, ok := manager.trafficMonitoring.trafficMap[instanceChains.outChain]
	if !ok {
		return inTrafficData.currentValue, 0, errors.Errorf("output chain %s for instance %s is not found",
			instanceChains.outChain, instanceID)
	}

	return inTrafficData.currentValue, outTrafficData.currentValue, nil
}

func (manager *NetworkManager) SetTrafficPeriod(period int) error {
	if manager.trafficMonitoring == nil {
		return errors.New("traffic monitoring is disabled")
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

func (manager *NetworkManager) getIPSubnet(networkID string) (allocIPNet *net.IPNet, err error) {
	ipSubnet, exist := manager.ipamSubnetwork.tryToGetExistIPNetFromPool(networkID)
	if !exist {
		if ipSubnet, err = checkExistNetInterface(bridgePrefix + networkID); err != nil {
			if ipSubnet, _, err = manager.ipamSubnetwork.requestIPNetPool(networkID); err != nil {
				return nil, aoserrors.Wrap(err)
			}
		}
	}

	return ipSubnet, nil
}

func (manager *NetworkManager) prepareNetRuntimeConfig(
	instanceID, networkID string, ipSubnet *net.IPNet, params NetworkParams) (
	netConfig *cni.NetworkConfigList, runtimeConfig *cni.RuntimeConf, err error,
) {
	netConfig, err = prepareNetworkConfigList(manager.networkDir, instanceID, networkID, ipSubnet, &params)
	if err != nil {
		return nil, nil, aoserrors.Wrap(err)
	}

	runtimeConfig, err = manager.prepareRuntimeConfig(instanceID, networkID, params.Hostname, params.Aliases)
	if err != nil {
		return nil, nil, aoserrors.Wrap(err)
	}

	if _, err = manager.cniInterface.ValidateNetworkList(context.Background(), netConfig); err != nil {
		return nil, nil, aoserrors.Wrap(err)
	}

	return netConfig, runtimeConfig, nil
}

func (manager *NetworkManager) deleteAllNetworks() error {
	manager.Lock()
	defer manager.Unlock()

	log.Debug("Delete all networks")

	filesNetworkID, err := ioutil.ReadDir(manager.networkDir)
	if err != nil {
		return nil // nolint:nilerr
	}

	for _, networkIDFile := range filesNetworkID {
		if networkErr := manager.deleteNetworkInstances(networkIDFile.Name()); networkErr != nil {
			log.Errorf("Can't delete network: %s", err)

			if err == nil {
				err = networkErr
			}
		}
	}

	os.RemoveAll(manager.networkDir)

	return aoserrors.Wrap(err)
}

func (manager *NetworkManager) isInstanceInNetwork(instanceID, networkID string) (status bool) {
	if instances, ok := manager.networkInstanceIP[networkID]; ok {
		if _, ok := instances[instanceID]; ok {
			return true
		}
	}

	return false
}

func (manager *NetworkManager) deleteNetworkInstances(networkID string) error {
	log.WithFields(log.Fields{"networkID": networkID}).Debug("Delete network instances")

	networkDir := path.Join(manager.networkDir, networkID)

	if _, err := os.Stat(networkDir); err != nil {
		log.WithFields(log.Fields{"networkID": networkID}).Warn("Network doesn't exist")
		return nil // nolint:nilerr
	}

	filesInstanceID, err := ioutil.ReadDir(networkDir)
	if err != nil {
		return aoserrors.Wrap(err)
	}

skip:
	for _, instanceIDFile := range filesInstanceID {
		for _, skipFile := range skipNetworkFileNames {
			if instanceIDFile.Name() == skipFile {
				continue skip
			}
		}

		if netErr := manager.tryRemoveInstanceFromNetwork(instanceIDFile.Name(), networkID); netErr != nil {
			if err == nil {
				err = netErr
			}
		}
	}

	if clearErr := manager.clearNetwork(networkID); clearErr != nil && err == nil {
		err = clearErr
	}

	return err
}

func (manager *NetworkManager) tryRemoveInstanceFromNetwork(instanceFileName, networkID string) error {
	instanceID, err := readInstanceIDFromFile(path.Join(manager.networkDir, networkID, instanceFileName))
	if err != nil {
		return nil // nolint:nilerr
	}

	if err := manager.removeInstanceFromNetwork(instanceID, networkID); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (manager *NetworkManager) clearNetwork(networkID string) error {
	log.WithFields(log.Fields{"networkID": networkID}).Debug("Clear network")

	networkDir := path.Join(manager.networkDir, networkID)

	if err := manager.postNetworkClear(networkID); err != nil {
		return err
	}

	os.RemoveAll(networkDir)

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

func (manager *NetworkManager) postNetworkClear(networkID string) error {
	manager.ipamSubnetwork.releaseIPNetPool(networkID)

	if err := removeBridgeInterface(networkID); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (manager *NetworkManager) prepareRuntimeConfig(instanceID, networkID, hostname string, aliases []string) (
	runtimeConfig *cni.RuntimeConf, err error,
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

	if hostname != "" {
		aliases = append([]string{hostname}, aliases...)
	}

	if len(aliases) != 0 {
		runtimeConfig.CapabilityArgs["aliases"] = map[string][]string{networkID: aliases}
	}

	return runtimeConfig, nil
}

func readInstanceIDFromFile(pathToInstanceID string) (instanceID string, err error) {
	f, err := os.Open(pathToInstanceID)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)

	var cniInstanceInfo []string

	for scanner.Scan() {
		line := scanner.Text()
		if line != instanceIfName {
			cniInstanceInfo = append(cniInstanceInfo, line)
		}
	}

	if len(cniInstanceInfo) != 1 {
		return "", aoserrors.Errorf("incorrect file content. There should be a container ID and a network interface name")
	}

	return cniInstanceInfo[0], nil
}

func getBridgePluginConfig(networkDir, networkID string, subnetwork *net.IPNet) (config json.RawMessage, err error) {
	minIPRange, maxIPRange := GetIPAddressRange(subnetwork)
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
				RangeStart: minIPRange,
				RangeEnd:   maxIPRange,
				Subnet:     types.IPNet(*subnetwork),
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

func getFirewallPluginConfig(instanceID string, exposedPorts, allowedConnections []string) (
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

	// AllowedConnections format instance-UUID/port/protocol
	for _, allowConn := range allowedConnections {
		connConf := strings.Split(allowConn, "/")
		if len(connConf) > allowedConnectionsExpectedLen || len(connConf) < 2 {
			return nil, aoserrors.Errorf("unsupported AllowedConnections format %s", connConf)
		}

		output := outputAccessConfig{UUID: connConf[0], Port: connConf[1], Protocol: "tcp"}
		if len(connConf) == allowedConnectionsExpectedLen {
			output.Protocol = connConf[2]
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

func getDNSPluginConfig(networkID string) (config json.RawMessage, err error) {
	configDNS := &aosDNSNetConf{
		Type:         "dnsname",
		MultiDomain:  true,
		DomainName:   networkID,
		Capabilities: map[string]bool{"aliases": true},
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

func prepareNetworkConfigList(networkDir, instanceID, networkID string, subnetwork *net.IPNet,
	params *NetworkParams,
) (cniNetworkConfig *cni.NetworkConfigList, err error) {
	networkConfig := cniNetwork{Name: networkID, CNIVersion: cniVersion}

	// Bridge

	bridgeConfig, err := getBridgePluginConfig(networkDir, networkID, subnetwork)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	networkConfig.Plugins = append(networkConfig.Plugins, bridgeConfig)

	// Firewall

	if len(params.AllowedConnections) > 0 || len(params.ExposedPorts) > 0 {
		firefallConfig, err := getFirewallPluginConfig(instanceID, params.ExposedPorts, params.AllowedConnections)
		if err != nil {
			return nil, aoserrors.Wrap(err)
		}

		networkConfig.Plugins = append(networkConfig.Plugins, firefallConfig)
	}

	// Bandwidth

	if params.IngressKbit > 0 || params.EgressKbit > 0 {
		bandwidthConfig, err := getBandwidthPluginConfig(params.IngressKbit, params.EgressKbit)
		if err != nil {
			return nil, aoserrors.Wrap(err)
		}

		networkConfig.Plugins = append(networkConfig.Plugins, bandwidthConfig)
	}

	// DNS

	dnsConfig, err := getDNSPluginConfig(networkID)
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
