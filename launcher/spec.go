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

	"github.com/aoscloud/aos_common/aoserrors"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runc/libcontainer/devices"
	"github.com/opencontainers/runc/libcontainer/specconv"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/servicemanager"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const defaultCPUPeriod uint64 = 100000

const (
	envAosServiceID     = "AOS_SERVICE_ID"
	envAosSubjectID     = "AOS_SUBJECT_ID"
	envAosInstanceIndex = "AOS_INSTANCE_INDEX"
	envAosInstanceID    = "AOS_INSTANCE_ID"
	envAosSecret        = "AOS_SECRET"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type serviceDevice struct {
	Name        string `json:"name"`
	Permissions string `json:"permissions"`
}

type serviceQuotas struct {
	CPULimit      *uint64 `json:"cpuLimit,omitempty"`
	RAMLimit      *uint64 `json:"ramLimit,omitempty"`
	PIDsLimit     *uint64 `json:"pidsLimit,omitempty"`
	NoFileLimit   *uint64 `json:"noFileLimit,omitempty"`
	TmpLimit      *uint64 `json:"tmpLimit,omitempty"`
	StateLimit    *uint64 `json:"stateLimit,omitempty"`
	StorageLimit  *uint64 `json:"storageLimit,omitempty"`
	UploadSpeed   *uint64 `json:"uploadSpeed,omitempty"`
	DownloadSpeed *uint64 `json:"downloadSpeed,omitempty"`
	UploadLimit   *uint64 `json:"uploadLimit,omitempty"`
	DownloadLimit *uint64 `json:"downloadLimit,omitempty"`
}

type serviceConfig struct {
	Created            time.Time                    `json:"created"`
	Author             string                       `json:"author"`
	Hostname           *string                      `json:"hostname,omitempty"`
	Sysctl             map[string]string            `json:"sysctl,omitempty"`
	ServiceTTL         *uint64                      `json:"serviceTtl,omitempty"`
	Quotas             serviceQuotas                `json:"quotas"`
	AllowedConnections map[string]struct{}          `json:"allowedConnections,omitempty"`
	Devices            []serviceDevice              `json:"devices,omitempty"`
	Resources          []string                     `json:"resources,omitempty"`
	Permissions        map[string]map[string]string `json:"permissions,omitempty"`
}

type runtimeSpec struct {
	ociSpec         runtimespec.Spec
	resourceManager ResourceManager
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (spec *runtimeSpec) applyImageConfig(image *imagespec.Image) error {
	strOS := strings.ToLower(image.OS)
	if strOS != "linux" {
		return aoserrors.Errorf("unsupported OS in image config %s", image.OS)
	}

	spec.ociSpec.Process.Args = nil
	spec.ociSpec.Process.Args = append(spec.ociSpec.Process.Args, image.Config.Entrypoint...)
	spec.ociSpec.Process.Args = append(spec.ociSpec.Process.Args, image.Config.Cmd...)

	spec.ociSpec.Process.Cwd = image.Config.WorkingDir
	if spec.ociSpec.Process.Cwd == "" {
		spec.ociSpec.Process.Cwd = "/"
	}

	spec.mergeEnv(image.Config.Env)

	return nil
}

func (spec *runtimeSpec) setRAMLimit(ramLimit uint64) {
	if spec.ociSpec.Linux.Resources.Memory == nil {
		spec.ociSpec.Linux.Resources.Memory = &runtimespec.LinuxMemory{}
	}

	value := int64(ramLimit)

	spec.ociSpec.Linux.Resources.Memory.Limit = &value
}

func (spec *runtimeSpec) setPIDsLimit(pidsLimit uint64) {
	if spec.ociSpec.Linux.Resources.Pids == nil {
		spec.ociSpec.Linux.Resources.Pids = &runtimespec.LinuxPids{}
	}

	spec.ociSpec.Linux.Resources.Pids.Limit = int64(pidsLimit)
}

func (spec *runtimeSpec) setCPULimit(cpuLimit uint64) {
	if spec.ociSpec.Linux.Resources.CPU == nil {
		spec.ociSpec.Linux.Resources.CPU = &runtimespec.LinuxCPU{}
	}

	cpuQuota := int64((defaultCPUPeriod * (cpuLimit)) / 100) // nolint:gomnd // Translate to percents
	cpuPeriod := defaultCPUPeriod

	spec.ociSpec.Linux.Resources.CPU.Period = &cpuPeriod
	spec.ociSpec.Linux.Resources.CPU.Quota = &cpuQuota
}

func (spec *runtimeSpec) applyServiceConfig(config *serviceConfig) error {
	if config.Hostname != nil {
		spec.ociSpec.Hostname = *config.Hostname
	}

	spec.ociSpec.Linux.Sysctl = config.Sysctl

	if config.Quotas.CPULimit != nil {
		spec.setCPULimit(*config.Quotas.CPULimit)
	}

	if config.Quotas.RAMLimit != nil {
		spec.setRAMLimit(*config.Quotas.RAMLimit)
	}

	if config.Quotas.PIDsLimit != nil {
		spec.setPIDsLimit(*config.Quotas.PIDsLimit)
		spec.addRlimit(runtimespec.POSIXRlimit{
			Type: "RLIMIT_NPROC",
			Hard: *config.Quotas.PIDsLimit,
			Soft: *config.Quotas.PIDsLimit,
		})
	}

	if config.Quotas.NoFileLimit != nil {
		spec.addRlimit(runtimespec.POSIXRlimit{
			Type: "RLIMIT_NOFILE",
			Hard: *config.Quotas.NoFileLimit,
			Soft: *config.Quotas.NoFileLimit,
		})
	}

	if config.Quotas.TmpLimit != nil {
		spec.addMount(runtimespec.Mount{
			Destination: "/tmp",
			Type:        "tmpfs",
			Source:      "tmpfs",
			Options: []string{
				"nosuid", "strictatime", "mode=1777", "size=" + strconv.FormatUint(*config.Quotas.TmpLimit, 10),
			},
		})
	}

	if err := spec.setDevices(config.Devices); err != nil {
		return err
	}

	if err := spec.setResources(config.Resources); err != nil {
		return err
	}

	return nil
}

func (spec *runtimeSpec) addRlimit(rlimit runtimespec.POSIXRlimit) {
	for i := range spec.ociSpec.Process.Rlimits {
		if spec.ociSpec.Process.Rlimits[i].Type == rlimit.Type {
			spec.ociSpec.Process.Rlimits[i] = rlimit

			return
		}
	}

	spec.ociSpec.Process.Rlimits = append(spec.ociSpec.Process.Rlimits, rlimit)
}

func getEnvVarName(envVar string) string {
	const numEnvFields = 2

	fields := strings.SplitN(envVar, "=", numEnvFields)

	if len(fields) < numEnvFields {
		return strings.TrimSpace(envVar)
	}

	return strings.TrimSpace(fields[0])
}

func (spec *runtimeSpec) mergeEnv(newVars []string) {
newVarsLoop:
	for _, newVar := range newVars {
		varName := getEnvVarName(newVar)

		for i, existingVar := range spec.ociSpec.Process.Env {
			if varName == getEnvVarName(existingVar) {
				spec.ociSpec.Process.Env[i] = newVar

				continue newVarsLoop
			}
		}

		spec.ociSpec.Process.Env = append(spec.ociSpec.Process.Env, newVar)
	}
}

func (spec *runtimeSpec) save(fileName string) error {
	file, err := os.Create(fileName)
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "\t")

	return aoserrors.Wrap(encoder.Encode(spec.ociSpec))
}

func (spec *runtimeSpec) addBindMount(source, destination, attr string) error {
	absSource, err := filepath.Abs(source)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	newMount := runtimespec.Mount{
		Destination: destination,
		Type:        "bind",
		Source:      absSource,
		Options:     []string{"bind", attr},
	}

	spec.addMount(newMount)

	return nil
}

func (spec *runtimeSpec) addMount(newMount runtimespec.Mount) {
	if newMount.Type == "" {
		newMount.Type = "bind"
	}

	existIndex := len(spec.ociSpec.Mounts)

	for i, mount := range spec.ociSpec.Mounts {
		if mount.Destination == newMount.Destination {
			existIndex = i
			break
		}
	}

	if existIndex == len(spec.ociSpec.Mounts) {
		spec.ociSpec.Mounts = append(spec.ociSpec.Mounts, newMount)
		return
	}

	spec.ociSpec.Mounts[existIndex] = newMount
}

func (spec *runtimeSpec) setUserUIDGID(uid, gid uint32) error {
	spec.ociSpec.Process.User.UID = uid
	spec.ociSpec.Process.User.GID = gid

	return nil
}

func (spec *runtimeSpec) bindHostDirs(workingDir string) {
	// All instances should have their own certificates
	// this mount for demo only and should be removed
	// mount /etc/ssl
	etcItems := []string{"nsswitch.conf", "ssl"}

	for _, item := range etcItems {
		// Check if in working dir
		absPath, _ := filepath.Abs(path.Join(workingDir, "etc", item))
		if _, err := os.Stat(absPath); err != nil {
			absPath = path.Join("/etc", item)
		}

		spec.ociSpec.Mounts = append(spec.ociSpec.Mounts, runtimespec.Mount{
			Destination: path.Join("/etc", item), Type: "bind", Source: absPath, Options: []string{"bind", "ro"},
		})
	}
}

func (spec *runtimeSpec) addHostDevice(hostPath, containerPath, permissions string) error {
	log.WithFields(log.Fields{"hostPath": hostPath, "containerPath": containerPath}).Debug("Add host device")

	stat, err := os.Lstat(hostPath)
	if err == nil && stat.Mode()&os.ModeSymlink == os.ModeSymlink {
		if linkPath, err := filepath.EvalSymlinks(hostPath); err == nil {
			hostPath = linkPath
		}
	}

	var specDevices []*devices.Device

	specDevice, err := devices.DeviceFromPath(hostPath, permissions)
	if err == nil {
		specDevice.Path = containerPath
		specDevices = append(specDevices, specDevice)
	} else {
		if !errors.Is(err, devices.ErrNotADevice) {
			return aoserrors.Wrap(err)
		}

		if stat, statErr := os.Stat(hostPath); statErr != nil || !stat.IsDir() {
			return aoserrors.Wrap(err)
		}

		_ = filepath.Walk(hostPath, func(path string, fileInfo os.FileInfo, _ error) error {
			if path == hostPath {
				return nil
			}

			if specDevice, err = devices.DeviceFromPath(path, permissions); err != nil {
				// skip device
				log.WithField("device", path).Warnf("Skip device: %v", err)

				return nil // nolint:nilerr // returning err stops path walk
			}

			specDevice.Path = strings.Replace(path, hostPath, containerPath, 1)
			specDevices = append(specDevices, specDevice)

			return nil
		})
	}

	spec.addSpecDevices(specDevices)

	return nil
}

func (spec *runtimeSpec) addSpecDevices(specDevices []*devices.Device) {
deviceLoop:
	for _, specDevice := range specDevices {
		// Skip already existing devices
		for _, existingDevice := range spec.ociSpec.Linux.Devices {
			if specDevice.Path == existingDevice.Path {
				continue deviceLoop
			}
		}

		spec.ociSpec.Linux.Devices = append(spec.ociSpec.Linux.Devices,
			runtimespec.LinuxDevice{
				Path:     specDevice.Path,
				Type:     string(specDevice.Type),
				Major:    specDevice.Major,
				Minor:    specDevice.Minor,
				FileMode: &specDevice.FileMode,
				UID:      &specDevice.Uid,
				GID:      &specDevice.Gid,
			})
		spec.ociSpec.Linux.Resources.Devices = append(spec.ociSpec.Linux.Resources.Devices,
			runtimespec.LinuxDeviceCgroup{
				Allow:  true,
				Type:   string(specDevice.Type),
				Major:  &specDevice.Major,
				Minor:  &specDevice.Minor,
				Access: string(specDevice.Permissions),
			})
	}
}

func (spec *runtimeSpec) addAdditionalGroup(name string) error {
	group, err := user.LookupGroup(name)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	parsedValues, err := strconv.ParseUint(group.Gid, 10, 32)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	gid := uint32(parsedValues)

	for _, value := range spec.ociSpec.Process.User.AdditionalGids {
		if value == gid {
			return nil
		}
	}

	spec.ociSpec.Process.User.AdditionalGids = append(spec.ociSpec.Process.User.AdditionalGids, gid)

	return nil
}

func (spec *runtimeSpec) setRootfs(rootfsPath string) {
	spec.ociSpec.Root = &runtimespec.Root{Path: rootfsPath, Readonly: false}
}

func (spec *runtimeSpec) setDevices(devices []serviceDevice) error {
	const numDeviceFields = 2

	for _, device := range devices {
		deviceInfo, err := spec.resourceManager.GetDeviceInfo(device.Name)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		for _, hostDevice := range deviceInfo.HostDevices {
			deviceFields := strings.SplitN(hostDevice, ":", numDeviceFields)
			if len(deviceFields) < 1 || len(deviceFields) > numDeviceFields {
				return aoserrors.Errorf("host device field is malformed: %s", hostDevice)
			}

			hostDevice = deviceFields[0]
			containerDevice := hostDevice

			if len(deviceFields) == numDeviceFields {
				containerDevice = deviceFields[1]
			}

			if err = spec.addHostDevice(hostDevice, containerDevice, device.Permissions); err != nil {
				return err
			}
		}

		for _, group := range deviceInfo.Groups {
			if err = spec.addAdditionalGroup(group); err != nil {
				return err
			}
		}
	}

	return nil
}

func (spec *runtimeSpec) setResources(resources []string) error {
	for _, resource := range resources {
		resourceInfo, err := spec.resourceManager.GetResourceInfo(resource)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		for _, group := range resourceInfo.Groups {
			if err = spec.addAdditionalGroup(group); err != nil {
				return err
			}
		}

		for _, mount := range resourceInfo.Mounts {
			spec.addMount(runtimespec.Mount{
				Destination: mount.Destination,
				Source:      mount.Source,
				Type:        mount.Type,
				Options:     mount.Options,
			})
		}

		spec.mergeEnv(resourceInfo.Env)
	}

	return nil
}

func (spec *runtimeSpec) setNamespacePath(namespaceType runtimespec.LinuxNamespaceType, namespacePath string) {
	for i, namespace := range spec.ociSpec.Linux.Namespaces {
		if namespace.Type == namespaceType {
			spec.ociSpec.Linux.Namespaces[i].Path = namespacePath
			return
		}
	}

	spec.ociSpec.Linux.Namespaces = append(spec.ociSpec.Linux.Namespaces, runtimespec.LinuxNamespace{
		Type: namespaceType, Path: namespacePath,
	})
}

func getJSONFromFile(fileName string, data interface{}) error {
	byteValue, err := ioutil.ReadFile(fileName)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = json.Unmarshal(byteValue, data); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) getImageConfig(service servicemanager.ServiceInfo) (*imagespec.Image, error) {
	imageParts, err := launcher.serviceProvider.GetImageParts(service)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	var imageConfig imagespec.Image

	if err = getJSONFromFile(imageParts.ImageConfigPath, &imageConfig); err != nil {
		return nil, err
	}

	return &imageConfig, nil
}

func (launcher *Launcher) getServiceConfig(service servicemanager.ServiceInfo) (*serviceConfig, error) {
	imageParts, err := launcher.serviceProvider.GetImageParts(service)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	var serviceConfig serviceConfig

	if imageParts.ServiceConfigPath != "" {
		if err = getJSONFromFile(
			imageParts.ServiceConfigPath, &serviceConfig); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	return &serviceConfig, nil
}

func (launcher *Launcher) createRuntimeSpec(instance *instanceInfo) (*runtimeSpec, error) {
	spec := &runtimeSpec{
		resourceManager: launcher.resourceManager,
		ociSpec:         *specconv.Example(),
	}

	spec.ociSpec.Process.Args = nil
	spec.ociSpec.Process.Env = nil
	spec.ociSpec.Process.Terminal = false

	spec.setRootfs(filepath.Join(instance.runtimeDir, instanceRootFS))
	spec.bindHostDirs(launcher.config.WorkingDir)
	spec.setNamespacePath(runtimespec.NetworkNamespace, launcher.networkManager.GetNetnsPath(instance.InstanceID))
	spec.mergeEnv(createAosEnvVars(instance))

	if err := spec.setUserUIDGID(uint32(instance.UID), uint32(instance.service.GID)); err != nil {
		return nil, err
	}

	if err := spec.applyImageConfig(instance.service.imageConfig); err != nil {
		return nil, err
	}

	if err := spec.applyServiceConfig(instance.service.serviceConfig); err != nil {
		return nil, err
	}

	fileName := filepath.Join(instance.runtimeDir, runtimeConfigFile)

	log.WithFields(
		instanceIdentLogFields(instance.InstanceIdent, log.Fields{"fileName": fileName}),
	).Debug("Save runtime spec")

	if err := spec.save(fileName); err != nil {
		return nil, err
	}

	return spec, nil
}

func createAosEnvVars(instance *instanceInfo) (aosEnvVars []string) {
	aosEnvVars = append(aosEnvVars, fmt.Sprintf("%s=%s", envAosServiceID, instance.ServiceID))
	aosEnvVars = append(aosEnvVars, fmt.Sprintf("%s=%s", envAosSubjectID, instance.SubjectID))
	aosEnvVars = append(aosEnvVars, fmt.Sprintf("%s=%d", envAosInstanceIndex, instance.Instance))
	aosEnvVars = append(aosEnvVars, fmt.Sprintf("%s=%s", envAosInstanceID, instance.InstanceID))

	if instance.secret != "" {
		aosEnvVars = append(aosEnvVars, fmt.Sprintf("%s=%s", envAosSecret, instance.secret))
	}

	return aosEnvVars
}
