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
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"sync"
	"testing"

	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

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
	if err := manager.AddServiceToNetwork("servicenm0", "network0", networkmanager.NetworkParams{}); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	if err := manager.IsServiceInNetwork("servicenm0", "network0"); err != nil {
		t.Errorf("Service should be in network %s", err)
	}

	if _, err := manager.GetServiceIP("servicenm0", "network0"); err != nil {
		t.Errorf("Can't get service ip: %s", err)
	}

	if err := manager.RemoveServiceFromNetwork("servicenm0", "network0"); err != nil {
		t.Fatalf("Can't remove service from network: %s", err)
	}

	if err := manager.IsServiceInNetwork("servicenm0", "network0"); err == nil {
		t.Error("Service should not be in network")
	}

	if err := manager.DeleteNetwork("network0"); err != nil {
		t.Fatalf("Can't Delete network: %s", err)
	}
}

func TestInternet(t *testing.T) {
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

	if err := manager.DeleteNetwork("network0"); err != nil {
		t.Fatalf("Can't Delete network: %s", err)
	}
}

func TestInterServiceConnection(t *testing.T) {
	container0Path := path.Join(tmpDir, "servicenm2")

	if err := createOCIContainer(container0Path, "servicenm2", []string{"sleep", "12"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork("servicenm2", "network0", networkmanager.NetworkParams{}); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		if err := runOCIContainer(container0Path, "servicenm2"); err != nil {
			t.Errorf("Error: %s", err)
		}
	}()

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

	wg.Wait()

	if err := manager.DeleteNetwork("network0"); err != nil {
		t.Fatalf("Can't Delete network: %s", err)
	}
}

func TestHostName(t *testing.T) {
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

	if err := manager.DeleteNetwork("network0"); err != nil {
		t.Fatalf("Can't Delete network: %s", err)
	}
}

func TestExposedPortAndAllowedConnection(t *testing.T) {
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

	defer manager.DeleteNetwork("networkSP1")

	servIP, err := manager.GetServiceIP(serverServiceID, "networkSP1")
	if err != nil {
		t.Fatalf("Can't get ip address from service: %s", err)
	}

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		if err := runOCIContainer(containerServerPath, serverServiceID); err != nil {
			t.Errorf("Error: %s", err)
		}
	}()

	containerClientPath := path.Join(tmpDir, "serviceClient")

	log.Debug("CURL: ", string("curl "+servIP+":"+serverPort+" --connect-timeout 2"))

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

	wg.Wait()

	manager.DeleteNetwork("networkSP2")
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

	if manager, err = networkmanager.New(&config.Config{WorkingDir: tmpDir}); err != nil {
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
	s.start()
	time.sleep(5)
	s.stop()`

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
		return errors.New(string(out))
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
		return fmt.Errorf("message: %s, err: %s", string(output), err)
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
