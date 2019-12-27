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

package nuancemodule

import (
	"encoding/json"

	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const Name = "nuance"

/*******************************************************************************
 * Types
 ******************************************************************************/

// NuanceModule nuance module instance
type NuanceModule struct {
	config moduleConfig
}

type moduleConfig struct {
}

/*******************************************************************************
 * init
 ******************************************************************************/

/*******************************************************************************
 * Public
 ******************************************************************************/

func New(configJSON []byte) (module *NuanceModule, err error) {
	log.Info("Create Nuance identification module")

	module = &NuanceModule{}

	if configJSON != nil {
		if err = json.Unmarshal(configJSON, &module.config); err != nil {
			return nil, err
		}
	}

	return module, nil
}

func (module *NuanceModule) Close() (err error) {
	log.Info("Close Nuance identification module")

	return nil
}

func (module *NuanceModule) GetSystemID() (vin string, err error) {
	return "1234567890", nil
}

func (module *NuanceModule) GetUsers() (users []string, err error) {
	return []string{"this-is-super-user"}, nil
}

func (module *NuanceModule) UsersChangedChannel() (channel <-chan []string) {
	return nil
}

func (module *NuanceModule) ErrorChannel() (channel <-chan error) {
	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/
