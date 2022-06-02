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
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/aoscloud/aos_common/resourcemonitor"
	"github.com/aoscloud/aos_common/utils/action"
	"github.com/aoscloud/aos_common/utils/fs"
	"github.com/google/uuid"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/layermanager"
	"github.com/aoscloud/aos_servicemanager/networkmanager"
	"github.com/aoscloud/aos_servicemanager/resourcemanager"
	"github.com/aoscloud/aos_servicemanager/runner"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
	"github.com/aoscloud/aos_servicemanager/storagestate"
	"github.com/aoscloud/aos_servicemanager/utils/uidgidpool"
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
	upperDirName           = "upperdir"
	workDirName            = "workdir"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// Storage storage interface.
type Storage interface {
	AddInstance(instance InstanceInfo) error
	UpdateInstance(instance InstanceInfo) error
	RemoveInstance(instanceID string) error
	GetInstanceByIdent(instanceIdent cloudprotocol.InstanceIdent) (InstanceInfo, error)
	GetInstanceByID(instanceID string) (InstanceInfo, error)
	GetAllInstances() ([]InstanceInfo, error)
	GetRunningInstances() ([]InstanceInfo, error)
	GetSubjectInstances(subjectID string) ([]InstanceInfo, error)
	GetOverrideEnvVars() ([]cloudprotocol.EnvVarsInstanceInfo, error)
	SetOverrideEnvVars(envVarsInfo []cloudprotocol.EnvVarsInstanceInfo) error
}

// ServiceProvider service provider.
type ServiceProvider interface {
	GetServiceInfo(serviceID string) (servicemanager.ServiceInfo, error)
	GetImageParts(service servicemanager.ServiceInfo) (servicemanager.ImageParts, error)
	ValidateService(service servicemanager.ServiceInfo) error
	ApplyService(service servicemanager.ServiceInfo) error
	RevertService(service servicemanager.ServiceInfo) error
	UseService(serviceID string, aosVersion uint64) error
}

// LayerProvider layer provider.
type LayerProvider interface {
	GetLayerInfoByDigest(digest string) (layermanager.LayerInfo, error)
}

// InstanceRunner interface to start/stop service instances.
type InstanceRunner interface {
	StartInstance(instanceID, runtimeDir string, params runner.StartInstanceParams) runner.InstanceStatus
	StopInstance(instanceID string) error
	InstanceStatusChannel() <-chan []runner.InstanceStatus
}

// ResourceManager provides API to validate, request and release resources.
type ResourceManager interface {
	GetDeviceInfo(device string) (aostypes.DeviceInfo, error)
	GetResourceInfo(resource string) (aostypes.ResourceInfo, error)
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
		instance cloudprotocol.InstanceIdent, permissions map[string]map[string]string,
	) (secret string, err error)
	UnregisterInstance(instance cloudprotocol.InstanceIdent) error
}

// StorageStateProvider provides API for instance storage/state.
type StorageStateProvider interface {
	Setup(
		instanceID string, params storagestate.SetupParams,
	) (storagePath, statePath string, stateChecksum []byte, err error)
	Cleanup(instanceID string) error
	Remove(instanceID string) error
	StateChangedChannel() <-chan storagestate.StateChangedInfo
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
	cloudprotocol.InstanceIdent
	AosVersion  uint64
	InstanceID  string
	UnitSubject bool
	Running     bool
	UID         int
}

// RuntimeStatus runtime status info.
type RuntimeStatus struct {
	RunStatus    *RunInstancesStatus
	UpdateStatus *UpdateInstancesStatus
}

// RunInstancesStatus run instances status.
type RunInstancesStatus struct {
	UnitSubjects  []string
	Instances     []cloudprotocol.InstanceStatus
	ErrorServices []cloudprotocol.ServiceStatus
}

// UpdateInstancesStatus update instances status.
type UpdateInstancesStatus struct {
	Instances []cloudprotocol.InstanceStatus
}

// Launcher launcher instance.
type Launcher struct {
	sync.Mutex

	storage              Storage
	serviceProvider      ServiceProvider
	layerProvider        LayerProvider
	instanceRunner       InstanceRunner
	resourceManager      ResourceManager
	networkManager       NetworkManager
	instanceRegistrar    InstanceRegistrar
	storageStateProvider StorageStateProvider
	instanceMonitor      InstanceMonitor
	alertSender          AlertSender

	config                 *config.Config
	currentSubjects        []string
	runtimeStatusChannel   chan RuntimeStatus
	cancelFunction         context.CancelFunc
	actionHandler          *action.Handler
	runMutex               sync.Mutex
	runInstancesInProgress bool
	currentInstances       map[string]*instanceInfo
	currentServices        map[string]*serviceInfo
	errorServices          []cloudprotocol.ServiceStatus
	uidPool                *uidgidpool.IdentifierPool
	currentEnvVars         []cloudprotocol.EnvVarsInstanceInfo
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

// OperationVersion defines current operation version
// IMPORTANT: if new functionality doesn't allow existing services to work
// properly, this value should be increased. It will force to remove all
// services and their storages before first start.
const OperationVersion = 8

// Mount, unmount instance FS functions.
// nolint:gochecknoglobals
var (
	MountFunc   = fs.OverlayMount
	UnmountFunc = fs.Umount
)

var (
	// ErrNotExist not exist instance error.
	ErrNotExist = errors.New("instance not exist")
	// ErrNoRuntimeStatus no current runtime status error.
	ErrNoRuntimeStatus = errors.New("no runtime status")
)

var defaultHostFSBinds = []string{"bin", "sbin", "lib", "lib64", "usr"} // nolint:gochecknoglobals // const

// nolint:gochecknoglobals // used to be overridden in unit tests
var (
	// RuntimeDir specifies directory where instance runtime spec is stored.
	RuntimeDir = "/run/aos/runtime"
	// CheckTLLsPeriod specified period different TTL timers are checked with.
	CheckTLLsPeriod = 1 * time.Hour
)

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new launcher object.
func New(config *config.Config, storage Storage, serviceProvider ServiceProvider, layerProvider LayerProvider,
	instanceRunner InstanceRunner, resourceManager ResourceManager, networkManager NetworkManager,
	instanceRegistrar InstanceRegistrar, storageStateProvider StorageStateProvider, instanceMonitor InstanceMonitor,
	alertSender AlertSender,
) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	launcher = &Launcher{
		storage: storage, serviceProvider: serviceProvider, layerProvider: layerProvider,
		instanceRunner: instanceRunner, resourceManager: resourceManager, networkManager: networkManager,
		instanceRegistrar: instanceRegistrar, storageStateProvider: storageStateProvider,
		instanceMonitor: instanceMonitor, alertSender: alertSender,

		config:               config,
		actionHandler:        action.New(maxParallelInstanceActions),
		runtimeStatusChannel: make(chan RuntimeStatus, 1),
		uidPool:              uidgidpool.NewUserIDPool(),
	}

	launcher.fillUIDPool()

	ctx, cancelFunction := context.WithCancel(context.Background())

	launcher.cancelFunction = cancelFunction

	go launcher.handleChannels(ctx)

	if err = launcher.prepareHostFSDir(); err != nil {
		return nil, err
	}

	if err = os.MkdirAll(RuntimeDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if launcher.currentEnvVars, err = launcher.storage.GetOverrideEnvVars(); err != nil {
		log.Errorf("Can't get current env vars: %v", err)
	}

	// Stop all running instances in case some of them still running on SM start. It could be if SM crashes, etc.
	if err = launcher.stopRunningInstances(); err != nil {
		log.Errorf("Stop running instances error: %v", err)
	}

	if err = launcher.removeOutdatedInstances(); err != nil {
		log.Errorf("Can't remove outdated instances: %v", err)
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

// SendCurrentRuntimeStatus forces launcher to send current runtime status.
func (launcher *Launcher) SendCurrentRuntimeStatus() error {
	launcher.Lock()
	defer launcher.Unlock()

	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	// Send current run status only if it is available
	if launcher.currentInstances == nil {
		return ErrNoRuntimeStatus
	}

	launcher.sendRunInstancesStatuses()

	return nil
}

// SubjectsChanged notifies launcher that subjects are changed.
func (launcher *Launcher) SubjectsChanged(subjects []string) error {
	launcher.Lock()
	defer launcher.Unlock()

	if subjects == nil {
		subjects = make([]string, 0)
	}

	if isSubjectsEqual(launcher.currentSubjects, subjects) {
		return nil
	}

	log.WithField("subjects", subjects).Info("Subjects changed")

	launcher.currentSubjects = subjects

	runInstances, err := launcher.storage.GetRunningInstances()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	// Remove unit subjects instances
	i := 0

	for _, instance := range runInstances {
		if !instance.UnitSubject {
			runInstances[i] = instance
			i++
		}
	}

	runInstances = runInstances[:i]

	for _, subject := range subjects {
		subjectInstances, err := launcher.storage.GetSubjectInstances(subject)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		runInstances = append(runInstances, subjectInstances...)
	}

	if err := launcher.updateRunningFlags(runInstances); err != nil {
		return err
	}

	launcher.runInstances(runInstances)

	return nil
}

// RunInstances runs desired services instances.
func (launcher *Launcher) RunInstances(instances []cloudprotocol.InstanceInfo) error {
	launcher.Lock()
	defer launcher.Unlock()

	log.Debug("Run instances")

	var runInstances []InstanceInfo

	// Convert cloudprotocol InstanceInfo to internal InstanceInfo
	for _, item := range instances {
		for i := uint64(0); i < item.NumInstances; i++ {
			// Get instance from current map. If not available, get it from storage. Otherwise, generate new instance.
			instanceIdent := cloudprotocol.InstanceIdent{
				ServiceID: item.ServiceID,
				SubjectID: item.SubjectID,
				Instance:  i,
			}

			instance, err := launcher.getCurrentInstance(instanceIdent)
			if err != nil {
				if instance, err = launcher.storage.GetInstanceByIdent(instanceIdent); err != nil {
					if instance, err = launcher.createNewInstance(instanceIdent); err != nil {
						return err
					}
				}
			}

			instance.Running = true

			if err := launcher.storage.UpdateInstance(instance); err != nil {
				return aoserrors.Wrap(err)
			}

			runInstances = append(runInstances, instance)
		}
	}

	if err := launcher.updateRunningFlags(runInstances); err != nil {
		return err
	}

	launcher.runInstances(runInstances)

	return nil
}

// RestartInstances restarts all running instances.
func (launcher *Launcher) RestartInstances() error {
	launcher.Lock()
	defer launcher.Unlock()

	launcher.stopCurrentInstances()

	runInstances := make([]InstanceInfo, 0, len(launcher.currentInstances))

	for _, instance := range launcher.currentInstances {
		runInstances = append(runInstances, instance.InstanceInfo)
	}

	launcher.runInstances(runInstances)

	return nil
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

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (launcher *Launcher) fillUIDPool() {
	instances, err := launcher.storage.GetAllInstances()
	if err != nil {
		log.Errorf("Can't fill UID pool: %v", err)
	}

	for _, instance := range instances {
		if err = launcher.uidPool.AddID(instance.UID); err != nil {
			log.WithFields(instanceIdentLogFields(instance.InstanceIdent, nil)).Errorf("Can't add UID to pool: %v", err)
		}
	}
}

func (launcher *Launcher) handleChannels(ctx context.Context) {
	for {
		select {
		case instances := <-launcher.instanceRunner.InstanceStatusChannel():
			launcher.updateInstancesStatuses(instances)

		case stateChangedInfo := <-launcher.storageStateProvider.StateChangedChannel():
			launcher.updateInstanceState(stateChangedInfo)

		case <-time.After(CheckTLLsPeriod):
			launcher.Lock()
			launcher.updateInstancesEnvVars()
			launcher.Unlock()

		case <-ctx.Done():
			return
		}
	}
}

func (launcher *Launcher) updateInstancesStatuses(instances []runner.InstanceStatus) {
	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	updateInstancesStatus := &UpdateInstancesStatus{Instances: make([]cloudprotocol.InstanceStatus, 0, len(instances))}

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

func (launcher *Launcher) updateInstanceState(stateChangedIngo storagestate.StateChangedInfo) {
	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	instance, ok := launcher.currentInstances[stateChangedIngo.InstanceID]
	if !ok {
		log.WithField("instanceID", stateChangedIngo.InstanceID).Errorf("Unknown instance state changed")

		return
	}

	if !bytes.Equal(stateChangedIngo.Checksum, instance.stateChecksum) {
		log.WithFields(log.Fields{
			"instanceID": stateChangedIngo.InstanceID,
			"checksum":   hex.EncodeToString(stateChangedIngo.Checksum),
		}).Debugf("Instance state changed")

		instance.stateChecksum = stateChangedIngo.Checksum
	}
}

func (launcher *Launcher) createNewInstance(instanceIdent cloudprotocol.InstanceIdent) (InstanceInfo, error) {
	instance := InstanceInfo{
		InstanceIdent: instanceIdent,
		InstanceID:    uuid.New().String(),
		UnitSubject:   launcher.isCurrentSubject(instanceIdent.SubjectID),
	}

	uid, err := launcher.uidPool.GetFreeID()
	if err != nil {
		return instance, aoserrors.Wrap(err)
	}

	instance.UID = uid

	if err := launcher.storage.AddInstance(instance); err != nil {
		return instance, aoserrors.Wrap(err)
	}

	return instance, nil
}

func (launcher *Launcher) updateRunningFlags(runInstances []InstanceInfo) error {
	// Clear running flag for not running instances
	currentRunInstances, err := launcher.storage.GetRunningInstances()
	if err != nil {
		return aoserrors.Wrap(err)
	}

currentLoop:
	for _, currentInstance := range currentRunInstances {
		for _, runInstance := range runInstances {
			if currentInstance.InstanceIdent == runInstance.InstanceIdent {
				continue currentLoop
			}
		}

		currentInstance.Running = false

		if err := launcher.storage.UpdateInstance(currentInstance); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	// Set running flag for running instances
	for _, runInstance := range runInstances {
		if !runInstance.Running {
			runInstance.Running = true

			if err := launcher.storage.UpdateInstance(runInstance); err != nil {
				return aoserrors.Wrap(err)
			}
		}
	}

	return nil
}

func (launcher *Launcher) runInstances(runInstances []InstanceInfo) {
	defer func() {
		launcher.runMutex.Lock()
		defer launcher.runMutex.Unlock()

		launcher.runInstancesInProgress = false
		launcher.sendRunInstancesStatuses()
	}()

	launcher.runMutex.Lock()

	launcher.runInstancesInProgress = true

	if launcher.currentInstances == nil {
		launcher.currentInstances = make(map[string]*instanceInfo)
	}

	launcher.cacheCurrentServices(runInstances)

	stopInstances := launcher.getStopInstances(runInstances)
	startInstances := launcher.getStartInstances(runInstances)

	launcher.runMutex.Unlock()

	launcher.stopInstances(stopInstances)
	launcher.startInstances(startInstances)

	launcher.checkNewServices()
}

func (launcher *Launcher) checkNewServices() {
	launcher.errorServices = nil

serviceLoop:
	for _, service := range launcher.currentServices {
		if service.IsActive {
			continue
		}

		for _, instance := range launcher.currentInstances {
			if instance.ServiceID == service.ServiceID &&
				instance.runStatus.State == cloudprotocol.InstanceStateActive {
				log.WithFields(log.Fields{"serviceID": service.ServiceID}).Debug("Apply new service")

				if err := launcher.serviceProvider.ApplyService(service.ServiceInfo); err != nil {
					log.WithField("serviceID", service.ServiceID).Errorf("Can't apply service: %v", err)

					launcher.errorServices = append(launcher.errorServices,
						service.cloudStatus(cloudprotocol.ErrorStatus, err))
				}

				continue serviceLoop
			}
		}

		log.WithFields(log.Fields{"serviceID": service.ServiceID}).Warn("Revert new service")

		if err := launcher.serviceProvider.RevertService(service.ServiceInfo); err != nil {
			log.WithField("serviceID", service.ServiceID).Errorf("Can't revert service: %v", err)

			launcher.errorServices = append(launcher.errorServices,
				service.cloudStatus(cloudprotocol.ErrorStatus, err))

			continue
		}

		launcher.errorServices = append(launcher.errorServices,
			service.cloudStatus(cloudprotocol.ErrorStatus, aoserrors.New("can't start any instances")))
	}
}

func (launcher *Launcher) getStopInstances(runInstances []InstanceInfo) []*instanceInfo {
	var stopInstances []*instanceInfo

stopLoop:
	for _, currentInstance := range launcher.currentInstances {
		for _, instance := range runInstances {
			if instance.InstanceID == currentInstance.InstanceID && currentInstance.service != nil &&
				currentInstance.service.AosVersion == launcher.currentServices[currentInstance.ServiceID].AosVersion {
				continue stopLoop
			}
		}

		delete(launcher.currentInstances, currentInstance.InstanceID)
		stopInstances = append(stopInstances, currentInstance)
	}

	return stopInstances
}

func (launcher *Launcher) stopInstances(instances []*instanceInfo) {
	for _, instance := range instances {
		if instance.isStarted {
			launcher.doStopAction(instance)
		}
	}

	launcher.actionHandler.Wait()
}

func (launcher *Launcher) doStopAction(instance *instanceInfo) {
	launcher.actionHandler.Execute(instance.InstanceID, func(instanceID string) (err error) {
		defer func() {
			if err != nil {
				log.WithFields(
					instanceIdentLogFields(instance.InstanceIdent, nil),
				).Errorf("Can't stop instance: %v", err)

				return
			}

			log.WithFields(instanceIdentLogFields(instance.InstanceIdent, nil)).Info("Instance successfully stopped")
		}()

		if stopErr := launcher.stopInstance(instance); stopErr != nil && err == nil {
			err = stopErr
		}

		return err
	})
}

func (launcher *Launcher) releaseRuntime(instance *instanceInfo) (err error) {
	if instance.service.serviceConfig.Permissions != nil {
		if registerErr := launcher.instanceRegistrar.UnregisterInstance(
			instance.InstanceIdent); registerErr != nil && err == nil {
			err = aoserrors.Wrap(registerErr)
		}
	}

	if networkErr := launcher.networkManager.RemoveInstanceFromNetwork(
		instance.InstanceID, instance.service.ServiceProvider); networkErr != nil && err == nil {
		err = aoserrors.Wrap(networkErr)
	}

	if deviceErr := launcher.resourceManager.ReleaseDevices(instance.InstanceID); deviceErr != nil && err == nil {
		err = aoserrors.Wrap(deviceErr)
	}

	if storageStateErr := launcher.storageStateProvider.Cleanup(
		instance.InstanceID); storageStateErr != nil && err == nil {
		err = aoserrors.Wrap(storageStateErr)
	}

	return err
}

func (launcher *Launcher) stopInstance(instance *instanceInfo) (err error) {
	log.WithFields(instanceIdentLogFields(instance.InstanceIdent, nil)).Debug("Stop instance")

	if !instance.isStarted {
		return aoserrors.New("instance already stopped")
	}

	instance.isStarted = false

	if instance.service == nil {
		return err
	}

	if runnerErr := launcher.instanceRunner.StopInstance(instance.InstanceID); runnerErr != nil && err == nil {
		err = aoserrors.Wrap(runnerErr)
	}

	if releaseErr := launcher.releaseRuntime(instance); releaseErr != nil && err == nil {
		err = releaseErr
	}

	if unmountErr := UnmountFunc(filepath.Join(instance.runtimeDir, instanceRootFS)); unmountErr != nil && err == nil {
		err = aoserrors.Wrap(unmountErr)
	}

	if removeErr := os.RemoveAll(filepath.Join(RuntimeDir, instance.InstanceID)); removeErr != nil && err == nil {
		err = aoserrors.Wrap(removeErr)
	}

	if monitorErr := launcher.instanceMonitor.StopInstanceMonitor(
		instance.InstanceID); monitorErr != nil && err == nil {
		err = aoserrors.Wrap(monitorErr)
	}

	return err
}

func (launcher *Launcher) getStartInstances(runInstances []InstanceInfo) []*instanceInfo {
	var startInstances []*instanceInfo // nolint:prealloc // size of startInstances is not determined

	for _, instance := range runInstances {
		startInstance, ok := launcher.currentInstances[instance.InstanceID]
		if ok && startInstance.isStarted {
			continue
		}

		if !ok {
			startInstance = &instanceInfo{
				InstanceInfo: instance,
				runtimeDir:   filepath.Join(RuntimeDir, instance.InstanceID),
			}

			launcher.currentInstances[instance.InstanceID] = startInstance
		}

		service, err := launcher.getCurrentServiceInfo(instance.ServiceID)
		if err != nil {
			launcher.instanceFailed(startInstance, err)

			continue
		}

		if startInstance.AosVersion != service.AosVersion {
			startInstance.AosVersion = service.AosVersion

			if err = launcher.storage.UpdateInstance(startInstance.InstanceInfo); err != nil {
				launcher.instanceFailed(startInstance, err)

				continue
			}
		}

		startInstance.service = service
		startInstances = append(startInstances, startInstance)
	}

	return startInstances
}

func (launcher *Launcher) startInstances(instances []*instanceInfo) {
	sort.Sort(byPriority(instances))

	var conflictInstances []*instanceInfo

	// Allocate devices

	i := 0

	for _, instance := range instances {
		currentConflictInstances, err := launcher.allocateDevices(instance)
		if err != nil {
			launcher.instanceFailed(instance, err)

			continue
		}

		instances[i] = instance
		i++

		conflictInstances = appendInstances(conflictInstances, currentConflictInstances...)
	}

	instances = instances[:i]

	for _, instance := range conflictInstances {
		log.WithFields(
			instanceIdentLogFields(instance.InstanceIdent, nil),
		).Debug("Release instance due to device conflicts")

		launcher.doStopAction(instance)
	}

	launcher.actionHandler.Wait()

	for _, instance := range conflictInstances {
		launcher.instanceFailed(instance, aoserrors.Wrap(resourcemanager.ErrNoAvailableDevice))
	}

	// Start instances
	for _, instance := range instances {
		launcher.doStartAction(instance)
	}

	launcher.actionHandler.Wait()
}

func (launcher *Launcher) doStartAction(instance *instanceInfo) {
	launcher.actionHandler.Execute(instance.InstanceID, func(instanceID string) (err error) {
		defer func() {
			if err != nil {
				launcher.instanceFailed(instance, err)
			}
		}()

		if err = launcher.startInstance(instance); err != nil {
			return err
		}

		return nil
	})
}

func (launcher *Launcher) getHostsFromResources(resources []string) (hosts []aostypes.Host, err error) {
	for _, resource := range resources {
		boardResource, err := launcher.resourceManager.GetResourceInfo(resource)
		if err != nil {
			return hosts, aoserrors.Wrap(err)
		}

		hosts = append(hosts, boardResource.Hosts...)
	}

	return hosts, nil
}

func (launcher *Launcher) setupNetwork(instance *instanceInfo) (err error) {
	networkFilesDir := filepath.Join(instance.runtimeDir, instanceMountPointsDir)

	if err = os.MkdirAll(networkFilesDir, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	params := networkmanager.NetworkParams{
		InstanceIdent:      instance.InstanceIdent,
		HostsFilePath:      filepath.Join(networkFilesDir, "etc", "hosts"),
		ResolvConfFilePath: filepath.Join(networkFilesDir, "etc", "resolv.conf"),
	}

	if params.Hosts, err = launcher.getHostsFromResources(instance.service.serviceConfig.Resources); err != nil {
		return err
	}

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

	params.AllowedConnections = make([]string, 0, len(instance.service.serviceConfig.AllowedConnections))

	for key := range instance.service.serviceConfig.AllowedConnections {
		params.AllowedConnections = append(params.AllowedConnections, key)
	}

	if err := launcher.networkManager.AddInstanceToNetwork(
		instance.InstanceID, instance.service.ServiceProvider, params); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) getLowPriorityInstance(instance *instanceInfo, deviceName string) (*instanceInfo, error) {
	deviceInstances, err := launcher.resourceManager.GetDeviceInstances(deviceName)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	checkInstances := make([]*instanceInfo, len(deviceInstances)+1)

	for i, instanceID := range deviceInstances {
		currentInstance, ok := launcher.currentInstances[instanceID]
		if !ok {
			return nil, aoserrors.Errorf("can't get allocated device instance: %s", instanceID)
		}

		checkInstances[i] = currentInstance
	}

	checkInstances[len(deviceInstances)] = instance

	sort.Sort(byPriority(checkInstances))

	lowPriorityInstance := checkInstances[len(checkInstances)-1]

	if lowPriorityInstance == instance {
		return nil, aoserrors.Wrap(resourcemanager.ErrNoAvailableDevice)
	}

	return lowPriorityInstance, nil
}

func (launcher *Launcher) allocateDevices(instance *instanceInfo) (conflictInstances []*instanceInfo, err error) {
	releasedDevices := make(map[string]*instanceInfo)

	defer func() {
		if err != nil {
			launcher.revertDeviceAllocation(instance, releasedDevices)
		}
	}()

	for _, device := range instance.service.serviceConfig.Devices {
		if err = launcher.resourceManager.AllocateDevice(device.Name, instance.InstanceID); err != nil {
			if !errors.Is(err, resourcemanager.ErrNoAvailableDevice) {
				return nil, aoserrors.Wrap(err)
			}

			lowPriorityInstance, err := launcher.getLowPriorityInstance(instance, device.Name)
			if err != nil {
				if errors.Is(err, resourcemanager.ErrNoAvailableDevice) {
					launcher.alertSender.SendAlert(deviceAllocateAlert(instance, device.Name, err))
				}

				return nil, err
			}

			if err = launcher.resourceManager.ReleaseDevice(device.Name, lowPriorityInstance.InstanceID); err != nil {
				return nil, aoserrors.Wrap(err)
			}

			releasedDevices[device.Name] = lowPriorityInstance
			conflictInstances = appendInstances(conflictInstances, lowPriorityInstance)

			if err = launcher.resourceManager.AllocateDevice(device.Name, instance.InstanceID); err != nil {
				return nil, aoserrors.Wrap(err)
			}
		}
	}

	for device, instance := range releasedDevices {
		launcher.alertSender.SendAlert(deviceAllocateAlert(instance, device, resourcemanager.ErrNoAvailableDevice))
	}

	return conflictInstances, nil
}

func (launcher *Launcher) revertDeviceAllocation(instance *instanceInfo, releasedDevices map[string]*instanceInfo) {
	if err := launcher.resourceManager.ReleaseDevices(instance.InstanceID); err != nil {
		log.WithFields(
			instanceIdentLogFields(instance.InstanceIdent, nil),
		).Errorf("Can't release instance devices: %v", err)
	}

	// Allocate back released devices
	for device, releasedInstance := range releasedDevices {
		if err := launcher.resourceManager.AllocateDevice(device, releasedInstance.InstanceID); err != nil {
			log.WithFields(
				instanceIdentLogFields(instance.InstanceIdent, nil),
			).Errorf("Can't allocate device %s: %v", device, err)
		}
	}
}

func (launcher *Launcher) setupRuntime(instance *instanceInfo) error {
	if instance.service.serviceConfig.Permissions != nil {
		secret, err := launcher.instanceRegistrar.RegisterInstance(
			instance.InstanceIdent, instance.service.serviceConfig.Permissions)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		instance.secret = secret
	}

	var (
		storageLimit uint64
		stateLimit   uint64
	)

	if instance.service.serviceConfig.Quotas.StorageLimit != nil {
		storageLimit = *instance.service.serviceConfig.Quotas.StorageLimit
	}

	if instance.service.serviceConfig.Quotas.StateLimit != nil {
		stateLimit = *instance.service.serviceConfig.Quotas.StateLimit
	}

	storagePath, statePath, stateChecksum, err := launcher.storageStateProvider.Setup(instance.InstanceID,
		storagestate.SetupParams{
			InstanceIdent: instance.InstanceIdent,
			UID:           instance.UID, GID: instance.service.GID,
			StorageQuota: storageLimit, StateQuota: stateLimit,
		})
	if err != nil {
		return aoserrors.Wrap(err)
	}

	instance.storagePath = storagePath
	instance.statePath = statePath

	// State checksum can be changed in state changed handler
	launcher.runMutex.Lock()
	instance.stateChecksum = stateChecksum
	launcher.runMutex.Unlock()

	if err = launcher.setupNetwork(instance); err != nil {
		return err
	}

	return nil
}

func (launcher *Launcher) startInstance(instance *instanceInfo) error {
	log.WithFields(instanceIdentLogFields(instance.InstanceIdent, nil)).Debug("Start instance")

	if instance.isStarted {
		return aoserrors.New("instance already started")
	}

	// clear run status
	launcher.runMutex.Lock()
	instance.runStatus = runner.InstanceStatus{}
	launcher.runMutex.Unlock()

	instance.isStarted = true

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

	if err := launcher.instanceMonitor.StartInstanceMonitor(
		instance.InstanceID, resourcemonitor.ResourceMonitorParams{
			InstanceIdent: instance.InstanceIdent,
			UID:           instance.UID,
			GID:           instance.service.GID,
			AlertRules:    instance.service.serviceConfig.AlertRules,
		}); err != nil {
		log.WithFields(
			instanceIdentLogFields(instance.InstanceIdent, nil),
		).Errorf("Can't start instance monitoring: %v", err)
	}

	runStatus := launcher.instanceRunner.StartInstance(
		instance.InstanceID, instance.runtimeDir, runner.StartInstanceParams{})

	// Update current status if it is not updated by runner status channel. Instance runner status goes asynchronously
	// by status channel. And therefore, new status may arrive before returning by StartInstance API. We detect this
	// situation by checking if run state is not empty value.
	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	if instance.runStatus.State == "" {
		instance.setRunStatus(runStatus)
	}

	return nil
}

func (launcher *Launcher) sendRunInstancesStatuses() {
	runInstancesStatuses := make([]cloudprotocol.InstanceStatus, 0, len(launcher.currentInstances))

	for _, currentInstance := range launcher.currentInstances {
		runInstancesStatuses = append(runInstancesStatuses, currentInstance.getCloudStatus())
	}

	launcher.runtimeStatusChannel <- RuntimeStatus{
		RunStatus: &RunInstancesStatus{
			UnitSubjects:  launcher.currentSubjects,
			Instances:     runInstancesStatuses,
			ErrorServices: launcher.errorServices,
		},
	}
}

func (launcher *Launcher) isCurrentSubject(subject string) bool {
	for _, currentSubject := range launcher.currentSubjects {
		if subject == currentSubject {
			return true
		}
	}

	return false
}

func isSubjectsEqual(subjects1, subjects2 []string) bool {
	if subjects1 == nil && subjects2 == nil {
		return true
	}

	if subjects1 == nil || subjects2 == nil {
		return false
	}

	if len(subjects1) != len(subjects2) {
		return false
	}

	sort.Strings(subjects1)
	sort.Strings(subjects2)

	for i := range subjects1 {
		if subjects1[i] != subjects2[i] {
			return false
		}
	}

	return true
}

func (launcher *Launcher) stopCurrentInstances() {
	stopInstances := make([]*instanceInfo, 0, len(launcher.currentInstances))

	for _, instance := range launcher.currentInstances {
		stopInstances = append(stopInstances, instance)
	}

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

	rootContent, err := ioutil.ReadDir("/")
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

func prepareStorageDir(path string, uid, gid int) (upperDir, workDir string, err error) {
	upperDir = filepath.Join(path, upperDirName)
	workDir = filepath.Join(path, workDirName)

	if err = os.MkdirAll(upperDir, 0o755); err != nil {
		return "", "", aoserrors.Wrap(err)
	}

	if err = os.Chown(upperDir, uid, gid); err != nil {
		return "", "", aoserrors.Wrap(err)
	}

	if err = os.MkdirAll(workDir, 0o755); err != nil {
		return "", "", aoserrors.Wrap(err)
	}

	if err = os.Chown(workDir, uid, gid); err != nil {
		return "", "", aoserrors.Wrap(err)
	}

	return upperDir, workDir, nil
}

func (launcher *Launcher) prepareRootFS(instance *instanceInfo, runtimeConfig *runtimeSpec) error {
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

	var upperDir, workDir string

	if instance.storagePath != "" {
		if upperDir, workDir, err = prepareStorageDir(
			instance.storagePath, instance.UID, instance.service.GID); err != nil {
			return err
		}
	}

	if err = MountFunc(rootfsDir, layersDir, workDir, upperDir); err != nil {
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

func (launcher *Launcher) stopRunningInstances() error {
	log.Debug("Stop running instances")

	runningInstances, err := launcher.storage.GetRunningInstances()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	launcher.cacheCurrentServices(runningInstances)

	var stopInstances []*instanceInfo // nolint:prealloc // stopInstances size is not determined

	for _, runningInstance := range runningInstances {
		service, err := launcher.getCurrentServiceInfo(runningInstance.ServiceID)
		if err != nil {
			log.WithFields(
				instanceIdentLogFields(runningInstance.InstanceIdent, nil),
			).Errorf("Can't get service info: %v", err)

			continue
		}

		stopInstances = append(stopInstances, &instanceInfo{
			InstanceInfo: runningInstance,
			isStarted:    true,
			service:      service,
		})
	}

	launcher.stopInstances(stopInstances)

	return nil
}

func (launcher *Launcher) removeOutdatedInstances() error {
	log.Debug("Remove outdated instances")

	instances, err := launcher.storage.GetAllInstances()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	launcher.cacheCurrentServices(instances)

	for _, instance := range instances {
		if _, serviceErr := launcher.getCurrentServiceInfo(instance.ServiceID); serviceErr != nil {
			if errors.Is(serviceErr, servicemanager.ErrNotExist) {
				if removeErr := launcher.removeInstance(instance); removeErr != nil {
					if err == nil {
						err = removeErr
					}

					log.WithFields(
						instanceIdentLogFields(instance.InstanceIdent, nil),
					).Errorf("Can't remove outdated instance: %v", removeErr)
				}

				continue
			}

			log.WithFields(
				instanceIdentLogFields(instance.InstanceIdent, nil),
			).Errorf("Instance service error: %v", err)
		}
	}

	return err
}

func (launcher *Launcher) removeInstance(instance InstanceInfo) (err error) {
	log.WithFields(instanceIdentLogFields(instance.InstanceIdent, nil)).Debug("Remove outdated instance")

	if storageStateErr := launcher.storageStateProvider.Remove(
		instance.InstanceID); storageStateErr != nil && err == nil {
		err = storageStateErr
	}

	if removeErr := launcher.storage.RemoveInstance(instance.InstanceID); removeErr != nil && err == nil {
		err = removeErr
	}

	return err
}

func deviceAllocateAlert(instance *instanceInfo, device string, err error) cloudprotocol.AlertItem {
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
