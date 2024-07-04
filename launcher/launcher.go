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

package launcher

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/aostypes"
	"github.com/aosedge/aos_common/api/cloudprotocol"
	"github.com/aosedge/aos_common/resourcemonitor"
	"github.com/aosedge/aos_common/utils/action"
	"github.com/aosedge/aos_common/utils/fs"
	"github.com/google/uuid"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"

	"github.com/aosedge/aos_servicemanager/config"
	"github.com/aosedge/aos_servicemanager/layermanager"
	"github.com/aosedge/aos_servicemanager/networkmanager"
	"github.com/aosedge/aos_servicemanager/runner"
	"github.com/aosedge/aos_servicemanager/servicemanager"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const maxParallelInstanceActions = 32

const (
	hostFSWiteoutsDir      = "hostfs/whiteouts"
	runtimeConfigFile      = "config.json"
	instanceRootFS         = "rootfs"
	instanceMountPointsDir = "mounts"
	instanceStateFile      = "/state.dat"
	instanceStorageDir     = "/storage"
	runxRunner             = "runx"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// NodeInfoProvider provides node information.
type NodeInfoProvider interface {
	GetNodeInfo() (cloudprotocol.NodeInfo, error)
}

// Storage storage interface.
type Storage interface {
	AddInstance(instance InstanceInfo) error
	UpdateInstance(instance InstanceInfo) error
	RemoveInstance(instanceID string) error
	GetAllInstances() ([]InstanceInfo, error)
	GetOverrideEnvVars() ([]cloudprotocol.EnvVarsInstanceInfo, error)
	SetOverrideEnvVars(envVarsInfo []cloudprotocol.EnvVarsInstanceInfo) error
	GetOnlineTime() (time.Time, error)
	SetOnlineTime(t time.Time) error
}

// ServiceProvider service provider.
type ServiceProvider interface {
	GetServiceInfo(serviceID string) (servicemanager.ServiceInfo, error)
	GetImageParts(service servicemanager.ServiceInfo) (servicemanager.ImageParts, error)
	ValidateService(service servicemanager.ServiceInfo) error
}

// LayerProvider layer provider.
type LayerProvider interface {
	GetLayerInfoByDigest(digest string) (layermanager.LayerInfo, error)
}

// InstanceRunner interface to start/stop service instances.
type InstanceRunner interface {
	StartInstance(instanceID, runtimeDir string, params runner.RunParameters) runner.InstanceStatus
	StopInstance(instanceID string) error
	InstanceStatusChannel() <-chan []runner.InstanceStatus
}

// ResourceManager provides API to validate, request and release resources.
type ResourceManager interface {
	GetDeviceInfo(device string) (cloudprotocol.DeviceInfo, error)
	GetResourceInfo(resource string) (cloudprotocol.ResourceInfo, error)
	AllocateDevice(device, instanceID string) error
	ReleaseDevice(device, instanceID string) error
	ReleaseDevices(instanceID string) error
	GetDeviceInstances(name string) (instanceIDs []string, err error)
}

// NetworkManager provides network access.
type NetworkManager interface {
	GetNetnsPath(instanceID string) string
	AddInstanceToNetwork(instanceID, networkID string, params networkmanager.NetworkParams) error
	RemoveInstanceFromNetwork(instanceID, networkID string) error
}

// InstanceRegistrar provides API to register/unregister instance.
type InstanceRegistrar interface {
	RegisterInstance(
		instance aostypes.InstanceIdent, permissions map[string]map[string]string,
	) (secret string, err error)
	UnregisterInstance(instance aostypes.InstanceIdent) error
}

// InstanceMonitor provides API to monitor instance parameters.
type InstanceMonitor interface {
	StartInstanceMonitor(instanceID string, params resourcemonitor.ResourceMonitorParams) error
	StopInstanceMonitor(instanceID string) error
}

// AlertSender provides interface to send alerts.
type AlertSender interface {
	SendAlert(alert cloudprotocol.AlertItem)
}

// InstanceInfo instance information.
type InstanceInfo struct {
	aostypes.InstanceInfo
	InstanceID string
}

// RuntimeStatus runtime status info.
type RuntimeStatus struct {
	RunStatus    *InstancesStatus
	UpdateStatus *InstancesStatus
}

// InstancesStatus instances status.
type InstancesStatus struct {
	Instances []cloudprotocol.InstanceStatus
}

// Launcher launcher instance.
type Launcher struct {
	sync.Mutex

	storage           Storage
	serviceProvider   ServiceProvider
	layerProvider     LayerProvider
	instanceRunner    InstanceRunner
	resourceManager   ResourceManager
	networkManager    NetworkManager
	instanceRegistrar InstanceRegistrar
	instanceMonitor   InstanceMonitor
	alertSender       AlertSender

	config                 *config.Config
	nodeInfo               cloudprotocol.NodeInfo
	runtimeStatusChannel   chan RuntimeStatus
	cancelFunction         context.CancelFunc
	actionHandler          *action.Handler
	runMutex               sync.Mutex
	runInstancesInProgress bool
	currentInstances       map[string]*runtimeInstanceInfo
	currentServices        map[string]*serviceInfo
	currentEnvVars         []cloudprotocol.EnvVarsInstanceInfo
	onlineTime             time.Time
	isCloudOnline          bool
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

// OperationVersion defines current operation version
// IMPORTANT: if new functionality doesn't allow existing services to work
// properly, this value should be increased. It will force to remove all
// services and their storages before first start.
const OperationVersion = 9

// Mount, unmount instance FS functions.
//
//nolint:gochecknoglobals
var (
	MountFunc   = fs.OverlayMount
	UnmountFunc = fs.Umount
)

// ErrNotExist not exist instance error.
var ErrNotExist = errors.New("instance not exist")

//nolint:gochecknoglobals // used to be overridden in unit tests
var (
	// RuntimeDir specifies directory where instance runtime spec is stored.
	RuntimeDir = "/run/aos/runtime"
	// CheckTTLsPeriod specifies period different TTL timers are checked with.
	CheckTTLsPeriod = 1 * time.Hour
)

var defaultHostFSBinds = []string{"bin", "sbin", "lib", "lib64", "usr"} //nolint:gochecknoglobals // const

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new launcher object.
func New(config *config.Config, nodeInfoProvider NodeInfoProvider, storage Storage, serviceProvider ServiceProvider,
	layerProvider LayerProvider, instanceRunner InstanceRunner, resourceManager ResourceManager,
	networkManager NetworkManager, instanceRegistrar InstanceRegistrar, instanceMonitor InstanceMonitor,
	alertSender AlertSender,
) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	nodeInfo, err := nodeInfoProvider.GetNodeInfo()
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	launcher = &Launcher{
		storage: storage, serviceProvider: serviceProvider, layerProvider: layerProvider,
		instanceRunner: instanceRunner, resourceManager: resourceManager, networkManager: networkManager,
		instanceRegistrar: instanceRegistrar, instanceMonitor: instanceMonitor, alertSender: alertSender,

		config:               config,
		nodeInfo:             nodeInfo,
		actionHandler:        action.New(maxParallelInstanceActions),
		runtimeStatusChannel: make(chan RuntimeStatus, 1),
		currentInstances:     make(map[string]*runtimeInstanceInfo),
	}

	ctx, cancelFunction := context.WithCancel(context.Background())

	launcher.cancelFunction = cancelFunction

	go launcher.handleChannels(ctx)

	if err = launcher.prepareHostFSDir(); err != nil {
		return nil, err
	}

	if err = os.MkdirAll(RuntimeDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if launcher.onlineTime, err = launcher.storage.GetOnlineTime(); err != nil {
		log.Errorf("Can't get online time: %v", err)
	}

	if launcher.currentEnvVars, err = launcher.storage.GetOverrideEnvVars(); err != nil {
		log.Errorf("Can't get current env vars: %v", err)
	}

	// Restart previously started instances
	if err = launcher.restartStoredInstances(); err != nil {
		log.Errorf("Restart instances error: %v", err)
	}

	return launcher, nil
}

// Close closes launcher.
func (launcher *Launcher) Close() (err error) {
	launcher.Lock()
	defer launcher.Unlock()

	log.Debug("Close launcher")

	launcher.cancelFunction()
	launcher.stopCurrentInstances()

	if removeErr := os.RemoveAll(RuntimeDir); removeErr != nil && err == nil {
		err = aoserrors.Wrap(removeErr)
	}

	return err
}

// RunInstances runs desired services instances.
func (launcher *Launcher) RunInstances(instances []aostypes.InstanceInfo, forceRestart bool) error {
	launcher.Lock()
	defer launcher.Unlock()

	if forceRestart {
		log.Debug("Restart instances")

		launcher.stopCurrentInstances()
	} else {
		log.Debug("Run instances")
	}

	return launcher.runInstances(launcher.getRunningInstances(instances))
}

// OverrideEnvVars overrides service instance environment variables.
func (launcher *Launcher) OverrideEnvVars(
	envVarsInfo []cloudprotocol.EnvVarsInstanceInfo,
) ([]cloudprotocol.EnvVarsInstanceStatus, error) {
	launcher.Lock()
	defer launcher.Unlock()

	envVarsStatus := launcher.setEnvVars(envVarsInfo)

	launcher.updateInstancesEnvVars()

	return envVarsStatus, nil
}

// RuntimeStatusChannel returns runtime status channel.
func (launcher *Launcher) RuntimeStatusChannel() <-chan RuntimeStatus {
	return launcher.runtimeStatusChannel
}

// CloudConnection sets cloud connection status.
func (launcher *Launcher) CloudConnection(connected bool) error {
	launcher.Lock()
	defer launcher.Unlock()

	log.WithField("connected", connected).Debug("Cloud connection changed")

	defer func() {
		launcher.isCloudOnline = connected
	}()

	if connected || launcher.isCloudOnline {
		launcher.onlineTime = time.Now()

		if err := launcher.storage.SetOnlineTime(launcher.onlineTime); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (launcher *Launcher) handleChannels(ctx context.Context) {
	for {
		select {
		case instances := <-launcher.instanceRunner.InstanceStatusChannel():
			launcher.updateInstancesStatuses(instances)

		case <-time.After(CheckTTLsPeriod):
			launcher.Lock()
			launcher.updateInstancesEnvVars()
			launcher.updateOfflineTimeouts()
			launcher.Unlock()

		case <-ctx.Done():
			return
		}
	}
}

func (launcher *Launcher) updateInstancesStatuses(instances []runner.InstanceStatus) {
	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	updateInstancesStatus := &InstancesStatus{Instances: make([]cloudprotocol.InstanceStatus, 0, len(instances))}

	for _, instanceStatus := range instances {
		currentInstance, ok := launcher.currentInstances[instanceStatus.InstanceID]
		if !ok {
			log.WithField("instanceID", instanceStatus.InstanceID).Warn("Not running instance status received")
			continue
		}

		if currentInstance.runStatus.State != instanceStatus.State {
			currentInstance.setRunStatus(instanceStatus)

			if !launcher.runInstancesInProgress {
				updateInstancesStatus.Instances = append(updateInstancesStatus.Instances,
					currentInstance.getCloudStatus())
			}
		}
	}

	if len(updateInstancesStatus.Instances) > 0 {
		launcher.runtimeStatusChannel <- RuntimeStatus{UpdateStatus: updateInstancesStatus}
	}
}

func (launcher *Launcher) runInstances(runInstances []InstanceInfo) error {
	launcher.runMutex.Lock()
	if launcher.runInstancesInProgress {
		launcher.runMutex.Unlock()

		return aoserrors.Errorf("run instances in progress")
	}

	defer func() {
		launcher.runMutex.Lock()
		defer launcher.runMutex.Unlock()

		launcher.runInstancesInProgress = false
		launcher.sendRunInstancesStatuses()
	}()

	launcher.runInstancesInProgress = true
	launcher.runMutex.Unlock()

	launcher.cacheCurrentServices(runInstances)

	stopInstances, startInstances := launcher.calculateInstances(runInstances)

	launcher.stopInstances(stopInstances)
	launcher.startInstances(startInstances)

	return nil
}

func (launcher *Launcher) calculateInstances(
	runInstances []InstanceInfo,
) (stopInstances, startInstances []*runtimeInstanceInfo) {
	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	currentInstances := make([]*runtimeInstanceInfo, 0, len(launcher.currentInstances))

	for _, currentInstance := range launcher.currentInstances {
		currentInstances = append(currentInstances, currentInstance)
	}

	sort.Slice(runInstances, func(i, j int) bool { return runInstances[i].Priority > runInstances[j].Priority })
	sort.Slice(currentInstances, func(i, j int) bool {
		return currentInstances[i].Priority > currentInstances[j].Priority
	})

	maxPriorityIncreased := len(runInstances) > 0 && len(currentInstances) > 0 &&
		runInstances[0].Priority > currentInstances[0].Priority

	var maxStartPriority uint64

runInstancesLoop:
	for _, runInstance := range runInstances {
		for i, currentInstance := range currentInstances {
			if currentInstance.InstanceIdent != runInstance.InstanceIdent {
				continue
			}

			if currentInstance.service != nil &&
				currentInstance.service.Version == launcher.currentServices[currentInstance.ServiceID].Version &&
				instanceInfoEqual(currentInstance.InstanceInfo.InstanceInfo, runInstance.InstanceInfo) &&
				currentInstance.runStatus.State == cloudprotocol.InstanceStateActive &&
				currentInstance.Priority >= maxStartPriority && !maxPriorityIncreased {
				currentInstances = append(currentInstances[:i], currentInstances[i+1:]...)

				continue runInstancesLoop
			}

			stopInstances = append(stopInstances, currentInstance)
			currentInstances = append(currentInstances[:i], currentInstances[i+1:]...)

			break
		}

		if runInstance.Priority > maxStartPriority {
			maxStartPriority = runInstance.Priority
		}

		startInstances = append(startInstances, newRuntimeInstanceInfo(runInstance))
	}

	stopInstances = append(stopInstances, currentInstances...)

	return stopInstances, startInstances
}

func (launcher *Launcher) stopInstances(instances []*runtimeInstanceInfo) {
	for _, instance := range instances {
		launcher.doStopAction(instance)
	}

	launcher.actionHandler.Wait()
}

func (launcher *Launcher) doStopAction(instance *runtimeInstanceInfo) {
	launcher.actionHandler.Execute(instance.InstanceID, func(instanceID string) (err error) {
		defer func() {
			if err != nil {
				log.WithFields(instanceLogFields(instance, nil)).Errorf("Can't stop instance: %v", err)

				return
			}

			log.WithFields(instanceLogFields(instance, nil)).Info("Instance successfully stopped")
		}()

		if stopErr := launcher.stopInstance(instance); stopErr != nil && err == nil {
			err = stopErr
		}

		return err
	})
}

func (launcher *Launcher) releaseRuntime(instance *runtimeInstanceInfo) (err error) {
	if instance.service.serviceConfig.Permissions != nil {
		if registerErr := launcher.instanceRegistrar.UnregisterInstance(
			instance.InstanceIdent); registerErr != nil && err == nil {
			err = aoserrors.Wrap(registerErr)
		}
	}

	if launcher.networkManager != nil {
		if networkErr := launcher.networkManager.RemoveInstanceFromNetwork(
			instance.InstanceID, instance.service.ServiceProvider); networkErr != nil && err == nil {
			err = aoserrors.Wrap(networkErr)
		}
	}

	if deviceErr := launcher.resourceManager.ReleaseDevices(instance.InstanceID); deviceErr != nil && err == nil {
		err = aoserrors.Wrap(deviceErr)
	}

	return err
}

func (launcher *Launcher) stopInstance(instance *runtimeInstanceInfo) (err error) {
	log.WithFields(instanceLogFields(instance, nil)).Debug("Stop instance")

	if err := func() error {
		launcher.runMutex.Lock()
		defer launcher.runMutex.Unlock()

		if _, ok := launcher.currentInstances[instance.InstanceID]; !ok {
			return aoserrors.New("instance already stopped")
		}

		return nil
	}(); err != nil {
		return err
	}

	defer func() {
		launcher.runMutex.Lock()
		defer launcher.runMutex.Unlock()

		delete(launcher.currentInstances, instance.InstanceID)
	}()

	if instance.service == nil {
		return nil
	}

	if monitorErr := launcher.instanceMonitor.StopInstanceMonitor(
		instance.InstanceID); monitorErr != nil && err == nil {
		err = aoserrors.Wrap(monitorErr)
	}

	if runnerErr := launcher.instanceRunner.StopInstance(instance.InstanceID); runnerErr != nil && err == nil {
		err = aoserrors.Wrap(runnerErr)
	}

	if releaseErr := launcher.releaseRuntime(instance); releaseErr != nil && err == nil {
		err = releaseErr
	}

	mountPoint := filepath.Join(instance.runtimeDir, instanceRootFS)

	if _, errStat := os.Stat(mountPoint); errStat == nil {
		if unmountErr := UnmountFunc(mountPoint); unmountErr != nil && err == nil {
			err = aoserrors.Wrap(unmountErr)
		}
	} else if !os.IsNotExist(errStat) && err == nil {
		err = aoserrors.Wrap(errStat)
	}

	if removeErr := os.RemoveAll(instance.runtimeDir); removeErr != nil && err == nil {
		err = aoserrors.Wrap(removeErr)
	}

	return err
}

func (launcher *Launcher) startInstances(instances []*runtimeInstanceInfo) {
	var currentPriority uint64

	if len(instances) > 0 {
		currentPriority = instances[0].Priority

		log.WithField("priority", currentPriority).Debug("Start instances with priority")
	}

	for _, instance := range instances {
		if currentPriority != instance.Priority {
			launcher.actionHandler.Wait()

			currentPriority = instance.Priority

			log.WithField("priority", currentPriority).Debug("Start instances with priority")
		}

		launcher.doStartAction(instance)
	}

	launcher.actionHandler.Wait()
}

func (launcher *Launcher) doStartAction(instance *runtimeInstanceInfo) {
	launcher.actionHandler.Execute(instance.InstanceID, func(instanceID string) (err error) {
		defer func() {
			if err != nil {
				launcher.runMutex.Lock()
				defer launcher.runMutex.Unlock()

				launcher.instanceFailed(instance, err)
			}
		}()

		if err = launcher.startInstance(instance); err != nil {
			return err
		}

		return nil
	})
}

func (launcher *Launcher) getHostsFromResources(resources []string) (hosts []cloudprotocol.HostInfo, err error) {
	for _, resource := range resources {
		unitResource, err := launcher.resourceManager.GetResourceInfo(resource)
		if err != nil {
			return hosts, aoserrors.Wrap(err)
		}

		hosts = append(hosts, unitResource.Hosts...)
	}

	return hosts, nil
}

func (launcher *Launcher) setupNetwork(instance *runtimeInstanceInfo) (err error) {
	networkFilesDir := filepath.Join(instance.runtimeDir, instanceMountPointsDir)

	if err = os.MkdirAll(filepath.Join(networkFilesDir, "etc"), 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	params := networkmanager.NetworkParams{
		InstanceIdent:      instance.InstanceIdent,
		HostsFilePath:      filepath.Join(networkFilesDir, "etc", "hosts"),
		ResolvConfFilePath: filepath.Join(networkFilesDir, "etc", "resolv.conf"),
		Hosts:              launcher.config.Hosts,
		NetworkParameters:  instance.NetworkParameters,
	}

	resourceHosts, err := launcher.getHostsFromResources(instance.service.serviceConfig.Resources)
	if err != nil {
		return err
	}

	params.Hosts = append(params.Hosts, resourceHosts...)

	if instance.service.serviceConfig.Quotas.DownloadSpeed != nil {
		params.IngressKbit = *instance.service.serviceConfig.Quotas.DownloadSpeed
	}

	if instance.service.serviceConfig.Quotas.UploadSpeed != nil {
		params.EgressKbit = *instance.service.serviceConfig.Quotas.UploadSpeed
	}

	if instance.service.serviceConfig.Quotas.DownloadLimit != nil {
		params.DownloadLimit = *instance.service.serviceConfig.Quotas.DownloadLimit
	}

	if instance.service.serviceConfig.Quotas.UploadLimit != nil {
		params.UploadLimit = *instance.service.serviceConfig.Quotas.UploadLimit
	}

	if instance.service.serviceConfig.Hostname != nil {
		params.Hostname = *instance.service.serviceConfig.Hostname
	}

	params.ExposedPorts = make([]string, 0, len(instance.service.imageConfig.Config.ExposedPorts))

	for key := range instance.service.imageConfig.Config.ExposedPorts {
		params.ExposedPorts = append(params.ExposedPorts, key)
	}

	if launcher.networkManager != nil {
		if err := launcher.networkManager.AddInstanceToNetwork(
			instance.InstanceID, instance.service.ServiceProvider, params); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (launcher *Launcher) allocateDevices(instance *runtimeInstanceInfo) (err error) {
	defer func() {
		if err != nil {
			if releaseErr := launcher.resourceManager.ReleaseDevices(instance.InstanceID); releaseErr != nil {
				log.WithField("instanceID", instance.InstanceID).Errorf("Can't release devices: %v", err)
			}
		}
	}()

	for _, device := range instance.service.serviceConfig.Devices {
		if err = launcher.resourceManager.AllocateDevice(device.Name, instance.InstanceID); err != nil {
			launcher.alertSender.SendAlert(deviceAllocateAlert(instance, device.Name, err))

			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (launcher *Launcher) setupRuntime(instance *runtimeInstanceInfo) error {
	if err := launcher.allocateDevices(instance); err != nil {
		return err
	}

	if instance.service.serviceConfig.Permissions != nil {
		secret, err := launcher.instanceRegistrar.RegisterInstance(
			instance.InstanceIdent, instance.service.serviceConfig.Permissions)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		instance.secret = secret
	}

	if launcher.networkManager != nil {
		if err := launcher.setupNetwork(instance); err != nil {
			return err
		}
	}

	return nil
}

func (launcher *Launcher) startInstance(instance *runtimeInstanceInfo) error {
	log.WithFields(instanceLogFields(instance, nil)).Debug("Start instance")

	if err := func() error {
		launcher.runMutex.Lock()
		defer launcher.runMutex.Unlock()

		if _, ok := launcher.currentInstances[instance.InstanceID]; ok {
			return aoserrors.New("instance already started")
		}

		launcher.currentInstances[instance.InstanceID] = instance

		service, err := launcher.getCurrentServiceInfo(instance.ServiceID)
		if err != nil {
			return err
		}

		instance.service = service
		instance.runStatus = runner.InstanceStatus{InstanceID: instance.InstanceID}

		return nil
	}(); err != nil {
		return err
	}

	if err := os.MkdirAll(instance.runtimeDir, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := launcher.setupRuntime(instance); err != nil {
		return err
	}

	runtimeSpec, err := launcher.createRuntimeSpec(instance)
	if err != nil {
		return err
	}

	if err := launcher.prepareRootFS(instance, runtimeSpec); err != nil {
		return err
	}

	runStatus := launcher.instanceRunner.StartInstance(
		instance.InstanceID, instance.runtimeDir, runner.RunParameters{
			StartInterval:   instance.service.serviceConfig.RunParameters.StartInterval.Duration,
			StartBurst:      instance.service.serviceConfig.RunParameters.StartBurst,
			RestartInterval: instance.service.serviceConfig.RunParameters.RestartInterval.Duration,
		})

	// Update current status if it is not updated by runner status channel. Instance runner status goes asynchronously
	// by status channel. And therefore, new status may arrive before returning by StartInstance API. We detect this
	// situation by checking if run state is not empty value.
	launcher.runMutex.Lock()

	if instance.runStatus.State == "" {
		instance.setRunStatus(runStatus)
	}

	launcher.runMutex.Unlock()

	monitorParams := resourcemonitor.ResourceMonitorParams{
		InstanceIdent: instance.InstanceIdent,
		UID:           int(instance.UID),
		GID:           int(instance.service.GID),
		AlertRules:    instance.service.serviceConfig.AlertRules,
	}

	if instance.StoragePath != "" {
		monitorParams.Partitions = append(monitorParams.Partitions, resourcemonitor.PartitionParam{
			Name: "storage",
			Path: launcher.getAbsStoragePath(instance.StoragePath),
		})
	}

	if instance.StatePath != "" {
		monitorParams.Partitions = append(monitorParams.Partitions, resourcemonitor.PartitionParam{
			Name: "state",
			Path: launcher.getAbsStatePath(instance.StatePath),
		})
	}

	if err := launcher.instanceMonitor.StartInstanceMonitor(instance.InstanceID, monitorParams); err != nil {
		log.WithFields(instanceLogFields(instance, nil)).Errorf("Can't start instance monitoring: %v", err)
	}

	return nil
}

func (launcher *Launcher) sendRunInstancesStatuses() {
	runInstancesStatuses := make([]cloudprotocol.InstanceStatus, 0, len(launcher.currentInstances))

	for _, currentInstance := range launcher.currentInstances {
		runInstancesStatuses = append(runInstancesStatuses, currentInstance.getCloudStatus())
	}

	launcher.runtimeStatusChannel <- RuntimeStatus{
		RunStatus: &InstancesStatus{Instances: runInstancesStatuses},
	}
}

func (launcher *Launcher) stopCurrentInstances() {
	stopInstances := make([]*runtimeInstanceInfo, 0, len(launcher.currentInstances))

	launcher.runMutex.Lock()

	for _, instance := range launcher.currentInstances {
		stopInstances = append(stopInstances, instance)
	}

	launcher.runMutex.Unlock()

	launcher.stopInstances(stopInstances)
}

func (launcher *Launcher) prepareHostFSDir() (err error) {
	witeoutsDir := path.Join(launcher.config.WorkingDir, hostFSWiteoutsDir)

	if err = os.MkdirAll(witeoutsDir, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	allowedDirs := defaultHostFSBinds

	if len(launcher.config.HostBinds) > 0 {
		allowedDirs = launcher.config.HostBinds
	}

	rootContent, err := os.ReadDir("/")
	if err != nil {
		return aoserrors.Wrap(err)
	}

rootLabel:
	for _, item := range rootContent {
		itemPath := path.Join(witeoutsDir, item.Name())

		for _, allowedItem := range allowedDirs {
			if item.Name() == allowedItem {
				continue rootLabel
			}
		}

		if _, err = os.Stat(itemPath); err == nil {
			// skip already exists items
			continue
		}

		if !os.IsNotExist(err) {
			return aoserrors.Wrap(err)
		}

		// Create whiteout for not allowed items
		if err = syscall.Mknod(itemPath, syscall.S_IFCHR, int(unix.Mkdev(0, 0))); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func prepareStorageDir(path string, uid, gid uint32) error {
	_, err := os.Stat(path)
	if err == nil {
		return nil
	}

	if err = os.MkdirAll(path, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = os.Chown(path, int(uid), int(gid)); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func prepareStateFile(path string, uid, gid uint32) error {
	_, err := os.Stat(path)
	if err == nil {
		return nil
	}

	if !os.IsNotExist(err) {
		return aoserrors.Wrap(err)
	}

	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer file.Close()

	if err = os.Chown(path, int(uid), int(gid)); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) getAbsStoragePath(path string) string {
	return filepath.Join(launcher.config.StorageDir, path)
}

func (launcher *Launcher) getAbsStatePath(path string) string {
	return filepath.Join(launcher.config.StateDir, path)
}

func (launcher *Launcher) prepareRootFS(instance *runtimeInstanceInfo, runtimeConfig *runtimeSpec) error {
	mountPointsDir := filepath.Join(instance.runtimeDir, instanceMountPointsDir)

	if err := launcher.createMountPoints(mountPointsDir, runtimeConfig.ociSpec.Mounts); err != nil {
		return err
	}

	imageParts, err := launcher.serviceProvider.GetImageParts(instance.service.ServiceInfo)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	layersDir := []string{mountPointsDir, imageParts.ServiceFSPath}

	for _, digest := range imageParts.LayersDigest {
		layer, err := launcher.layerProvider.GetLayerInfoByDigest(digest)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		layersDir = append(layersDir, layer.Path)
	}

	layersDir = append(layersDir, path.Join(launcher.config.WorkingDir, hostFSWiteoutsDir), "/")

	rootfsDir := filepath.Join(instance.runtimeDir, instanceRootFS)

	if err = os.MkdirAll(rootfsDir, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = MountFunc(rootfsDir, layersDir, "", ""); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func getMountPermissions(mount runtimespec.Mount) (permissions uint64, err error) {
	for _, option := range mount.Options {
		nameValue := strings.Split(strings.TrimSpace(option), "=")

		if len(nameValue) > 1 && nameValue[0] == "mode" {
			if permissions, err = strconv.ParseUint(nameValue[1], 8, 32); err != nil {
				return 0, aoserrors.Wrap(err)
			}
		}
	}

	return permissions, nil
}

func createMountPoint(path string, mount runtimespec.Mount, isDir bool) error {
	permissions, err := getMountPermissions(mount)
	if err != nil {
		return err
	}

	mountPoint := filepath.Join(path, mount.Destination)

	if isDir {
		if err := os.MkdirAll(mountPoint, 0o755); err != nil {
			return aoserrors.Wrap(err)
		}
	} else {
		if err = os.MkdirAll(filepath.Dir(mountPoint), 0o755); err != nil {
			return aoserrors.Wrap(err)
		}

		file, err := os.OpenFile(mountPoint, os.O_CREATE, 0o644)
		if err != nil {
			return aoserrors.Wrap(err)
		}
		defer file.Close()
	}

	if permissions != 0 {
		if err := os.Chmod(mountPoint, os.FileMode(permissions)); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (launcher *Launcher) createMountPoints(path string, mounts []runtimespec.Mount) error {
	for _, mount := range mounts {
		switch mount.Type {
		case "proc", "tmpfs", "sysfs":
			if err := createMountPoint(path, mount, true); err != nil {
				return err
			}

		case "bind":
			stat, err := os.Stat(mount.Source)
			if err != nil {
				return aoserrors.Wrap(err)
			}

			if err := createMountPoint(path, mount, stat.IsDir()); err != nil {
				return err
			}
		}
	}

	return nil
}

func (launcher *Launcher) restartStoredInstances() error {
	log.Debug("Restart stored instances")

	currentInstances, err := launcher.storage.GetAllInstances()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	launcher.cacheCurrentServices(currentInstances)

	launcher.runMutex.Lock()

	for _, currentInstance := range currentInstances {
		instance := newRuntimeInstanceInfo(currentInstance)

		launcher.currentInstances[currentInstance.InstanceID] = instance

		service, err := launcher.getCurrentServiceInfo(instance.ServiceID)
		if err != nil {
			log.WithFields(instanceLogFields(instance, nil)).Errorf("Instance failed: %v", err)

			instance.runStatus.State = cloudprotocol.InstanceStateFailed
			instance.runStatus.Err = err

			continue
		}

		instance.service = service
	}

	launcher.runMutex.Unlock()

	launcher.stopCurrentInstances()

	return launcher.runInstances(currentInstances)
}

func (launcher *Launcher) getRunningInstances(instances []aostypes.InstanceInfo) []InstanceInfo {
	var runningInstances []InstanceInfo

	curInstances, err := launcher.storage.GetAllInstances()
	if err != nil {
		log.Errorf("Can't get all instances: %v", err)
	}

newInstancesLoop:
	for _, newInstance := range instances {
		for i, curInstance := range curInstances {
			if curInstance.InstanceIdent == newInstance.InstanceIdent {
				// Update instance if parameters are changed
				if !instanceInfoEqual(curInstance.InstanceInfo, newInstance) {
					curInstance.InstanceInfo = newInstance

					if err := launcher.storage.UpdateInstance(curInstance); err != nil {
						log.Errorf("Can't update instance: %v", err)
					}
				}

				runningInstances = append(runningInstances, curInstance)
				curInstances = append(curInstances[:i], curInstances[i+1:]...)

				continue newInstancesLoop
			}
		}

		// Create new instance

		runInstance := InstanceInfo{
			InstanceInfo: newInstance,
			InstanceID:   uuid.New().String(),
		}

		if err := launcher.storage.AddInstance(runInstance); err != nil {
			log.Errorf("Can't add instance: %v", err)
		}

		runningInstances = append(runningInstances, runInstance)
	}

	// Remove old instances

	for _, curInstance := range curInstances {
		if err := launcher.storage.RemoveInstance(curInstance.InstanceID); err != nil {
			log.Errorf("Can't remove instance: %v", err)
		}
	}

	return runningInstances
}

func (launcher *Launcher) getOfflineTimeoutInstances() []*runtimeInstanceInfo {
	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	now := time.Now()
	instances := make([]*runtimeInstanceInfo, 0)

	for _, instance := range launcher.currentInstances {
		if instance.service.serviceConfig.OfflineTTL.Duration != 0 &&
			launcher.onlineTime.Add(instance.service.serviceConfig.OfflineTTL.Duration).Before(now) &&
			!errors.Is(instance.runStatus.Err, errOfflineTimeout) {
			instances = append(instances, instance)
		}
	}

	return instances
}

func (launcher *Launcher) setOfflineInstancesStatus(instances []*runtimeInstanceInfo) {
	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	updateInstancesStatus := &InstancesStatus{Instances: make([]cloudprotocol.InstanceStatus, 0, len(instances))}

	for _, instance := range instances {
		launcher.currentInstances[instance.InstanceID] = instance
		launcher.instanceFailed(instance, errOfflineTimeout)

		updateInstancesStatus.Instances = append(updateInstancesStatus.Instances, instance.getCloudStatus())
	}

	launcher.runtimeStatusChannel <- RuntimeStatus{UpdateStatus: updateInstancesStatus}
}

func (launcher *Launcher) updateOfflineTimeouts() {
	if launcher.isCloudOnline {
		launcher.onlineTime = time.Now()

		if err := launcher.storage.SetOnlineTime(launcher.onlineTime); err != nil {
			log.Errorf("Can't set online time: %v", err)
		}

		return
	}

	instances := launcher.getOfflineTimeoutInstances()
	if len(instances) == 0 {
		return
	}

	launcher.stopInstances(instances)
	launcher.setOfflineInstancesStatus(instances)
}

func deviceAllocateAlert(instance *runtimeInstanceInfo, device string, err error) cloudprotocol.AlertItem {
	return cloudprotocol.AlertItem{
		Timestamp: time.Now(),
		Tag:       cloudprotocol.AlertTagDeviceAllocate,
		Payload: cloudprotocol.DeviceAllocateAlert{
			InstanceIdent: instance.InstanceIdent,
			Device:        device,
			Message:       err.Error(),
		},
	}
}

func instanceInfoEqual(info1, info2 aostypes.InstanceInfo) bool {
	if info1.InstanceIdent != info2.InstanceIdent ||
		info1.NetworkParameters.Subnet != info2.NetworkParameters.Subnet ||
		info1.NetworkParameters.IP != info2.NetworkParameters.IP ||
		info1.NetworkParameters.VlanID != info2.NetworkParameters.VlanID ||
		info1.UID != info2.UID ||
		info1.Priority != info2.Priority ||
		info1.StoragePath != info2.StoragePath ||
		info1.StatePath != info2.StatePath {

		return false
	}

	return networkParametersEqual(info1.NetworkParameters, info2.NetworkParameters)
}

func networkParametersEqual(params1, params2 aostypes.NetworkParameters) bool {
	if len(params1.DNSServers) != len(params2.DNSServers) ||
		len(params1.FirewallRules) != len(params2.FirewallRules) {
		return false
	}

next:
	for _, dns1 := range params1.DNSServers {
		for _, dns2 := range params2.DNSServers {
			if dns1 == dns2 {
				continue next
			}
		}

		return false
	}

nextRule:
	for _, rule1 := range params1.FirewallRules {
		for _, rule2 := range params2.FirewallRules {
			if rule1 == rule2 {
				continue nextRule
			}
		}

		return false
	}

	return true
}
