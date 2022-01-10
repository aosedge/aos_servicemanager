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
	"sync"

	"github.com/aoscloud/aos_common/api/cloudprotocol"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/runner"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

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
	GetRunningInstances() ([]InstanceInfo, error)
	GetSubjectInstances(subjectID string) ([]InstanceInfo, error)
}

// ServiceProvider service provider.
type ServiceProvider interface {
	GetServiceInfo(serviceID string) (servicemanager.ServiceInfo, error)
}

// InstanceRunner interface to start/stop service instances.
type InstanceRunner interface {
	StartInstance(instanceID, runtimeDir string, params runner.StartInstanceParams) runner.InstanceStatus
	StopInstance(instanceID string) error
	InstanceStatusChannel() <-chan []runner.InstanceStatus
}

// InstanceInfo instance information.
type InstanceInfo struct {
	cloudprotocol.InstanceIdent
	InstanceID  string
	UnitSubject bool
	Running     bool
}

// RuntimeStatus runtime status info.
type RuntimeStatus struct {
	RunStatus    *RunInstancesStatus
	UpdateStatus *UpdateInstancesStatus
}

// RunInstancesStatus run instances status.
type RunInstancesStatus struct {
	UnitSubjects     []string
	Instances        []cloudprotocol.InstanceStatus
	RevertedServices []cloudprotocol.ServiceStatus
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

	runtimeStatusChannel chan RuntimeStatus
}

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new launcher object.
func New(config *config.Config, storage Storage, serviceProvider ServiceProvider,
	instanceRunner InstanceRunner,
) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	launcher = &Launcher{
		storage: storage, serviceProvider: serviceProvider, instanceRunner: instanceRunner,

		runtimeStatusChannel: make(chan RuntimeStatus, 1),
	}

	return launcher, nil
}

// Close closes launcher.
func (launcher *Launcher) Close() (err error) {
	launcher.Lock()
	defer launcher.Unlock()

	log.Debug("Close launcher")

	return nil
}

// SendCurrentRuntimeStatus forces launcher to send current runtime status.
func (launcher *Launcher) SendCurrentRuntimeStatus() error {
	launcher.Lock()
	defer launcher.Unlock()

	return nil
}

// SubjectsChanged notifies launcher that subjects are changed.
func (launcher *Launcher) SubjectsChanged(subjects []string) error {
	launcher.Lock()
	defer launcher.Unlock()

	return nil
}

// RunInstances runs desired services instances.
func (launcher *Launcher) RunInstances(instances []cloudprotocol.InstanceInfo) error {
	launcher.Lock()
	defer launcher.Unlock()

	return nil
}

// RestartInstances restarts all running instances.
func (launcher *Launcher) RestartInstances() error {
	launcher.Lock()
	defer launcher.Unlock()

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
