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

package config_test

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"path"
	"testing"
	"time"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Private
 ******************************************************************************/

func createConfigFile() (err error) {
	configContent := `{
	"fcrypt" : {
		"CACert" : "CACert",
		"ClientCert" : "ClientCert",
		"ClientKey" : "ClientKey",
		"OfflinePrivKey" : "OfflinePrivKey",
		"OfflineCert" : "OfflineCert"	
	},
	"serviceDiscovery" : "www.aos.com",
	"workingDir" : "workingDir",
	"upgradeDir" : "/var/aos/upgrade",
	"visServer" : "wss://localhost:8088",
	"umServer" : "wss://localhost:8089",
	"defaultServiceTTLDays" : 30,
	"monitoring": {
		"sendPeriod": "00:05:00",
		"pollPeriod": "00:00:01",
		"maxOfflineMessages": 25,
		"netnsBridgeIP": "192.168.0.0/24",
		"ram": {
			"minTimeout": "00:00:10",
			"minThreshold": 10,
			"maxThreshold": 150
		},
		"outTraffic": {
			"minTimeout": "00:00:20",
			"minThreshold": 10,
			"maxThreshold": 150
		}
	},
	"logging": {
		"maxPartSize": 1024,
		"maxPartCount": 10
	},
	"alerts": {
		"sendPeriod": "00:00:20",
		"maxMessageSize": 1024,
		"maxOfflineMessages": 32
	},
	"identifier": {
		"param1": "testParam",
		"param2": 123
	}
}`

	if err := ioutil.WriteFile(path.Join("tmp", "aos_servicemanager.cfg"), []byte(configContent), 0644); err != nil {
		return err
	}

	return nil
}

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	if err = createConfigFile(); err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	if err := os.RemoveAll("tmp"); err != nil {
		return err
	}

	return nil
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error creating service images: %s", err)
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

func TestGetCrypt(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.Crypt.CACert != "CACert" {
		t.Errorf("Wrong CACert value: %s", config.Crypt.CACert)
	}

	if config.Crypt.ClientCert != "ClientCert" {
		t.Errorf("Wrong ClientCert value: %s", config.Crypt.ClientCert)
	}

	if config.Crypt.ClientKey != "ClientKey" {
		t.Errorf("Wrong ClientKey value: %s", config.Crypt.ClientKey)
	}

	if config.Crypt.OfflinePrivKey != "OfflinePrivKey" {
		t.Errorf("Wrong OfflinePrivKey value: %s", config.Crypt.OfflinePrivKey)
	}

	if config.Crypt.OfflineCert != "OfflineCert" {
		t.Errorf("Wrong OfflineCert value: %s", config.Crypt.OfflineCert)
	}
}

func TestGetServiceDiscoveryURL(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.ServiceDiscoveryURL != "www.aos.com" {
		t.Errorf("Wrong server URL value: %s", config.ServiceDiscoveryURL)
	}
}

func TestGetWorkingDir(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.WorkingDir != "workingDir" {
		t.Errorf("Wrong workingDir value: %s", config.WorkingDir)
	}
}

func TestGetStorageDirAsWorkingDir(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.StorageDir != "/var/aos/storage" {
		t.Errorf("Wrong storageDir value: %s", config.StorageDir)
	}
}

func TestGetVisServerURL(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.VISServerURL != "wss://localhost:8088" {
		t.Errorf("Wrong VIS server value: %s", config.VISServerURL)
	}
}

func TestGetUMServerURL(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.UMServerURL != "wss://localhost:8089" {
		t.Errorf("Wrong UM server value: %s", config.UMServerURL)
	}
}

func TestGetUpgradeDir(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.UpgradeDir != "/var/aos/upgrade" {
		t.Errorf("Wrong upgrade dir value: %s", config.UpgradeDir)
	}
}

func TestGetDefaultServiceTTL(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.DefaultServiceTTL != 30 {
		t.Errorf("Wrong default service TTL value: %d", config.DefaultServiceTTL)
	}
}

func TestDurationMarshal(t *testing.T) {
	d := config.Duration{Duration: 32 * time.Second}

	result, err := json.Marshal(d)
	if err != nil {
		t.Errorf("Can't marshal: %s", err)
	}

	if string(result) != `"00:00:32"` {
		t.Errorf("Wrong value: %s", result)
	}
}

func TestGetMonitoringConfig(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.Monitoring.SendPeriod.Duration != 5*time.Minute {
		t.Errorf("Wrong send period value: %s", config.Monitoring.SendPeriod)
	}

	if config.Monitoring.PollPeriod.Duration != 1*time.Second {
		t.Errorf("Wrong poll period value: %s", config.Monitoring.PollPeriod)
	}

	if config.Monitoring.RAM.MinTimeout.Duration != 10*time.Second {
		t.Errorf("Wrong value: %s", config.Monitoring.RAM.MinTimeout)
	}

	if config.Monitoring.OutTraffic.MinTimeout.Duration != 20*time.Second {
		t.Errorf("Wrong value: %s", config.Monitoring.RAM.MinTimeout)
	}

	if config.Monitoring.NetnsBridgeIP != "192.168.0.0/24" {
		t.Errorf("Wrong value: %s", config.Monitoring.NetnsBridgeIP)
	}
}

func TestGetLoggingConfig(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.Logging.MaxPartSize != 1024 {
		t.Errorf("Wrong max part size: %d", config.Logging.MaxPartSize)
	}

	if config.Logging.MaxPartCount != 10 {
		t.Errorf("Wrong max part count: %d", config.Logging.MaxPartCount)
	}
}

func TestGetAlertsConfig(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.Alerts.SendPeriod.Duration != 20*time.Second {
		t.Errorf("Wrong poll period value: %s", config.Alerts.SendPeriod)
	}

	if config.Alerts.MaxMessageSize != 1024 {
		t.Errorf("Wrong max message size value: %d", config.Alerts.MaxMessageSize)
	}

	if config.Alerts.MaxOfflineMessages != 32 {
		t.Errorf("Wrong max offline message value: %d", config.Alerts.MaxOfflineMessages)
	}
}

func TestGetIdentification(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	type Params struct {
		Param1 string
		Param2 int
	}

	var params Params

	if err = json.Unmarshal(config.Identifier, &params); err != nil {
		t.Errorf("Can't parse identifier params: %s", err)
	}

	if params.Param1 != "testParam" {
		t.Errorf("Wrong param1: %v", params.Param1)
	}

	if params.Param2 != 123 {
		t.Errorf("Wrong param2: %v", params.Param2)
	}
}
