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

package networkmanager_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	cni "github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/networkmanager"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testCNIInterface struct {
	networkConfig        *cni.NetworkConfigList
	runtimeConfig        *cni.RuntimeConf
	errorAddNetwork      bool
	emptyIPAddress       bool
	errorValidateNetwork bool
}

type cniNetwork struct {
	Name       string            `json:"name"`
	CNIVersion string            `json:"cniVersion"`
	Plugins    []json.RawMessage `json:"plugins"`
}

type testDNSParam struct {
	hosts              []string
	expectedCountHosts int
	networkID          string
	expectedError      bool
}

type testPluginsData struct {
	params        networkmanager.NetworkParams
	networkConfig string
	dnsParam      testDNSParam
}

type testTrafficMonitoringData struct {
	period                int
	expectedInputTraffic  uint64
	expectedOutputTraffic uint64
}

type trafficData struct {
	lastUpdate   time.Time
	currentValue uint64
}

type testTrafficStorage struct {
	chains             map[string]trafficData
	disableSaveTraffic bool
	disableLoadTraffic bool
}

type iptablesData struct {
	countChain int
	limit      uint64
}

type testIPTablesInterface struct {
	disableResetMonitoringTraffic bool
	chain                         map[string]iptablesData
	trafficLimitCounter           uint64
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var tmpDir string

/***********************************************************************************************************************
 * Init
 **********************************************************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/***********************************************************************************************************************
 * Main
 **********************************************************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	ret := m.Run()

	if err := os.RemoveAll(tmpDir); err != nil {
		log.Errorf("Can't remove tmp folder: %s", err)
	}

	os.Exit(ret)
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestDeleteAllNetwork(t *testing.T) {
	// need to test cleaning networking at startup
	networkDir := path.Join(tmpDir, "cni", "networks", "network0")

	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatalf("Can't create network dir: %s", err)
	}

	instanceCNIPath := path.Join(networkDir, "instance0")

	if err := ioutil.WriteFile(instanceCNIPath, []byte("instance0"), 0o600); err != nil {
		t.Fatalf("Can't write network instance data: %s", err)
	}

	networkmanager.CNIPlugins = &testCNIInterface{}

	manager, err := networkmanager.New(&config.Config{WorkingDir: tmpDir}, nil)
	if err != nil {
		t.Fatalf("Can't create network manager: %s", err)
	}
	defer manager.Close()

	if _, err := os.Stat(instanceCNIPath); err == nil {
		t.Error("Instance CNI file should be removed")
	}
}

func TestBaseNetwork(t *testing.T) {
	container0Path := path.Join(tmpDir, "instance0")

	if err := os.MkdirAll(container0Path, 0o755); err != nil {
		t.Fatalf("Can't create instance dir: %s", err)
	}

	cniInterface := &testCNIInterface{}

	networkmanager.CNIPlugins = cniInterface
	networkmanager.GetIPSubnet = getIPSubnet

	defer func() {
		networkmanager.GetIPSubnet = nil
	}()

	manager, err := networkmanager.New(&config.Config{WorkingDir: tmpDir}, nil)
	if err != nil {
		t.Fatalf("Can't create network manager: %s", err)
	}
	defer manager.Close()

	plugins := createPlugins([]string{
		createBridgePlugin(tmpDir + `/`),
		createFirewallPlugin("", ""), createDNSPlugin(),
	})

	if _, err := manager.GetInstanceIP("instance0", "network0"); err == nil {
		t.Error("Instance IP must not be present")
	}

	hostsPath := path.Join(container0Path, "hosts")
	resolvConfPath := path.Join(container0Path, "resolv.conf")

	if err := manager.AddInstanceToNetwork("instance0", "network0", networkmanager.NetworkParams{
		Hostname:           "myhost",
		HostsFilePath:      hostsPath,
		ResolvConfFilePath: resolvConfPath,
	}); err != nil {
		t.Fatalf("Can't add instance to network: %s", err)
	}

	if cniInterface.runtimeConfig.ContainerID != "instance0" {
		t.Errorf(
			"Unexpected runtime containerID. Current %s, expected instance0", cniInterface.runtimeConfig.ContainerID)
	}

	if cniInterface.runtimeConfig.IfName == "" {
		t.Error("Interface name must be present in container configuration")
	}

	if cniInterface.runtimeConfig.NetNS == "" {
		t.Error("Path to network namespace must be present in container configuration")
	}

	for _, arg := range cniInterface.runtimeConfig.Args {
		if len(arg) < 2 {
			t.Error("Unexpected size of args")
		}

		if arg[0] != "IgnoreUnknown" && arg[0] != "K8S_POD_NAME" {
			t.Error("Unexpected args. Expected IgnoreUnknown or K8S_POD_NAME")
		}

		if arg[0] == "K8S_POD_NAME" && arg[1] != "instance0" {
			t.Error("Expected K8S_POD_NAME be equal to instance0")
		}
	}

	if err := manager.AddInstanceToNetwork("instance0", "network0", networkmanager.NetworkParams{
		Hostname:           "myhost",
		HostsFilePath:      hostsPath,
		ResolvConfFilePath: resolvConfPath,
	}); err == nil {
		t.Fatal("There should be an error instance0 is already in the network0")
	}

	content, err := readFromFile(hostsPath)
	if err != nil {
		t.Fatalf("Can't read from hosts file: %s", err)
	}

	if content != "127.0.0.1localhost::1localhostip6-localhostip6-loopback192.168.0.1network0myhost" {
		t.Error("Wrong contents of the host file")
	}

	content, err = readFromFile(resolvConfPath)
	if err != nil {
		t.Fatalf("Can't read from resolv.conf file: %s", err)
	}

	if content != "nameserver1.1.1.1" {
		t.Error("Wrong contents of the resolv.conf file")
	}

	if string(cniInterface.networkConfig.Bytes) != plugins {
		t.Errorf("Wrong network config: %s", string(cniInterface.networkConfig.Bytes))
	}

	ip, err := manager.GetInstanceIP("instance0", "network0")
	if err != nil {
		t.Fatalf("Can't get instance IP: %s", err)
	}

	if ip != "192.168.0.1" {
		t.Errorf("Incorrect ip address expected 192.168.0.1 current %s", ip)
	}

	if err := manager.RemoveInstanceFromNetwork("instance0", "network0"); err != nil {
		t.Fatalf("Can't remove instance from network: %s", err)
	}

	if _, err := manager.GetInstanceIP("instance0", "network0"); err == nil {
		t.Fatal("Instance should not be in network")
	}
}

func TestFirewallPlugin(t *testing.T) {
	testData := []testPluginsData{
		{
			params: networkmanager.NetworkParams{
				ExposedPorts:       []string{"900"},
				AllowedConnections: []string{"instance0" + "/900"},
			},
			networkConfig: createPlugins([]string{
				createBridgePlugin(""),
				createFirewallPlugin("900", "900"),
				createDNSPlugin(),
			}),
		},
		{
			params: networkmanager.NetworkParams{
				ExposedPorts:       []string{"800"},
				AllowedConnections: []string{"instance0" + "/800"},
			},
			networkConfig: createPlugins([]string{
				createBridgePlugin(""),
				createFirewallPlugin("800", "800"),
				createDNSPlugin(),
			}),
		},
	}

	cniInterface := &testCNIInterface{}

	networkmanager.CNIPlugins = cniInterface
	networkmanager.GetIPSubnet = getIPSubnet

	defer func() {
		networkmanager.GetIPSubnet = nil
	}()

	manager, err := networkmanager.New(&config.Config{}, nil)
	if err != nil {
		t.Fatalf("Can't create network manager: %s", err)
	}
	defer manager.Close()

	for _, item := range testData {
		if err := manager.AddInstanceToNetwork("instance0", "network0", item.params); err != nil {
			t.Fatalf("Can't add instance to network: %s", err)
		}

		if string(cniInterface.networkConfig.Bytes) != item.networkConfig {
			t.Errorf("Wrong network config: %s", string(cniInterface.networkConfig.Bytes))
		}

		if err := manager.RemoveInstanceFromNetwork("instance0", "network0"); err != nil {
			t.Fatalf("Can't remove instance from network: %s", err)
		}
	}
}

func TestBandwithPlugin(t *testing.T) {
	testData := []testPluginsData{
		{
			params: networkmanager.NetworkParams{
				IngressKbit: 1200,
				EgressKbit:  1200,
			},
			networkConfig: createPlugins([]string{
				createBridgePlugin(""),
				createFirewallPlugin("", ""),
				createBandwithPlugin(1200000, 1200000),
				createDNSPlugin(),
			}),
		},
		{
			params: networkmanager.NetworkParams{
				IngressKbit: 400,
				EgressKbit:  300,
			},
			networkConfig: createPlugins([]string{
				createBridgePlugin(""),
				createFirewallPlugin("", ""),
				createBandwithPlugin(400000, 300000),
				createDNSPlugin(),
			}),
		},
	}

	cniInterface := &testCNIInterface{}

	networkmanager.CNIPlugins = cniInterface
	networkmanager.GetIPSubnet = getIPSubnet

	defer func() {
		networkmanager.GetIPSubnet = nil
	}()

	manager, err := networkmanager.New(&config.Config{}, nil)
	if err != nil {
		t.Fatalf("Can't create network manager: %s", err)
	}
	defer manager.Close()

	for _, item := range testData {
		if err := manager.AddInstanceToNetwork("instance0", "network0", item.params); err != nil {
			t.Fatalf("Can't add instance to network: %s", err)
		}

		if string(cniInterface.networkConfig.Bytes) != item.networkConfig {
			t.Errorf("Wrong network config: %s", string(cniInterface.networkConfig.Bytes))
		}

		if err := manager.RemoveInstanceFromNetwork("instance0", "network0"); err != nil {
			t.Fatalf("Can't remove instance from network: %s", err)
		}
	}
}

func TestDNSPluginPositive(t *testing.T) {
	testData := []testPluginsData{
		{
			params: networkmanager.NetworkParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					Instance:  0,
					SubjectID: "user1",
					ServiceID: "service0",
				},
			},
			dnsParam: testDNSParam{
				hosts: []string{
					"0.user1.service0", "user1.service0", "0.user1.service0.network0", "user1.service0.network0",
				},
				expectedCountHosts: 4,
				networkID:          "network0",
			},
		},
		{
			params: networkmanager.NetworkParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					Instance:  1,
					SubjectID: "user1",
					ServiceID: "service0",
				},
				Hostname: "myHost",
			},
			dnsParam: testDNSParam{
				hosts:              []string{"myHost", "1.user1.service0", "1.user1.service0.network0"},
				expectedCountHosts: 3,
				networkID:          "network0",
			},
		},
		{
			params: networkmanager.NetworkParams{
				Hostname: "myHost1.domain",
			},
			dnsParam: testDNSParam{
				hosts:              []string{"myHost1.domain", "myHost1.domain.network0"},
				expectedCountHosts: 2,
				networkID:          "network0",
			},
		},
		{
			params: networkmanager.NetworkParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					Instance:  2,
					SubjectID: "user1",
					ServiceID: "service0",
				},
				Hostname: "myHost2",
				Aliases:  []string{"alias1", "alias2.domain", "alias3"},
			},
			dnsParam: testDNSParam{
				hosts: []string{
					"alias1", "alias2.domain", "alias3", "myHost2",
					"2.user1.service0", "alias2.domain.network0", "2.user1.service0.network0",
				},
				expectedCountHosts: 7,
				networkID:          "network0",
			},
		},
		{
			params: networkmanager.NetworkParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					Instance:  0,
					SubjectID: "user1",
					ServiceID: "service0",
				},
			},
			dnsParam: testDNSParam{
				hosts: []string{
					"0.user1.service0", "user1.service0", "0.user1.service0.network1", "user1.service0.network1",
				},
				expectedCountHosts: 4,
				networkID:          "network1",
			},
		},
		{
			params: networkmanager.NetworkParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					Instance:  2,
					SubjectID: "user1",
					ServiceID: "service0",
				},
				Hostname: "myHost2.domain",
				Aliases:  []string{"alias1.domain", "alias2", "alias3"},
			},
			dnsParam: testDNSParam{
				hosts: []string{
					"alias1.domain", "alias2", "alias3", "myHost2.domain", "2.user1.service0",
					"alias1.domain.network1", "myHost2.domain.network1", "2.user1.service0.network1",
				},
				expectedCountHosts: 8,
				networkID:          "network1",
			},
		},
	}

	cniInterface := &testCNIInterface{}

	networkmanager.CNIPlugins = cniInterface
	networkmanager.GetIPSubnet = getIPSubnet

	defer func() {
		networkmanager.GetIPSubnet = nil
	}()

	manager, err := networkmanager.New(&config.Config{}, nil)
	if err != nil {
		t.Fatalf("Can't create network manager: %s", err)
	}
	defer manager.Close()

	for i, item := range testData {
		if err := manager.AddInstanceToNetwork(
			fmt.Sprintf("instance%d", i), item.dnsParam.networkID, item.params); err != nil {
			t.Fatalf("Can't add instance to network: %s", err)
		}

		aliases, ok := cniInterface.runtimeConfig.CapabilityArgs["aliases"].(map[string][]string)
		if !ok {
			t.Error("Incorrect aliases type")
		}

		hosts := aliases[item.dnsParam.networkID]
		if item.dnsParam.expectedCountHosts != len(hosts) {
			t.Errorf("Incorrect hosts count, expected %d", item.dnsParam.expectedCountHosts)
		}

		if !reflect.DeepEqual(hosts, item.dnsParam.hosts) {
			t.Errorf("Incorrect list of hosts %s", hosts)
		}
	}

	for i, item := range testData {
		if err := manager.RemoveInstanceFromNetwork(fmt.Sprintf("instance%d", i), item.dnsParam.networkID); err != nil {
			t.Fatalf("Can't remove instance from network: %s", err)
		}
	}
}

func TestDNSPluginNegative(t *testing.T) {
	testData := []testPluginsData{
		{
			params: networkmanager.NetworkParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					Instance:  0,
					SubjectID: "user1",
					ServiceID: "service0",
				},
			},
			dnsParam: testDNSParam{
				networkID:     "network0",
				expectedError: false,
			},
		},
		{
			params: networkmanager.NetworkParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					Instance:  0,
					SubjectID: "user1",
					ServiceID: "service0",
				},
				Hostname: "myHost",
			},
			dnsParam: testDNSParam{
				networkID:     "network0",
				expectedError: true,
			},
		},
		{
			params: networkmanager.NetworkParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					Instance:  0,
					SubjectID: "user1",
					ServiceID: "service0",
				},
				Aliases: []string{"alias1", "alias2", "alias3"},
			},
			dnsParam: testDNSParam{
				networkID:     "network1",
				expectedError: false,
			},
		},
		{
			params: networkmanager.NetworkParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					Instance:  1,
					SubjectID: "user2",
					ServiceID: "service0",
				},
				Hostname: "myHost",
				Aliases:  []string{"alias1"},
			},
			dnsParam: testDNSParam{
				networkID:     "network1",
				expectedError: true,
			},
		},
	}

	cniInterface := &testCNIInterface{}

	networkmanager.CNIPlugins = cniInterface
	networkmanager.GetIPSubnet = getIPSubnet

	defer func() {
		networkmanager.GetIPSubnet = nil
	}()

	manager, err := networkmanager.New(&config.Config{}, nil)
	if err != nil {
		t.Fatalf("Can't create network manager: %s", err)
	}
	defer manager.Close()

	for i, item := range testData {
		err := manager.AddInstanceToNetwork(fmt.Sprintf("instance%d", i), item.dnsParam.networkID, item.params)
		if !item.dnsParam.expectedError && err != nil {
			t.Fatalf("Can't add instance to network: %s", err)
			continue
		}

		if item.dnsParam.expectedError && err == nil {
			t.Fatalf("Should be error: can't add instance to network")
		}
	}
}

func TestTrafficMonitoring(t *testing.T) {
	networkmanager.CNIPlugins = &testCNIInterface{}

	storage := testTrafficStorage{chains: make(map[string]trafficData)}

	iptableInterface := &testIPTablesInterface{
		disableResetMonitoringTraffic: true,
		chain:                         make(map[string]iptablesData),
		trafficLimitCounter:           20,
	}

	networkmanager.IPTables = iptableInterface

	manager, err := networkmanager.New(&config.Config{}, &storage)
	if err != nil {
		t.Fatalf("Can't create network manager: %s", err)
	}

	testData := []testTrafficMonitoringData{
		{
			period:                networkmanager.DayPeriod,
			expectedInputTraffic:  20,
			expectedOutputTraffic: 20,
		},
		{
			period:                networkmanager.HourPeriod,
			expectedInputTraffic:  40,
			expectedOutputTraffic: 40,
		},
		{
			period:                networkmanager.MonthPeriod,
			expectedInputTraffic:  60,
			expectedOutputTraffic: 60,
		},
		{
			period:                networkmanager.YearPeriod,
			expectedInputTraffic:  80,
			expectedOutputTraffic: 80,
		},
		{
			period:                networkmanager.MinutePeriod,
			expectedInputTraffic:  100,
			expectedOutputTraffic: 100,
		},
	}

	if err := manager.AddInstanceToNetwork("instance0", "network0", networkmanager.NetworkParams{
		DownloadLimit: 300,
		UploadLimit:   300,
	}); err != nil {
		t.Fatalf("Can't add instance to network: %s", err)
	}

	for _, item := range testData {
		if err := manager.SetTrafficPeriod(item.period); err != nil {
			t.Errorf("Can't set traffic period: %s", err)
		}

		in, out, err := manager.GetInstanceTraffic("instance0")
		if err != nil {
			t.Fatalf("Can't get instance traffic: %s", err)
		}

		if in != item.expectedInputTraffic && out != item.expectedOutputTraffic {
			t.Error("Unexpected instance traffic")
		}
	}

	if err := manager.RemoveInstanceFromNetwork("instance0", "network0"); err != nil {
		t.Fatalf("Can't remove instance from network: %s", err)
	}

	networkmanager.IsSamePeriod = iptableInterface.isSamePeriod

	testData = []testTrafficMonitoringData{
		{
			expectedInputTraffic:  40,
			expectedOutputTraffic: 40,
		},
		{
			expectedInputTraffic:  60,
			expectedOutputTraffic: 60,
		},
		{
			expectedInputTraffic:  60,
			expectedOutputTraffic: 60,
		},
		{
			expectedInputTraffic:  0,
			expectedOutputTraffic: 0,
		},
		{
			expectedInputTraffic:  20,
			expectedOutputTraffic: 20,
		},
	}

	if err := manager.AddInstanceToNetwork("instance1", "network0", networkmanager.NetworkParams{
		DownloadLimit: 40,
		UploadLimit:   40,
	}); err != nil {
		t.Fatalf("Can't add instance to network: %s", err)
	}

	in, out, err := manager.GetSystemTraffic()
	if err != nil {
		t.Errorf("Can't get system traffic: %s", err)
	}

	if in != 160 && out != 160 {
		t.Error("Unexpected system traffic")
	}

	for i := 0; i < 3; i++ {
		in, out, err = manager.GetInstanceTraffic("instance1")
		if err != nil {
			t.Fatalf("Can't get instance traffic: %s", err)
		}

		if in != testData[i].expectedInputTraffic && out != testData[i].expectedOutputTraffic {
			t.Error("Unexpected instance traffic")
		}
	}

	iptableInterface.disableResetMonitoringTraffic = false

	in, out, err = manager.GetInstanceTraffic("instance1")
	if err != nil {
		t.Fatalf("Can't get instance traffic: %s", err)
	}

	if in != 0 && out != 0 {
		t.Error("Unexpected instance traffic")
	}

	iptableInterface.disableResetMonitoringTraffic = true

	for i := 3; i < len(testData); i++ {
		in, out, err = manager.GetInstanceTraffic("instance1")
		if err != nil {
			t.Fatalf("Can't get instance traffic: %s", err)
		}

		if in != testData[i].expectedInputTraffic && out != testData[i].expectedOutputTraffic {
			t.Error("Unexpected instance traffic")
		}
	}

	if err := manager.SetTrafficPeriod(networkmanager.YearPeriod + 1); err == nil {
		t.Error("Should be an error: incorrect traffic period period")
	}

	if err := manager.RemoveInstanceFromNetwork("instance1", "network0"); err != nil {
		t.Fatalf("Can't remove instance from network: %s", err)
	}

	if _, _, err := manager.GetInstanceTraffic("instance1"); err == nil {
		t.Error("Should be an error: can't get instance traffic after remove instance from network")
	}

	manager.Close()

	if _, _, err = manager.GetSystemTraffic(); err == nil {
		t.Error("Should be error: can't get system traffic after close network manager")
	}

	iptableInterface.Clear()

	if _, _, err = manager.GetInstanceTraffic("instance1"); err == nil {
		t.Error("Should be error: can't get instance traffic iptables rules are empty")
	}

	if _, _, err = manager.GetSystemTraffic(); err == nil {
		t.Error("Should be error: can't get system traffic iptables rules are empty")
	}

	storage.disableSaveTraffic = true

	manager, err = networkmanager.New(&config.Config{}, &storage)
	if err != nil {
		t.Fatalf("Can't create network manager: %s", err)
	}

	storage.disableLoadTraffic = true

	if err := manager.AddInstanceToNetwork("instance2", "network0", networkmanager.NetworkParams{
		DownloadLimit: 40,
		UploadLimit:   40,
	}); err == nil {
		t.Fatalf("Should be error: can't add instance to network")
	}

	manager.Close()
}

func TestAddNetworkFail(t *testing.T) {
	cniInterface := &testCNIInterface{
		errorAddNetwork: true,
		emptyIPAddress:  true,
	}

	networkmanager.CNIPlugins = cniInterface

	manager, err := networkmanager.New(&config.Config{}, nil)
	if err != nil {
		t.Fatalf("Can't create network manager: %s", err)
	}
	defer manager.Close()

	if err := manager.AddInstanceToNetwork("instance0", "network0", networkmanager.NetworkParams{}); err == nil {
		t.Error("Should be error: can't add instance to network")
	}

	cniInterface.errorAddNetwork = false

	if err := manager.AddInstanceToNetwork("instance0", "network0", networkmanager.NetworkParams{}); err == nil {
		t.Error("Should be error: can't add instance to network")
	}

	cniInterface.emptyIPAddress = false
	cniInterface.errorValidateNetwork = true

	if err := manager.AddInstanceToNetwork("instance0", "network0", networkmanager.NetworkParams{}); err == nil {
		t.Error("Should be error: can't add instance to network")
	}

	cniInterface.errorValidateNetwork = false

	if err := manager.AddInstanceToNetwork("instance0", "network0", networkmanager.NetworkParams{
		ExposedPorts: []string{"800/9000/10000"},
	}); err == nil {
		t.Error("Should be error: can't add instance to network")
	}

	if err := manager.AddInstanceToNetwork("instance0", "network0", networkmanager.NetworkParams{
		AllowedConnections: []string{"instance0" + "/800/900/1000"},
	}); err == nil {
		t.Error("Should be error: can't add instance to network")
	}

	networkmanager.GetIPSubnet = getIPSubnet

	defer func() {
		networkmanager.GetIPSubnet = nil
	}()

	if err := manager.AddInstanceToNetwork("instance0", "network0", networkmanager.NetworkParams{}); err != nil {
		t.Fatalf("Can't add instance to network: %s", err)
	}

	if err := manager.AddInstanceToNetwork("instance0", "network0", networkmanager.NetworkParams{}); err == nil {
		t.Errorf("Should be an error: instance is already in the network")
	}

	if err := manager.AddInstanceToNetwork("instance1", "network0", networkmanager.NetworkParams{
		Hostname:           "myhost",
		HostsFilePath:      "/path/hostfilepath",
		ResolvConfFilePath: "/path/resolveconfpath",
	}); err == nil {
		t.Errorf("Should be an error: incorrect hostname and resolv.conf file path")
	}

	if err := manager.RemoveInstanceFromNetwork("instance0", "network0"); err != nil {
		t.Fatalf("Can't remove instance from network: %s", err)
	}

	if err := manager.RemoveInstanceFromNetwork("instance0", "network0"); err != nil {
		t.Fatalf("Can't remove instance from network: %v", err)
	}

	if _, _, err := manager.GetSystemTraffic(); err == nil {
		t.Error("Should be an error: traffic monitoring is disabled")
	}

	if _, _, err := manager.GetInstanceTraffic("instance0"); err == nil {
		t.Error("Should be an error: traffic monitoring is disabled")
	}

	if err := manager.SetTrafficPeriod(networkmanager.MinutePeriod); err == nil {
		t.Error("Should be an error: traffic monitoring is disabled")
	}
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (storage *testTrafficStorage) SetTrafficMonitorData(chain string, timestamp time.Time, value uint64) error {
	if storage.disableSaveTraffic {
		return aoserrors.New("problem to save traffic")
	}

	storage.chains[chain] = trafficData{lastUpdate: timestamp, currentValue: value}

	return nil
}

func (storage *testTrafficStorage) GetTrafficMonitorData(chain string) (timestamp time.Time, value uint64, err error) {
	if storage.disableLoadTraffic {
		return timestamp, 0, aoserrors.New("problem to load traffic")
	}

	data, ok := storage.chains[chain]
	if !ok {
		return timestamp, 0, networkmanager.ErrEntryNotExist
	}

	return data.lastUpdate, data.currentValue, nil
}

func (storage *testTrafficStorage) RemoveTrafficMonitorData(chain string) error {
	if _, ok := storage.chains[chain]; !ok {
		return networkmanager.ErrEntryNotExist
	}

	delete(storage.chains, chain)

	return nil
}

func createPlugins(plugins []string) string {
	networkConfig := `{"name":"network0","cniVersion":"0.4.0","plugins":[`

	for i, plugin := range plugins {
		networkConfig += plugin
		if i != len(plugins)-1 {
			networkConfig += `,`
		}
	}

	return networkConfig + `]}`
}

func readFromFile(path string) (content string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	defer func() {
		if err = file.Close(); err != nil {
			log.Error(err)
		}
	}()

	b, err := ioutil.ReadAll(file)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	return removeSpaces(string(b)), nil
}

func createBridgePlugin(dataDir string) string {
	str := removeSpaces(fmt.Sprintf(`{
		"type": "bridge",
		"bridge": "br-network0",
		"isGateway": true,
		"ipMasq": true,
		"hairpinMode": true,
		"ipam": {
			"rangeStart": "172.17.0.1",
			"rangeEnd": "172.17.255.254",
			"subnet": "172.17.0.0/16",
			"Name": "",
			"type": "host-local",
			"routes": [{
				"dst": "0.0.0.0/0"
			}],
			"dataDir": "%scni/networks",
			"resolvConf": "",
			"ranges": null
		}
	}`, dataDir))

	return str
}

func createDNSPlugin() string {
	return `{"type":"dnsname","multiDomain":true,"domainName":"network0","capabilities":{"aliases":true}}`
}

func createBandwithPlugin(in, out int) string {
	return fmt.Sprintf(
		`{"type":"bandwidth","ingressRate":%d,"ingressBurst":12800,"egressRate":%d,"egressBurst":12800}`, in, out)
}

func createFirewallPlugin(inPort, outPort string) string {
	str := removeSpaces(`{
		"type": "aos-firewall",
		"uuid": "instance0",
		"iptablesAdminChainName": "INSTANCE_instance0",
		"allowPublicConnections": true`)

	if inPort != "" {
		str += fmt.Sprintf(`,"inputAccess":[{"port":"%s","protocol":"tcp"}]`, inPort)
	}

	if outPort != "" {
		str += fmt.Sprintf(`,"outputAccess":[{"uuid":"instance0","port":"%s","protocol":"tcp"}]`, outPort)
	}

	return str + "}"
}

func getIPAddressRange(subnetwork *net.IPNet) (ipLowNetRange net.IP, ipHighNetRange net.IP) {
	return net.ParseIP("172.17.0.1"), net.ParseIP("172.17.255.254")
}

func getIPSubnet(networkID string) (allocIPNet *net.IPNet, err error) {
	_, ipnet, err := net.ParseCIDR("172.17.0.0/16")

	return ipnet, aoserrors.Wrap(err)
}

func (c *testCNIInterface) AddNetworkList(ctx context.Context, list *cni.NetworkConfigList, rt *cni.RuntimeConf) (
	types.Result, error,
) {
	if c.errorAddNetwork {
		return nil, aoserrors.New("problem add instance to network")
	}

	c.networkConfig = list
	c.runtimeConfig = rt

	result := &current.Result{
		CNIVersion: current.ImplementedSpecVersion,
		Interfaces: []*current.Interface{},
		DNS: types.DNS{
			Nameservers: []string{"1.1.1.1"},
		},
	}

	if !c.emptyIPAddress {
		ipConfig := &current.IPConfig{
			Address: net.IPNet{IP: net.ParseIP("192.168.0.1")},
		}

		result.IPs = append(result.IPs, ipConfig)
	}

	return result, nil
}

func (c *testCNIInterface) ValidateNetworkList(ctx context.Context, list *cni.NetworkConfigList) ([]string, error) {
	if c.errorValidateNetwork {
		return nil, aoserrors.New("problem to validate network")
	}

	return nil, nil
}

func (c *testCNIInterface) GetNetworkListCachedConfig(list *cni.NetworkConfigList, rt *cni.RuntimeConf) (
	[]byte, *cni.RuntimeConf, error,
) {
	if c.networkConfig == nil {
		return nil, nil, aoserrors.New("network configuration empty")
	}

	networkConfig := cniNetwork{Name: list.Name, CNIVersion: list.CNIVersion}

	for _, net := range c.networkConfig.Plugins {
		networkConfig.Plugins = append(networkConfig.Plugins, net.Bytes)
	}

	config, err := json.Marshal(networkConfig)
	if err != nil {
		return nil, nil, aoserrors.Wrap(err)
	}

	return config, rt, nil
}

func (c *testCNIInterface) DelNetworkList(ctx context.Context, list *cni.NetworkConfigList, rt *cni.RuntimeConf) error {
	if c.networkConfig == nil {
		return aoserrors.New("network list empty")
	}

	return nil
}

func (c *testCNIInterface) AddNetwork(
	ctx context.Context, net *cni.NetworkConfig, rt *cni.RuntimeConf,
) (types.Result, error) {
	return nil, nil
}

func (c *testCNIInterface) CheckNetwork(ctx context.Context, net *cni.NetworkConfig, rt *cni.RuntimeConf) error {
	return nil
}

func (c *testCNIInterface) CheckNetworkList(
	ctx context.Context, net *cni.NetworkConfigList, rt *cni.RuntimeConf,
) error {
	return nil
}

func (c *testCNIInterface) DelNetwork(ctx context.Context, net *cni.NetworkConfig, rt *cni.RuntimeConf) error {
	return nil
}

func (c *testCNIInterface) GetNetworkCachedConfig(net *cni.NetworkConfig, rt *cni.RuntimeConf) (
	[]byte, *cni.RuntimeConf, error,
) {
	return nil, nil, nil
}

func (c *testCNIInterface) GetNetworkCachedResult(net *cni.NetworkConfig, rt *cni.RuntimeConf) (types.Result, error) {
	return nil, nil
}

func (c *testCNIInterface) GetNetworkListCachedResult(
	net *cni.NetworkConfigList, rt *cni.RuntimeConf,
) (types.Result, error) {
	return nil, nil
}

func (c *testCNIInterface) ValidateNetwork(ctx context.Context, net *cni.NetworkConfig) ([]string, error) {
	return nil, nil
}

func (iptables *testIPTablesInterface) isSamePeriod(trafficPeriod int, t1, t2 time.Time) bool {
	return iptables.disableResetMonitoringTraffic
}

func (iptables *testIPTablesInterface) Clear() {
	for key := range iptables.chain {
		delete(iptables.chain, key)
	}
}

func (iptables *testIPTablesInterface) ListWithCounters(table, chain string) ([]string, error) {
	iptablesData, ok := iptables.chain[chain]
	if !ok {
		return nil, networkmanager.ErrRuleNotExist
	}

	var counters []string

	for i := 0; i < iptablesData.countChain; i++ {
		counters = append(counters, fmt.Sprintf("-c 0 %d", iptablesData.limit))
	}

	iptablesData.limit += iptables.trafficLimitCounter

	iptables.chain[chain] = iptablesData

	return counters, nil
}

func (iptables *testIPTablesInterface) Append(table, chain string, rulespec ...string) error {
	data, ok := iptables.chain[chain]
	if !ok {
		return networkmanager.ErrRuleNotExist
	}

	data.countChain++

	iptables.chain[chain] = data

	return nil
}

func (iptables *testIPTablesInterface) Delete(table, chain string, rulespec ...string) error {
	data, ok := iptables.chain[chain]
	if !ok || data.countChain == 0 {
		return networkmanager.ErrRuleNotExist
	}

	data.countChain--

	if data.countChain == 0 {
		data.limit = 0
	}

	iptables.chain[chain] = data

	return nil
}

func (iptables *testIPTablesInterface) NewChain(table, chain string) error {
	iptables.chain[chain] = iptablesData{}

	return nil
}

func (iptables *testIPTablesInterface) Insert(table, chain string, pos int, rulespec ...string) error {
	return nil
}

func (iptables *testIPTablesInterface) ClearChain(table, chain string) error {
	if _, ok := iptables.chain[chain]; !ok {
		return networkmanager.ErrRuleNotExist
	}

	iptables.chain[chain] = iptablesData{}

	return nil
}

func (iptables *testIPTablesInterface) DeleteChain(table, chain string) error {
	if _, ok := iptables.chain[chain]; !ok {
		return networkmanager.ErrRuleNotExist
	}

	delete(iptables.chain, chain)

	return nil
}

func (iptables *testIPTablesInterface) ListChains(table string) ([]string, error) {
	listChain := make([]string, 0, len(iptables.chain))

	for name := range iptables.chain {
		listChain = append(listChain, name)
	}

	return listChain, nil
}

func setup() (err error) {
	networkmanager.GetIPAddressRange = getIPAddressRange

	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func removeSpaces(str string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}

		return r
	}, str)
}
