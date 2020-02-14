package nuanceidentifier

import (
	"encoding/json"

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
	config instanceConfig
}

type instanceConfig struct {
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

	return instance, nil
}

// Close closes vis identifier instance
func (instance *Instance) Close() (err error) {
	log.Info("Close Nuance identification instance")

	return nil
}

// GetSystemID returns the system ID
func (instance *Instance) GetSystemID() (systemID string, err error) {
	return "1234567890", nil
}

// GetUsers returns the user claims
func (instance *Instance) GetUsers() (users []string, err error) {
	return []string{"this-is-super-user"}, nil
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
