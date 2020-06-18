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

package resourcemanager

import (
	"io/ioutil"
	"os"
	"path"
	"reflect"
	"testing"

	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Vars
 ******************************************************************************/

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

	if err := cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestValidAvailableResources(t *testing.T) {
	if err := createRealResourceConfigFile(); err != nil {
		t.Errorf("Can't write invalid resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "available_configuration.cfg"))
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	err = rm.AreResourcesValid()
	if err != nil {
		t.Errorf("Detect unavailable device. Error: %s", err)
	}
}

func TestInValidAvailableResources(t *testing.T) {
	if err := createInValidResourceConfigFile(); err != nil {
		t.Errorf("Can't write invalid resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "available_configuration.cfg"))
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	err = rm.AreResourcesValid()
	if err == nil {
		t.Errorf("Can't detect unavailable device")
	}
}

func TestUnavailableResources(t *testing.T) {
	rm, err := New(path.Join(tmpDir, "available_configuration.cfg"))
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	err = rm.AreResourcesValid()
	if err == nil {
		t.Errorf("Proceed without resource configuration")
	}

	err = rm.RequestDevice("random", "service0")
	if err == nil {
		t.Errorf("Proceed without resource configuration")
	}

	err = rm.ReleaseDevice("random", "service0")
	if err == nil {
		t.Errorf("Proceed without resource configuration")
	}
}

func TestRequestAndReleaseDeviceResources(t *testing.T) {
	if err := createTestResourceConfigFile(); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "available_configuration.cfg"))
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
	if err := createTestResourceConfigFile(); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "available_configuration.cfg"))
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	// request correct resource
	deviceResource, err := rm.RequestDeviceResourceByName("random")
	if err != nil {
		t.Fatalf("Can't request resource: %s", err)
	}

	randomResource := DeviceResource{Name: "random", SharedCount: 0, Groups: []string{"root"},
		HostDevices: []string{"/dev/random"}}
	if !reflect.DeepEqual(deviceResource, randomResource) {
		t.Fatalf("deviceResource is not equal to randomResource")
	}

	// request not existed device class
	deviceResource, err = rm.RequestDeviceResourceByName("some_unavailable_device")
	if err == nil {
		t.Fatalf("Can request resource: some_unavailable_device")
	}
}

func TestRequestLimitDeviceResources(t *testing.T) {
	if err := createTestResourceConfigFile(); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "available_configuration.cfg"))
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
	if err := createTestResourceConfigFile(); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "available_configuration.cfg"))
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	err = rm.RequestDevice("null", "service0")
	if err != nil {
		t.Fatalf("Can't request device: %s", err)
	}

	// release not requested device
	err = rm.ReleaseDevice("random", "service0")
	if err == nil {
		t.Fatalf("Can release device")
	} else {
		log.Debugf("Can't release: %s", err)
	}

	// release device for not existing service
	err = rm.ReleaseDevice("null", "service1")
	if err == nil {
		t.Fatalf("Can release device")
	} else {
		log.Debugf("Can't release: %s", err)
	}

	// release correct device for proper service
	err = rm.ReleaseDevice("null", "service0")
	if err != nil {
		t.Fatalf("Can't release device: %s", err)
	}
}

func TestRequestReleaseUnavailableDeviceResources(t *testing.T) {
	if err := createTestResourceConfigFile(); err != nil {
		t.Errorf("Can't write resource configuration")
	}

	rm, err := New(path.Join(tmpDir, "available_configuration.cfg"))
	if err != nil {
		t.Fatalf("Can't create resource manager: %s", err)
	}

	err = rm.RequestDevice("some_unavailable_device", "service0")
	if err == nil {
		t.Fatalf("Can request unavailable device")
	} else {
		log.Debugf("Can't request: %s", err)
	}

	err = rm.ReleaseDevice("some_unavailable_device", "service0")
	if err == nil {
		t.Fatalf("Can release unavailable device")
	} else {
		log.Debugf("Can't request: %s", err)
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

	return nil
}

func cleanup() (err error) {
	if err := os.RemoveAll(tmpDir); err != nil {
		log.Errorf("Can't remove tmp dir: %s", err)
	}

	return nil
}

func createRealResourceConfigFile() (err error) {
	configContent := `{
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

	if err := ioutil.WriteFile(path.Join(tmpDir, "available_configuration.cfg"), []byte(configContent), 0644); err != nil {
		return err
	}

	return nil
}

func createTestResourceConfigFile() (err error) {
	configContent := `{
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
			"name": "gpu0",
			"sharedCount": 1,
			"hostDevices": [
				"/dev/gpu0/card",
				"/dev/gpu0/render"
			]
		}
	]
}`

	if err := ioutil.WriteFile(path.Join(tmpDir, "available_configuration.cfg"), []byte(configContent), 0644); err != nil {
		return err
	}

	return nil
}

func createInValidResourceConfigFile() (err error) {
	configContent := `{
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

	if err := ioutil.WriteFile(path.Join(tmpDir, "available_configuration.cfg"), []byte(configContent), 0644); err != nil {
		return err
	}

	return nil
}
