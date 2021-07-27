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
	"context"
	"encoding/json"
	"reflect"
	"strconv"
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

	ctx               context.Context
	cancel            context.CancelFunc
	statusTimer       *time.Timer
	boardConfigStatus itemStatus
	componentStatuses map[string]*itemStatus
	layerStatuses     map[string]*itemStatus
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

	instance = &Instance{
		boardConfigUpdater: boardConfigUpdater,
		componentUpdater:   componentUpdater,
		layerUpdater:       layerUpdater,
		serviceUpdater:     serviceUpdater,
		statusSender:       statusSender,
	}

	instance.ctx, instance.cancel = context.WithCancel(context.Background())

	go instance.handleComponentStatuses()

	return instance, nil
}

// Close closes unit status handler
func (instance *Instance) Close() (err error) {
	instance.Lock()
	defer instance.Unlock()

	log.Debug("Close unit status handler")

	if instance.statusTimer != nil {
		instance.statusTimer.Stop()
	}

	instance.cancel()

	return nil
}

// Init initialize unit status
func (instance *Instance) Init() (err error) {
	// This function can't be fully locked because GetComponentsInfo may be blocked for while and
	// at same time componentUpdater may send component status through status channel which is handled
	// in handleComponentStatuses. To avoid dead lock componentUpdater.GetComponentsInfo() and
	// handleComponentStatuses should not be locked at same time.

	log.Debug("Init unit status")

	// Get initial board config info

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

	// Get initial components info

	instance.componentStatuses = make(map[string]*itemStatus)

	componentsInfo, err := instance.componentUpdater.GetComponentsInfo()
	if err != nil {
		return err
	}

	for _, componentInfo := range componentsInfo {
		instance.Lock()
		instance.updateComponentStatus(componentInfo)
		instance.Unlock()
	}

	// Get initial layers info

	instance.layerStatuses = make(map[string]*itemStatus)

	layersInfo, err := instance.layerUpdater.GetLayersInfo()
	if err != nil {
		return err
	}

	for _, layerInfo := range layersInfo {
		instance.Lock()
		instance.updateLayerStatus(layerInfo)
		instance.Unlock()
	}

	instance.Lock()
	defer instance.Unlock()

	// Send current status

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

	case *amqp.ComponentInfo:
		return amqpStatus.Status

	case *amqp.LayerInfo:
		return amqpStatus.Status

	default:
		return amqp.UnknownStatus
	}
}

func (descriptor *statusDescriptor) getVersion() (version string) {
	switch amqpStatus := descriptor.amqpStatus.(type) {
	case *amqp.BoardConfigInfo:
		return amqpStatus.VendorVersion

	case *amqp.ComponentInfo:
		return amqpStatus.VendorVersion

	case *amqp.LayerInfo:
		return strconv.FormatUint(amqpStatus.AosVersion, 10)

	default:
		return ""
	}
}

func (status *itemStatus) isInstalled() (installed bool, descriptor statusDescriptor) {
	for _, element := range *status {
		if element.getStatus() == amqp.InstalledStatus {
			return true, element
		}
	}

	return false, statusDescriptor{}
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

	if err := instance.updateComponents(
		desiredStatus.Components, desiredStatus.CertificateChains, desiredStatus.Certificates); err != nil {
		log.Errorf("Can't update components: %s", err)
	}

	if err := instance.installLayers(
		desiredStatus.Layers, desiredStatus.CertificateChains, desiredStatus.Certificates); err != nil {
		log.Errorf("Can't install layers: %s", err)
	}

	if err := instance.removeLayers(
		desiredStatus.Layers, desiredStatus.CertificateChains, desiredStatus.Certificates); err != nil {
		log.Errorf("Can't remove layers: %s", err)
	}
}

func (instance *Instance) sendCurrentStatus() {
	unitStatus := amqp.UnitStatus{
		BoardConfig: make([]amqp.BoardConfigInfo, 0, len(instance.boardConfigStatus)),
		Components:  make([]amqp.ComponentInfo, 0, len(instance.componentStatuses)),
		Layers:      make([]amqp.LayerInfo, 0, len(instance.layerStatuses)),
	}

	for _, status := range instance.boardConfigStatus {
		unitStatus.BoardConfig = append(unitStatus.BoardConfig, *status.amqpStatus.(*amqp.BoardConfigInfo))
	}

	for _, componentStatus := range instance.componentStatuses {
		for _, status := range *componentStatus {
			unitStatus.Components = append(unitStatus.Components, *status.amqpStatus.(*amqp.ComponentInfo))
		}
	}

	for _, layerStatus := range instance.layerStatuses {
		for _, status := range *layerStatus {
			unitStatus.Layers = append(unitStatus.Layers, *status.amqpStatus.(*amqp.LayerInfo))
		}
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

func (instance *Instance) updateComponentStatus(componentInfo amqp.ComponentInfo) {
	log.WithFields(log.Fields{
		"id":            componentInfo.ID,
		"status":        componentInfo.Status,
		"vendorVersion": componentInfo.VendorVersion,
		"error":         componentInfo.Error}).Debug("Update component status")

	componentStatus, ok := instance.componentStatuses[componentInfo.ID]
	if !ok {
		componentStatus = &itemStatus{}
		instance.componentStatuses[componentInfo.ID] = componentStatus
	}

	instance.updateStatus(componentStatus, statusDescriptor{&componentInfo})
}

func (instance *Instance) updateLayerStatus(layerInfo amqp.LayerInfo) {
	log.WithFields(log.Fields{
		"id":         layerInfo.ID,
		"digest":     layerInfo.Digest,
		"status":     layerInfo.Status,
		"aosVersion": layerInfo.AosVersion,
		"error":      layerInfo.Error}).Debug("Update layer status")

	layerStatus, ok := instance.layerStatuses[layerInfo.Digest]
	if !ok {
		layerStatus = &itemStatus{}
		instance.layerStatuses[layerInfo.Digest] = layerStatus
	}

	instance.updateStatus(layerStatus, statusDescriptor{&layerInfo})
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

func (instance *Instance) handleComponentStatuses() {
	for {
		select {
		case componentInfo, ok := <-instance.componentUpdater.UpdateStatus():
			if !ok {
				return
			}

			instance.Lock()
			instance.updateComponentStatus(componentInfo)
			instance.Unlock()

		case <-instance.ctx.Done():
			return
		}
	}
}

func (instance *Instance) updateComponents(components []amqp.ComponentInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (err error) {
	log.Debug("Update components")

	var updateComponents []amqp.ComponentInfoFromCloud

	for _, component := range components {
		updated := false

		if itemStatus, ok := instance.componentStatuses[component.ID]; ok {
			if installed, descriptor := itemStatus.isInstalled(); installed &&
				descriptor.getVersion() == component.VendorVersion {
				updated = true
			}
		}

		if !updated {
			updateComponents = append(updateComponents, component)
		}
	}

	if len(updateComponents) == 0 {
		log.Debug("All components are up to date")

		return nil
	}

	if err = instance.componentUpdater.UpdateComponents(updateComponents, chains, certs); err != nil {
		return err
	}

	return nil
}

func (instance *Instance) installLayers(desiredLayers []amqp.LayerInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (err error) {
	log.Debug("Install layers")

	var selectCases []reflect.SelectCase

	for _, desiredLayer := range desiredLayers {
		installed := false

		if layerStatus, ok := instance.layerStatuses[desiredLayer.Digest]; ok {
			installed, _ = layerStatus.isInstalled()
		}

		if !installed {
			selectCases = append(selectCases, reflect.SelectCase{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf(instance.layerUpdater.InstallLayer(desiredLayer, chains, certs)),
			})
		}
	}

	if len(selectCases) == 0 {
		log.Debug("No layers need to be installed")

		return nil
	}

	instance.processSelectedCases(selectCases, func(value interface{}) {
		instance.Lock()
		defer instance.Unlock()

		layerInfo := value.(amqp.LayerInfo)

		if layerInfo.Status == amqp.ErrorStatus {
			if err == nil {
				err = aoserrors.New(layerInfo.Error)
			}
		}

		instance.updateLayerStatus(layerInfo)
	})

	return err
}

func (instance *Instance) removeLayers(desiredLayers []amqp.LayerInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (err error) {
	log.Debug("Remove layers")

	var selectCases []reflect.SelectCase

nextLayer:
	for digest, layerStatus := range instance.layerStatuses {
		if installed, _ := layerStatus.isInstalled(); !installed {
			continue
		}

		for _, desiredLayer := range desiredLayers {
			if desiredLayer.Digest == digest {
				continue nextLayer
			}
		}

		selectCases = append(selectCases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(instance.layerUpdater.UninstallLayer(digest)),
		})
	}

	if len(selectCases) == 0 {
		log.Debug("No layers need to be removed")

		return nil
	}

	instance.processSelectedCases(selectCases, func(value interface{}) {
		instance.Lock()
		defer instance.Unlock()

		layerInfo := value.(amqp.LayerInfo)

		if layerInfo.Status == amqp.ErrorStatus {
			if err == nil {
				err = aoserrors.New(layerInfo.Error)
			}
		}

		instance.updateLayerStatus(layerInfo)
	})

	return err
}

func (instance *Instance) processSelectedCases(selectCases []reflect.SelectCase,
	processValue func(value interface{})) {

	selectCases = append(selectCases, reflect.SelectCase{
		Dir:  reflect.SelectRecv,
		Chan: reflect.ValueOf(instance.ctx.Done()),
	})

	for len(selectCases) != 1 {
		chosen, value, ok := reflect.Select(selectCases)
		if !ok {
			// ctx canceled
			if chosen == len(selectCases)-1 {
				return
			}

			// remove closed channel
			selectCases = append(selectCases[:chosen], selectCases[chosen+1:]...)

			continue
		}

		processValue(value.Interface())
	}
}
