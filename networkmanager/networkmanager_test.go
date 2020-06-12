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
	"strconv"
	"sync"
	"testing"

	"github.com/docker/docker/pkg/reexec"
	"github.com/docker/docker/pkg/stringid"
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
	if reexec.Init() {
		return
	}

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

func TestCreateDeleteNetwork(t *testing.T) {
	if err := manager.CreateNetwork("network0"); err != nil {
		t.Fatalf("Can't create network: %s", err)
	}

	if err := manager.NetworkExists("network0"); err != nil {
		t.Errorf("Network should exist: %s", err)
	}

	if err := manager.DeleteNetwork("network0"); err != nil {
		t.Fatalf("Can't Delete network: %s", err)
	}

	if err := manager.NetworkExists("network0"); err == nil {
		t.Errorf("Network should not exist: %s", err)
	}
}

func TestAddRemoveService(t *testing.T) {
	if err := manager.CreateNetwork("network0"); err != nil {
		t.Fatalf("Can't create network: %s", err)
	}

	if err := manager.AddServiceToNetwork("service0", "network0", tmpDir, ""); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	if result, _ := manager.IsServiceInNetwork("service0", "network0"); !result {
		t.Error("Service should be in network")
	}

	if err := manager.RemoveServiceFromNetwork("service0", "network0"); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	if result, _ := manager.IsServiceInNetwork("service0", "network0"); result {
		t.Error("Service should not be in network")
	}

	if err := manager.DeleteNetwork("network0"); err != nil {
		t.Fatalf("Can't Delete network: %s", err)
	}
}

func TestInternet(t *testing.T) {
	if err := manager.CreateNetwork("network0"); err != nil {
		t.Fatalf("Can't create network: %s", err)
	}

	containerPath := path.Join(tmpDir, "service0")

	if err := createOCIContainer(containerPath, "service0", []string{"ping", "google.com", "-c10", "-w10"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork("service0", "network0", containerPath, ""); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	if err := runOCIContainer(containerPath, "service0"); err != nil {
		t.Errorf("Error: %s", err)
	}

	if err := manager.DeleteNetwork("network0"); err != nil {
		t.Fatalf("Can't Delete network: %s", err)
	}
}

func TestInterServiceConnection(t *testing.T) {
	if err := manager.CreateNetwork("network0"); err != nil {
		t.Fatalf("Can't create network: %s", err)
	}

	container0Path := path.Join(tmpDir, "service0")

	if err := createOCIContainer(container0Path, "service0", []string{"sleep", "12"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork("service0", "network0", container0Path, "service1"); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		if err := runOCIContainer(container0Path, "service0"); err != nil {
			t.Errorf("Error: %s", err)
		}
	}()

	container1Path := path.Join(tmpDir, "service1")

	if err := createOCIContainer(container1Path, "service1", []string{"ping", "service0", "-c10", "-w10"}); err != nil {
		t.Fatalf("Can't create service container: %s", err)
	}

	if err := manager.AddServiceToNetwork("service1", "network0", container1Path, "service1"); err != nil {
		t.Fatalf("Can't add service to network: %s", err)
	}

	if err := runOCIContainer(container1Path, "service1"); err != nil {
		t.Errorf("Error: %s", err)
	}

	wg.Wait()

	if err := manager.DeleteNetwork("network0"); err != nil {
		t.Fatalf("Can't Delete network: %s", err)
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

	for _, mount := range []string{"/bin", "/sbin", "/lib", "/lib64", "/usr"} {
		spec.Mounts = append(spec.Mounts, runtimespec.Mount{Destination: mount,
			Type: "bind", Source: mount, Options: []string{"bind", "ro"}})
	}

	for _, mount := range []string{"hosts", "resolv.conf"} {
		spec.Mounts = append(spec.Mounts, runtimespec.Mount{Destination: path.Join("/etc", mount),
			Type: "bind", Source: path.Join(imagePath, "etc", mount), Options: []string{"bind", "ro"}})
	}

	spec.Hooks = new(runtimespec.Hooks)

	spec.Hooks.Prestart = append(spec.Hooks.Poststart, runtimespec.Hook{
		Path: path.Join("/proc", strconv.Itoa(os.Getpid()), "exe"),
		Args: []string{
			"libnetwork-setkey",
			"-exec-root=/run/aos",
			containerID,
			stringid.TruncateID(manager.GetID()),
		}})

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
