// Package config provides set of API to provide aos configuration
package config

import (
	"encoding/json"
	"io/ioutil"
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

// Config instance
type Config struct {
	Crypt               Crypt  `json:"fcrypt"`
	ServiceDiscoveryURL string `json:"serviceDiscovery"`
	VISServerURL        string `json:"visServer"`
	WorkingDir          string `json:"workingDir"`
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

	config = &Config{}

	if err = json.Unmarshal(raw, &config); err != nil {
		return config, err
	}

	return config, nil
}
