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
	"sync"

	cni "github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/plugins/plugins/ipam/host-local/backend/allocator"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netns"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	bridgePrefix     = "br-"
	containerIfName  = "eth0"
	pathToNetNs      = "/run/netns"
	cniBinPath       = "/opt/cni/bin"
	pathToCNINetwork = "/var/lib/cni/networks/"
	cniVersion       = "0.4.0"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// NetworkManager network manager instance
type NetworkManager struct {
	cniConfig      *cni.CNIConfig
	ipamSubnetwork *ipSubnetwork
	hosts          []config.Host
	sync.Mutex
}

// NetworkParams network parameters set for service
type NetworkParams struct {
	Hostname string
	Aliases  []string
}

type cniPlugins struct {
	Name       string        `json:"name"`
	CNIVersion string        `json:"cniVersion"`
	Plugins    []interface{} `json:"plugins"`
}

type bridgeNetConf struct {
	Type         string               `json:"type"`
	BrName       string               `json:"bridge"`
	IsGW         bool                 `json:"isGateway"`
	IsDefaultGW  bool                 `json:"isDefaultGateway,omitempty"`
	ForceAddress bool                 `json:"forceAddress,omitempty"`
	IPMasq       bool                 `json:"ipMasq"`
	MTU          int                  `json:"mtu,omitempty"`
	HairpinMode  bool                 `json:"hairpinMode"`
	PromiscMode  bool                 `json:"promiscMode,omitempty"`
	Vlan         int                  `json:"vlan,omitempty"`
	IPAM         allocator.IPAMConfig `json:"ipam"`
}

type firewallNetConf struct {
	Type                   string `json:"type"`
	Backend                string `json:"backend"`
	IptablesAdminChainName string `json:"iptablesAdminChainName,omitempty"`
	FirewalldZone          string `json:"firewalldZone,omitempty"`
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates network manager instance
func New(cfg *config.Config) (manager *NetworkManager, err error) {
	log.Debug("Create network manager")

	manager = &NetworkManager{hosts: cfg.Hosts}

	manager.cniConfig = cni.NewCNIConfigWithCacheDir([]string{cniBinPath}, cfg.WorkingDir, nil)
	if manager.ipamSubnetwork, err = newIPam(); err != nil {
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

// DeleteNetwork deletes SP network
func (manager *NetworkManager) DeleteNetwork(spID string) (err error) {
	log.WithFields(log.Fields{"spID": spID}).Debug("Delete network")

	filesServiceID, _ := ioutil.ReadDir(path.Join(pathToCNINetwork, spID))
	for _, serviceIDFile := range filesServiceID {
		if netErr := manager.tryRemoveServiceFromNetwork(serviceIDFile.Name(), spID); netErr != nil {
			err = netErr
		}
	}

	if clearErr := manager.postSPNetworkClear(spID); clearErr != nil {
		err = clearErr
	}

	return err
}

// AddServiceToNetwork adds service to SP network
func (manager *NetworkManager) AddServiceToNetwork(serviceID, spID string) (err error) {
	log.WithFields(log.Fields{"serviceID": serviceID, "spID": spID}).Debug("Add service to network")

	manager.Lock()
	defer manager.Unlock()

	ipSubnet, exist := manager.ipamSubnetwork.tryToGetExistIPNetFromPool(spID)
	if !exist {
		if ipSubnet, err = checkExistNetInterface(bridgePrefix + spID); err != nil {
			if ipSubnet, _, err = manager.ipamSubnetwork.requestIPNetPool(spID); err != nil {
				return err
			}
		}
	}

	if err = createNetNS(serviceID); err != nil {
		return err
	}

	defer func() {
		if err != nil && !exist {
			manager.ipamSubnetwork.releaseIPNetPool(spID)
		}
	}()

	netConfig := prepareNetworkConfigList(spID, ipSubnet)

	runtimeConfig := &cni.RuntimeConf{
		ContainerID: serviceID,
		NetNS:       path.Join(pathToNetNs, serviceID),
		IfName:      containerIfName,
	}

	ctx := context.Background()
	err = manager.cniConfig.CheckNetworkList(ctx, netConfig, runtimeConfig)
	if err == nil {
		return fmt.Errorf("service %s already in SP network %s", serviceID, spID)
	}

	resAdd, err := manager.cniConfig.AddNetworkList(ctx, netConfig, runtimeConfig)
	if err != nil {
		return err
	}

	result, _ := current.GetResult(resAdd)

	if len(result.IPs) == 0 {
		return fmt.Errorf("error getting IP address for service %s", serviceID)
	}

	log.WithFields(log.Fields{"serviceID": serviceID, "IP": result.IPs[0].Address.IP.String()}).Debug("The service has been added to the network")

	return nil
}

// RemoveServiceFromNetwork removes service from network
func (manager *NetworkManager) RemoveServiceFromNetwork(serviceID, spID string) (err error) {
	log.WithFields(log.Fields{"serviceID": serviceID}).Debug("Remove service from network")

	defer netns.DeleteNamed(serviceID)

	if result, _ := manager.IsServiceInNetwork(serviceID, spID); !result {
		log.Warnf("Service %s is not in network %s", serviceID, spID)
		return nil
	}

	manager.Lock()
	defer manager.Unlock()

	netConfigByte, runtimeConfig, err := manager.getCNICachedResult(serviceID, spID)
	if err != nil {
		return err
	}

	netConfig, err := cni.ConfListFromBytes(netConfigByte)
	if err != nil {
		return err
	}

	ctx := context.Background()

	if err = manager.cniConfig.DelNetworkList(ctx, netConfig, runtimeConfig); err != nil {
		return err
	}

	log.WithFields(log.Fields{"serviceID": serviceID}).Debug("Service successfully removed from network")

	return nil
}

// IsServiceInNetwork returns true if service belongs to network
func (manager *NetworkManager) IsServiceInNetwork(serviceID, spID string) (result bool, err error) {
	log.WithFields(log.Fields{"serviceID": serviceID, "spID": spID}).Debug("Check present service in network")

	manager.Lock()
	defer manager.Unlock()

	resByte, runtimeConfig, err := manager.getCNICachedResult(serviceID, spID)
	if err != nil {
		return false, err
	}

	if resByte == nil {
		return false, fmt.Errorf("Service is not in network")
	}

	netConfig, err := cni.ConfListFromBytes(resByte)

	ctx := context.Background()
	err = manager.cniConfig.CheckNetworkList(ctx, netConfig, runtimeConfig)

	if err != nil {
		return false, err
	}

	return true, nil
}

// GetServiceIP return service IP address
func (manager *NetworkManager) GetServiceIP(serviceID, spID string) (ip string, err error) {
	log.WithFields(log.Fields{"serviceID": serviceID, "spID": spID}).Debug("Get service IP")

	manager.Lock()
	defer manager.Unlock()

	runtimeConfig, netConfig := getRuntimeNetConfig(serviceID, spID)

	cachedResult, err := manager.cniConfig.GetNetworkListCachedResult(netConfig, runtimeConfig)

	if err != nil || cachedResult == nil {
		return "", err
	}

	result, err := current.GetResult(cachedResult)
	if err != nil {
		return "", err
	}

	if len(result.IPs) == 0 {
		return "", fmt.Errorf("Error in getting the IP address for the service: %s", serviceID)
	}

	ip = result.IPs[0].Address.IP.String()

	log.Debugf("IP address %s for service %s", ip, serviceID)

	return ip, nil
}

// DeleteAllNetworks deletes all networks
func (manager *NetworkManager) DeleteAllNetworks() (err error) {
	log.Debug("Delete all networks")

	filesSpID, _ := ioutil.ReadDir(pathToCNINetwork)

	for _, spIDFile := range filesSpID {
		filesServiceID, _ := ioutil.ReadDir(path.Join(pathToCNINetwork, spIDFile.Name()))
		for _, serviceIDFile := range filesServiceID {
			if netErr := manager.tryRemoveServiceFromNetwork(serviceIDFile.Name(), spIDFile.Name()); netErr != nil {
				err = netErr
			}
		}
		if clearErr := manager.postSPNetworkClear(spIDFile.Name()); clearErr != nil {
			err = clearErr
		}
	}
	os.RemoveAll(pathToCNINetwork)

	return err
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (manager *NetworkManager) postSPNetworkClear(spID string) (err error) {
	manager.ipamSubnetwork.releaseIPNetPool(spID)

	if err = removeBridgeInterface(spID); err != nil {
		return err
	}

	return nil
}

func (manager *NetworkManager) getCNICachedResult(serviceID, spID string) (cachedConfig []byte, runtimeConfig *cni.RuntimeConf, err error) {
	runtimeConfig, netConfig := getRuntimeNetConfig(serviceID, spID)
	cachedConfig, _, err = manager.cniConfig.GetNetworkListCachedConfig(netConfig, runtimeConfig)

	return cachedConfig, runtimeConfig, err
}

func getRuntimeNetConfig(serviceID, spID string) (*cni.RuntimeConf, *cni.NetworkConfigList) {
	runtimeConfig := &cni.RuntimeConf{
		ContainerID: serviceID,
		NetNS:       path.Join(pathToNetNs, serviceID),
		IfName:      containerIfName,
	}

	networkingConfig := &cni.NetworkConfigList{
		Name:       spID,
		CNIVersion: cniVersion,
	}

	return runtimeConfig, networkingConfig
}

func (manager *NetworkManager) tryRemoveServiceFromNetwork(serviceIDFileName, spIDFileName string) error {
	// skipped files
	lockFileName := "lock"
	reservedFileName := "last_reserved_ip.0"

	if serviceIDFileName == reservedFileName || serviceIDFileName == lockFileName {
		return nil
	}

	serviceID, err := readServiceIDFromFile(path.Join(pathToCNINetwork, spIDFileName, serviceIDFileName))
	if err != nil {
		return nil
	}

	if result, _ := manager.IsServiceInNetwork(serviceID, spIDFileName); result {
		if netErr := manager.RemoveServiceFromNetwork(serviceID, spIDFileName); netErr != nil {
			return netErr
		}
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
		if line != containerIfName {
			cniServiceInfo = append(cniServiceInfo, line)
		}
	}
	if len(cniServiceInfo) != 1 {
		return "", fmt.Errorf("incorrect file content. There should be a container ID and a network interface name")
	}

	return cniServiceInfo[0], nil
}

func prepareNetworkConfigList(nameService string, subnetwork *net.IPNet) (cniNetworkConfig *cni.NetworkConfigList) {
	minIPRange, maxIPRange := getIPAddressRange(subnetwork)
	_, defaultRoute, _ := net.ParseCIDR("0.0.0.0/0")

	configBridge := bridgeNetConf{
		Type:        "bridge",
		BrName:      bridgePrefix + nameService,
		IsGW:        true,
		IPMasq:      true,
		HairpinMode: true,
		IPAM: allocator.IPAMConfig{
			Type: "host-local",
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

	firewall := firewallNetConf{
		Type:    "firewall",
		Backend: "iptables",
	}

	dataBridge, _ := json.Marshal(configBridge)
	dataFirewall, _ := json.Marshal(firewall)

	plugins := []*cni.NetworkConfig{
		{
			Network: &types.NetConf{
				Type: configBridge.Type,
				IPAM: types.IPAM{Type: configBridge.IPAM.Type},
			},
			Bytes: []byte(dataBridge),
		},
		{
			Network: &types.NetConf{
				Type: firewall.Type,
			},
			Bytes: []byte(dataFirewall),
		},
	}

	networkPlugin := cniPlugins{
		Name:       nameService,
		CNIVersion: cniVersion,
		Plugins: []interface{}{
			configBridge,
			firewall,
		},
	}

	dataNetwork, _ := json.Marshal(networkPlugin)

	return &cni.NetworkConfigList{
		Name:       networkPlugin.Name,
		CNIVersion: networkPlugin.CNIVersion,
		Plugins:    plugins,
		Bytes:      []byte(dataNetwork),
	}
}
