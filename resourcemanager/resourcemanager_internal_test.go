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
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoscloud/aos_common/aoserrors"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	driDevPath   = "/dev/dri/by-path/"
	stdinDevPath = "/dev/stdin"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type alertSender struct{}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var (
	tmpDir          string
	testAlertSender = &alertSender{}
)

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
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

	if err := cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestProcessHostDevice(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err != nil {
		t.Errorf("Board config error: %s", err)
	}

	var hs []string
	if _, exist := os.Stat(driDevPath); exist == nil || os.IsExist(exist) {
		// Test directory with symlinks
		hs, err = rm.processHostDevice(driDevPath)
		if err != nil {
			t.Errorf("Can't process device directory. Error: %s", err)
		}

		originalHs, err := getDevicePathContents(driDevPath)
		if err != nil {
			t.Errorf("Can't process device directory. Error: %s", err)
		}

		if !reflect.DeepEqual(hs, originalHs) {
			t.Errorf("Device path contents are not equal. Error: %s", err)
		}
	}

	// Test symlink
	hs, err = rm.processHostDevice(stdinDevPath)
	if err != nil {
		t.Errorf("Can't process device directory. Error: %s", err)
	}

	linkName, err := filepath.EvalSymlinks(stdinDevPath)
	if err != nil {
		t.Errorf("Can't read symlink. Error: %s", err)
	}

	if linkName != hs[0] {
		t.Errorf("Device symlink is not equal. Error: %s", err)
	}
}

func TestValidBoardConfiguration(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Errorf("Can't write invalid resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err != nil {
		t.Errorf("Board config error: %s", err)
	}
}

func TestEmptyResourcesConfig(t *testing.T) {
	if err := writeTestBoardConfigFile(createEmptyBoardConfigJSON()); err != nil {
		t.Errorf("Can't write invalid resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err != nil {
		t.Errorf("Board config error: %s", err)
	}
}

func TestInValidBoardConfiguration(t *testing.T) {
	if err := writeTestBoardConfigFile(createInvalidBoardConfigJSON()); err != nil {
		t.Errorf("Can't write invalid resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err == nil {
		t.Errorf("Can't detect unavailable device")
	}
}

func TestUnavailableResources(t *testing.T) {
	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err == nil {
		t.Errorf("Proceed without resource configuration")
	}

	if err = rm.RequestDevice("random", "service0"); err == nil {
		t.Errorf("Proceed without resource configuration")
	}

	if err = rm.ReleaseDevice("random", "service0"); err != nil {
		t.Errorf("Can't release device: %s", err)
	}
}

func TestRequestAndReleaseDeviceResources(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	err = rm.RequestDevice("random", "service0")
	if err != nil {
		t.Fatalf("Can't request device: %s", err)
	}

	err = rm.RequestDevice("random", "service1")
	if err != nil {
		t.Fatalf("Can't request device: %s", err)
	}

	err = rm.ReleaseDevice("random", "service0")
	if err != nil {
		t.Fatalf("Can't release device: %s", err)
	}

	err = rm.ReleaseDevice("random", "service1")
	if err != nil {
		t.Fatalf("Can't release device: %s", err)
	}
}

func TestRequestDeviceResourceByName(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	// request correct resource
	deviceResource, err := rm.RequestDeviceResourceByName("random")
	if err != nil {
		t.Fatalf("Can't request resource: %s", err)
	}

	randomResource := DeviceResource{
		Name: "random", SharedCount: 0, Groups: []string{"root"},
		HostDevices: []string{"/dev/random"},
	}
	if !reflect.DeepEqual(deviceResource, randomResource) {
		t.Fatalf("deviceResource is not equal to randomResource")
	}

	// request dir resource
	deviceResource, err = rm.RequestDeviceResourceByName("input")
	if err != nil {
		t.Fatalf("Can't request resource: %s", err)
	}

	inputResource := DeviceResource{
		Name: "input", SharedCount: 2, Groups: nil,
		HostDevices: []string{},
	}

	inputResource.HostDevices, err = getDevicePathContents("/dev/input/by-path")
	if err != nil {
		t.Fatalf("Can't request process pts dir: %s", err)
	}

	if !reflect.DeepEqual(deviceResource, inputResource) {
		t.Fatalf("deviceResource is not equal to inputResource")
	}

	deviceResource, err = rm.RequestDeviceResourceByName("stdin")
	if err != nil {
		t.Fatalf("Can't request resource: %s", err)
	}

	linkName, err := filepath.EvalSymlinks("/dev/stdin")
	if err != nil {
		t.Fatalf("Can't read symlink with error: %s", err)
	}

	stdoutResource := DeviceResource{
		Name: "stdin", SharedCount: 2, Groups: nil,
		HostDevices: []string{linkName},
	}

	if !reflect.DeepEqual(deviceResource, stdoutResource) {
		t.Fatalf("deviceResource is not equal to stdoutResource")
	}

	// request not existed device class
	if _, err = rm.RequestDeviceResourceByName("some_unavailable_device"); err == nil {
		t.Fatalf("Can request resource: some_unavailable_device")
	}
}

func TestRequestBoardResourceByName(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Error("Can't write resource configuration: ", err)
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	// request incorrect resource
	_, err = rm.RequestBoardResourceByName("invalid_id")
	if err == nil {
		t.Errorf("Should be error: resource is not present in board configuration")
	}

	originalConfig := BoardResource{
		Name: "system-dbus",
		Mounts: []FileSystemMount{{
			Destination: "/var/run/dbus/system_bus_socket",
			Options:     []string{"rw", "bind"},
			Source:      "/var/run/dbus/system_bus_socket",
			Type:        "bind",
		}},
		Env: []string{"DBUS_SYSTEM_BUS_ADDRESS=unix:path=/var/run/dbus/system_bus_socket"},
	}

	boardResource, err := rm.RequestBoardResourceByName("system-dbus")
	if err != nil {
		t.Error("Can't get board config file: ", err)
	}

	if !reflect.DeepEqual(originalConfig, boardResource) {
		t.Error("boardConfg in not equal to original one")
	}
}

func TestRequestLimitDeviceResources(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	// only 2 service can use this device
	err = rm.RequestDevice("null", "service0")
	if err != nil {
		t.Fatalf("Can't request device: %s", err)
	}

	// request double time (should be ignored)
	err = rm.RequestDevice("null", "service0")
	if err != nil {
		t.Fatalf("Can't request device: %s", err)
	}

	err = rm.RequestDevice("null", "service1")
	if err != nil {
		t.Fatalf("Can't request device: %s", err)
	}

	err = rm.RequestDevice("null", "service2")
	if err == nil {
		t.Fatalf("Can request device")
	} else {
		log.Debugf("Can't request: %s", err)
	}

	err = rm.ReleaseDevice("null", "service0")
	if err != nil {
		t.Fatalf("Can't release device: %s", err)
	}

	err = rm.ReleaseDevice("null", "service1")
	if err != nil {
		t.Fatalf("Can't release device: %s", err)
	}
}

func TestReleaseNotRequestedDeviceResources(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	err = rm.RequestDevice("null", "service0")
	if err != nil {
		t.Fatalf("Can't request device: %s", err)
	}

	// release not requested device
	if err = rm.ReleaseDevice("random", "service0"); err != nil {
		t.Errorf("Can't release device: %s", err)
	}

	// release device for not existing service
	if err = rm.ReleaseDevice("null", "service1"); err != nil {
		t.Errorf("Can't release device: %s", err)
	}

	// release correct device for proper service
	if err = rm.ReleaseDevice("null", "service0"); err != nil {
		t.Errorf("Can't release device: %s", err)
	}
}

func TestRequestReleaseUnavailableDeviceResources(t *testing.T) {
	if err := writeTestBoardConfigFile(createTestBoardConfigJSON("1.0")); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.RequestDevice("some_unavailable_device", "service0"); err == nil {
		t.Errorf("Can request unavailable device")
	}

	if err = rm.ReleaseDevice("some_unavailable_device", "service0"); err != nil {
		t.Errorf("Can't release device: %s", err)
	}
}

func TestResourceConfigNotExist(t *testing.T) {
	rm, err := New(path.Join(tmpDir, "non_exist_config.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err == nil {
		t.Errorf("Resources should be invalid if config is not exits")
	}
}

func TestResourceConfigInvalidVersion(t *testing.T) {
	if err := writeTestBoardConfigFile(createWrongVersionBoardConfigJSON()); err != nil {
		t.Errorf("Can't write invalid resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board_wrong_version.cfg"), testAlertSender)
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	if err = rm.boardConfigError; err == nil {
		t.Errorf("Resources should be invalid in case of version mismatch")
	}
}

func TestGetBoardConfigInfo(t *testing.T) {
	vendorVersion := "2.1"

	if err := writeTestBoardConfigFile(createTestBoardConfigJSON(vendorVersion)); err != nil {
		t.Fatalf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
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
		t.Fatalf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "aos_board.cfg"), testAlertSender)
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
		t.Fatalf("Can't write resource configuration")
	}

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
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func getDevicePathContents(device string) (hostDevices []string, err error) {
	err = filepath.Walk(device,
		func(path string, info os.FileInfo, err error) error {
			if info.IsDir() || err != nil {
				return aoserrors.Wrap(err)
			}

			if info.Mode()&os.ModeSymlink == os.ModeSymlink {
				linkName, err := filepath.EvalSymlinks(path)
				if err != nil {
					return aoserrors.Wrap(err)
				}

				hostDevices = append(hostDevices, linkName)
			} else {
				hostDevices = append(hostDevices, path)
			}
			return nil
		})

	return hostDevices, err
}

func setup() (err error) {
	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = os.MkdirAll(tmpDir, 0755); err != nil {
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
	if err := ioutil.WriteFile(path.Join(tmpDir, "aos_board.cfg"), []byte(content), 0644); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (sender *alertSender) SendValidateResourceAlert(source string, errors map[string][]error) {
	log.Debugf("SendValidateResourceAlert source %s", source)
}

func (sender *alertSender) SendRequestResourceAlert(source string, message string) {
	log.Debugf("SendRequestResourceAlert source %s, message %s", source, message)
}
