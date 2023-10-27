// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2021 Renesas Electronics Corporation.
// Copyright (C) 2021 EPAM Systems, Inc.
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

// Package resourcemanager provides set of API to provide access to system resources such as devices, cpu, ram, etc
package resourcemanager

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	log "github.com/sirupsen/logrus"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	devHostDirectory  = "/dev/"
	userHostDirectory = "/etc/group"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// ResourceManager instance.
type ResourceManager struct {
	sync.Mutex

	nodeType         string
	allocatedDevices map[string][]string
	hostDevices      []string
	hostGroups       []string
	unitConfigFile   string
	unitConfig       unitConfig
	unitConfigError  error
	alertSender      AlertSender
}

// AlertSender provides alert sender interface.
type AlertSender interface {
	SendAlert(alert cloudprotocol.AlertItem)
}

type unitConfig struct {
	aostypes.NodeUnitConfig
	VendorVersion string `json:"vendorVersion"`
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

// ErrNoAvailableDevice indicates there is no device available.
var ErrNoAvailableDevice = errors.New("no device available")

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new resource manager object.
func New(nodeType, unitConfigFile string, alertSender AlertSender) (resourcemanager *ResourceManager, err error) {
	log.Debug("New resource manager")

	resourcemanager = &ResourceManager{
		nodeType:       nodeType,
		unitConfigFile: unitConfigFile,
		alertSender:    alertSender,
	}

	if resourcemanager.hostDevices, err = resourcemanager.discoverHostDevices(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if resourcemanager.hostGroups, err = resourcemanager.discoverHostGroups(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	resourcemanager.allocatedDevices = make(map[string][]string)

	if err = resourcemanager.loadUnitConfiguration(); err != nil {
		log.Errorf("Unit configuration error: %s", err)
	}

	log.WithField("version", resourcemanager.unitConfig.VendorVersion).Debug("Unit config version")

	return resourcemanager, nil
}

// GetUnitConfigInfo returns unit config info.
func (resourcemanager *ResourceManager) GetUnitConfigInfo() (version string) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	return resourcemanager.unitConfig.VendorVersion
}

// CheckUnitConfig checks unit config.
func (resourcemanager *ResourceManager) CheckUnitConfig(configJSON, version string) error {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	return resourcemanager.checkUnitConfig(configJSON, version)
}

// UpdateUnitConfig updates unit configuration.
func (resourcemanager *ResourceManager) UpdateUnitConfig(configJSON, version string) error {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	if err := resourcemanager.checkUnitConfig(configJSON, version); err != nil {
		return aoserrors.Wrap(err)
	}

	config := unitConfig{VendorVersion: version}

	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return aoserrors.Wrap(err)
	}

	dataToWrite, err := json.Marshal(config)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err := os.WriteFile(resourcemanager.unitConfigFile, dataToWrite, 0o600); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := resourcemanager.loadUnitConfiguration(); err != nil {
		return aoserrors.Wrap(err)
	}

	log.WithField("version", resourcemanager.unitConfig.VendorVersion).Debug("Update unit configuration")

	return nil
}

// GetDeviceInfo returns device information.
func (resourcemanager *ResourceManager) GetDeviceInfo(name string) (deviceInfo aostypes.DeviceInfo, err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	if deviceInfo, err = resourcemanager.getAvailableDevice(name); err != nil {
		return deviceInfo, aoserrors.Wrap(err)
	}

	return deviceInfo, nil
}

// GetResourceInfo returns resource information.
func (resourcemanager *ResourceManager) GetResourceInfo(name string) (aostypes.ResourceInfo, error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	for _, resource := range resourcemanager.unitConfig.Resources {
		if resource.Name == name {
			return resource, nil
		}
	}

	return aostypes.ResourceInfo{}, aoserrors.New("resource is not available")
}

// AllocateDevice tries to allocate device.
func (resourcemanager *ResourceManager) AllocateDevice(device, instanceID string) error {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	log.WithFields(log.Fields{"instanceID": instanceID, "device": device}).Debug("Allocate device")

	if resourcemanager.unitConfigError != nil {
		return aoserrors.Wrap(resourcemanager.unitConfigError)
	}

	deviceInfo, err := resourcemanager.getAvailableDevice(device)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	// get list of instances that are using this device
	instances := resourcemanager.allocatedDevices[device]

	if contains(instances, instanceID) {
		log.WithFields(log.Fields{
			"device": device, "instanceID": instanceID,
		}).Warn("Device is already allocated by instance")

		return nil
	}

	// deviceInfo.SharedCount == 0: device can be shared unlimited times
	// len(instances) < deviceInfo.SharedCount: provide device until list less then sharedCount value
	if deviceInfo.SharedCount != 0 && len(instances) >= deviceInfo.SharedCount {
		return aoserrors.Wrap(ErrNoAvailableDevice)
	}

	resourcemanager.allocatedDevices[device] = append(instances, instanceID)

	return nil
}

// ReleaseDevice releases device for instance.
func (resourcemanager *ResourceManager) ReleaseDevice(device, instanceID string) error {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	log.WithFields(log.Fields{"instanceID": instanceID, "device": device}).Debug("Release device")

	instances, ok := resourcemanager.allocatedDevices[device]
	if !ok || !contains(instances, instanceID) {
		return aoserrors.New("device is not allocated for instance")
	}

	resourcemanager.allocatedDevices[device] = removeFromSlice(instances, instanceID)

	if len(resourcemanager.allocatedDevices[device]) == 0 {
		delete(resourcemanager.allocatedDevices, device)
	}

	return nil
}

// ReleaseDevices releases all previously allocated device.
func (resourcemanager *ResourceManager) ReleaseDevices(instanceID string) (err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	for device, instances := range resourcemanager.allocatedDevices {
		if contains(instances, instanceID) {
			log.WithFields(log.Fields{"instanceID": instanceID, "device": device}).Debug("Release device")

			resourcemanager.allocatedDevices[device] = removeFromSlice(instances, instanceID)

			if len(resourcemanager.allocatedDevices[device]) == 0 {
				delete(resourcemanager.allocatedDevices, device)
			}
		}
	}

	return nil
}

// GetDeviceInstances returns ID list of instances that allocate specific device.
func (resourcemanager *ResourceManager) GetDeviceInstances(device string) ([]string, error) {
	if _, err := resourcemanager.getAvailableDevice(device); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	instances, ok := resourcemanager.allocatedDevices[device]
	if !ok {
		return nil, nil
	}

	return instances, nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (resourcemanager *ResourceManager) checkUnitConfig(configJSON, version string) error {
	nodeConfig := aostypes.NodeUnitConfig{}

	if version == resourcemanager.unitConfig.VendorVersion {
		return aoserrors.New("invalid vendor version")
	}

	if err := json.Unmarshal([]byte(configJSON), &nodeConfig); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := resourcemanager.validateUnitConfig(nodeConfig); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (resourcemanager *ResourceManager) discoverHostDevices() (hostDevices []string, err error) {
	err = filepath.Walk(devHostDirectory,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return aoserrors.Wrap(err)
			}

			hostDevices = append(hostDevices, path)

			return nil
		})
	if err != nil {
		return []string{}, aoserrors.Wrap(err)
	}

	return hostDevices, nil
}

func (resourcemanager *ResourceManager) discoverHostGroups() (hostGroups []string, err error) {
	file, err := os.Open(userHostDirectory)
	if err != nil {
		return hostGroups, aoserrors.Wrap(err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)

	for {
		line, err := reader.ReadString('\n')

		// skip all line starting with #
		if !strings.HasPrefix(line, "#") {
			// get group name
			lineSlice := strings.Split(line, ":")

			if len(lineSlice) > 0 {
				hostGroups = append(hostGroups, lineSlice[0])
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return hostGroups, aoserrors.Wrap(err)
		}
	}

	return hostGroups, nil
}

func (resourcemanager *ResourceManager) loadUnitConfiguration() (err error) {
	defer func() {
		resourcemanager.unitConfigError = aoserrors.Wrap(err)
	}()

	resourcemanager.unitConfig = unitConfig{}

	byteValue, err := os.ReadFile(resourcemanager.unitConfigFile)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = json.Unmarshal(byteValue, &resourcemanager.unitConfig); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = resourcemanager.validateUnitConfig(resourcemanager.unitConfig.NodeUnitConfig); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (resourcemanager *ResourceManager) validateUnitConfig(config aostypes.NodeUnitConfig) (err error) {
	if config.NodeType != resourcemanager.nodeType {
		return aoserrors.New("invalid node type")
	}

	if err = resourcemanager.validateDevices(config.Devices); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

// compare available devices from unit config with host (real) devices.
func (resourcemanager *ResourceManager) validateDevices(devices []aostypes.DeviceInfo) error {
	var deviceErrors []cloudprotocol.ResourceValidateError

	// compare available device names and additional groups with system ones
	for _, device := range devices {
		deviceError := cloudprotocol.ResourceValidateError{Name: device.Name}

		// check devices
		for _, hostDevice := range device.HostDevices {
			if !contains(resourcemanager.hostDevices, hostDevice) {
				err := aoserrors.Errorf("device %s is not present on system", hostDevice)

				log.Errorf("Device validation error: %s", err)

				deviceError.Errors = append(deviceError.Errors, err.Error())
			}
		}

		// check additional groups
		for _, group := range device.Groups {
			if !contains(resourcemanager.hostGroups, group) {
				err := aoserrors.Errorf("%s group is not present on system", group)

				log.Errorf("Device validation error: %s", err)

				deviceError.Errors = append(deviceError.Errors, err.Error())
			}
		}

		if len(deviceError.Errors) > 0 {
			deviceErrors = append(deviceErrors, deviceError)
		}
	}

	if len(deviceErrors) != 0 {
		if resourcemanager.alertSender != nil {
			resourcemanager.alertSender.SendAlert(cloudprotocol.AlertItem{
				Timestamp: time.Now(),
				Tag:       cloudprotocol.AlertTagResourceValidate,
				Payload: cloudprotocol.ResourceValidateAlert{
					ResourcesErrors: deviceErrors,
				},
			})
		}

		return aoserrors.New("device resources are not valid")
	}

	return nil
}

func (resourcemanager *ResourceManager) getAvailableDevice(name string) (deviceInfo aostypes.DeviceInfo, err error) {
	for _, deviceInfo = range resourcemanager.unitConfig.Devices {
		if deviceInfo.Name == name {
			return deviceInfo, nil
		}
	}

	return deviceInfo, aoserrors.Errorf("device is not available")
}

func contains(arr []string, str string) bool {
	for _, a := range arr {
		if a == str {
			return true
		}
	}

	return false
}

func removeFromSlice(arr []string, str string) []string {
	for i, a := range arr {
		if a == str {
			arr = append(arr[:i], arr[i+1:]...)
			break
		}
	}

	return arr
}
