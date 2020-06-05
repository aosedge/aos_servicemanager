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
	"io/ioutil"
	"os"
	"testing"

	"github.com/docker/docker/pkg/reexec"
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

	if err := manager.AddServiceToNetwork("service0", "network0"); err != nil {
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
