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
	layersDir         string
	layerInfoProvider LayerInfoProvider
}

// LayerInfoProvider provides API to add, remove or access layer information
type LayerInfoProvider interface {
	AddLayer(digest, layerID, path, osVersion string) (err error)
	DeleteLayerByDigest(digest string) (err error)
	GetLayerPathByDigest(digest string) (path string, err error)
	GetLayersInfo() (layersList []amqp.LayerInfo, err error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New(layersStorageDir string, infoProvider LayerInfoProvider) (layermanager *LayerManager, err error) {
	layermanager = &LayerManager{layersDir: layersStorageDir, layerInfoProvider: infoProvider}

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

	return layermanager.layerInfoProvider.GetLayersInfo()
}

// GetLayerPathByDigest privides fs layer path by layer digest
func (layermanager *LayerManager) GetLayerPathByDigest(layerDigest string) (layerPath string, err error) {

	return layerPath, nil
}
