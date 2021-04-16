// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
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
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/containernetworking/cni/libcni"
	cni "github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/plugins/plugins/ipam/host-local/backend/allocator"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netns"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	bridgePrefix     = "br-"
	serviceIfName    = "eth0"
	pathToNetNs      = "/run/netns"
	cniBinPath       = "/opt/cni/bin"
	cniVersion       = "0.4.0"
	adminChainPrefix = "SERVICE_"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// NetworkManager network manager instance
type NetworkManager struct {
	sync.Mutex
	cniConfig      *cni.CNIConfig
	ipamSubnetwork *ipSubnetwork
	hosts          []config.Host
	networkDir     string
}

// NetworkParams network parameters set for service
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

/*******************************************************************************
 * Vars
 ******************************************************************************/

var skipNetworkFileNames = []string{"lock", "last_reserved_ip.0"}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates network manager instance
func New(cfg *config.Config) (manager *NetworkManager, err error) {
	log.Debug("Create network manager")

	cniDir := path.Join(cfg.WorkingDir, "cni")

	manager = &NetworkManager{
		hosts:      cfg.Hosts,
		cniConfig:  cni.NewCNIConfigWithCacheDir([]string{cniBinPath}, cniDir, nil),
		networkDir: path.Join(cniDir, "networks"),
	}

	if manager.ipamSubnetwork, err = newIPam(); err != nil {
		return nil, err
	}

	if err = manager.DeleteAllNetworks(); err != nil {
		log.Errorf("Can't delete all networks: %s", err)
	}

	if err = os.RemoveAll(cniDir); err != nil {
		return nil, err
	}

	return manager, nil
}

// Close closes network manager instance
func (manager *NetworkManager) Close() (err error) {
	log.Debug("Close network manager")

	return nil
}

// GetNetNsPathByName get path to service network namespace
func GetNetNsPathByName(serviceID string) (pathToNetNS string) {
	return path.Join(pathToNetNs, serviceID)
}

// AddServiceToNetwork adds service to SP network
func (manager *NetworkManager) AddServiceToNetwork(serviceID, spID string, params NetworkParams) (err error) {
	manager.Lock()
	defer manager.Unlock()

	log.WithFields(log.Fields{"serviceID": serviceID, "spID": spID}).Debug("Add service to network")

	if err = manager.isServiceInNetwork(serviceID, spID); err == nil {
		log.WithFields(log.Fields{"serviceID": serviceID, "spID": spID}).Warn("Service already in the network")

		return err
	}

	ipSubnet, exist := manager.ipamSubnetwork.tryToGetExistIPNetFromPool(spID)
	if !exist {
		if ipSubnet, err = checkExistNetInterface(bridgePrefix + spID); err != nil {
			if ipSubnet, _, err = manager.ipamSubnetwork.requestIPNetPool(spID); err != nil {
				return err
			}
		}
	}

	defer func() {
		if err != nil {
			manager.ipamSubnetwork.releaseIPNetPool(spID)
		}
	}()

	if err = createNetNS(serviceID); err != nil {
		return err
	}

	defer func() {
		if err != nil {
			netns.DeleteNamed(serviceID)
		}
	}()

	netConfig, err := prepareNetworkConfigList(manager.networkDir, serviceID, spID, ipSubnet, &params)
	if err != nil {
		return err
	}

	runtimeConfig, err := prepareRuntimeConfig(serviceID, spID, params.Hostname, params.Aliases)
	if err != nil {
		return err
	}

	if _, err = manager.cniConfig.ValidateNetworkList(context.Background(), netConfig); err != nil {
		return err
	}

	defer func() {
		if err != nil {
			if err := manager.cniConfig.DelNetworkList(context.Background(), netConfig, runtimeConfig); err != nil {
				log.Errorf("Can't delete network list: %s", err)
			}
		}
	}()

	resAdd, err := manager.cniConfig.AddNetworkList(context.Background(), netConfig, runtimeConfig)
	if err != nil {
		return err
	}

	result, err := current.GetResult(resAdd)
	if err != nil {
		return err
	}

	if len(result.IPs) == 0 {
		return fmt.Errorf("error getting IP address for service %s", serviceID)
	}

	serviceIP := result.IPs[0].Address.IP.String()

	if params.HostsFilePath != "" {
		if err = writeHostToHostsFile(params.HostsFilePath, serviceIP,
			serviceID, params.Hostname, params.Hosts); err != nil {
			return err
		}
	}

	if params.ResolvConfFilePath != "" {
		mainServers := []string{"8.8.8.8"}

		if len(result.DNS.Nameservers) != 0 {
			mainServers = result.DNS.Nameservers
		}

		if err = writeResolveConfFile(params.ResolvConfFilePath, mainServers, params.DNSSevers); err != nil {
			return err
		}
	}

	log.WithFields(log.Fields{
		"serviceID": serviceID,
		"IP":        serviceIP,
	}).Debug("Service has been added to the network")

	return nil
}

// RemoveServiceFromNetwork removes service from network
func (manager *NetworkManager) RemoveServiceFromNetwork(serviceID, spID string) (err error) {
	manager.Lock()
	defer manager.Unlock()

	log.WithFields(log.Fields{"serviceID": serviceID}).Debug("Remove service from network")

	if err = manager.isServiceInNetwork(serviceID, spID); err != nil {
		log.WithFields(log.Fields{"serviceID": serviceID}).Warn("Service is not in network")

		return err
	}

	if err = manager.removeServiceFromNetwork(serviceID, spID); err != nil {
		return err
	}

	return nil
}

// IsServiceInNetwork returns true if service belongs to network
func (manager *NetworkManager) IsServiceInNetwork(serviceID, spID string) (err error) {
	manager.Lock()
	defer manager.Unlock()

	log.WithFields(log.Fields{"serviceID": serviceID, "spID": spID}).Debug("Check present service in network")

	return manager.isServiceInNetwork(serviceID, spID)
}

// GetServiceIP return service IP address
func (manager *NetworkManager) GetServiceIP(serviceID, spID string) (ip string, err error) {
	manager.Lock()
	defer manager.Unlock()

	log.WithFields(log.Fields{"serviceID": serviceID, "spID": spID}).Debug("Get service IP")

	cachedResult, err := manager.cniConfig.GetNetworkListCachedResult(getRuntimeNetConfig(serviceID, spID))
	if err != nil {
		return "", err
	}

	if cachedResult == nil {
		return "", fmt.Errorf("service %s not found in network %s", serviceID, spID)
	}

	result, err := current.GetResult(cachedResult)
	if err != nil {
		return "", err
	}

	if len(result.IPs) == 0 {
		return "", fmt.Errorf("error in getting the IP address for the service: %s", serviceID)
	}

	ip = result.IPs[0].Address.IP.String()

	log.Debugf("IP address %s for service %s", ip, serviceID)

	return ip, nil
}

// DeleteNetwork deletes SP network
func (manager *NetworkManager) DeleteNetwork(spID string) (err error) {
	manager.Lock()
	defer manager.Unlock()

	return manager.deleteNetwork(spID)
}

// DeleteAllNetworks deletes all networks
func (manager *NetworkManager) DeleteAllNetworks() (err error) {
	manager.Lock()
	defer manager.Unlock()

	log.Debug("Delete all networks")

	filesSpID, err := ioutil.ReadDir(manager.networkDir)
	if err != nil {
		return nil
	}

	for _, spIDFile := range filesSpID {
		if networkErr := manager.deleteNetwork(spIDFile.Name()); networkErr != nil {
			log.Errorf("Can't delete network: %s", err)

			if err == nil {
				err = networkErr
			}
		}
	}

	os.RemoveAll(manager.networkDir)

	return err
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (manager *NetworkManager) isServiceInNetwork(serviceID, spID string) (err error) {
	cachedResult, err := manager.cniConfig.GetNetworkListCachedResult(getRuntimeNetConfig(serviceID, spID))
	if err != nil {
		return err
	}

	if cachedResult == nil {
		return errors.Errorf("service %s is not in network %s", serviceID, spID)
	}

	return nil
}

func (manager *NetworkManager) deleteNetwork(spID string) (err error) {
	log.WithFields(log.Fields{"spID": spID}).Debug("Delete network")

	networkDir := path.Join(manager.networkDir, spID)

	if _, err := os.Stat(networkDir); err != nil {
		log.WithFields(log.Fields{"spID": spID}).Warn("Network doesn't exist")
		return nil
	}

	filesServiceID, err := ioutil.ReadDir(networkDir)
	if err != nil {
		return err
	}

	for _, serviceIDFile := range filesServiceID {
		skip := false

		for _, skipFile := range skipNetworkFileNames {
			if serviceIDFile.Name() == skipFile {
				skip = true

				break
			}
		}

		if skip {
			continue
		}

		if netErr := manager.tryRemoveServiceFromNetwork(serviceIDFile.Name(), spID); netErr != nil {
			if err == nil {
				err = netErr
			}
		}
	}

	if clearErr := manager.postSPNetworkClear(spID); clearErr != nil {
		if err == nil {
			err = clearErr
		}
	}

	os.RemoveAll(networkDir)

	return nil
}

func (manager *NetworkManager) removeServiceFromNetwork(serviceID, spID string) (err error) {
	defer netns.DeleteNamed(serviceID)

	networkConfig, runtimeConfig := getRuntimeNetConfig(serviceID, spID)

	confBytes, runtimeConfig, err := manager.cniConfig.GetNetworkListCachedConfig(networkConfig, runtimeConfig)
	if err != nil {
		return err
	}

	if confBytes == nil {
		return fmt.Errorf("service %s not found in network %s", serviceID, spID)
	}

	if networkConfig, err = libcni.ConfListFromBytes(confBytes); err != nil {
		return err
	}

	if err = manager.cniConfig.DelNetworkList(context.Background(), networkConfig, runtimeConfig); err != nil {
		return err
	}

	log.WithFields(log.Fields{"serviceID": serviceID}).Debug("Service successfully removed from network")

	return nil
}

func (manager *NetworkManager) postSPNetworkClear(spID string) (err error) {
	manager.ipamSubnetwork.releaseIPNetPool(spID)

	if err = removeBridgeInterface(spID); err != nil {
		return err
	}

	return nil
}

func (manager *NetworkManager) tryRemoveServiceFromNetwork(serviceIDFileName, spIDFileName string) (err error) {
	serviceID, err := readServiceIDFromFile(path.Join(manager.networkDir, spIDFileName, serviceIDFileName))
	if err != nil {
		return nil
	}

	if err = manager.isServiceInNetwork(serviceID, spIDFileName); err != nil {
		return nil
	}

	if err = manager.removeServiceFromNetwork(serviceID, spIDFileName); err != nil {
		return err
	}

	return nil
}

func readServiceIDFromFile(pathToServiceID string) (serviceID string, err error) {
	f, err := os.Open(pathToServiceID)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var cniServiceInfo []string
	for scanner.Scan() {
		line := scanner.Text()
		if line != serviceIfName {
			cniServiceInfo = append(cniServiceInfo, line)
		}
	}
	if len(cniServiceInfo) != 1 {
		return "", fmt.Errorf("incorrect file content. There should be a container ID and a network interface name")
	}

	return cniServiceInfo[0], nil
}

func getBridgePluginConfig(networkDir, spID string, subnetwork *net.IPNet) (config json.RawMessage, err error) {
	minIPRange, maxIPRange := getIPAddressRange(subnetwork)
	_, defaultRoute, _ := net.ParseCIDR("0.0.0.0/0")

	configBridge := &bridgeNetConf{
		Type:        "bridge",
		Bridge:      bridgePrefix + spID,
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

	return json.Marshal(configBridge)
}

func getFirewallPluginConfig(serviceID string, exposedPorts, allowedConnections []string) (config json.RawMessage, err error) {
	aosFirewall := &aosFirewallNetConf{
		Type:                   "aos-firewall",
		UUID:                   serviceID,
		IptablesAdminChainName: adminChainPrefix + serviceID,
		AllowPublicConnections: true,
	}

	// ExposedPorts format port/protocol
	for _, exposePort := range exposedPorts {
		portConfig := strings.Split(exposePort, "/")
		if len(portConfig) > 2 || len(portConfig) == 0 {
			return nil, fmt.Errorf("unsupported ExposedPorts format %s", exposePort)
		}

		input := inputAccessConfig{Port: portConfig[0], Protocol: "tcp"}
		if len(portConfig) == 2 {
			input.Protocol = portConfig[1]
		}

		aosFirewall.InputAccess = append(aosFirewall.InputAccess, input)
	}

	// AllowedConnections format service-UUID/port/protocol
	for _, allowConn := range allowedConnections {
		connConf := strings.Split(allowConn, "/")
		if len(connConf) > 3 || len(connConf) < 2 {
			return nil, fmt.Errorf("unsupported AllowedConnections format %s", connConf)
		}

		output := outputAccessConfig{UUID: connConf[0], Port: connConf[1], Protocol: "tcp"}
		if len(connConf) == 3 {
			output.Protocol = connConf[2]
		}

		aosFirewall.OutputAccess = append(aosFirewall.OutputAccess, output)
	}

	return json.Marshal(aosFirewall)
}

func getBandwidthPluginConfig(ingressKbit, egressKbit uint64) (config json.RawMessage, err error) {
	bandwidth := &bandwidthNetConf{
		Type: "bandwidth",
	}

	// the burst argument was selected relative to the mtu network interface
	burst := uint64(12800) // bits == 1600 byte

	if ingressKbit > 0 {
		bandwidth.IngressRate = ingressKbit * 1000
		bandwidth.IngressBurst = burst
	}

	if egressKbit > 0 {
		bandwidth.EgressRate = egressKbit * 1000
		bandwidth.EgressBurst = burst
	}

	return json.Marshal(bandwidth)
}

func getDNSPluginConfig(spID string) (config json.RawMessage, err error) {
	configDNS := &aosDNSNetConf{
		Type:         "dnsname",
		MultiDomain:  true,
		DomainName:   spID,
		Capabilities: map[string]bool{"aliases": true},
	}

	return json.Marshal(configDNS)
}

func getRuntimeNetConfig(serviceID, spID string) (networkingConfig *cni.NetworkConfigList, runtimeConfig *cni.RuntimeConf) {
	networkingConfig = &cni.NetworkConfigList{
		Name:       spID,
		CNIVersion: cniVersion,
	}

	runtimeConfig = &cni.RuntimeConf{
		ContainerID: serviceID,
		NetNS:       path.Join(pathToNetNs, serviceID),
		IfName:      serviceIfName,
	}

	return networkingConfig, runtimeConfig
}

func prepareRuntimeConfig(serviceID, spID, hostname string, aliases []string) (runtimeConfig *cni.RuntimeConf, err error) {
	runtimeConfig = &cni.RuntimeConf{
		ContainerID: serviceID,
		NetNS:       GetNetNsPathByName(serviceID),
		IfName:      serviceIfName,
		Args: [][2]string{
			{"IgnoreUnknown", "1"},
			{"K8S_POD_NAME", serviceID},
		},
		CapabilityArgs: make(map[string]interface{}),
	}

	if hostname != "" {
		aliases = append([]string{hostname}, aliases...)
	}

	if len(aliases) != 0 {
		runtimeConfig.CapabilityArgs["aliases"] = map[string][]string{spID: aliases}
	}

	return runtimeConfig, nil
}

func prepareNetworkConfigList(networkDir, serviceID, spID string, subnetwork *net.IPNet,
	params *NetworkParams) (cniNetworkConfig *cni.NetworkConfigList, err error) {
	networkConfig := cniNetwork{Name: spID, CNIVersion: cniVersion}

	// Bridge

	bridgeConfig, err := getBridgePluginConfig(networkDir, spID, subnetwork)
	if err != nil {
		return nil, err
	}

	networkConfig.Plugins = append(networkConfig.Plugins, bridgeConfig)

	// Firewall

	if len(params.AllowedConnections) > 0 || len(params.ExposedPorts) > 0 {
		firefallConfig, err := getFirewallPluginConfig(serviceID, params.ExposedPorts, params.AllowedConnections)
		if err != nil {
			return nil, err
		}

		networkConfig.Plugins = append(networkConfig.Plugins, firefallConfig)
	}

	// Bandwidth

	if params.IngressKbit > 0 || params.EgressKbit > 0 {
		bandwidthConfig, err := getBandwidthPluginConfig(params.IngressKbit, params.EgressKbit)
		if err != nil {
			return nil, err
		}

		networkConfig.Plugins = append(networkConfig.Plugins, bandwidthConfig)
	}

	// DNS

	dnsConfig, err := getDNSPluginConfig(spID)
	if err != nil {
		return nil, err
	}

	networkConfig.Plugins = append(networkConfig.Plugins, dnsConfig)

	networkConfigBytes, err := json.Marshal(networkConfig)
	if err != nil {
		return nil, err
	}

	return cni.ConfListFromBytes(networkConfigBytes)
}
