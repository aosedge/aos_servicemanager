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

package vismodule

import "aos_servicemanager/database"

/*******************************************************************************
 * Consts
 ******************************************************************************/

const Name = "vis"

/*******************************************************************************
 * Types
 ******************************************************************************/

// VisModule vis module instance
type VisModule struct {
}

// ServiceProvider provides service entry
type ServiceProvider interface {
	GetService(id string) (entry database.ServiceEntry, err error)
}

/*******************************************************************************
 * init
 ******************************************************************************/

/*******************************************************************************
 * Public
 ******************************************************************************/

func New(configJSON []byte, serviceProvider ServiceProvider) (module *VisModule, err error) {
	return &VisModule{}, nil
}

func (module *VisModule) Close() (err error) {
	return nil
}

func (module *VisModule) GetSystemID() (vin string, err error) {
	return "", nil
}

func (module *VisModule) GetUsers() (users []string, err error) {
	return nil, nil
}

func (module *VisModule) UsersChangedChannel() (channel <-chan []string) {
	return nil
}

func (module *VisModule) ErrorChannel() (channel <-chan error) {
	return nil
}
