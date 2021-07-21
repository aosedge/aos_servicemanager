// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2021 Renesas Inc.
// Copyright 2021 EPAM Systems Inc.
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

package unitstatushandler

import (
	"encoding/json"
	"sync"

	amqp "aos_servicemanager/amqphandler"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// StatusSender sends unit status to cloud
type StatusSender interface {
	SendUnitStatus(unitStatus amqp.UnitStatus) (err error)
}

// BoardConfigUpdater updates board configuration
type BoardConfigUpdater interface {
	GetBoardConfigInfo() (info []amqp.BoardConfigInfo, err error)
	CheckBoardConfig(configJSON json.RawMessage) (vendorVersion string, err error)
	UpdateBoardConfig(configJSON json.RawMessage) (err error)
}

// ComponentUpdater updates components
type ComponentUpdater interface {
	GetComponentsInfo() (info []amqp.ComponentInfo, err error)
	UpdateComponents(components []amqp.ComponentInfoFromCloud,
		chains []amqp.CertificateChain, certs []amqp.Certificate) (err error)
	UpdateStatus() (statusChannel <-chan amqp.ComponentInfo)
}

// LayerUpdater updates layers
type LayerUpdater interface {
	GetLayersInfo() (info []amqp.LayerInfo, err error)
	InstallLayer(layerInfo amqp.LayerInfoFromCloud,
		chains []amqp.CertificateChain, certs []amqp.Certificate) (statusChannel <-chan amqp.LayerInfo)
	UninstallLayer(digest string) (statusChannel <-chan amqp.LayerInfo)
}

// ServiceUpdater updates services
type ServiceUpdater interface {
	GetServicesInfo() (info []amqp.ServiceInfo, err error)
	InstallService(serviceInfo amqp.ServiceInfoFromCloud,
		chains []amqp.CertificateChain, certs []amqp.Certificate) (statusChannel <-chan amqp.ServiceInfo)
	UninstallService(id string) (statusChannel <-chan amqp.ServiceInfo)
	StartServices()
	StopServices()
}

// Instance instance of unit status handler
type Instance struct {
	sync.Mutex

	isDesiredStatusProcessing bool
	pendindDesiredStatus      *amqp.DecodedDesiredStatus
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new unit status handler instance
func New(
	boardConfigUpdater BoardConfigUpdater,
	componentUpdater ComponentUpdater,
	layerUpdater LayerUpdater,
	serviceUpdater ServiceUpdater,
	statusSender StatusSender) (instance *Instance, err error) {
	return &Instance{}, nil
}

// Init initialize unit status
func (instance *Instance) Init() (err error) {
	return nil
}

// ProcessDesiredStatus processes desired status
func (instance *Instance) ProcessDesiredStatus(desiredStatus amqp.DecodedDesiredStatus) {
	instance.Lock()
	defer instance.Unlock()

	if instance.isDesiredStatusProcessing {
		instance.pendindDesiredStatus = &desiredStatus

		return
	}

	instance.isDesiredStatusProcessing = true

	go instance.processDesiredStatus(desiredStatus)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (instance *Instance) finishProcessDesiredStatus() {
	instance.Lock()
	defer instance.Unlock()

	if instance.pendindDesiredStatus != nil {
		go instance.processDesiredStatus(*instance.pendindDesiredStatus)
		instance.pendindDesiredStatus = nil
	} else {
		instance.isDesiredStatusProcessing = false
	}
}

func (instance *Instance) processDesiredStatus(desiredStatus amqp.DecodedDesiredStatus) {
	defer instance.finishProcessDesiredStatus()
}
