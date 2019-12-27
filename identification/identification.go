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

package identification

import (
	"encoding/json"
	"fmt"

	"aos_servicemanager/config"
	"aos_servicemanager/database"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

/*******************************************************************************
 * Vars
 ******************************************************************************/

var moduleMap = map[string]NewFunc{}

/*******************************************************************************
 * Types
 ******************************************************************************/

type NewFunc func(configJSON []byte, db *database.Database) (module Module, err error)

// Module identification module interface
type Module interface {
	// Close closes module
	Close() (err error)
	// GetSystemID returns the system ID
	GetSystemID() (vin string, err error)
	// GetUsers returns the user claims
	GetUsers() (users []string, err error)
	// UsersChangedChannel returns users changed channel
	UsersChangedChannel() (channel <-chan []string)
	// ErrorChannel returns error channel
	ErrorChannel() (channel <-chan error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

func Register(name string, newFunc NewFunc) {
	moduleMap[name] = newFunc
}

func New(config *config.Config, db *database.Database) (module Module, err error) {
	newFunc, ok := moduleMap[config.Identification.Module]
	if !ok {
		return nil, fmt.Errorf("identification module '%s' not found", config.Identification.Module)
	}

	paramsJSON, err := json.Marshal(config.Identification.Params)
	if err != nil {
		return nil, err
	}

	return newFunc(paramsJSON, db)
}
