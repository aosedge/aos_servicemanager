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

package pluginprovider

import (
	"encoding/json"
	"fmt"

	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// NewIdentifier plugin edintifier
type NewIdentifier func(configJSON []byte) (dierectIdentifier Identifier, err error)

//Identifier interface for identifier
type Identifier interface {
	// Close closes identifier
	Close() (err error)
	// GetSystemID returns the system ID
	GetSystemID() (systemID string, err error)
	// GetUsers returns the user claims
	GetUsers() (users []string, err error)
	// SetUsers sets the user claims
	SetUsers(users []string) (err error)
	// UsersChangedChannel returns users changed channel
	UsersChangedChannel() (channel <-chan []string)
	// ErrorChannel returns error channel
	ErrorChannel() (channel <-chan error)
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var identifiers = make(map[string]NewIdentifier)

/*******************************************************************************
 * Public
 ******************************************************************************/

// RegisterIdentifier registers identifier
func RegisterIdentifier(plugin string, newFunc NewIdentifier) {
	log.WithField("plugin", plugin).Info("Register identifier")

	identifiers[plugin] = newFunc
}

//GetIdentifier create plugged in identifier
func GetIdentifier(identifierType string, configJSON json.RawMessage) (identifier Identifier, err error) {
	newFunc, ok := identifiers[identifierType]
	if !ok {
		return nil, fmt.Errorf("plugin %s not found", identifierType)
	}

	return newFunc(configJSON)
}
