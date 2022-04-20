// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2022 Renesas Electronics Corporation.
// Copyright (C) 2022 EPAM Systems, Inc.
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

package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/coreos/go-systemd/v22/dbus"
	log "github.com/sirupsen/logrus"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const unitStatusChannelSize = 10

const (
	defaultStartTimeout    = 30
	startTimeoutMultiplier = 2
)

const systemdUnitNameTamplate = "aos-service@%s.service"

const (
	errNotLoaded = "not loaded"
	jobStateDone = "done"
)

/***********************************************************************************************************************
  Types
 **********************************************************************************************************************/

// StartInstanceParams start instance parameters.
type StartInstanceParams struct {
	StartInterval   time.Duration
	StartBurst      uint
	RestartInterval time.Duration
}

// InstanceStatus service instance status.
type InstanceStatus struct {
	InstanceID string
	State      string
	Err        error
	ExitCode   int
}

// Runner runner instance.
type Runner struct {
	sync.RWMutex
	systemd            *dbus.Conn
	instanceStatusChan chan []InstanceStatus
	runningUnits       map[string]chan dbus.UnitStatus
	stopChan           chan struct{}
}

/***********************************************************************************************************************
  Public
 **********************************************************************************************************************/

// New creates new systemd runner.
func New() (runner *Runner, err error) {
	runner = &Runner{
		instanceStatusChan: make(chan []InstanceStatus, unitStatusChannelSize),
		runningUnits:       make(map[string]chan dbus.UnitStatus),
		stopChan:           make(chan struct{}, 1),
	}

	// Create systemd connection
	if runner.systemd, err = dbus.NewSystemConnectionContext(context.Background()); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	go runner.monitorUnitStates()

	return runner, nil
}

// Close closes runner.
func (runner *Runner) Close() {
	log.Debug("Close runner")

	runner.stopChan <- struct{}{}

	runner.systemd.Close()
}

func (runner *Runner) InstanceStatusChannel() <-chan []InstanceStatus {
	return runner.instanceStatusChan
}

// StartInstance starts service instance as systemd unit.
func (runner *Runner) StartInstance(instanceID, runtimeDir string, params StartInstanceParams) (status InstanceStatus) {
	status.InstanceID = instanceID
	status.State = cloudprotocol.InstanceStateFailed

	unitName := fmt.Sprintf(systemdUnitNameTamplate, instanceID)

	unitStatusChannel := make(chan dbus.UnitStatus, 1)

	runner.Lock()

	runner.runningUnits[unitName] = unitStatusChannel

	runner.Unlock()

	defer func() {
		runner.Lock()

		runner.runningUnits[unitName] = nil

		close(unitStatusChannel)

		if status.State == cloudprotocol.InstanceStateFailed {
			delete(runner.runningUnits, unitName)

			if status.Err == nil {
				status.Err = aoserrors.Errorf("can't start service instance id %s", status.InstanceID)
			}
		}

		runner.Unlock()
	}()

	channel := make(chan string)

	if _, status.Err = runner.systemd.StartUnitContext(
		context.Background(), unitName, "replace", channel); status.Err != nil {
		return status
	}

	jobState := <-channel

	log.WithFields(log.Fields{"name": unitName, "job state": jobState}).Debug("Start service")

	if jobState != jobStateDone {
		return status
	}

	status.State = runner.getStartingState(unitStatusChannel, params)

	return status
}

// StopInstance stops service instance.
func (runner *Runner) StopInstance(instanceID string) (err error) {
	unitName := fmt.Sprintf(systemdUnitNameTamplate, instanceID)

	runner.Lock()

	delete(runner.runningUnits, fmt.Sprintf(systemdUnitNameTamplate, instanceID))

	runner.Unlock()

	channel := make(chan string)

	if _, err := runner.systemd.StopUnitContext(context.Background(), unitName, "replace", channel); err != nil {
		if strings.Contains(err.Error(), errNotLoaded) {
			log.WithField("id", instanceID).Warn("Service not loaded")

			return nil
		}

		return aoserrors.Wrap(err)
	}

	status := <-channel

	log.WithFields(log.Fields{"id": instanceID, "status": status}).Debug("Stop service")

	return nil
}

/***********************************************************************************************************************
  Private
 **********************************************************************************************************************/

func (runner *Runner) monitorUnitStates() {
	statusChan, errChan := runner.systemd.SubscribeUnitsCustom(
		time.Second, 0, isUnitStatusChanged, runner.isUnitUnderMonitoring)

	for {
		select {
		case changes := <-statusChan:
			instancesStatus := []InstanceStatus{}

			runner.RLock()

			for _, unitStatus := range changes {
				if unitStatus == nil {
					continue
				}

				startChan, ok := runner.runningUnits[unitStatus.Name]
				if ok {
					if startChan != nil {
						startChan <- *unitStatus
					} else {
						instancesStatus = append(instancesStatus, unitStatusToInstanceStatus(unitStatus))
					}
				}
			}

			runner.RUnlock()

			if len(instancesStatus) == 0 {
				continue
			}

			select {
			case runner.instanceStatusChan <- instancesStatus:

			default:
				log.Error("Instance status channel full")
			}

		case err := <-errChan:
			log.Errorf("Error monitoring systemd unit status: %s", err)

		case <-runner.stopChan:
			return
		}
	}
}

func (runner *Runner) getStartingState(unitStatusChannel <-chan dbus.UnitStatus, params StartInstanceParams) string {
	if params.StartInterval == 0 {
		params.StartInterval = defaultStartTimeout * time.Second
	}

	var currentState string

	for {
		select {
		case unitStatus := <-unitStatusChannel:
			if unitStatus.ActiveState == cloudprotocol.InstanceStateFailed {
				return cloudprotocol.InstanceStateFailed
			}

			currentState = unitStatus.ActiveState

		case <-time.After(startTimeoutMultiplier * params.StartInterval):
			if currentState != cloudprotocol.InstanceStateActive {
				return cloudprotocol.InstanceStateFailed
			}

			return cloudprotocol.InstanceStateActive
		}
	}
}

func (runner *Runner) isUnitUnderMonitoring(unitName string) bool {
	runner.RLock()
	defer runner.RUnlock()

	_, exists := runner.runningUnits[unitName]

	return !exists
}

func unitStatusToInstanceStatus(unitStatus *dbus.UnitStatus) (runnerStatus InstanceStatus) {
	runnerStatus.InstanceID = strings.TrimPrefix(strings.TrimSuffix(unitStatus.Name, ".service"), "aos-service@")

	runnerStatus.State = unitStateToInstanceState(unitStatus.ActiveState)

	return runnerStatus
}

func unitStateToInstanceState(uintStatus string) string {
	if uintStatus == cloudprotocol.InstanceStateActive {
		return cloudprotocol.InstanceStateActive
	}

	return cloudprotocol.InstanceStateFailed
}

// isUnitStatusChanged returns true if the provided UnitStatus objects
// are not equivalent. false is returned if the objects are equivalent.
// Only the Name, Description and state-related fields are used in
// the comparison.
func isUnitStatusChanged(u1, u2 *dbus.UnitStatus) bool {
	return u1.Name != u2.Name ||
		u1.Description != u2.Description ||
		u1.LoadState != u2.LoadState ||
		u1.ActiveState != u2.ActiveState ||
		u1.SubState != u2.SubState
}
