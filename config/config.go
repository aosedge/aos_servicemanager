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

// Package config provides set of API to provide aos configuration
package config

import (
	"encoding/json"
	"io/ioutil"
	"path"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/resourcemonitor"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	defaultServiceAlertPriority = 4
	defaultSystemAlertPriority  = 3
	maxAlertPriorityLevel       = 7
	minAlertPriorityLevel       = 0
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Logging configuration for system and service logging.
type Logging struct {
	MaxPartSize  uint64 `json:"maxPartSize"`
	MaxPartCount uint64 `json:"maxPartCount"`
}

// Alerts configuration for alerts.
type Alerts struct {
	Disabled             bool     `json:"disabled"`
	Filter               []string `json:"filter"`
	ServiceAlertPriority int      `json:"serviceAlertPriority"`
	SystemAlertPriority  int      `json:"systemAlertPriority"`
}

// Host strunct represent entry in /etc/hosts.
type Host struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
}

// Migration struct represents path for db migration.
type Migration struct {
	MigrationPath       string `json:"migrationPath"`
	MergedMigrationPath string `json:"mergedMigrationPath"`
}

// Config instance.
type Config struct {
	CACert             string `json:"caCert"`
	SMServerURL        string `json:"smServerUrl"`
	CertStorage        string `json:"certStorage"`
	IAMServerURL       string `json:"iamServer"`
	IAMPublicServerURL string `json:"iamPublicServer"`
	WorkingDir         string `json:"workingDir"`
	StorageDir         string `json:"storageDir"`
	StateDir           string `json:"stateDir"`
	ServicesDir        string `json:"servicesDir"`
	LayersDir          string `json:"layersDir"`
	DownloadDir        string `json:"downloadDir"`

	BoardConfigFile           string                 `json:"boardConfigFile"`
	DefaultServiceTTLDays     uint64                 `json:"defaultServiceTtlDays"`
	ServiceHealthCheckTimeout aostypes.Duration      `json:"serviceHealthCheckTimeout"`
	Monitoring                resourcemonitor.Config `json:"monitoring"`
	Logging                   Logging                `json:"logging"`
	Alerts                    Alerts                 `json:"alerts"`
	HostBinds                 []string               `json:"hostBinds"`
	Hosts                     []Host                 `json:"hosts,omitempty"`
	Migration                 Migration              `json:"migration"`
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new config object.
func New(fileName string) (config *Config, err error) {
	raw, err := ioutil.ReadFile(fileName)
	if err != nil {
		return config, aoserrors.Wrap(err)
	}

	config = &Config{
		DefaultServiceTTLDays:     30,                                            // nolint:gomnd
		ServiceHealthCheckTimeout: aostypes.Duration{Duration: 35 * time.Second}, // nolint:gomnd
		Monitoring: resourcemonitor.Config{
			SendPeriod: aostypes.Duration{Duration: 1 * time.Minute},
			PollPeriod: aostypes.Duration{Duration: 10 * time.Second},
		},
		Logging: Logging{
			MaxPartSize:  524288, // nolint:gomnd
			MaxPartCount: 20,     // nolint:gomnd
		},
		Alerts: Alerts{
			SystemAlertPriority:  defaultSystemAlertPriority,
			ServiceAlertPriority: defaultServiceAlertPriority,
		},
	}

	if err = json.Unmarshal(raw, &config); err != nil {
		return config, aoserrors.Wrap(err)
	}

	if config.Monitoring.WorkingDir == "" {
		config.Monitoring.WorkingDir = config.WorkingDir
	}

	if config.Monitoring.StorageDir == "" {
		config.Monitoring.WorkingDir = config.StorageDir
	}

	if config.CertStorage == "" {
		config.CertStorage = "/var/aos/crypt/sm/"
	}

	if config.StorageDir == "" {
		config.StorageDir = path.Join(config.WorkingDir, "storages")
	}

	if config.LayersDir == "" {
		config.LayersDir = path.Join(config.WorkingDir, "srvlib")
	}

	if config.ServicesDir == "" {
		config.ServicesDir = path.Join(config.WorkingDir, "servicemanager", "services")
	}

	if config.DownloadDir == "" {
		config.DownloadDir = path.Join(config.WorkingDir, "download")
	}

	if config.BoardConfigFile == "" {
		config.BoardConfigFile = path.Join(config.WorkingDir, "aos_board.cfg")
	}

	if config.Migration.MigrationPath == "" {
		config.Migration.MigrationPath = "/usr/share/aos/servicemanager/migration"
	}

	if config.Migration.MergedMigrationPath == "" {
		config.Migration.MergedMigrationPath = path.Join(config.WorkingDir, "mergedMigration")
	}

	if config.Alerts.ServiceAlertPriority > maxAlertPriorityLevel ||
		config.Alerts.ServiceAlertPriority < minAlertPriorityLevel {
		log.Warnf("Default value %d for service alert priority is assigned", defaultServiceAlertPriority)
		config.Alerts.ServiceAlertPriority = defaultServiceAlertPriority
	}

	if config.Alerts.SystemAlertPriority > maxAlertPriorityLevel ||
		config.Alerts.SystemAlertPriority < minAlertPriorityLevel {
		log.Warnf("Default value %d for system alert priority is assigned", defaultSystemAlertPriority)
		config.Alerts.SystemAlertPriority = defaultSystemAlertPriority
	}

	return config, nil
}
