// Package config provides set of API to provide aos configuration
package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Crypt configuration structure with certificates info
type Crypt struct {
	CACert         string
	ClientCert     string
	ClientKey      string
	OfflinePrivKey string
	OfflineCert    string
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
	NetnsBridgeIP      string     `json:"netnsBridgeIP"`
	RAM                *AlertRule `json:"ram"`
	CPU                *AlertRule `json:"cpu"`
	UsedDisk           *AlertRule `json:"usedDisk"`
	InTraffic          *AlertRule `json:"inTraffic"`
	OutTraffic         *AlertRule `json:"outTraffic"`
}

// Logging configuration for system and service logging
type Logging struct {
	MaxPartSize uint64 `json:"maxPartSize"`
}

// Alerts configuration for alerts
type Alerts struct {
	SendPeriod         Duration `json:"sendPeriod"`
	MaxMessageSize     int      `json:"maxMessagesize"`
	MaxOfflineMessages int      `json:"maxOfflineMessages"`
}

// Config instance
type Config struct {
	Crypt               Crypt      `json:"fcrypt"`
	ServiceDiscoveryURL string     `json:"serviceDiscovery"`
	VISServerURL        string     `json:"visServer"`
	WorkingDir          string     `json:"workingDir"`
	DefaultServiceTTL   uint64     `json:"defaultServiceTTLDays"`
	Monitoring          Monitoring `json:"monitoring"`
	Logging             Logging    `json:"logging"`
	Alerts              Alerts     `json:"alerts"`
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
		Monitoring: Monitoring{
			SendPeriod:         Duration{1 * time.Minute},
			PollPeriod:         Duration{10 * time.Second},
			MaxOfflineMessages: 25,
			NetnsBridgeIP:      "172.19.0.0/16"},
		Logging: Logging{
			MaxPartSize: 524288},
		Alerts: Alerts{
			SendPeriod:         Duration{10 * time.Second},
			MaxMessageSize:     65536,
			MaxOfflineMessages: 25}}

	if err = json.Unmarshal(raw, &config); err != nil {
		return config, err
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
		d.Duration = time.Duration(value)
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
		return fmt.Errorf("Invalid duration value: %v", value)
	}
}
