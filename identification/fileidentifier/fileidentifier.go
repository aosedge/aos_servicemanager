// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2020 Renesas Inc.
// Copyright 2020 EPAM Systems Inc.
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

package fileidentifier

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"sync"

	log "github.com/sirupsen/logrus"

	"aos_servicemanager/pluginprovider"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const usersChangedChannelSize = 1

/*******************************************************************************
 * Types
 ******************************************************************************/

// Instance vis identifier instance
type Instance struct {
	sync.Mutex

	config              instanceConfig
	usersChangedChannel chan []string

	systemID string
	users    []string
}

type instanceConfig struct {
	SystemIDPath string `json:"systemIDPath"`
	UsersPath    string `json:"usersPath"`
}

/*******************************************************************************
 * init
 ******************************************************************************/

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new file identifier instance
func New(configJSON []byte) (identifier pluginprovider.Identifier, err error) {
	log.Info("Create file identification instance")

	instance := &Instance{}

	if err = json.Unmarshal(configJSON, &instance.config); err != nil {
		return nil, err
	}

	instance.usersChangedChannel = make(chan []string, usersChangedChannelSize)

	if err = instance.readSystemID(); err != nil {
		return nil, err
	}

	if err = instance.readUsers(); err != nil {
		log.Warnf("Can't read users: %s. Empty users will be used", err)
	}

	return instance, nil
}

// Close closes vis identifier instance
func (instance *Instance) Close() (err error) {
	log.Info("Close file identification instance")

	return nil
}

// GetSystemID returns the system ID
func (instance *Instance) GetSystemID() (systemID string, err error) {
	instance.Lock()
	defer instance.Unlock()

	log.WithField("systemID", instance.systemID).Debug("Get system ID")

	return instance.systemID, err
}

// GetUsers returns the user claims
func (instance *Instance) GetUsers() (users []string, err error) {
	instance.Lock()
	defer instance.Unlock()

	log.WithField("users", instance.users).Debug("Get users")

	return instance.users, err
}

// SetUsers sets the user claims
func (instance *Instance) SetUsers(users []string) (err error) {
	instance.Lock()
	defer instance.Unlock()

	log.WithField("users", users).Debug("Set users")

	if reflect.DeepEqual(instance.users, users) {
		return nil
	}

	instance.users = users

	if len(instance.usersChangedChannel) != usersChangedChannelSize {
		instance.usersChangedChannel <- users
	}

	if err = instance.writeUsers(); err != nil {
		return err
	}

	return nil
}

// UsersChangedChannel returns users changed channel
func (instance *Instance) UsersChangedChannel() (channel <-chan []string) {
	return instance.usersChangedChannel
}

// ErrorChannel returns error channel
func (instance *Instance) ErrorChannel() (channel <-chan error) {
	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (instance *Instance) readSystemID() (err error) {
	data, err := ioutil.ReadFile(instance.config.SystemIDPath)
	if err != nil {
		return err
	}

	instance.systemID = string(data)

	return nil
}

func (instance *Instance) readUsers() (err error) {
	instance.users = make([]string, 0)

	file, err := os.Open(instance.config.UsersPath)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		instance.users = append(instance.users, scanner.Text())
	}

	return nil
}

func (instance *Instance) writeUsers() (err error) {
	file, err := os.Create(instance.config.UsersPath)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)

	for _, claim := range instance.users {
		fmt.Fprintln(writer, claim)
	}

	return writer.Flush()
}
