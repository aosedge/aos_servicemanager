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

// Package layermanager provides set of API to controls service fs layers
package layermanager

import (
	amqp "aos_servicemanager/amqphandler"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// LayerManager instance
type LayerManager struct {
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New() (layermanager *LayerManager, err error) {
	layermanager = &LayerManager{}

	return layermanager, nil
}

// ProcessDesiredLayersList add, remove
func (layermanager *LayerManager) ProcessDesiredLayersList(layerList []amqp.LayerInfoFromCloud,
	chain amqp.CertificateChain,
	certs amqp.Certificate) (err error) {

	return nil
}

// GetLayersInfo provied list of already installed fs layers
func (layermanager *LayerManager) GetLayersInfo() (layers []amqp.LayerInfo, err error) {

	return layers, nil
}

// GetLayerPathByDigest privides fs layer path by layer digest
func (layermanager *LayerManager) GetLayerPathByDigest(layerDigest string) (layerPath string, err error) {

	return layerPath, nil
}
