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
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"

	amqp "aos_servicemanager/amqphandler"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const sendStatusPeriod = 30 * time.Second

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

	boardConfigUpdater BoardConfigUpdater
	componentUpdater   ComponentUpdater
	layerUpdater       LayerUpdater
	serviceUpdater     ServiceUpdater
	statusSender       StatusSender

	isDesiredStatusProcessing bool
	pendindDesiredStatus      *amqp.DecodedDesiredStatus

	statusTimer       *time.Timer
	boardConfigStatus itemStatus
}

type statusDescriptor struct {
	amqpStatus interface{}
}

type itemStatus []statusDescriptor

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
	log.Debug("Create unit status handler")

	return &Instance{
		boardConfigUpdater: boardConfigUpdater,
		componentUpdater:   componentUpdater,
		layerUpdater:       layerUpdater,
		serviceUpdater:     serviceUpdater,
		statusSender:       statusSender,
	}, nil
}

// Close closes unit status handler
func (instance *Instance) Close() (err error) {
	instance.Lock()
	defer instance.Unlock()

	log.Debug("Close unit status handler")

	if instance.statusTimer != nil {
		instance.statusTimer.Stop()
	}

	return nil
}

// Init initialize unit status
func (instance *Instance) Init() (err error) {
	log.Debug("Init unit status")

	instance.boardConfigStatus = nil

	boardConfigInfo, err := instance.boardConfigUpdater.GetBoardConfigInfo()
	if err != nil {
		return err
	}

	for _, info := range boardConfigInfo {
		instance.Lock()
		instance.updateBoardConfigStatus(info)
		instance.Unlock()
	}

	instance.Lock()
	defer instance.Unlock()

	instance.sendCurrentStatus()

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

func (descriptor *statusDescriptor) getStatus() (status string) {
	switch amqpStatus := descriptor.amqpStatus.(type) {
	case *amqp.BoardConfigInfo:
		return amqpStatus.Status

	default:
		return amqp.UnknownStatus
	}
}

func (descriptor *statusDescriptor) getVersion() (version string) {
	switch amqpStatus := descriptor.amqpStatus.(type) {
	case *amqp.BoardConfigInfo:
		return amqpStatus.VendorVersion

	default:
		return ""
	}
}

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

	if desiredStatus.BoardConfig != nil {
		if err := instance.updateBoardConfig(desiredStatus.BoardConfig); err != nil {
			log.Errorf("Can't update board config: %s", err)
		}
	}
}

func (instance *Instance) sendCurrentStatus() {
	unitStatus := amqp.UnitStatus{
		BoardConfig: make([]amqp.BoardConfigInfo, 0, len(instance.boardConfigStatus)),
	}

	for _, status := range instance.boardConfigStatus {
		unitStatus.BoardConfig = append(unitStatus.BoardConfig, *status.amqpStatus.(*amqp.BoardConfigInfo))
	}

	if err := instance.statusSender.SendUnitStatus(unitStatus); err != nil {
		log.Errorf("Can't send unit status: %s", err)
	}

	if instance.statusTimer != nil {
		instance.statusTimer.Stop()
		instance.statusTimer = nil
	}
}

func (instance *Instance) updateBoardConfigStatus(boardConfigInfo amqp.BoardConfigInfo) {
	log.WithFields(log.Fields{
		"status":        boardConfigInfo.Status,
		"vendorVersion": boardConfigInfo.VendorVersion,
		"error":         boardConfigInfo.Error}).Debug("Update board config status")

	instance.updateStatus(&instance.boardConfigStatus, statusDescriptor{&boardConfigInfo})
}

func (instance *Instance) statusChanged() {
	if instance.statusTimer != nil {
		return
	}

	instance.statusTimer = time.AfterFunc(sendStatusPeriod, func() {
		instance.Lock()
		defer instance.Unlock()

		instance.sendCurrentStatus()
	})
}

func (instance *Instance) updateStatus(status *itemStatus, descriptor statusDescriptor) {
	defer instance.statusChanged()

	if descriptor.getStatus() == amqp.InstalledStatus {
		*status = itemStatus{descriptor}

		return
	}

	for i, element := range *status {
		if element.getVersion() == descriptor.getVersion() {
			(*status)[i] = descriptor

			return
		}
	}

	*status = append(*status, descriptor)
}

func (instance *Instance) updateBoardConfig(config json.RawMessage) (err error) {
	log.Debug("Update board config")

	var boardConfigInfo amqp.BoardConfigInfo

	defer func() {
		if err != nil {
			boardConfigInfo.Status = amqp.ErrorStatus
			boardConfigInfo.Error = err.Error()
		}

		instance.updateBoardConfigStatus(boardConfigInfo)
	}()

	if boardConfigInfo.VendorVersion, err = instance.boardConfigUpdater.CheckBoardConfig(config); err != nil {
		return err
	}

	boardConfigInfo.Status = amqp.InstallingStatus

	instance.updateBoardConfigStatus(boardConfigInfo)

	instance.serviceUpdater.StopServices()
	defer instance.serviceUpdater.StartServices()

	if err = instance.boardConfigUpdater.UpdateBoardConfig(config); err != nil {
		return aoserrors.Wrap(err)
	}

	boardConfigInfo.Status = amqp.InstalledStatus

	return nil
}
