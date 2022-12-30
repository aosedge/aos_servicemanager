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

package config_test

import (
	"io/ioutil"
	"log"
	"os"
	"path"
	"reflect"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"

	"github.com/aoscloud/aos_servicemanager/config"
)

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func createConfigFile() (err error) {
	configContent := `{
	"CACert": "CACert",	
	"certStorage": "sm",
	"iamProtectedServerUrl": "localhost:8089",
	"iamPublicServerUrl": "localhost:8090",
	"cmServerUrl": "aoscm:8093",
	"workingDir": "workingDir",
	"storageDir": "/var/aos/storage",
	"stateDir": "/var/aos/state",
	"servicesDir": "/var/aos/servicemanager/services",
	"servicesPartLimit": 10,
	"layersDir": "/var/aos/srvlib",
	"layersPartLimit": 20,
	"downloadDir": "/var/aos/servicemanager/download",
	"extractDir": "/var/aos/servicemanager/extract",
	"remoteNode": true,
	"runnerFeatures": ["crun", "runc"],
	"unitConfigFile": "/var/aos/aos_unit.cfg",
	"layerTtlDays": 40,
	"serviceHealthCheckTimeout": "10s",
	"monitoring": {
		"sendPeriod": "5m",
		"pollPeriod": "1s",
		"ram": {
			"minTimeout": "10s",
			"minThreshold": 10,
			"maxThreshold": 150
		},
		"outTraffic": {
			"minTimeout": "20s",
			"minThreshold": 10,
			"maxThreshold": 150
		}
	},
	"logging": {
		"maxPartSize": 1024,
		"maxPartCount": 10
	},
	"journalAlerts": {		
		"filter": ["(test)", "(regexp)"],
		"serviceAlertPriority": 7,
		"systemAlertPriority": 5
	},
	"hostBinds": ["dir0", "dir1", "dir2"],
	"hosts": [{
			"ip": "127.0.0.1",
			"hostName" : "wwwivi"
		},
		{
			"ip": "127.0.0.1",
			"hostName" : "wwwaosum"
		}
	],
	"migration": {
		"migrationPath" : "/usr/share/aos_servicemnager/migration",
		"mergedMigrationPath" : "/var/aos/servicemanager/mergedMigration"
	}
}`

	if err := ioutil.WriteFile(path.Join("tmp", "aos_servicemanager.cfg"), []byte(configContent), 0o600); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = createConfigFile(); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func cleanup() (err error) {
	if err := os.RemoveAll("tmp"); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

/***********************************************************************************************************************
 * Main
 **********************************************************************************************************************/

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

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestGetCrypt(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.CACert != "CACert" {
		t.Errorf("Wrong CACert value: %s", config.CACert)
	}
}

func TestCMServerURL(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.CMServerURL != "aoscm:8093" {
		t.Errorf("Wrong cmServer url value: %s", config.CMServerURL)
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

	if config.StateDir != "/var/aos/state" {
		t.Errorf("Wrong stateDir value: %s", config.StateDir)
	}
}

func TestGetUnitConfigFile(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.UnitConfigFile != "/var/aos/aos_unit.cfg" {
		t.Errorf("Wrong unit config value: %s", config.UnitConfigFile)
	}
}

func TestGetLayersDir(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.LayersDir != "/var/aos/srvlib" {
		t.Errorf("Wrong layers dir value: %s", config.LayersDir)
	}
}

func TestGetServicesDir(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.ServicesDir != "/var/aos/servicemanager/services" {
		t.Errorf("Wrong services dir value: %s", config.ServicesDir)
	}
}

func TestGetDownloadDir(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.DownloadDir != "/var/aos/servicemanager/download" {
		t.Errorf("Wrong download dir value: %s", config.DownloadDir)
	}
}

func TestGetExtractDir(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %v", err)
	}

	if config.ExtractDir != "/var/aos/servicemanager/extract" {
		t.Errorf("Wrong extract dir value: %v", config.ExtractDir)
	}
}

func TestGetIAMProtectedServerURL(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.IAMProtectedServerURL != "localhost:8089" {
		t.Errorf("Wrong IAM server value: %s", config.IAMProtectedServerURL)
	}
}

func TestGetIAMPublicServerURL(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.IAMPublicServerURL != "localhost:8090" {
		t.Errorf("Wrong IAM public server value: %s", config.IAMPublicServerURL)
	}
}

func TestGetDefaultServiceTTL(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.ServiceTTLDays != 30 {
		t.Errorf("Wrong default service TTL value: %d", config.ServiceTTLDays)
	}
}

func TestGetLayersTTL(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %v", err)
	}

	if config.LayerTTLDays != 40 {
		t.Errorf("Wrong LayerTTLDays value: %v", config.LayerTTLDays)
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
		t.Errorf("Wrong value: %s", config.Monitoring.OutTraffic.MinTimeout.Duration)
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

	filter := []string{"(test)", "(regexp)"}

	if !reflect.DeepEqual(config.JournalAlerts.Filter, filter) {
		t.Errorf("Wrong filter value: %v", config.JournalAlerts.Filter)
	}

	if config.JournalAlerts.ServiceAlertPriority != 7 {
		t.Errorf("Wrong service alert priority: %d", config.JournalAlerts.ServiceAlertPriority)
	}

	if config.JournalAlerts.SystemAlertPriority != 5 {
		t.Errorf("Wrong system alert priority: %d", config.JournalAlerts.SystemAlertPriority)
	}
}

func TestHostBinds(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	dirs := []string{"dir0", "dir1", "dir2"}

	if !reflect.DeepEqual(config.HostBinds, dirs) {
		t.Errorf("Wrong hostDirs value: %v", config.HostBinds)
	}
}

func TestHosts(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if len(config.Hosts) != 2 {
		t.Errorf("Wrong count of hosts entry: 2!= %d", len(config.Hosts))
	}

	if config.Hosts[0].IP != "127.0.0.1" {
		t.Errorf("Incorrect ip")
	}

	if config.Hosts[1].Hostname != "wwwaosum" {
		t.Errorf("Incorrect hostname")
	}
}

func TestDatabaseMigration(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.Migration.MigrationPath != "/usr/share/aos_servicemnager/migration" {
		t.Errorf("Wrong migrationPath /usr/share/aos_servicemanager/migration != %s", config.Migration.MigrationPath)
	}

	if config.Migration.MergedMigrationPath != "/var/aos/servicemanager/mergedMigration" {
		t.Errorf("Wrong migrationPath /var/aos/servicemanager/mergedMigration != %s", config.Migration.MergedMigrationPath)
	}
}

func TestCertStorage(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.CertStorage != "sm" {
		t.Errorf("Wrong CertStorage value: %s", config.CertStorage)
	}
}

func TestServiceHealthCheckTimeout(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.ServiceHealthCheckTimeout.Duration != 10*time.Second {
		t.Errorf("Wrong ServiceHealthCheckTimeout value: %s", config.ServiceHealthCheckTimeout.String())
	}
}

func TestPartLimit(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %v", err)
	}

	if config.ServicesPartLimit != 10 {
		t.Errorf("Wrong ServicesPartLimit value: %v", config.ServicesPartLimit)
	}

	if config.LayersPartLimit != 20 {
		t.Errorf("Wrong LayersPartLimit value: %v", config.LayersPartLimit)
	}
}

func TestRemoteNodeFalg(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %v", err)
	}

	if config.RemoteNode != true {
		t.Errorf("Wrong remoteNode value: %v", config.RemoteNode)
	}
}

func TestRunnerFeatures(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %v", err)
	}

	if !reflect.DeepEqual(config.RunnerFeatures, []string{"crun", "runc"}) {
		t.Errorf("Wrong runnerFeatures value: %v", config.RunnerFeatures)
	}
}
