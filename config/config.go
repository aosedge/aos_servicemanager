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

// Package config provides set of API to provide aos configuration
package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path"
	"time"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Crypt configuration structure with crypto attributes
type Crypt struct {
	CACert    string `json:"CACert"`
	TpmDevice string `json:"tpmDevice,omitempty"`
}

// UmController configuration for update controller
type UmController struct {
	ServerURL     string           `json:"serverUrl"`
	Cert          string           `json:"cert"`
	Key           string           `json:"key"`
	FileServerURL string           `json:"fileServerUrl,omitempty"`
	UmClients     []UmClientConfig `json:"umClients"`
	UpdateDir     string           `json:"updateDir"`
}

// UmClientConfig update manager config
type UmClientConfig struct {
	UmID     string `json:"umId"`
	Priority uint32 `json:"priority"`
	IsLocal  bool   `json:"isLocal,omitempty"`
}

// Duration represents duration in format "00:00:00"
type Duration struct {
	time.Duration
}

// AlertRule describes alert rule
type AlertRule struct {
	MinTimeout   Duration `json:"minTimeout"`
	MinThreshold uint64   `json:"minThreshold"`
	MaxThreshold uint64   `json:"maxThreshold"`
}

// Monitoring configuration for system monitoring
type Monitoring struct {
	Disabled           bool       `json:"disabled"`
	SendPeriod         Duration   `json:"sendPeriod"`
	PollPeriod         Duration   `json:"pollPeriod"`
	MaxOfflineMessages int        `json:"maxOfflineMessages"`
	BridgeIP           string     `json:"bridgeIP"`
	RAM                *AlertRule `json:"ram"`
	CPU                *AlertRule `json:"cpu"`
	UsedDisk           *AlertRule `json:"usedDisk"`
	InTraffic          *AlertRule `json:"inTraffic"`
	OutTraffic         *AlertRule `json:"outTraffic"`
}

// Logging configuration for system and service logging
type Logging struct {
	MaxPartSize  uint64 `json:"maxPartSize"`
	MaxPartCount uint64 `json:"maxPartCount"`
}

// Alerts configuration for alerts
type Alerts struct {
	Disabled           bool     `json:"disabled"`
	SendPeriod         Duration `json:"sendPeriod"`
	MaxMessageSize     int      `json:"maxMessagesize"`
	MaxOfflineMessages int      `json:"maxOfflineMessages"`
	Filter             []string `json:"filter"`
}

// Host strunct represent entry in /etc/hosts
type Host struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
}

// Migration struct represents path for db migration
type Migration struct {
	MigrationPath       string `json:"migrationPath"`
	MergedMigrationPath string `json:"mergedMigrationPath"`
}

// Config instance
type Config struct {
	Crypt               Crypt        `json:"fcrypt"`
	ServiceDiscoveryURL string       `json:"serviceDiscovery"`
	UmController        UmController `json:"umController"`
	VISServerURL        string       `json:"visServer"`
	CMServerURL         string       `json:"cmServer"`
	WorkingDir          string       `json:"workingDir"`
	UpdateDir           string       `json:"updateDir"`
	StorageDir          string       `json:"storageDir"`
	LayersDir           string       `json:"layersDir"`
	BoardConfigFile     string       `json:"boardConfigFile"`
	DefaultServiceTTL   uint64       `json:"defaultServiceTTLDays"`
	UnitStatusTimeout   uint64       `json:"unitStatusTimeout"`
	Monitoring          Monitoring   `json:"monitoring"`
	Logging             Logging      `json:"logging"`
	Alerts              Alerts       `json:"alerts"`
	Identifier          struct {
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	} `json:"identifier"`
	HostBinds []string  `json:"hostBinds"`
	Hosts     []Host    `json:"hosts,omitempty"`
	Migration Migration `json:"migration"`
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new config object
func New(fileName string) (config *Config, err error) {
	raw, err := ioutil.ReadFile(fileName)
	if err != nil {
		return config, err
	}

	config = &Config{
		DefaultServiceTTL: 30,
		UnitStatusTimeout: 30,
		Monitoring: Monitoring{
			SendPeriod:         Duration{1 * time.Minute},
			PollPeriod:         Duration{10 * time.Second},
			MaxOfflineMessages: 25,
			BridgeIP:           "172.19.0.0/16"},
		Logging: Logging{
			MaxPartSize:  524288,
			MaxPartCount: 20},
		Alerts: Alerts{
			SendPeriod:         Duration{10 * time.Second},
			MaxMessageSize:     65536,
			MaxOfflineMessages: 25}}

	if err = json.Unmarshal(raw, &config); err != nil {
		return config, err
	}

	if config.StorageDir == "" {
		config.StorageDir = path.Join(config.WorkingDir, "storages")
	}

	if config.UpdateDir == "" {
		config.UpdateDir = path.Join(config.WorkingDir, "update")
	}

	if config.LayersDir == "" {
		config.LayersDir = path.Join(config.WorkingDir, "srvlib")
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

	if config.UmController.UpdateDir == "" {
		config.UmController.UpdateDir = path.Join(config.WorkingDir, "update")
	}

	return config, nil
}

// MarshalJSON marshals JSON Duration type
func (d Duration) MarshalJSON() (b []byte, err error) {
	t, err := time.Parse("15:04:05", "00:00:00")
	if err != nil {
		return nil, err
	}
	t.Add(d.Duration)

	return json.Marshal(t.Add(d.Duration).Format("15:04:05"))
}

// UnmarshalJSON unmarshals JSON Duration type
func (d *Duration) UnmarshalJSON(b []byte) (err error) {
	var v interface{}

	if err := json.Unmarshal(b, &v); err != nil {
		return err
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
				return err
			}
			t2, err := time.Parse("15:04:05", "00:00:00")
			if err != nil {
				return err
			}

			tmp = t1.Sub(t2)
		}

		d.Duration = tmp

		return nil

	default:
		return fmt.Errorf("invalid duration value: %v", value)
	}
}
