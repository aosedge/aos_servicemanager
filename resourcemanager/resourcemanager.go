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

// Package resourcemanager provides set of API to provide access to system resources such as devices, cpu, ram, etc
package resourcemanager

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jinzhu/copier"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const devHostDirectory = "/dev/"
const userHostDirectory = "/etc/group"

/*******************************************************************************
 * Types
 ******************************************************************************/

// ResourceManager instance
type ResourceManager struct {
	deviceWithServices map[string][]string // [device_name]:[serviceIDs,...]
	hostDevices        []string
	hostGroups         []string
	boardConfigFile    string
	boardConfiguration BoardConfiguration
	areResourcesValid  error
	sync.Mutex
	sender Sender
}

// Sender provides sender interface
type Sender interface {
	SendValidateResourceAlert(source string, errors map[string][]error)
	SendRequestResourceAlert(source string, message string)
}

// FileSystemMount specifies a mount instructions.
type FileSystemMount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type,omitempty"`
	Source      string   `json:"source,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// DeviceResource describes Device available resource
type DeviceResource struct {
	Name        string   `json:"name"`
	SharedCount int      `json:"sharedCount,omitempty"`
	Groups      []string `json:"groups,omitempty"`
	HostDevices []string `json:"hostDevices"`
}

// BoardResource describes other board resource
type BoardResource struct {
	Name   string            `json:"name"`
	Groups []string          `json:"groups,omitempty"`
	Mounts []FileSystemMount `json:"mounts,omitempty"`
	Env    []string          `json:"env,omitempty"`
}

// BoardConfiguration resources that are proviced by Cloud for using at AOS services
type BoardConfiguration struct {
	Version   uint64           `json:"version,omitempty"`
	Devices   []DeviceResource `json:"devices"`
	Resources []BoardResource  `json:"resources"`
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new resource manager object
func New(boardConfigFile string, sender Sender) (resourcemanager *ResourceManager, err error) {
	log.Debug("New ResourceManager")

	resourcemanager = &ResourceManager{boardConfigFile: boardConfigFile, sender: sender}

	if resourcemanager.hostDevices, err = resourcemanager.discoverHostDevices(); err != nil {
		return nil, err
	}

	if resourcemanager.hostGroups, err = resourcemanager.discoverHostGroups(); err != nil {
		return nil, err
	}

	if resourcemanager.boardConfiguration, err = resourcemanager.parseBoardConfiguration(boardConfigFile); err != nil {
		log.Errorf("Can't parse resource configuration file: %s", boardConfigFile)
	}

	// do validation only if non-zero amount of the devices was provided
	if len(resourcemanager.boardConfiguration.Devices) != 0 {
		resourcemanager.areResourcesValid = resourcemanager.validateDeviceResources()
	} else {
		resourcemanager.areResourcesValid = nil
	}

	// init map with available device names
	resourcemanager.deviceWithServices = make(map[string][]string)

	return resourcemanager, nil
}

// AreResourcesValid check that available devices from resources configuration with host (real) devices
func (resourcemanager *ResourceManager) AreResourcesValid() (err error) {
	return resourcemanager.areResourcesValid
}

// RequestDeviceResourceByName requests list of device resources for class names
func (resourcemanager *ResourceManager) RequestDeviceResourceByName(name string) (deviceResource DeviceResource, err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	log.Debugf("ResourceManager: RequestDeviceResourceByName(%s)", name)

	tempDeviceResource, err := resourcemanager.getAvailableDeviceByName(name)

	copier.Copy(&deviceResource, &tempDeviceResource)

	//Cleanup host devices, releasing allocated memory
	deviceResource.HostDevices = nil

	for _, hostDevice := range tempDeviceResource.HostDevices {
		listOfDevices, err := resourcemanager.processHostDevice(hostDevice)
		if err != nil {
			log.Errorf("ResourceManager: RequestDeviceResourceByName(%s). Can't get list of devices for %s", name, hostDevice)
			return deviceResource, err
		}

		deviceResource.HostDevices = append(deviceResource.HostDevices, listOfDevices...)
	}

	if err != nil {
		return deviceResource, err
	}

	return deviceResource, nil
}

// RequestDevice requests Device by name for service id
func (resourcemanager *ResourceManager) RequestDevice(device string, serviceID string) (err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	log.Debugf("ResourceManager: RequestDevice(%s, %s)", device, serviceID)

	// check that Unit has restriction on devices
	// if not sent alert to cloud and error as return
	if !resourcemanager.isboardConfigurationChecked() {
		message := errors.New("resource configuration is not provided")

		if resourcemanager.sender != nil {
			resourcemanager.sender.SendRequestResourceAlert(serviceID, message.Error())
		}
		return message
	}

	// check that requested device class is contained in available resources
	// it can be file or directory
	deviceResource, err := resourcemanager.getAvailableDeviceByName(device)
	if err != nil {
		return err
	}

	// get list of services that are using this device
	listOfServices := resourcemanager.deviceWithServices[device]

	// deviceResource.SharedCount == 0: device can be shared unlimited times
	// deviceResource.SharedCount > len(listOfServices): provide device until list less then sharedCount value
	if deviceResource.SharedCount == 0 || deviceResource.SharedCount > len(listOfServices) {
		if contains(listOfServices, serviceID) {
			log.Warnf("Device %s is already used by %s service", device, serviceID)
		} else {
			log.Debugf("Provide Device %s for %s service", device, serviceID)

			// update map of devices
			// 1. Append list of used service
			// 2. set updated device's map to devices' class map by key: name (class name of device (alias))
			resourcemanager.deviceWithServices[device] = append(listOfServices, serviceID)
		}
	} else {
		message := fmt.Errorf("device: %s is unavailable", device)

		if resourcemanager.sender != nil {
			resourcemanager.sender.SendRequestResourceAlert(serviceID, message.Error())
		}
		return message
	}

	return nil
}

// RequestBoardResourceByName requests configuration by name
func (resourcemanager *ResourceManager) RequestBoardResourceByName(name string) (boardResource BoardResource, err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	log.Debugf("ResourceManager: RequestBoardResourceByName(%s)", name)

	for _, resource := range resourcemanager.boardConfiguration.Resources {
		if resource.Name == name {
			return resource, nil
		}
	}

	return boardResource, fmt.Errorf("resource is not present in board configuration")
}

// ReleaseDevice request to release device for service id
func (resourcemanager *ResourceManager) ReleaseDevice(device string, serviceID string) (err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	log.Debugf("ResourceManager: ReleaseDevice(%s, %s)", device, serviceID)

	// check that Unit has restriction on devices
	// if not sent alert to cloud and error as return
	if !resourcemanager.isboardConfigurationChecked() {
		message := errors.New("resource configuration is not provided")

		if resourcemanager.sender != nil {
			resourcemanager.sender.SendRequestResourceAlert(serviceID, message.Error())
		}

		return message
	}

	// check that requested device class is contained in available resources
	if _, err = resourcemanager.getAvailableDeviceByName(device); err != nil {
		return err
	}

	// get list of services that are using this device
	listOfServices := resourcemanager.deviceWithServices[device]

	// check that service has requested this device
	if contains(listOfServices, serviceID) {
		log.Debugf("Release Device %s for %s service", device, serviceID)

		// update map of devices
		// 1. remove serviceID from list of services for device
		// 2. set updated device's map to devices' class map by key: name (class name of device (alias))
		resourcemanager.deviceWithServices[device] = removeFromSlice(listOfServices, serviceID)
	} else {
		message := fmt.Errorf("device: %s was not provided for %s service", device, serviceID)

		if resourcemanager.sender != nil {
			resourcemanager.sender.SendRequestResourceAlert(serviceID, message.Error())
		}
		return message
	}

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func handleDir(device string) (hostDevices []string, err error) {
	err = filepath.Walk(device,
		func(path string, info os.FileInfo, err error) error {
			if info.IsDir() || err != nil {
				return err
			}

			if info.Mode()&os.ModeSymlink == os.ModeSymlink {
				linkName, err := filepath.EvalSymlinks(path)
				if err != nil {
					return err
				}

				hostDevices = append(hostDevices, linkName)
			} else {
				hostDevices = append(hostDevices, path)
			}
			return nil
		})

	return hostDevices, err
}

func (resourcemanager *ResourceManager) processHostDevice(device string) (hostDevices []string, err error) {
	fi, err := os.Lstat(device)
	if err != nil {
		return []string{}, err
	}

	if fi.IsDir() {
		return handleDir(device)
		//this is dir
	} else if fi.Mode()&os.ModeSymlink == os.ModeSymlink {
		s, err := filepath.EvalSymlinks(device)
		return []string{s}, err
	}

	return []string{device}, nil
}

func (resourcemanager *ResourceManager) discoverHostDevices() (hostDevices []string, err error) {
	err = filepath.Walk(devHostDirectory,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			hostDevices = append(hostDevices, path)

			return nil
		})
	if err != nil {
		return []string{}, err
	}

	return hostDevices, nil
}

func (resourcemanager *ResourceManager) discoverHostGroups() (hostGroups []string, err error) {
	file, err := os.Open(userHostDirectory)
	if err != nil {
		return hostGroups, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)

	for {
		line, err := reader.ReadString('\n')

		// skip all line starting with #
		if strings.HasPrefix(line, "#") != true {
			// get group name
			lineSlice := strings.Split(line, ":")

			if len(lineSlice) > 0 {
				hostGroups = append(hostGroups, lineSlice[0])
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return hostGroups, err
		}
	}

	return hostGroups, nil
}

func (resourcemanager *ResourceManager) parseBoardConfiguration(boardConfigFile string) (
	boardConfiguration BoardConfiguration,
	err error) {
	resources := BoardConfiguration{}

	byteValue, err := ioutil.ReadFile(boardConfigFile)
	if err != nil {
		return resources, err
	}

	if err = json.Unmarshal(byteValue, &resources); err != nil {
		return resources, err
	}

	// print debug information that resource configuration has been parsed succesfully
	log.Debugf("Available resources %s", byteValue)

	return resources, nil
}

// compare available devices from resources configuration with host (real) devices
func (resourcemanager *ResourceManager) validateDeviceResources() (err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	log.Debugf("ResourceManager: validateDeviceResources()")

	deviceErrors := make(map[string][]error)

	// compare available device names and additional groups with system ones
	for _, avaliableDevice := range resourcemanager.boardConfiguration.Devices {
		// check devices
		for _, availableHostDevice := range avaliableDevice.HostDevices {
			if contains(resourcemanager.hostDevices, availableHostDevice) != true {
				deviceErrors[avaliableDevice.Name] = append(deviceErrors[avaliableDevice.Name],
					fmt.Errorf("device: %s is not presented on system", availableHostDevice))
			}
		}

		// check additional groups
		for _, additionalGroup := range avaliableDevice.Groups {
			if contains(resourcemanager.hostGroups, additionalGroup) != true {
				deviceErrors[avaliableDevice.Name] = append(deviceErrors[avaliableDevice.Name],
					fmt.Errorf("%s group is not presented on system", additionalGroup))
			}
		}
	}

	if len(deviceErrors) != 0 {
		for name, reasons := range deviceErrors {
			log.Errorf("Device error -> name: %s", name)
			for _, reason := range reasons {
				log.Errorf("Reason: %s", reason.Error())
			}
		}

		if resourcemanager.sender != nil {
			resourcemanager.sender.SendValidateResourceAlert("servicemanager", deviceErrors)
		}

		return errors.New("device resources are not valid")
	}

	return nil
}

func (resourcemanager *ResourceManager) isboardConfigurationChecked() (status bool) {
	return resourcemanager.areResourcesValid == nil
}

func (resourcemanager *ResourceManager) getAvailableDeviceByName(
	name string) (deviceResource DeviceResource, err error) {
	for _, deviceResource = range resourcemanager.boardConfiguration.Devices {
		if strings.Contains(name, deviceResource.Name) {
			return deviceResource, nil
		}
	}

	return deviceResource, fmt.Errorf("device is not presented at available resources")
}

func contains(arr []string, str string) bool {
	for _, a := range arr {
		if strings.Contains(a, str) {
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
