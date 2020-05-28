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

// Package launcher provides set of API to controls services lifecycle

package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runc/libcontainer/specconv"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"

	"aos_servicemanager/platform"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const serviceStorageFolder = "/home/service/storage"

/*******************************************************************************
 * Types
 ******************************************************************************/

type serviceSpec struct {
	ocSpec   runtimespec.Spec
	fileName string
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var errNotDevice = errors.New("not a device")

/*******************************************************************************
 * Private
 ******************************************************************************/

func loadServiceSpec(fileName string) (spec *serviceSpec, err error) {
	log.WithField("fileName", fileName).Debug("Load service spec")

	spec = &serviceSpec{fileName: fileName}

	specJSON, err := ioutil.ReadFile(spec.fileName)
	if err != nil {
		return nil, err
	}

	if err = json.Unmarshal(specJSON, &spec.ocSpec); err != nil {
		return spec, err
	}

	return spec, nil
}

func generateSpecFromImageConfig(fileImagConfigPath, fileNameRuntimeSpec string) (spec *serviceSpec, err error) {
	imageConfigJSONFile, err := os.Open(fileImagConfigPath)
	if err != nil {
		return nil, err
	}

	defer imageConfigJSONFile.Close()

	byteValue, _ := ioutil.ReadAll(imageConfigJSONFile)
	if err != nil {
		return nil, err
	}

	var imageConfig imagespec.Image
	if err = json.Unmarshal(byteValue, &imageConfig); err != nil {
		return nil, err
	}

	strOS := strings.ToLower(imageConfig.OS)
	if strOS != "linux" {
		return nil, fmt.Errorf("unsupported OS in image config %s", imageConfig.OS)
	}

	spec = &serviceSpec{fileName: fileNameRuntimeSpec}

	spec.ocSpec = *specconv.Example()

	spec.mergeEnv(imageConfig.Config.Env)

	spec.createArgs(&imageConfig.Config)

	spec.ocSpec.Process.Cwd = imageConfig.Config.WorkingDir
	if spec.ocSpec.Process.Cwd == "" {
		spec.ocSpec.Process.Cwd = "/"
	}

	return spec, nil
}

func (spec *serviceSpec) mergeEnv(configEnv []string) {
	var resultEnvArray []string

	for _, result := range spec.ocSpec.Process.Env {
		data := strings.SplitN(result, "=", 2)
		key := data[0]
		var keyWasFound bool

		for i, data := range configEnv {
			if !strings.Contains(data, "=") {
				if data == key {
					keyWasFound = true
				}
			} else {
				keyValue := strings.SplitN(data, "=", 2)
				if keyValue[0] == key {
					keyWasFound = true
				}
			}

			if keyWasFound {
				resultEnvArray = append(resultEnvArray, data)
				configEnv = append(configEnv[:i], configEnv[i+1:]...)
				break
			}
		}

		if !keyWasFound {
			resultEnvArray = append(resultEnvArray, result)
		}
	}

	spec.ocSpec.Process.Env = append(resultEnvArray, configEnv...)
}

func (spec *serviceSpec) createArgs(config *imagespec.ImageConfig) {
	spec.ocSpec.Process.Args = append(config.Entrypoint, config.Cmd...)
}

func (spec *serviceSpec) save() (err error) {
	log.WithField("fileName", spec.fileName).Debug("Save service spec")

	file, err := os.Create(spec.fileName)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "\t")

	return encoder.Encode(spec.ocSpec)
}

func (spec *serviceSpec) addStorageFolder(storageFolder string) (err error) {
	absStorageFolder, err := filepath.Abs(storageFolder)
	if err != nil {
		return err
	}

	newMount := runtimespec.Mount{
		Destination: serviceStorageFolder,
		Type:        "bind",
		Source:      absStorageFolder,
		Options:     []string{"bind", "rw"}}

	storageIndex := len(spec.ocSpec.Mounts)

	for i, mount := range spec.ocSpec.Mounts {
		if mount.Destination == serviceStorageFolder {
			storageIndex = i
			break
		}
	}

	if storageIndex == len(spec.ocSpec.Mounts) {
		spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, newMount)

		return nil
	}

	spec.ocSpec.Mounts[storageIndex] = newMount

	return nil
}

func (spec *serviceSpec) removeStorageFolder() (err error) {
	var mounts []runtimespec.Mount

	for _, mount := range spec.ocSpec.Mounts {
		if mount.Destination == serviceStorageFolder {

			continue
		}

		mounts = append(mounts, mount)
	}

	spec.ocSpec.Mounts = mounts

	return nil
}

func (spec *serviceSpec) setUser(user string) (err error) {
	spec.ocSpec.Process.User.UID, spec.ocSpec.Process.User.GID, err = platform.GetUserUIDGID(user)
	if err != nil {
		return err
	}

	return nil
}

func (spec *serviceSpec) bindHostDirs(workingDir string) (err error) {
	// TODO: all services should have their own certificates
	// this mound for demo only and should be removed
	// mount /etc/ssl
	etcItems := []string{"hosts", "resolv.conf", "nsswitch.conf", "group", "ssl"}

	for _, item := range etcItems {
		// Check if in working dir
		absPath, _ := filepath.Abs(path.Join(workingDir, "etc", item))
		if _, err := os.Stat(absPath); err != nil {
			absPath = path.Join("/etc", item)
		}

		spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, runtimespec.Mount{Destination: path.Join("/etc", item), Type: "bind", Source: absPath, Options: []string{"bind", "ro"}})
	}

	return nil
}

func (spec *serviceSpec) disableTerminal() (err error) {
	spec.ocSpec.Process.Terminal = false

	return nil
}

func (spec *serviceSpec) addPrestartHook(path string) (err error) {
	if spec.ocSpec.Hooks == nil {
		spec.ocSpec.Hooks = &runtimespec.Hooks{}
	}

	spec.ocSpec.Hooks.Prestart = append(spec.ocSpec.Hooks.Prestart, runtimespec.Hook{Path: path})

	return nil
}

func addDevice(deviceName string) (device *runtimespec.LinuxDevice, err error) {
	log.WithFields(log.Fields{"device": deviceName}).Debug("Add device")

	var stat unix.Stat_t

	if err := unix.Lstat(deviceName, &stat); err != nil {
		return nil, err
	}

	if unix.Major(stat.Rdev) == 0 {
		return nil, errNotDevice
	}

	devType := ""

	switch {
	case stat.Mode&unix.S_IFBLK == unix.S_IFBLK:
		devType = "b"
	case stat.Mode&unix.S_IFCHR == unix.S_IFCHR:
		devType = "c"
	}

	mode := os.FileMode(stat.Mode)

	return &runtimespec.LinuxDevice{
		Type:     devType,
		Path:     deviceName,
		Major:    int64(unix.Major(stat.Rdev)),
		Minor:    int64(unix.Minor(stat.Rdev)),
		FileMode: &mode,
		UID:      &stat.Uid,
		GID:      &stat.Gid,
	}, nil
}

func addDevices(deviceName string) (devices []runtimespec.LinuxDevice, err error) {
	stat, err := os.Stat(deviceName)
	if err != nil {
		return nil, err
	}

	switch {
	case stat.IsDir():
		files, err := ioutil.ReadDir(deviceName)
		if err != nil {
			return nil, err
		}

		for _, file := range files {
			switch file.Name() {
			case "pts", "shm", "fd", "mqueue", ".lxc", ".lxd-mounts", ".udev":
				log.WithField("device", file.Name()).Warnf("Device skipped")

				continue

			default:
				dirDevices, err := addDevices(path.Join(deviceName, file.Name()))
				if err != nil {
					return nil, err
				}

				devices = append(devices, dirDevices...)
			}
		}

		return devices, nil

	case stat.Name() == "console":
		log.WithField("device", deviceName).Warnf("Device skipped")

		return devices, nil

	default:
		device, err := addDevice(deviceName)
		if err != nil {
			if err == errNotDevice {
				log.WithField("device", deviceName).Warnf("Device skipped as not a device node")

				return nil, nil
			}

			return nil, err
		}

		devices = append(devices, *device)

		return devices, nil
	}
}

func (spec *serviceSpec) addHostDevice(deviceName string) (err error) {
	log.WithFields(log.Fields{"device": deviceName}).Debug("Add host device")

	specDevices, err := addDevices(deviceName)
	if err != nil {
		return err
	}

	spec.ocSpec.Linux.Devices = append(spec.ocSpec.Linux.Devices, specDevices...)

	for _, specDevice := range specDevices {
		major, minor := specDevice.Major, specDevice.Minor

		spec.ocSpec.Linux.Resources.Devices = append(spec.ocSpec.Linux.Resources.Devices, runtimespec.LinuxDeviceCgroup{
			Allow:  true,
			Type:   specDevice.Type,
			Major:  &major,
			Minor:  &minor,
			Access: "rwm",
		})
	}

	return nil
}

func (spec *serviceSpec) addGroup(groupName string) (err error) {
	log.WithFields(log.Fields{"group": groupName}).Debug("Add group")

	group, err := user.LookupGroup(groupName)
	if err != nil {
		return err
	}

	gid, err := strconv.ParseUint(group.Gid, 10, 32)
	if err != nil {
		return err
	}

	spec.ocSpec.Process.User.AdditionalGids = append(spec.ocSpec.Process.User.AdditionalGids, uint32(gid))

	return nil
}

func (spec *serviceSpec) setRootfs(rootfsPath string) (err error) {
	log.WithFields(log.Fields{"rootfs": rootfsPath}).Debug("Add rootfs to spec")

	spec.ocSpec.Root = &runtimespec.Root{Path: rootfsPath}

	return nil
}
