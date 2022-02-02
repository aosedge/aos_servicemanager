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
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/aoscloud/aos_common/utils/action"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/networkmanager"
	"github.com/aoscloud/aos_servicemanager/resourcemanager"
	"github.com/aoscloud/aos_servicemanager/runner"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
	"github.com/aoscloud/aos_servicemanager/utils/uidgidpool"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const maxParallelInstanceActions = 32

const (
	hostFSWiteoutsDir = "hostfs/whiteouts"
	runtimeConfigFile = "config.json"
	instanceRootFS    = "rootfs"
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
}

// ServiceProvider service provider.
type ServiceProvider interface {
	GetServiceInfo(serviceID string) (servicemanager.ServiceInfo, error)
	GetImageParts(service servicemanager.ServiceInfo) (servicemanager.ImageParts, error)
}

// InstanceRunner interface to start/stop service instances.
type InstanceRunner interface {
	StartInstance(instanceID, runtimeDir string, params runner.StartInstanceParams) runner.InstanceStatus
	StopInstance(instanceID string) error
	InstanceStatusChannel() <-chan []runner.InstanceStatus
}

// ResourceManager provides API to validate, request and release resources.
type ResourceManager interface {
	GetDeviceInfo(device string) (resourcemanager.DeviceInfo, error)
	GetResourceInfo(resource string) (resourcemanager.ResourceInfo, error)
}

// NetworkManager provides network access.
type NetworkManager interface {
	GetNetnsPath(instanceID string) string
	AddInstanceToNetwork(instanceID, networkID string, params networkmanager.NetworkParams) error
	RemoveInstanceFromNetwork(instanceID, networkID string) error
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

	storage         Storage
	serviceProvider ServiceProvider
	instanceRunner  InstanceRunner
	resourceManager ResourceManager
	networkManager  NetworkManager

	config                 *config.Config
	currentSubjects        []string
	runtimeStatusChannel   chan RuntimeStatus
	cancelFunction         context.CancelFunc
	actionHandler          *action.Handler
	runMutex               sync.Mutex
	runInstancesInProgress bool
	currentInstances       map[string]*instanceInfo
	currentServices        map[string]*serviceInfo
	uidPool                *uidgidpool.IdentifierPool
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var (
	// ErrNotExist not exist instance error.
	ErrNotExist = errors.New("instance not exist")
	// ErrNoRuntimeStatus no current runtime status error.
	ErrNoRuntimeStatus = errors.New("no runtime status")
)

var defaultHostFSBinds = []string{"bin", "sbin", "lib", "lib64", "usr"} // nolint:gochecknoglobals // const

// RuntimeDir specifies directory where instance runtime spec is stored.
// nolint:gochecknoglobals // used to be overridden in unit tests
var RuntimeDir = "/run/aos/runtime"

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new launcher object.
func New(config *config.Config, storage Storage, serviceProvider ServiceProvider, instanceRunner InstanceRunner,
	resourceManager ResourceManager, networkManager NetworkManager,
) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	launcher = &Launcher{
		storage: storage, serviceProvider: serviceProvider, instanceRunner: instanceRunner,
		resourceManager: resourceManager, networkManager: networkManager,

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

	runInstances := make([]InstanceInfo, 0, len(instances))

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

	return nil, nil
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

func (launcher *Launcher) stopInstance(instance *instanceInfo) (err error) {
	log.WithFields(instanceIdentLogFields(instance.InstanceIdent, nil)).Debug("Stop instance")

	if !instance.isStarted {
		return aoserrors.New("instance already stopped")
	}

	instance.isStarted = false

	if runnerErr := launcher.instanceRunner.StopInstance(instance.InstanceID); runnerErr != nil && err == nil {
		err = aoserrors.Wrap(runnerErr)
	}

	if removeErr := os.RemoveAll(filepath.Join(RuntimeDir, instance.InstanceID)); removeErr != nil && err == nil {
		err = aoserrors.Wrap(removeErr)
	}

	return err
}

func (launcher *Launcher) getStartInstances(runInstances []InstanceInfo) []*instanceInfo {
	startInstances := make([]*instanceInfo, 0, len(runInstances))

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

func (launcher *Launcher) startInstance(instance *instanceInfo) (err error) {
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

	if _, err := launcher.createRuntimeSpec(instance); err != nil {
		return err
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
			UnitSubjects: launcher.currentSubjects,
			Instances:    runInstancesStatuses,
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
