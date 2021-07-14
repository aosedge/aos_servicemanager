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

package networkmanager_test

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"

	"aos_servicemanager/config"
	"aos_servicemanager/networkmanager"
)

/*******************************************************************************
 * Vars
 ******************************************************************************/

var manager *networkmanager.NetworkManager
var tmpDir string

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	ret := m.Run()

	cleanup()

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestAddRemoveService(t *testing.T) {
	t.Cleanup(func() { manager.DeleteNetwork("network0") })

	numServices := 10

	statusChannel := make(chan error, numServices)

	for i := 0; i < numServices; i++ {
		go func(serviceID string) {
			statusChannel <- manager.AddServiceToNetwork(serviceID, "network0", networkmanager.NetworkParams{})
		}(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numServices; i++ {
		if err := <-statusChannel; err != nil {
			t.Errorf("Can't add service to network: %s", err)
		}
	}

	for i := 0; i < numServices; i++ {
		if err := manager.IsServiceInNetwork(fmt.Sprintf("service%d", i), "network0"); err != nil {
			t.Errorf("Service should be in network: %s", err)
		}

		if _, err := manager.GetServiceIP(fmt.Sprintf("service%d", i), "network0"); err != nil {
			t.Errorf("Can't get service ip: %s", err)
		}
	}

	for i := 0; i < numServices; i++ {
		go func(serviceID string) {
			statusChannel <- manager.RemoveServiceFromNetwork(serviceID, "network0")
		}(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numServices; i++ {
		if err := <-statusChannel; err != nil {
			t.Errorf("Can't remove service from network: %s", err)
		}
	}

	for i := 0; i < numServices; i++ {
		if err := manager.IsServiceInNetwork(fmt.Sprintf("service%d", i), "network0"); err == nil {
			t.Error("Service should not be in network")
		}
	}
}

func TestDeleteAllNetworks(t *testing.T) {
	t.Cleanup(func() {
		manager.DeleteAllNetworks()
	})

	numNetworks := 3
	numServices := 3

	for j := 0; j < numNetworks; j++ {
		for i := 0; i < numServices; i++ {
			if err := manager.AddServiceToNetwork(fmt.Sprintf("service%d%d", j, i),
				fmt.Sprintf("network%d", j), networkmanager.NetworkParams{}); err != nil {
				t.Fatalf("Can't add service to network: %s", err)
			}
		}
	}

	if err := manager.DeleteAllNetworks(); err != nil {
		t.Fatalf("Can't delete all networks: %s", err)
	}

	for j := 0; j < numNetworks; j++ {
		for i := 0; i < numServices; i++ {
			if err := manager.IsServiceInNetwork(fmt.Sprintf("service%d%d", j, i),
				fmt.Sprintf("network%d", j)); err == nil {
				t.Fatalf("Service should not be in network: %s", err)
			}
		}
	}
}

func TestInternet(t *testing.T) {
	t.Cleanup(func() { manager.DeleteNetwork("network0") })

	containerPath := path.Join(tmpDir, "servicenm1")

	if err := createOCIContainer(containerPath, "servicenm1", []string{"ping", "google.com", "-c10", "-w15"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork("servicenm1", "network0", networkmanager.NetworkParams{
		ResolvConfFilePath: path.Join(containerPath, "etc", "resolv.conf"),
	}); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	if err := runOCIContainer(containerPath, "servicenm1"); err != nil {
		t.Errorf("Error: %s", err)
	}
}

func TestInterServiceConnection(t *testing.T) {
	t.Cleanup(func() {
		killOCIContainer("servicenm2")
		manager.DeleteNetwork("network0")
	})

	container0Path := path.Join(tmpDir, "servicenm2")

	if err := createOCIContainer(container0Path, "servicenm2", []string{"sleep", "infinity"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork("servicenm2", "network0", networkmanager.NetworkParams{}); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	go runOCIContainer(container0Path, "servicenm2")

	container1Path := path.Join(tmpDir, "servicenm3")

	if err := createOCIContainer(container1Path, "servicenm3", []string{"ping", "myservicenm0", "-c10", "-w15"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	ip, err := manager.GetServiceIP("servicenm2", "network0")
	if err != nil {
		t.Fatalf("Can't get ip address from service: %s", err)
	}

	if err := manager.AddServiceToNetwork("servicenm3", "network0", networkmanager.NetworkParams{
		Hosts:         []config.Host{{IP: ip, Hostname: "myservicenm0"}},
		HostsFilePath: path.Join(container1Path, "etc", "hosts"),
	}); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	if err := runOCIContainer(container1Path, "servicenm3"); err != nil {
		t.Errorf("Error: %s", err)
	}
}

func TestHostName(t *testing.T) {
	t.Cleanup(func() { manager.DeleteNetwork("network0") })

	container0Path := path.Join(tmpDir, "servicenm4")

	if err := createOCIContainer(container0Path, "servicenm4", []string{"ping", "myhost", "-c10", "-w15"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork("servicenm4", "network0", networkmanager.NetworkParams{
		Hostname:      "myhost",
		HostsFilePath: path.Join(container0Path, "etc", "hosts"),
	}); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	if err := runOCIContainer(container0Path, "servicenm4"); err != nil {
		t.Errorf("Error: %s", err)
	}
}

func TestExposedPortAndAllowedConnection(t *testing.T) {
	t.Cleanup(func() {
		killOCIContainer("serviceServer")
		manager.DeleteNetwork("networkSP1")
		manager.DeleteNetwork("networkSP2")
	})

	serverPort := "9000"
	containerServerPath := path.Join(tmpDir, "serviceServer")
	serverServiceID := "serviceServer"

	if err := createOCIContainerWitHttpServer(containerServerPath, serverServiceID, serverPort); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork(serverServiceID, "networkSP1",
		networkmanager.NetworkParams{ExposedPorts: []string{serverPort}}); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	servIP, err := manager.GetServiceIP(serverServiceID, "networkSP1")
	if err != nil {
		t.Fatalf("Can't get ip address from service: %s", err)
	}

	go runOCIContainer(containerServerPath, serverServiceID)

	time.Sleep(1 * time.Second)

	containerClientPath := path.Join(tmpDir, "serviceClient")

	if err := createOCIContainer(containerClientPath, "serviceClient", []string{"curl", servIP + ":" + serverPort,
		"--connect-timeout", "2"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork("serviceClient", "networkSP2", networkmanager.NetworkParams{
		AllowedConnections: []string{serverServiceID + "/" + serverPort}}); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	if err := runOCIContainer(containerClientPath, "serviceClient"); err != nil {
		t.Errorf("Error: %s", err)
	}
}

func TestNetworkDNS(t *testing.T) {
	t.Cleanup(func() {
		killOCIContainer("service0")
		manager.DeleteNetwork("network0")
		manager.DeleteNetwork("network1")
	})

	container0Path := path.Join(tmpDir, "service0")

	if err := createOCIContainer(container0Path, "service0", []string{"sleep", "infinity"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork("service0", "network0", networkmanager.NetworkParams{
		Hostname: "myhost",
		Aliases:  []string{"alias1"},
	}); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	go runOCIContainer(container0Path, "service0")

	container1Path := path.Join(tmpDir, "service1")

	// Other SP network

	hostNames := []string{
		"service0.network0",
		"myhost.network0",
		"alias1.network0",
	}

	for _, hostName := range hostNames {
		if err := createOCIContainer(container1Path, "service1", []string{"ping", hostName, "-c1"}); err != nil {
			t.Fatalf("Can't create service container: %s", err)
		}

		if err := manager.AddServiceToNetwork("service1", "network1", networkmanager.NetworkParams{
			ResolvConfFilePath: path.Join(container1Path, "etc", "resolv.conf"),
		}); err != nil {
			t.Fatalf("Can't add service to network: %s", err)
		}

		if err := runOCIContainer(container1Path, "service1"); err != nil {
			t.Errorf("Error: %s", err)
		}

		if err := manager.RemoveServiceFromNetwork("service1", "network1"); err != nil {
			t.Fatalf("Can't remove service from network: %s", err)
		}
	}

	hostNames = []string{
		"service0", "service0.network0",
		"myhost", "myhost.network0",
		"alias1", "alias1.network0",
	}

	// Same SP network

	for _, hostName := range hostNames {
		if err := createOCIContainer(container1Path, "service1", []string{"ping", hostName, "-c1"}); err != nil {
			t.Fatalf("Can't create service container: %s", err)
		}

		if err := manager.AddServiceToNetwork("service1", "network0", networkmanager.NetworkParams{
			ResolvConfFilePath: path.Join(container1Path, "etc", "resolv.conf"),
		}); err != nil {
			t.Fatalf("Can't add service to network: %s", err)
		}

		if err := runOCIContainer(container1Path, "service1"); err != nil {
			t.Errorf("Error: %s", err)
		}

		if err := manager.RemoveServiceFromNetwork("service1", "network0"); err != nil {
			t.Fatalf("Can't remove service from network: %s", err)
		}
	}
}

func TestBandwidth(t *testing.T) {
	t.Cleanup(func() {
		killOCIContainer("service0")
		manager.DeleteNetwork("network0")
	})

	container0Path := path.Join(tmpDir, "service0")

	var setULSpeed uint64 = 1000
	var setDLSpeed uint64 = 4000

	if err := createOCIContainer(container0Path, "service0", []string{"iperf", "-s"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork("service0", "network0", networkmanager.NetworkParams{
		IngressKbit: setDLSpeed,
		EgressKbit:  setULSpeed,
	}); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	go runOCIContainer(container0Path, "service0")

	ip, err := manager.GetServiceIP("service0", "network0")
	if err != nil {
		t.Fatalf("Can't get ip address from service: %s", err)
	}

	time.Sleep(1 * time.Second)

	output, err := exec.Command("iperf", "-c", ip, "-d", "-r", "-t5", "-yc").CombinedOutput()
	if err != nil {
		t.Fatalf("iperf failed: %s", err)
	}

	ulSpeed := -1.0
	dlSpeed := -1.0

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		result := strings.Split(line, ",")
		if len(result) >= 9 {
			if result[4] == "5001" {
				value, err := strconv.ParseFloat(result[8], 64)
				if err != nil {
					t.Errorf("Can't parse ul speed: %s", err)
					continue
				}
				dlSpeed = value / 1000
			} else {
				value, err := strconv.ParseFloat(result[8], 64)
				if err != nil {
					t.Errorf("Can't parse ul speed: %s", err)
					continue
				}
				ulSpeed = value / 1000
			}
		}
	}

	if ulSpeed < 0.0 || dlSpeed < 0.0 {
		t.Error("Can't determine UL/DL speed")
	}

	// Max delta 5%
	delta := 1.05

	if ulSpeed > float64(setULSpeed)*delta || dlSpeed > float64(setDLSpeed)*delta {
		t.Errorf("Speed limit exceeds expected level: DL %0.2f, UL %0.2f", dlSpeed, ulSpeed)
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return err
	}

	if err = os.MkdirAll(tmpDir, 0755); err != nil {
		return err
	}

	if manager, err = networkmanager.New(&config.Config{WorkingDir: tmpDir}, nil); err != nil {
		return err
	}

	return nil
}

func cleanup() {
	if err := manager.DeleteAllNetworks(); err != nil {
		log.Errorf("Can't remove networks: %s", err)
	}

	manager.Close()

	if err := os.RemoveAll(tmpDir); err != nil {
		log.Errorf("Can't remove tmp dir: %s", err)
	}
}

func createOCIContainerWitHttpServer(imagePath string, containerID string, port string) (err error) {
	if err = createOCIContainer(imagePath, containerID, []string{"/usr/bin/python3.7", "/httpserver.py"}); err != nil {
		return err
	}

	httpServerContent := `#!/usr/bin/python

import threading
import time
from http.server import ThreadingHTTPServer, SimpleHTTPRequestHandler

class MyServer(threading.Thread):
	def run(self):
		self.server = ThreadingHTTPServer(('',` + port + `), SimpleHTTPRequestHandler)
		self.server.serve_forever()
	def stop(self):
		self.server.shutdown()

if __name__ == '__main__':
	s = MyServer()
	s.start()`

	if err := ioutil.WriteFile(path.Join(imagePath, "rootfs", "httpserver.py"), []byte(httpServerContent), 0644); err != nil {
		return err
	}

	return nil
}

func createOCIContainer(imagePath string, containerID string, args []string) (err error) {
	if err = os.RemoveAll(imagePath); err != nil {
		return err
	}

	if err = os.MkdirAll(path.Join(imagePath, "rootfs"), 0755); err != nil {
		return err
	}

	out, err := exec.Command("runc", "spec", "-b", imagePath).CombinedOutput()
	if err != nil {
		return aoserrors.New(string(out))
	}

	if err := addHostResolvFiles(imagePath); err != nil {
		return err
	}

	specJSON, err := ioutil.ReadFile(path.Join(imagePath, "config.json"))
	if err != nil {
		return err
	}

	var spec runtimespec.Spec

	if err = json.Unmarshal(specJSON, &spec); err != nil {
		return err
	}

	spec.Process.Terminal = false

	spec.Process.Args = args

	for i, ns := range spec.Linux.Namespaces {
		switch ns.Type {
		case runtimespec.NetworkNamespace:
			spec.Linux.Namespaces[i].Path = networkmanager.GetNetNsPathByName(containerID)
		}
	}

	for _, mount := range []string{"/bin", "/sbin", "/lib", "/lib64", "/usr", "/etc/nsswitch.conf"} {
		spec.Mounts = append(spec.Mounts, runtimespec.Mount{Destination: mount,
			Type: "bind", Source: mount, Options: []string{"bind", "ro"}})
	}

	for _, mount := range []string{"hosts", "resolv.conf"} {
		spec.Mounts = append(spec.Mounts, runtimespec.Mount{Destination: path.Join("/etc", mount),
			Type: "bind", Source: path.Join(imagePath, "etc", mount), Options: []string{"bind", "ro"}})
	}

	spec.Process.Capabilities.Bounding = append(spec.Process.Capabilities.Bounding, "CAP_NET_RAW")
	spec.Process.Capabilities.Effective = append(spec.Process.Capabilities.Effective, "CAP_NET_RAW")
	spec.Process.Capabilities.Inheritable = append(spec.Process.Capabilities.Inheritable, "CAP_NET_RAW")
	spec.Process.Capabilities.Permitted = append(spec.Process.Capabilities.Permitted, "CAP_NET_RAW")
	spec.Process.Capabilities.Ambient = append(spec.Process.Capabilities.Ambient, "CAP_NET_RAW")

	if specJSON, err = json.Marshal(&spec); err != nil {
		return err
	}

	if err = ioutil.WriteFile(path.Join(imagePath, "config.json"), specJSON, 0644); err != nil {
		return err
	}

	return nil
}

func runOCIContainer(imagePath string, containerID string) (err error) {
	output, err := exec.Command("runc", "run", "-b", imagePath, containerID).CombinedOutput()
	if err != nil {
		return aoserrors.Errorf("message: %s, err: %s", string(output), err)
	}

	return nil
}

func killOCIContainer(containerID string) (err error) {
	output, err := exec.Command("runc", "delete", "-f", containerID).CombinedOutput()
	if err != nil {
		return aoserrors.Errorf("message: %s, err: %s", string(output), err)
	}

	return nil
}

func addHostResolvFiles(pathToContainer string) (err error) {
	etcPath := path.Join(pathToContainer, "etc")
	if err = os.MkdirAll(etcPath, 0755); err != nil {
		return err
	}

	if _, err = os.Create(path.Join(etcPath, "hosts")); err != nil {
		return err
	}

	if _, err = os.Create(path.Join(etcPath, "resolv.conf")); err != nil {
		return err
	}

	return nil
}
