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
	"time"

	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runc/libcontainer/specconv"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const defaultCPUPeriod uint64 = 100000

/*******************************************************************************
 * Types
 ******************************************************************************/

//serviceManifest OCI image manifest with aos sevice config
type serviceManifest struct {
	imagespec.Manifest
	AosService *imagespec.Descriptor `json:"aosService,omitempty"`
}

// Device configuration for system and service logging
type Device struct {
	Name        string `json:"name"`
	Permissions string `json:"permissions"`
}

type aosServiceConfig struct {
	Created         time.Time          `json:"created"`
	Author          string             `json:"author"`
	ServiceProvider string             `json:"serviceProvider"`
	Hostname        *string            `json:"hostname,omitempty"`
	Sysctl          *map[string]string `json:"sysctl,omitempty"`
	ServiceTTL      *uint64            `json:"serviceTTL,omitempty"`
	Quotas          struct {
		StateLimit     *uint64 `json:"stateLimit,omitempty"`
		StorageLimit   *uint64 `json:"storageLimit,omitempty"`
		UploadSpeed    *uint64 `json:"uploadSpeed,omitempty"`
		DownloadSpeed  *uint64 `json:"downloadSpeed,omitempty"`
		UploadLimit    *uint64 `json:"uploadLimit,omitempty"`
		DownloadLimit  *uint64 `json:"downloadLimit,omitempty"`
		RAMLimit       *int64  `json:"ramLimit,omitempty"`
		PidsLimit      *int64  `json:"pidsLimit,omitempty"`
		NoFileLimit    *uint64 `json:"noFileLimit,omitempty"`
		CPULimit       *uint64 `json:"cpuLimit,omitempty"`
		TmpLimit       *uint64 `json:"tmpLimit,omitempty"`
		VisPermissions string  `json:"visPermissions,omitempty"`
	} `json:"quotas"`
	Mounts *[]struct {
		ContainerPath string   `json:"containerPath"`
		Type          string   `json:"type,omitempty"`
		HostPath      string   `json:"hostPath"`
		Options       []string `json:"options,omitempty"`
	} `json:"mounts,omitempty"`
	AllowedConnections map[string]struct{} `json:"AllowedConnections,omitempty"`
	Devices            []Device            `json:"devices,omitempty"`
	Resources          []string            `json:"resources,omitempty"`
}

type serviceSpec struct {
	ocSpec          runtimespec.Spec
	runtimeFileName string
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var errNotDevice = errors.New("not a device")

/*******************************************************************************
 * Private
 ******************************************************************************/

func getJSONFromFile(fileName string, data interface{}) (err error) {
	byteValue, err := ioutil.ReadFile(fileName)
	if err != nil {
		return err
	}

	if err = json.Unmarshal(byteValue, data); err != nil {
		return err
	}

	return nil
}

func loadServiceSpec(fileName string) (spec *serviceSpec, err error) {
	log.WithField("fileName", fileName).Debug("Load service spec")

	spec = &serviceSpec{runtimeFileName: fileName}

	if err = getJSONFromFile(spec.runtimeFileName, &spec.ocSpec); err != nil {
		return spec, err
	}

	return spec, nil
}
func getImageSpecFromImageConfig(fileImageConfigPath string) (spec imagespec.Image, err error) {
	var imageConfig imagespec.Image

	if err = getJSONFromFile(fileImageConfigPath, &imageConfig); err != nil {
		return imageConfig, err
	}

	return imageConfig, nil
}

func generateRuntimeSpec(imageConfig imagespec.Image, fileNameRuntimeSpec string) (spec *serviceSpec, err error) {
	strOS := strings.ToLower(imageConfig.OS)
	if strOS != "linux" {
		return nil, fmt.Errorf("unsupported OS in image config %s", imageConfig.OS)
	}

	spec = &serviceSpec{runtimeFileName: fileNameRuntimeSpec}

	spec.ocSpec = *specconv.Example()

	spec.ocSpec.Process.Terminal = false

	spec.mergeEnv(imageConfig.Config.Env)

	spec.createArgs(&imageConfig.Config)

	spec.ocSpec.Process.Cwd = imageConfig.Config.WorkingDir
	if spec.ocSpec.Process.Cwd == "" {
		spec.ocSpec.Process.Cwd = "/"
	}

	return spec, nil
}

func getAosServiceConfig(path string) (serviceConfig aosServiceConfig, err error) {
	if err = getJSONFromFile(path, &serviceConfig); err != nil {
		if !os.IsNotExist(err) {
			return serviceConfig, err
		}
	}

	if serviceConfig.ServiceProvider == "" {
		serviceConfig.ServiceProvider = defaultServiceProvider
	}

	return serviceConfig, nil
}

func (spec *serviceSpec) applyAosServiceConfig(aosConfig aosServiceConfig) (err error) {
	if aosConfig.Hostname != nil {
		spec.ocSpec.Hostname = *aosConfig.Hostname
	}

	if aosConfig.Sysctl != nil {
		spec.ocSpec.Linux.Sysctl = *aosConfig.Sysctl
	}

	if aosConfig.Quotas.RAMLimit != nil {
		if spec.ocSpec.Linux.Resources.Memory == nil {
			spec.ocSpec.Linux.Resources.Memory = &runtimespec.LinuxMemory{}
		}

		spec.ocSpec.Linux.Resources.Memory.Limit = aosConfig.Quotas.RAMLimit
	}

	if aosConfig.Quotas.PidsLimit != nil {
		if spec.ocSpec.Linux.Resources.Pids == nil {
			spec.ocSpec.Linux.Resources.Pids = &runtimespec.LinuxPids{}
		}

		spec.ocSpec.Linux.Resources.Pids.Limit = *aosConfig.Quotas.PidsLimit

		ociRlimit := runtimespec.POSIXRlimit{Type: "RLIMIT_NPROC",
			Hard: (uint64)(spec.ocSpec.Linux.Resources.Pids.Limit),
			Soft: (uint64)(spec.ocSpec.Linux.Resources.Pids.Limit)}

		spec.addRlimit(ociRlimit)
	}

	if aosConfig.Quotas.NoFileLimit != nil {
		ociRlimit := runtimespec.POSIXRlimit{Type: "RLIMIT_NOFILE",
			Hard: *aosConfig.Quotas.NoFileLimit,
			Soft: *aosConfig.Quotas.NoFileLimit}

		spec.addRlimit(ociRlimit)
	}

	if aosConfig.Quotas.CPULimit != nil {
		if spec.ocSpec.Linux.Resources.CPU == nil {
			spec.ocSpec.Linux.Resources.CPU = &runtimespec.LinuxCPU{}
		}

		cpuQuota := int64((defaultCPUPeriod * (*aosConfig.Quotas.CPULimit)) / 100)
		cpuPeriod := defaultCPUPeriod

		spec.ocSpec.Linux.Resources.CPU.Period = &cpuPeriod
		spec.ocSpec.Linux.Resources.CPU.Quota = &cpuQuota
	}

	//add tmp folder
	if aosConfig.Quotas.TmpLimit != nil {
		sizeStr := "size=" + strconv.FormatUint(*aosConfig.Quotas.TmpLimit, 10)
		newMount := runtimespec.Mount{
			Destination: "/tmp",
			Type:        "tmpfs",
			Source:      "tmpfs",
			Options:     []string{"nosuid", "strictatime", "mode=1777", sizeStr}}

		spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, newMount)
	}

	return nil
}

func (spec *serviceSpec) addRlimit(rlimit runtimespec.POSIXRlimit) {
	for i := range spec.ocSpec.Process.Rlimits {
		if spec.ocSpec.Process.Rlimits[i].Type == rlimit.Type {
			spec.ocSpec.Process.Rlimits[i] = rlimit

			return
		}
	}

	spec.ocSpec.Process.Rlimits = append(spec.ocSpec.Process.Rlimits, rlimit)
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
	log.WithField("fileName", spec.runtimeFileName).Debug("Save service spec")

	file, err := os.Create(spec.runtimeFileName)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "\t")

	return encoder.Encode(spec.ocSpec)
}

func (spec *serviceSpec) addBindMount(source, destination, attr string) (err error) {
	absSource, err := filepath.Abs(source)
	if err != nil {
		return err
	}

	newMount := runtimespec.Mount{
		Destination: destination,
		Type:        "bind",
		Source:      absSource,
		Options:     []string{"bind", attr}}

	return spec.addMount(newMount)
}

func (spec *serviceSpec) addMount(newMount runtimespec.Mount) (err error) {
	if newMount.Type == "" {
		newMount.Type = "bind"
	}

	existIndex := len(spec.ocSpec.Mounts)

	for i, mount := range spec.ocSpec.Mounts {
		if mount.Destination == newMount.Destination {
			existIndex = i

			break
		}
	}

	if existIndex == len(spec.ocSpec.Mounts) {
		spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, newMount)

		return nil
	}

	spec.ocSpec.Mounts[existIndex] = newMount

	return nil
}

func (spec *serviceSpec) removeBindMount(destination string) (err error) {
	var mounts []runtimespec.Mount

	for _, mount := range spec.ocSpec.Mounts {
		if mount.Destination == destination {
			continue
		}

		mounts = append(mounts, mount)
	}

	spec.ocSpec.Mounts = mounts

	return nil
}

func (spec *serviceSpec) setUserUIDGID(uid, gid uint32) {
	spec.ocSpec.Process.User.UID = uid
	spec.ocSpec.Process.User.GID = gid
}

func (spec *serviceSpec) bindHostDirs(workingDir string) (err error) {
	// TODO: all services should have their own certificates
	// this mound for demo only and should be removed
	// mount /etc/ssl
	etcItems := []string{"nsswitch.conf", "group", "ssl"}

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

func (spec *serviceSpec) createPrestartHook(path string, args []string) (err error) {
	if spec.ocSpec.Hooks == nil {
		spec.ocSpec.Hooks = &runtimespec.Hooks{}
	}

	spec.ocSpec.Hooks.Prestart = []runtimespec.Hook{{Path: path, Args: args}}

	return nil
}

func addDevice(hostDevice Device, uid, gid uint32) (device *runtimespec.LinuxDevice, err error) {
	log.WithFields(log.Fields{"device": hostDevice.Name}).Debug("Add device")

	var stat unix.Stat_t

	if err := unix.Lstat(hostDevice.Name, &stat); err != nil {
		return nil, err
	}

	if unix.Major(stat.Rdev) == 0 {
		return nil, errNotDevice
	}

	var (
		devType string
		mode    os.FileMode
	)

	switch {
	case stat.Mode&unix.S_IFBLK == unix.S_IFBLK:
		devType = "b"
		mode = unix.S_IFBLK
	case stat.Mode&unix.S_IFCHR == unix.S_IFCHR:
		devType = "c"
		mode = unix.S_IFCHR
	case stat.Mode&unix.S_IFIFO == unix.S_IFIFO:
		devType = "p"
		mode = unix.S_IFIFO
	default:
		return nil, errNotDevice
	}

	if strings.Contains(hostDevice.Permissions, "r") {
		mode = mode | unix.S_IRUSR
	}

	if strings.Contains(hostDevice.Permissions, "w") {
		mode = mode | unix.S_IWUSR
	}

	return &runtimespec.LinuxDevice{
		Type:     devType,
		Path:     hostDevice.Name,
		Major:    int64(unix.Major(stat.Rdev)),
		Minor:    int64(unix.Minor(stat.Rdev)),
		FileMode: &mode,
		UID:      &uid,
		GID:      &gid,
	}, nil
}

func addDevices(hostDevice Device, uid, gid uint32) (devices []runtimespec.LinuxDevice, err error) {
	stat, err := os.Stat(hostDevice.Name)
	if err != nil {
		return nil, err
	}

	switch {
	case stat.IsDir():
		files, err := ioutil.ReadDir(hostDevice.Name)
		if err != nil {
			return nil, err
		}

		for _, file := range files {
			switch file.Name() {
			case "pts", "shm", "fd", "mqueue", ".lxc", ".lxd-mounts", ".udev":
				log.WithField("device", file.Name()).Warnf("Device skipped")

				continue

			default:
				dirDevice := Device{Name: path.Join(hostDevice.Name, file.Name()),
					Permissions: hostDevice.Permissions}
				dirDevices, err := addDevices(dirDevice, uid, gid)
				if err != nil {
					return nil, err
				}

				devices = append(devices, dirDevices...)
			}
		}

		return devices, nil

	case stat.Name() == "console":
		log.WithField("device", hostDevice.Name).Warnf("Device skipped")

		return devices, nil

	default:
		device, err := addDevice(hostDevice, uid, gid)
		if err != nil {
			if err == errNotDevice {
				log.WithField("device", hostDevice.Name).Warnf("Device skipped as not a device node")

				return nil, nil
			}

			return nil, err
		}

		devices = append(devices, *device)

		return devices, nil
	}
}

func (spec *serviceSpec) addHostDevice(hostDevice Device) (err error) {
	log.WithFields(log.Fields{"device": hostDevice.Name}).Debug("Add host device")

	specDevices, err := addDevices(hostDevice, spec.ocSpec.Process.User.UID, spec.ocSpec.Process.User.UID)
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
			Access: hostDevice.Permissions,
		})
	}

	return nil
}

func (spec *serviceSpec) addAdditionalGroup(groupName string) (err error) {
	log.WithFields(log.Fields{"group": groupName}).Debug("Add group")

	group, err := user.LookupGroup(groupName)
	if err != nil {
		return err
	}

	parsedValues, err := strconv.ParseUint(group.Gid, 10, 32)
	if err != nil {
		return err
	}

	gid := uint32(parsedValues)

	for _, value := range spec.ocSpec.Process.User.AdditionalGids {
		if value == gid {
			log.Debugf("gid %d already added", gid)
			return nil
		}
	}

	spec.ocSpec.Process.User.AdditionalGids = append(spec.ocSpec.Process.User.AdditionalGids, gid)

	return nil
}

func (spec *serviceSpec) setRootfs(rootfsPath string) (err error) {
	log.WithFields(log.Fields{"rootfs": rootfsPath}).Debug("Add rootfs to spec")

	spec.ocSpec.Root = &runtimespec.Root{Path: rootfsPath, Readonly: false}

	return nil
}
