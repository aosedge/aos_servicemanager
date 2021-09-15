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
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jinzhu/copier"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const devHostDirectory = "/dev/"
const userHostDirectory = "/etc/group"
const supportedFormatVersion = 1

/*******************************************************************************
 * Types
 ******************************************************************************/

// ResourceManager instance
type ResourceManager struct {
	sync.Mutex

	deviceWithServices map[string][]string // [device_name]:[serviceIDs,...]
	hostDevices        []string
	hostGroups         []string
	boardConfigFile    string
	boardConfiguration BoardConfiguration
	boardConfigError   error
	alertSender        AlertSender
}

// AlertSender provides alert sender interface
type AlertSender interface {
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
	Hosts  []config.Host     `json:"hosts,omitempty"`
}

// BoardConfiguration resources that are proviced by Cloud for using at AOS services
type BoardConfiguration struct {
	FormatVersion uint64           `json:"formatVersion"`
	VendorVersion string           `json:"vendorVersion"`
	Devices       []DeviceResource `json:"devices"`
	Resources     []BoardResource  `json:"resources"`
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new resource manager object
func New(boardConfigFile string, alertSender AlertSender) (resourcemanager *ResourceManager, err error) {
	log.Debug("New ResourceManager")

	resourcemanager = &ResourceManager{
		boardConfigFile: boardConfigFile,
		alertSender:     alertSender}

	if resourcemanager.hostDevices, err = resourcemanager.discoverHostDevices(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if resourcemanager.hostGroups, err = resourcemanager.discoverHostGroups(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	// init map with available device names
	resourcemanager.deviceWithServices = make(map[string][]string)

	if err = resourcemanager.loadBoardConfiguration(); err != nil {
		log.Errorf("Board configuration error: %s", err)
	}

	log.WithField("version", resourcemanager.boardConfiguration.VendorVersion).Debug("Board config version")

	return resourcemanager, nil
}

// GetBoardConfigInfo returns board config info
func (resourcemanager *ResourceManager) GetBoardConfigInfo() (version string) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	return resourcemanager.boardConfiguration.VendorVersion
}

// CheckBoardConfig checks board config
func (resourcemanager *ResourceManager) CheckBoardConfig(configJSON string) (vendorVersion string, err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	return resourcemanager.checkBoardConfig(configJSON)
}

// UpdateBoardConfig updates board configuration
func (resourcemanager *ResourceManager) UpdateBoardConfig(configJSON string) (err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	newVendorVersion, err := resourcemanager.checkBoardConfig(configJSON)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	log.WithField("newVersion", newVendorVersion).Debug("Update board configuration")

	if err = ioutil.WriteFile(resourcemanager.boardConfigFile, []byte(configJSON), 0644); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = resourcemanager.loadBoardConfiguration(); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
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
			return deviceResource, aoserrors.Wrap(err)
		}

		deviceResource.HostDevices = append(deviceResource.HostDevices, listOfDevices...)
	}

	if err != nil {
		return deviceResource, aoserrors.Wrap(err)
	}

	return deviceResource, nil
}

// RequestDevice requests Device by name for service id
func (resourcemanager *ResourceManager) RequestDevice(device string, serviceID string) (err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	log.Debugf("ResourceManager: RequestDevice(%s, %s)", device, serviceID)

	// check board configuration
	// if it has error, send alert to cloud and return error
	if resourcemanager.boardConfigError != nil {
		message := aoserrors.Errorf("resource configuration error: %s", resourcemanager.boardConfigError)

		if resourcemanager.alertSender != nil {
			resourcemanager.alertSender.SendRequestResourceAlert(serviceID, message.Error())
		}

		return message
	}

	// check that requested device class is contained in available resources
	// it can be file or directory
	deviceResource, err := resourcemanager.getAvailableDeviceByName(device)
	if err != nil {
		return aoserrors.Wrap(err)
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
		message := aoserrors.Errorf("device: %s is unavailable", device)

		if resourcemanager.alertSender != nil {
			resourcemanager.alertSender.SendRequestResourceAlert(serviceID, message.Error())
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

	return boardResource, aoserrors.Errorf("resource is not present in board configuration")
}

// ReleaseDevice request to release device for service id
func (resourcemanager *ResourceManager) ReleaseDevice(device string, serviceID string) (err error) {
	resourcemanager.Lock()
	defer resourcemanager.Unlock()

	log.Debugf("ResourceManager: ReleaseDevice(%s, %s)", device, serviceID)

	// get list of services that are using this device
	listOfServices, ok := resourcemanager.deviceWithServices[device]
	if !ok || !contains(listOfServices, serviceID) {
		log.Warnf("Device: %s was not provided for %s service", device, serviceID)

		return nil
	}

	log.Debugf("Release Device %s for %s service", device, serviceID)

	// update map of devices
	// 1. remove serviceID from list of services for device
	// 2. set updated device's map to devices' class map by key: name (class name of device (alias))
	resourcemanager.deviceWithServices[device] = removeFromSlice(listOfServices, serviceID)

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (resourcemanager *ResourceManager) checkBoardConfig(configJSON string) (vendorVersion string, err error) {
	boardConfig := BoardConfiguration{VendorVersion: "unknown"}

	if err = json.Unmarshal([]byte(configJSON), &boardConfig); err != nil {
		return boardConfig.VendorVersion, aoserrors.Wrap(err)
	}

	if boardConfig.VendorVersion == resourcemanager.boardConfiguration.VendorVersion {
		return boardConfig.VendorVersion, aoserrors.New("invalid vendor version")
	}

	if err = resourcemanager.validateBoardConfig(boardConfig); err != nil {
		return boardConfig.VendorVersion, aoserrors.Wrap(err)
	}

	return boardConfig.VendorVersion, nil
}

func handleDir(device string) (hostDevices []string, err error) {
	err = filepath.Walk(device,
		func(path string, info os.FileInfo, err error) error {
			if info.IsDir() || err != nil {
				return aoserrors.Wrap(err)
			}

			if info.Mode()&os.ModeSymlink == os.ModeSymlink {
				linkName, err := filepath.EvalSymlinks(path)
				if err != nil {
					return aoserrors.Wrap(err)
				}

				hostDevices = append(hostDevices, linkName)
			} else {
				hostDevices = append(hostDevices, path)
			}
			return nil
		})

	return hostDevices, aoserrors.Wrap(err)
}

func (resourcemanager *ResourceManager) processHostDevice(device string) (hostDevices []string, err error) {
	fi, err := os.Lstat(device)
	if err != nil {
		return []string{}, aoserrors.Wrap(err)
	}

	if fi.IsDir() {
		return handleDir(device)
		//this is dir
	} else if fi.Mode()&os.ModeSymlink == os.ModeSymlink {
		s, err := filepath.EvalSymlinks(device)
		return []string{s}, aoserrors.Wrap(err)
	}

	return []string{device}, nil
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
			return hostGroups, aoserrors.Wrap(err)
		}
	}

	return hostGroups, nil
}

func (resourcemanager *ResourceManager) loadBoardConfiguration() (err error) {
	defer func() {
		resourcemanager.boardConfigError = aoserrors.Wrap(err)
	}()

	resourcemanager.boardConfiguration = BoardConfiguration{}

	byteValue, err := ioutil.ReadFile(resourcemanager.boardConfigFile)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = json.Unmarshal(byteValue, &resourcemanager.boardConfiguration); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = resourcemanager.validateBoardConfig(resourcemanager.boardConfiguration); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (resourcemanager *ResourceManager) validateBoardConfig(config BoardConfiguration) (err error) {
	if config.FormatVersion != supportedFormatVersion {
		return aoserrors.New("unsupported board configuration format version")
	}

	if err = resourcemanager.validateDeviceResources(config.Devices); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

// compare available devices from resources configuration with host (real) devices
func (resourcemanager *ResourceManager) validateDeviceResources(devices []DeviceResource) (err error) {
	log.Debugf("ResourceManager: validateDeviceResources()")

	deviceErrors := make(map[string][]error)

	// compare available device names and additional groups with system ones
	for _, avaliableDevice := range devices {
		// check devices
		for _, availableHostDevice := range avaliableDevice.HostDevices {
			if contains(resourcemanager.hostDevices, availableHostDevice) != true {
				deviceErrors[avaliableDevice.Name] = append(deviceErrors[avaliableDevice.Name],
					aoserrors.Errorf("device: %s is not presented on system", availableHostDevice))
			}
		}

		// check additional groups
		for _, additionalGroup := range avaliableDevice.Groups {
			if contains(resourcemanager.hostGroups, additionalGroup) != true {
				deviceErrors[avaliableDevice.Name] = append(deviceErrors[avaliableDevice.Name],
					aoserrors.Errorf("%s group is not presented on system", additionalGroup))
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

		if resourcemanager.alertSender != nil {
			resourcemanager.alertSender.SendValidateResourceAlert("servicemanager", deviceErrors)
		}

		return aoserrors.New("device resources are not valid")
	}

	return nil
}

func (resourcemanager *ResourceManager) getAvailableDeviceByName(
	name string) (deviceResource DeviceResource, err error) {
	for _, deviceResource = range resourcemanager.boardConfiguration.Devices {
		if strings.Contains(name, deviceResource.Name) {
			return deviceResource, nil
		}
	}

	return deviceResource, aoserrors.Errorf("device is not presented at available resources")
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
