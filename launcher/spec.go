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
	"io/ioutil"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"

	"github.com/opencontainers/runtime-spec/specs-go"
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
	ocSpec   specs.Spec
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

	newMount := specs.Mount{
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
	var mounts []specs.Mount

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

func (spec *serviceSpec) mountHostFS(workingDir string) (err error) {
	mounts := []specs.Mount{
		specs.Mount{Destination: "/bin", Type: "bind", Source: "/bin", Options: []string{"bind", "ro"}},
		specs.Mount{Destination: "/sbin", Type: "bind", Source: "/sbin", Options: []string{"bind", "ro"}},
		specs.Mount{Destination: "/lib", Type: "bind", Source: "/lib", Options: []string{"bind", "ro"}},
		specs.Mount{Destination: "/usr", Type: "bind", Source: "/usr", Options: []string{"bind", "ro"}},
		// TODO: mount individual tmp
		// "destination": "/tmp",
		// "type": "tmpfs",
		// "source": "tmpfs",
		// "options": ["nosuid","strictatime","mode=755","size=65536k"]
		specs.Mount{Destination: "/tmp", Type: "bind", Source: "/tmp", Options: []string{"bind", "rw"}},
	}

	spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, mounts...)

	// add lib64 if exists
	if _, err := os.Stat("/lib64"); err == nil {
		spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, specs.Mount{Destination: "/lib64", Type: "bind", Source: "/lib64", Options: []string{"bind", "ro"}})
	}

	// add hosts
	hosts, _ := filepath.Abs(path.Join(workingDir, "etc", "hosts"))
	if _, err := os.Stat(hosts); err != nil {
		hosts = "/etc/hosts"
	}
	spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "hosts"), Type: "bind", Source: hosts, Options: []string{"bind", "ro"}})

	// add resolv.conf
	resolvConf, _ := filepath.Abs(path.Join(workingDir, "etc", "resolv.conf"))
	if _, err := os.Stat(resolvConf); err != nil {
		resolvConf = "/etc/resolv.conf"
	}
	spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "resolv.conf"), Type: "bind", Source: resolvConf, Options: []string{"bind", "ro"}})

	// add nsswitch.conf
	nsswitchConf, _ := filepath.Abs(path.Join(workingDir, "etc", "nsswitch.conf"))
	if _, err := os.Stat(nsswitchConf); err != nil {
		nsswitchConf = "/etc/nsswitch.conf"
	}
	spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "nsswitch.conf"), Type: "bind", Source: nsswitchConf, Options: []string{"bind", "ro"}})

	// TODO: all services should have their own certificates
	// this mound for demo only and should be removed
	// mount /etc/ssl
	spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "ssl"), Type: "bind", Source: path.Join("/etc", "ssl"), Options: []string{"bind", "ro"}})

	return nil
}

func (spec *serviceSpec) disableTerminal() (err error) {
	spec.ocSpec.Process.Terminal = false

	return nil
}

func (spec *serviceSpec) addPrestartHook(path string) (err error) {
	if spec.ocSpec.Hooks == nil {
		spec.ocSpec.Hooks = &specs.Hooks{}
	}

	spec.ocSpec.Hooks.Prestart = append(spec.ocSpec.Hooks.Prestart, specs.Hook{Path: path})

	return nil
}

func addDevice(deviceName string) (device *specs.LinuxDevice, err error) {
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

	return &specs.LinuxDevice{
		Type:     devType,
		Path:     deviceName,
		Major:    int64(unix.Major(stat.Rdev)),
		Minor:    int64(unix.Minor(stat.Rdev)),
		FileMode: &mode,
		UID:      &stat.Uid,
		GID:      &stat.Gid,
	}, nil
}

func addDevices(deviceName string) (devices []specs.LinuxDevice, err error) {
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

		spec.ocSpec.Linux.Resources.Devices = append(spec.ocSpec.Linux.Resources.Devices, specs.LinuxDeviceCgroup{
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
