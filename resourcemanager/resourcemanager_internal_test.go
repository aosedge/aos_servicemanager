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

package resourcemanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/aostypes"
	"github.com/aosedge/aos_common/api/cloudprotocol"
	log "github.com/sirupsen/logrus"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type alertSender struct {
	alerts []cloudprotocol.ResourceValidateAlert
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
		log.Fatalf("Error setting up: %v", err)
	}

	ret := m.Run()

	if err := cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %v", err)
	}

	os.Exit(ret)
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestValidNodeConfiguration(t *testing.T) {
	if err := writeTestNodeConfigFile(createTestNodeConfigFile("1.0.0")); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	if err = rm.nodeConfigError; err != nil {
		t.Errorf("Node config error: %v", err)
	}
}

func TestEmptyResourcesConfig(t *testing.T) {
	if err := writeTestNodeConfigFile(createEmptyNodeConfigJSON()); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	if err = rm.nodeConfigError; err != nil {
		t.Errorf("Node config error: %v", err)
	}
}

func TestInvalidNodeConfiguration(t *testing.T) {
	if err := writeTestNodeConfigFile(createInvalidNodeConfigJSON()); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	testAlertSender := &alertSender{}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	if err = rm.nodeConfigError; err == nil {
		t.Error("Can't detect unavailable devices")
	}

	testAlertSender.checkAlert(t)
}

func TestUnavailableResources(t *testing.T) {
	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	if err = rm.nodeConfigError; err == nil {
		t.Error("Proceed without node configuration")
	}

	if err = rm.AllocateDevice("random", "instance0"); err == nil {
		t.Error("Proceed without node configuration")
	}
}

func TestGetDeviceInfo(t *testing.T) {
	if err := writeTestNodeConfigFile(createTestNodeConfigFile("1.0.0")); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	// request standalone device
	deviceInfo, err := rm.GetDeviceInfo("random")
	if err != nil {
		t.Fatalf("Can't get device info: %v", err)
	}

	if !reflect.DeepEqual(deviceInfo, cloudprotocol.DeviceInfo{
		Name: "random", SharedCount: 0, Groups: []string{"root"},
		HostDevices: []string{"/dev/random"},
	}) {
		t.Errorf("Wrong device info: %v", deviceInfo)
	}

	// request not existed device class
	if _, err = rm.GetDeviceInfo("some_unavailable_device"); err == nil {
		t.Error("Device should be unavailable")
	}
}

func TestGetResourceInfo(t *testing.T) {
	if err := writeTestNodeConfigFile(createTestNodeConfigFile("1.0.0")); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	resourceInfo, err := rm.GetResourceInfo("system-dbus")
	if err != nil {
		t.Errorf("Can't get resource inf: %v", err)
	}

	if !reflect.DeepEqual(resourceInfo, cloudprotocol.ResourceInfo{
		Name: "system-dbus",
		Mounts: []cloudprotocol.FileSystemMount{{
			Destination: "/var/run/dbus/system_bus_socket",
			Options:     []string{"rw", "bind"},
			Source:      "/var/run/dbus/system_bus_socket",
			Type:        "bind",
		}},
		Env: []string{"DBUS_SYSTEM_BUS_ADDRESS=unix:path=/var/run/dbus/system_bus_socket"},
	}) {
		t.Errorf("Wrong resource info: %v", resourceInfo)
	}

	// request incorrect resource
	if _, err = rm.GetResourceInfo("invalid_id"); err == nil {
		t.Error("Resource should be unavailable")
	}
}

func TestAllocateAndReleaseDevices(t *testing.T) {
	if err := writeTestNodeConfigFile(createTestNodeConfigFile("1.0.0")); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	if err = rm.AllocateDevice("random", "instance0"); err != nil {
		t.Fatalf("Can't allocate device: %v", err)
	}

	if err = rm.AllocateDevice("random", "instance1"); err != nil {
		t.Fatalf("Can't allocate device: %v", err)
	}

	if err = rm.ReleaseDevice("random", "instance0"); err != nil {
		t.Fatalf("Can't release devices: %v", err)
	}

	if err = rm.ReleaseDevice("random", "instance1"); err != nil {
		t.Fatalf("Can't release devices: %v", err)
	}
}

func TestAllocateUnavailableDevice(t *testing.T) {
	if err := writeTestNodeConfigFile(createTestNodeConfigFile("1.0.0")); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	if err = rm.AllocateDevice("some_unavailable_device", "instance0"); err == nil {
		t.Error("Device should be unavailable")
	}
}

func TestReleaseNotAllocatedDevice(t *testing.T) {
	if err := writeTestNodeConfigFile(createTestNodeConfigFile("1.0.0")); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	if err = rm.AllocateDevice("null", "instance0"); err != nil {
		t.Fatalf("Can't allocate device: %v", err)
	}

	// release not allocated device
	if err = rm.ReleaseDevice("random", "instance0"); err == nil {
		t.Error("Expected error due to release not allocated device")
	}

	// release device for not existing instance
	if err = rm.ReleaseDevice("null", "instance1"); err == nil {
		t.Error("Expected error due to release not allocated device")
	}

	// release correct device for proper instance
	if err = rm.ReleaseDevice("null", "instance0"); err != nil {
		t.Errorf("Can't release device: %v", err)
	}
}

func TestAllocateLimitedDevice(t *testing.T) {
	if err := writeTestNodeConfigFile(createTestNodeConfigFile("1.0.0")); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	// only two instances can use this device
	if err = rm.AllocateDevice("null", "instance0"); err != nil {
		t.Errorf("Can't allocate device: %v", err)
	}

	// allocate again, should be ignored as already allocated
	if err = rm.AllocateDevice("null", "instance0"); err != nil {
		t.Errorf("Can't allocate device: %v", err)
	}

	if err = rm.AllocateDevice("null", "instance1"); err != nil {
		t.Errorf("Can't allocate device: %v", err)
	}

	if err = rm.AllocateDevice("null", "instance2"); !errors.Is(err, ErrNoAvailableDevice) {
		t.Error("Expect no device available error")
	}
}

func TestGetDeviceInstances(t *testing.T) {
	if err := writeTestNodeConfigFile(createTestNodeConfigFile("1.0.0")); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	// get instances of non existing device

	if _, err := rm.GetDeviceInstances("non_exist_device"); err == nil {
		t.Errorf("Error is expected due to non existing device")
	}

	// get instaces of random device

	expectedInstances := []string{"instance0", "instance1", "instance2", "instance3", "instance4"}

	for _, instance := range expectedInstances {
		if err = rm.AllocateDevice("random", instance); err != nil {
			t.Fatalf("Can't allocate device: %v", err)
		}
	}

	instances, err := rm.GetDeviceInstances("random")
	if err != nil {
		t.Fatalf("Can't get device instances: %v", err)
	}

	if !reflect.DeepEqual(instances, expectedInstances) {
		t.Errorf("Wrong device instances: %v", instances)
	}

	// release some instances

	for _, instance := range expectedInstances[1:4] {
		if err = rm.ReleaseDevice("random", instance); err != nil {
			t.Fatalf("Can't release device: %v", err)
		}
	}

	if instances, err = rm.GetDeviceInstances("random"); err != nil {
		t.Fatalf("Can't get device instances: %v", err)
	}

	expectedInstances = append(expectedInstances[:1], expectedInstances[4:]...)

	if !reflect.DeepEqual(instances, expectedInstances) {
		t.Errorf("Wrong device instances: %v", instances)
	}
}

func TestReleaseDevices(t *testing.T) {
	if err := writeTestNodeConfigFile(createTestNodeConfigFile("1.0.0")); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	allocateDevices := []string{"random", "null", "input", "stdin"}

	for _, device := range allocateDevices {
		if err = rm.AllocateDevice(device, "instance0"); err != nil {
			t.Fatalf("Can't allocate device: %v", err)
		}
	}

	for _, device := range allocateDevices {
		instances, err := rm.GetDeviceInstances(device)
		if err != nil {
			t.Fatalf("Can't get device instances: %v", err)
		}

		if len(instances) == 0 {
			t.Fatalf("Wrong device instances count: %d", len(instances))
		}

		if instances[0] != "instance0" {
			t.Errorf("Wrong instance ID: %s", instances[0])
		}
	}

	if err = rm.ReleaseDevices("instance0"); err != nil {
		t.Fatalf("Can't release devices: %v", err)
	}

	for _, device := range allocateDevices {
		instances, err := rm.GetDeviceInstances(device)
		if err != nil {
			t.Fatalf("Can't get device instances: %v", err)
		}

		if len(instances) > 0 {
			t.Errorf("Wrong device instances count: %d", len(instances))
		}
	}
}

func TestNotExistNodeConfig(t *testing.T) {
	rm, err := New(path.Join(tmpDir, "non_exist_config.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	if err = rm.nodeConfigError; err != nil {
		t.Error("Node config should be valid if config is not exits")
	}

	if rm.nodeConfig.Version != "0.0.0" {
		t.Error("Wrong node config version")
	}
}

func TestGetNodeConfigStatus(t *testing.T) {
	vendorVersion := "2.1.0"

	if err := writeTestNodeConfigFile(createTestNodeConfigFile(vendorVersion)); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	version, err := rm.GetNodeConfigStatus()
	if err != nil {
		t.Errorf("Node config status error: %v", err)
	}

	if version != vendorVersion {
		t.Errorf("Wrong node config version: %v", version)
	}
}

func TestUpdateNodeConfig(t *testing.T) {
	if err := writeTestNodeConfigFile(createTestNodeConfigFile("1.0.0")); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	newVendorVersion := "2.0.0"

	if err = rm.UpdateNodeConfig(createTestNodeConfigJSON(), newVendorVersion); err != nil {
		t.Fatalf("Can't update node config: %v", err)
	}

	version, err := rm.GetNodeConfigStatus()
	if err != nil {
		t.Errorf("Node config status error: %v", err)
	}

	if version != newVendorVersion {
		t.Errorf("Wrong node config version: %s", version)
	}
}

func TestUpdateErrorNodeConfig(t *testing.T) {
	currentConfigVersion := "1.0.0"

	if err := writeTestNodeConfigFile(createTestNodeConfigFile(currentConfigVersion)); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	testAlertSender := &alertSender{}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	if err = rm.UpdateNodeConfig(createInvalidNodeConfigJSON(), "3.5"); err == nil {
		t.Errorf("Update should fail")
	}

	version, err := rm.GetNodeConfigStatus()
	if err != nil {
		t.Errorf("Node config status error: %v", err)
	}

	if version != currentConfigVersion {
		t.Errorf("Wrong node config version: %s", version)
	}

	testAlertSender.checkAlert(t)
}

func TestGetNodeConfig(t *testing.T) {
	cloudNodeConfig := cloudprotocol.NodeConfig{
		NodeType: "mainType",
		Devices: []cloudprotocol.DeviceInfo{
			{Name: "random", SharedCount: 0, Groups: []string{"root"}, HostDevices: []string{"/dev/random"}},
			{Name: "null", SharedCount: 2, HostDevices: []string{"/dev/null"}},
		},
		ResourceRatios: &aostypes.ResourceRatiosInfo{CPU: newFloat(1.0), RAM: newFloat(2.0), Storage: newFloat(3.0)},
		Resources: []cloudprotocol.ResourceInfo{
			{Name: "bluetooth", Groups: []string{"bluetooth"}},
			{Name: "wifi", Groups: []string{"wifi-group"}},
		},
	}
	rmNodeConfig := nodeConfig{
		Version:    "1.0.0",
		NodeConfig: cloudNodeConfig,
	}

	configJSON, err := json.Marshal(rmNodeConfig)
	if err != nil {
		t.Fatalf("Can't marshal node config: %v", err)
	}

	if err := writeTestNodeConfigFile(string(configJSON)); err != nil {
		t.Fatalf("Can't write node config: %v", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_node.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %v", err)
	}

	getNodeConfig, err := rm.GetCurrentNodeConfig()
	if err != nil {
		t.Fatalf("Can't get node config: %v", err)
	}

	if !reflect.DeepEqual(getNodeConfig, rmNodeConfig.NodeConfig) {
		t.Errorf("Wrong node config: %v", getNodeConfig)
	}

	newNodeConfig := cloudprotocol.NodeConfig{
		NodeType: "mainType",
		Devices: []cloudprotocol.DeviceInfo{
			{Name: "random", SharedCount: 0, Groups: []string{"root"}, HostDevices: []string{"/dev/random"}},
			{Name: "null", SharedCount: 2, HostDevices: []string{"/dev/null"}},
			{Name: "input", SharedCount: 2, HostDevices: []string{"/dev/input/by-path"}},
		},
		ResourceRatios: &aostypes.ResourceRatiosInfo{CPU: newFloat(1.0), RAM: newFloat(2.0), Storage: newFloat(3.0)},
		Resources: []cloudprotocol.ResourceInfo{
			{Name: "bluetooth", Groups: []string{"bluetooth"}},
			{Name: "wifi", Groups: []string{"wifi-group"}},
			{Name: "system-dbus", Mounts: []cloudprotocol.FileSystemMount{
				{
					Destination: "/var/run/dbus/system_bus_socket", Type: "bind",
					Source: "/var/run/dbus/system_bus_socket", Options: []string{"rw", "bind"},
				},
			}},
		},
	}

	newConfigJSON, err := json.Marshal(newNodeConfig)
	if err != nil {
		t.Fatalf("Can't marshal node config: %v", err)
	}

	curNodeConfigListener := rm.SubscribeCurrentNodeConfigChange()

	if err = rm.UpdateNodeConfig(string(newConfigJSON), "2.0.0"); err != nil {
		t.Fatalf("Can't update node config: %v", err)
	}

	select {
	case nodeConfig := <-curNodeConfigListener:
		if !reflect.DeepEqual(nodeConfig, newNodeConfig) {
			t.Errorf("Wrong node config: %v", nodeConfig)
		}

	case <-time.After(5 * time.Second):
		t.Fatal("Can't wait for node config update")
	}
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func setup() (err error) {
	if tmpDir, err = os.MkdirTemp("", "aos_"); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = os.MkdirAll(tmpDir, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func cleanup() (err error) {
	if err := os.RemoveAll(tmpDir); err != nil {
		log.Errorf("Can't remove tmp dir: %v", err)
	}

	return nil
}

func createTestNodeConfigFile(version string) (configJSON string) {
	return fmt.Sprintf(`{
	"version": "%s",
	"nodeType": "mainType",
	"devices": [
		{
			"name": "random",
			"sharedCount": 0,
			"groups": [
				"root"
			],
			"hostDevices": [
				"/dev/random"
			]
		},
		{
			"name": "null",
			"sharedCount": 2,
			"hostDevices": [
				"/dev/null"
			]
		},
		{
			"name": "input",
			"sharedCount": 2,
			"hostDevices": [
				"/dev/input/by-path"
			]
		},
		{
			"name": "stdin",
			"sharedCount": 2,
			"hostDevices": [
				"/dev/stdin"
			]
		}
	],
	"resources": [
		{
			"name": "bluetooth",
			"groups": ["bluetooth"]
		},
		{
			"name": "wifi",
			"groups": ["wifi-group"]
		},
		{
			"name": "system-dbus",
			"mounts": [{
				"destination": "/var/run/dbus/system_bus_socket",
				"type": "bind",
				"source": "/var/run/dbus/system_bus_socket",
				"options": ["rw", "bind"]
			}],
			"env": ["DBUS_SYSTEM_BUS_ADDRESS=unix:path=/var/run/dbus/system_bus_socket"]
		}
	]
}`, version)
}

func createTestNodeConfigJSON() (configJSON string) {
	return `{
	"nodeType": "mainType",
	"devices": [
		{
			"name": "random",
			"sharedCount": 0,
			"groups": [
				"root"
			],
			"hostDevices": [
				"/dev/random"
			]
		},
		{
			"name": "null",
			"sharedCount": 2,
			"hostDevices": [
				"/dev/null"
			]
		},
		{
			"name": "input",
			"sharedCount": 2,
			"hostDevices": [
				"/dev/input/by-path"
			]
		},
		{
			"name": "stdin",
			"sharedCount": 2,
			"hostDevices": [
				"/dev/stdin"
			]
		}
	],
	"resources": [
		{
			"name": "bluetooth",
			"groups": ["bluetooth"]
		},
		{
			"name": "wifi",
			"groups": ["wifi-group"]
		},
		{
			"name": "system-dbus",
			"mounts": [{
				"destination": "/var/run/dbus/system_bus_socket",
				"type": "bind",
				"source": "/var/run/dbus/system_bus_socket",
				"options": ["rw", "bind"]
			}],
			"env": ["DBUS_SYSTEM_BUS_ADDRESS=unix:path=/var/run/dbus/system_bus_socket"]
		}
	]
}`
}

func createInvalidNodeConfigJSON() (configJSON string) {
	return `{
	"vendorVersion": "3.5",
	"nodeType": "mainType",
	"devices": [
		{
			"name": "some_not_existed_device",
			"sharedCount": 0,
			"groups": [
				"user1"
			],
			"hostDevices": [
				"some_not_existed_device"
			]
		}
	]
}`
}

func createEmptyNodeConfigJSON() (configJSON string) {
	return `{
		"vendorVersion": "1.0.0",
		"nodeType": "mainType",
		"devices": []
}`
}

func writeTestNodeConfigFile(content string) (err error) {
	if err := os.WriteFile(path.Join(tmpDir, "aos_node.cfg"), []byte(content), 0o600); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (sender *alertSender) SendAlert(alert interface{}) {
	resourceValidateAlert, ok := alert.(cloudprotocol.ResourceValidateAlert)
	if !ok {
		return
	}

	sender.alerts = append(sender.alerts, resourceValidateAlert)
}

func (sender *alertSender) checkAlert(t *testing.T) {
	t.Helper()

	if len(sender.alerts) != 1 {
		t.Fatalf("Wrong resources errors count: %d", len(sender.alerts))
	}

	if sender.alerts[0].Name != "some_not_existed_device" {
		t.Errorf("Wrong alert device name: %s", sender.alerts[0].Name)
	}

	if len(sender.alerts[0].Errors) != 2 {
		t.Errorf("Wrong alert errors count: %d", len(sender.alerts[0].Errors))
	}

	for _, errInfo := range sender.alerts[0].Errors {
		if !strings.Contains(errInfo.Message, "is not present on system") {
			t.Errorf("Wrong alert error message: %v", errInfo.Message)
		}
	}
}

func newFloat(value float64) *float64 {
	return &value
}
