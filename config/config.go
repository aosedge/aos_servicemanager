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

// Duration represents duration in format "00:00:00".
type Duration struct {
	time.Duration
}

// AlertRule describes alert rule.
type AlertRule struct {
	MinTimeout   Duration `json:"minTimeout"`
	MinThreshold uint64   `json:"minThreshold"`
	MaxThreshold uint64   `json:"maxThreshold"`
}

// Monitoring configuration for system monitoring.
type Monitoring struct {
	Disabled   bool       `json:"disabled"`
	SendPeriod Duration   `json:"sendPeriod"`
	PollPeriod Duration   `json:"pollPeriod"`
	RAM        *AlertRule `json:"ram"`
	CPU        *AlertRule `json:"cpu"`
	UsedDisk   *AlertRule `json:"usedDisk"`
	InTraffic  *AlertRule `json:"inTraffic"`
	OutTraffic *AlertRule `json:"outTraffic"`
}

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
	CACert                    string     `json:"caCert"`
	SMServerURL               string     `json:"smServerUrl"`
	CertStorage               string     `json:"certStorage"`
	IAMServerURL              string     `json:"iamServer"`
	IAMPublicServerURL        string     `json:"iamPublicServer"`
	WorkingDir                string     `json:"workingDir"`
	StorageDir                string     `json:"storageDir"`
	ServicesDir               string     `json:"servicesDir"`
	LayersDir                 string     `json:"layersDir"`
	BoardConfigFile           string     `json:"boardConfigFile"`
	DefaultServiceTTLDays     uint64     `json:"defaultServiceTtlDays"`
	ServiceHealthCheckTimeout Duration   `json:"serviceHealthCheckTimeout"`
	Monitoring                Monitoring `json:"monitoring"`
	Logging                   Logging    `json:"logging"`
	Alerts                    Alerts     `json:"alerts"`
	HostBinds                 []string   `json:"hostBinds"`
	Hosts                     []Host     `json:"hosts,omitempty"`
	Migration                 Migration  `json:"migration"`
	Runner                    string     `json:"runner"`
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
		DefaultServiceTTLDays:     30, // nolint:gomnd
		ServiceHealthCheckTimeout: Duration{35 * time.Second},
		Runner:                    "runc",
		Monitoring: Monitoring{
			SendPeriod: Duration{1 * time.Minute},
			PollPeriod: Duration{10 * time.Second},
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

// MarshalJSON marshals JSON Duration type.
func (d Duration) MarshalJSON() (b []byte, err error) {
	t, err := time.Parse("15:04:05", "00:00:00")
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	t.Add(d.Duration)

	b, err = json.Marshal(t.Add(d.Duration).Format("15:04:05"))
	if err != nil {
		return b, aoserrors.Wrap(err)
	}

	return b, nil
}

// UnmarshalJSON unmarshals JSON Duration type.
func (d *Duration) UnmarshalJSON(b []byte) (err error) {
	var v interface{}

	if err := json.Unmarshal(b, &v); err != nil {
		return aoserrors.Wrap(err)
	}

	switch value := v.(type) {
	case float64:
		d.Duration = time.Duration(value) * time.Second

		return nil

	case string:
		tmp, err := time.ParseDuration(value)
		if err != nil {
			t1, err := time.Parse("15:04:05", value)
			if err != nil {
				return aoserrors.Wrap(err)
			}

			t2, err := time.Parse("15:04:05", "00:00:00")
			if err != nil {
				return aoserrors.Wrap(err)
			}

			tmp = t1.Sub(t2)
		}

		d.Duration = tmp

		return nil

	default:
		return aoserrors.Errorf("invalid duration value: %v", value)
	}
}
