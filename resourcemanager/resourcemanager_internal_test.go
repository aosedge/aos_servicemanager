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
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	log "github.com/sirupsen/logrus"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type alertSender struct {
	alert cloudprotocol.ResourceValidateAlert
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

	if err := cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestValidBoardConfiguration(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err != nil {
		t.Errorf("Board config error: %s", err)
	}
}

func TestEmptyResourcesConfig(t *testing.T) {
	if err := writeTestBoardConfigFile(createEmptyBoardConfigJSON()); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err != nil {
		t.Errorf("Board config error: %s", err)
	}
}

func TestInvalidBoardConfiguration(t *testing.T) {
	if err := writeTestBoardConfigFile(createInvalidBoardConfigJSON()); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	testAlertSender := &alertSender{}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err == nil {
		t.Error("Can't detect unavailable devices")
	}

	testAlertSender.checkAlert(t)
}

func TestUnavailableResources(t *testing.T) {
	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err == nil {
		t.Error("Proceed without board configuration")
	}

	if err = rm.AllocateDevice("random", "instance0"); err == nil {
		t.Error("Proceed without board configuration")
	}
}

func TestGetDeviceInfo(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	// request standalone device
	deviceInfo, err := rm.GetDeviceInfo("random")
	if err != nil {
		t.Fatalf("Can't get device info: %s", err)
	}

	if !reflect.DeepEqual(deviceInfo, aostypes.DeviceInfo{
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
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	resourceInfo, err := rm.GetResourceInfo("system-dbus")
	if err != nil {
		t.Errorf("Can't get resource inf: %s", err)
	}

	if !reflect.DeepEqual(resourceInfo, aostypes.ResourceInfo{
		Name: "system-dbus",
		Mounts: []aostypes.FileSystemMount{{
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
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.AllocateDevice("random", "instance0"); err != nil {
		t.Fatalf("Can't allocate device: %s", err)
	}

	if err = rm.AllocateDevice("random", "instance1"); err != nil {
		t.Fatalf("Can't allocate device: %s", err)
	}

	if err = rm.ReleaseDevice("random", "instance0"); err != nil {
		t.Fatalf("Can't release devices: %s", err)
	}

	if err = rm.ReleaseDevice("random", "instance1"); err != nil {
		t.Fatalf("Can't release devices: %s", err)
	}
}

func TestAllocateUnavailableDevice(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.AllocateDevice("some_unavailable_device", "instance0"); err == nil {
		t.Error("Device should be unavailable")
	}
}

func TestReleaseNotAllocatedDevice(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.AllocateDevice("null", "instance0"); err != nil {
		t.Fatalf("Can't allocate device: %s", err)
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
		t.Errorf("Can't release device: %s", err)
	}
}

func TestAllocateLimitedDevice(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	// only two instances can use this device
	if err = rm.AllocateDevice("null", "instance0"); err != nil {
		t.Errorf("Can't allocate device: %s", err)
	}

	// allocate again, should be ignored as already allocated
	if err = rm.AllocateDevice("null", "instance0"); err != nil {
		t.Errorf("Can't allocate device: %s", err)
	}

	if err = rm.AllocateDevice("null", "instance1"); err != nil {
		t.Errorf("Can't allocate device: %s", err)
	}

	if err = rm.AllocateDevice("null", "instance2"); !errors.Is(err, ErrNoAvailableDevice) {
		t.Error("Expect no device available error")
	}
}

func TestGetDeviceInstances(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	// get instances of non existing device

	if _, err := rm.GetDeviceInstances("non_exist_device"); err == nil {
		t.Errorf("Error is expected due to non existing device")
	}

	// get instaces of random device

	expectedInstances := []string{"instance0", "instance1", "instance2", "instance3", "instance4"}

	for _, instance := range expectedInstances {
		if err = rm.AllocateDevice("random", instance); err != nil {
			t.Fatalf("Can't allocate device: %s", err)
		}
	}

	instances, err := rm.GetDeviceInstances("random")
	if err != nil {
		t.Fatalf("Can't get device instances: %s", err)
	}

	if !reflect.DeepEqual(instances, expectedInstances) {
		t.Errorf("Wrong device instances: %v", instances)
	}

	// release some instances

	for _, instance := range expectedInstances[1:4] {
		if err = rm.ReleaseDevice("random", instance); err != nil {
			t.Fatalf("Can't release device: %s", err)
		}
	}

	if instances, err = rm.GetDeviceInstances("random"); err != nil {
		t.Fatalf("Can't get device instances: %s", err)
	}

	expectedInstances = append(expectedInstances[:1], expectedInstances[4:]...)

	if !reflect.DeepEqual(instances, expectedInstances) {
		t.Errorf("Wrong device instances: %v", instances)
	}
}

func TestReleaseDevices(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	allocateDevices := []string{"random", "null", "input", "stdin"}

	for _, device := range allocateDevices {
		if err = rm.AllocateDevice(device, "instance0"); err != nil {
			t.Fatalf("Can't allocate device: %s", err)
		}
	}

	for _, device := range allocateDevices {
		instances, err := rm.GetDeviceInstances(device)
		if err != nil {
			t.Fatalf("Can't get device instances: %s", err)
		}

		if len(instances) == 0 {
			t.Fatalf("Wrong device instances count: %d", len(instances))
		}

		if instances[0] != "instance0" {
			t.Errorf("Wrong instance ID: %s", instances[0])
		}
	}

	if err = rm.ReleaseDevices("instance0"); err != nil {
		t.Fatalf("Can't release devices: %s", err)
	}

	for _, device := range allocateDevices {
		instances, err := rm.GetDeviceInstances(device)
		if err != nil {
			t.Fatalf("Can't get device instances: %s", err)
		}

		if len(instances) > 0 {
			t.Errorf("Wrong device instances count: %d", len(instances))
		}
	}
}

func TestNotExistBoardConfig(t *testing.T) {
	rm, err := New(path.Join(tmpDir, "non_exist_config.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err == nil {
		t.Error("Board config should be invalid if config is not exits")
	}
}

func TestInvalidVersionBoardConfig(t *testing.T) {
	if err := writeTestBoardConfigFile(createWrongVersionBoardConfigJSON()); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board_wrong_version.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err == nil {
		t.Errorf("Board config should be invalid in case of version mismatch")
	}
}

func TestGetBoardConfigInfo(t *testing.T) {
	vendorVersion := "2.1"

	if err := writeTestBoardConfigFile(createTestBoardConfigJSON(vendorVersion)); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	version := rm.GetBoardConfigInfo()

	if version != vendorVersion {
		t.Errorf("Wrong board config version: %s", version)
	}
}

func TestUpdateBoardConfig(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), &alertSender{})
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	newVendorVersion := "2.0"

	if err = rm.UpdateBoardConfig(createTestBoardConfigJSON(newVendorVersion)); err != nil {
		t.Fatalf("Can't update board config: %s", err)
	}

	version := rm.GetBoardConfigInfo()

	if version != newVendorVersion {
		t.Errorf("Wrong board config version: %s", version)
	}
}

func TestUpdateErrorBoardConfig(t *testing.T) {
	currentConfigVersion := "1.0"

	if err := writeTestBoardConfigFile(createTestBoardConfigJSON(currentConfigVersion)); err != nil {
		t.Fatalf("Can't write board config: %s", err)
	}

	testAlertSender := &alertSender{}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.UpdateBoardConfig(createInvalidBoardConfigJSON()); err == nil {
		t.Errorf("Update should fail")
	}

	version := rm.GetBoardConfigInfo()
	if version != currentConfigVersion {
		t.Errorf("Wrong board config version: %s", version)
	}

	testAlertSender.checkAlert(t)
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func setup() (err error) {
	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = os.MkdirAll(tmpDir, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func cleanup() (err error) {
	if err := os.RemoveAll(tmpDir); err != nil {
		log.Errorf("Can't remove tmp dir: %s", err)
	}

	return nil
}

func createWrongVersionBoardConfigJSON() (configJSON string) {
	return `{
	"formatVersion": 256,
	"vendorVersion": "1.0",
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
		}
	]
}`
}

func createTestBoardConfigJSON(version string) (configJSON string) {
	return fmt.Sprintf(`{
	"formatVersion": 1,
	"vendorVersion": "%s", 
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

func createInvalidBoardConfigJSON() (configJSON string) {
	return `{
	"formatVersion": 1,
	"vendorVersion": "3.5",
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

func createEmptyBoardConfigJSON() (configJSON string) {
	return `{
		"formatVersion": 1,
		"vendorVersion": "1.0",
		"devices": []
}`
}

func writeTestBoardConfigFile(content string) (err error) {
	if err := ioutil.WriteFile(path.Join(tmpDir, "aos_board.cfg"), []byte(content), 0o600); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (sender *alertSender) SendAlert(alert cloudprotocol.AlertItem) {
	resourceValidateAlert, ok := alert.Payload.(cloudprotocol.ResourceValidateAlert)
	if !ok {
		return
	}

	sender.alert = resourceValidateAlert
}

func (sender *alertSender) checkAlert(t *testing.T) {
	t.Helper()

	if len(sender.alert.ResourcesErrors) != 1 {
		t.Fatalf("Wrong resources errors count: %d", len(sender.alert.ResourcesErrors))
	}

	if sender.alert.ResourcesErrors[0].Name != "some_not_existed_device" {
		t.Errorf("Wrong alert device name: %s", sender.alert.ResourcesErrors[0].Name)
	}

	if len(sender.alert.ResourcesErrors[0].Errors) != 2 {
		t.Errorf("Wrong alert errors count: %d", len(sender.alert.ResourcesErrors[0].Errors))
	}

	for _, errMessage := range sender.alert.ResourcesErrors[0].Errors {
		if !strings.Contains(errMessage, "is not present on system") {
			t.Errorf("Wrong alert error message: %s", errMessage)
		}
	}
}
