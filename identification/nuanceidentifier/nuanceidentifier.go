package nuanceidentifier

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"strings"

	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

/*******************************************************************************
 * Types
 ******************************************************************************/

// Instance nuance instance
type Instance struct {
	config   instanceConfig
	systemID string
	users    []string
}

type instanceConfig struct {
	SystemIDFile string
	UsersFile    string
}

/*******************************************************************************
 * init
 ******************************************************************************/

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new nuance identifier instance
func New(configJSON []byte) (instance *Instance, err error) {
	log.Info("Create Nuance identification instance")

	instance = &Instance{}

	if configJSON != nil {
		if err = json.Unmarshal(configJSON, &instance.config); err != nil {
			return nil, err
		}
	}

	if instance.config.SystemIDFile == "" {
		return nil, errors.New("system ID file is not defined")
	}

	if instance.config.UsersFile == "" {
		return nil, errors.New("users file is not defined")
	}

	data, err := ioutil.ReadFile(instance.config.SystemIDFile)
	if err != nil {
		return nil, err
	}

	instance.systemID = strings.TrimSpace(string(data))

	data, err = ioutil.ReadFile(instance.config.UsersFile)
	if err != nil {
		return nil, err
	}

	var users struct {
		Claim []string
	}

	if err = json.Unmarshal(data, &users); err != nil {
		return nil, err
	}

	instance.users = users.Claim

	return instance, nil
}

// Close closes vis identifier instance
func (instance *Instance) Close() (err error) {
	log.Info("Close Nuance identification instance")

	return nil
}

// GetSystemID returns the system ID
func (instance *Instance) GetSystemID() (systemID string, err error) {
	return instance.systemID, nil
}

// GetUsers returns the user claims
func (instance *Instance) GetUsers() (users []string, err error) {
	return instance.users, nil
}

// UsersChangedChannel returns users changed channel
func (instance *Instance) UsersChangedChannel() (channel <-chan []string) {
	return nil
}

// ErrorChannel returns error channel
func (instance *Instance) ErrorChannel() (channel <-chan error) {
	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/
