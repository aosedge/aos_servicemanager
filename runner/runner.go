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
	"os"
	"path/filepath"
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
	defaultStartInterval   = 1 * time.Second
	defaultStartBurst      = 3
	defaultRestartInterval = 100 * time.Millisecond
	startTimeoutMultiplier = 1.2
)

const systemdUnitNameTemplate = "aos-service@%s.service"

const (
	errNotLoaded  = "not loaded"
	jobStatusDone = "done"
)

const (
	systemdDropInsDir  = "/run/systemd/system"
	parametersFileName = "parameters.conf"
)

const statusPollPeriod = 1 * time.Second

/***********************************************************************************************************************
  Types
 **********************************************************************************************************************/

// RunParameters run instance parameters.
type RunParameters struct {
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
func (runner *Runner) StartInstance(instanceID, runtimeDir string, params RunParameters) (status InstanceStatus) {
	status.InstanceID = instanceID
	status.State = cloudprotocol.InstanceStateFailed

	unitName := fmt.Sprintf(systemdUnitNameTemplate, instanceID)

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
				status.Err = aoserrors.Errorf("instance failed")
			}
		}

		runner.Unlock()
	}()

	if params.StartInterval == 0 {
		params.StartInterval = defaultStartInterval
	}

	if params.StartBurst == 0 {
		params.StartBurst = defaultStartBurst
	}

	if params.RestartInterval == 0 {
		params.RestartInterval = defaultRestartInterval
	}

	if status.Err = runner.setRunParameters(unitName, params); status.Err != nil {
		return status
	}

	channel := make(chan string)

	if _, status.Err = runner.systemd.StartUnitContext(
		context.Background(), unitName, "replace", channel); status.Err != nil {
		return status
	}

	jobStatus := <-channel

	log.WithFields(log.Fields{"name": unitName, "jobStatus": jobStatus, "instanceID": instanceID}).Debug("Start service")

	if jobStatus != jobStatusDone {
		return status
	}

	status.State = runner.getStartingState(unitName, unitStatusChannel, params)

	return status
}

// StopInstance stops service instance.
func (runner *Runner) StopInstance(instanceID string) (err error) {
	unitName := fmt.Sprintf(systemdUnitNameTemplate, instanceID)

	runner.Lock()

	delete(runner.runningUnits, fmt.Sprintf(systemdUnitNameTemplate, instanceID))

	runner.Unlock()

	channel := make(chan string)

	if _, stopErr := runner.systemd.StopUnitContext(
		context.Background(), unitName, "replace", channel); stopErr != nil {
		if strings.Contains(stopErr.Error(), errNotLoaded) {
			log.WithField("id", instanceID).Warn("Service not loaded")
		} else if err == nil {
			err = aoserrors.Wrap(stopErr)
		}
	} else {
		jobStatus := <-channel

		log.WithFields(log.Fields{"id": instanceID, "jobStatus": jobStatus}).Debug("Stop service")

		if jobStatus != jobStatusDone && err == nil {
			err = aoserrors.Errorf("job status %s", jobStatus)
		}
	}

	if removeErr := runner.removeRunParameters(
		fmt.Sprintf(systemdUnitNameTemplate, instanceID)); removeErr != nil && err != nil {
		err = removeErr
	}

	return err
}

/***********************************************************************************************************************
  Private
 **********************************************************************************************************************/

func (runner *Runner) monitorUnitStates() {
	statusChan, errChan := runner.systemd.SubscribeUnitsCustom(
		statusPollPeriod, 0, isUnitStatusChanged, runner.isUnitUnderMonitoring)

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

func (runner *Runner) getStartingState(
	unitName string, unitStatusChannel <-chan dbus.UnitStatus, params RunParameters,
) string {
	var currentState string

	statuses, err := runner.systemd.ListUnitsByNamesContext(context.Background(), []string{unitName})
	if err == nil && len(statuses) > 0 {
		currentState = statuses[0].ActiveState
	}

	for {
		select {
		case unitStatus := <-unitStatusChannel:
			if unitStatus.ActiveState == cloudprotocol.InstanceStateFailed {
				return cloudprotocol.InstanceStateFailed
			}

			currentState = unitStatus.ActiveState

		case <-time.After(time.Duration(startTimeoutMultiplier * float32(params.StartInterval))):
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

func (runner *Runner) setRunParameters(unitName string, params RunParameters) error {
	const parametersFormat = `[Unit]
StartLimitIntervalSec=%s
StartLimitBurst=%d

[Service]
RestartSec=%s
`

	if params.StartInterval < 1*time.Microsecond || params.RestartInterval < 1*time.Microsecond {
		return aoserrors.New("invalid parameters")
	}

	parametersDir := filepath.Join(systemdDropInsDir, unitName+".d")

	if err := os.MkdirAll(parametersDir, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := os.WriteFile( // nolint:gosec // To fix systemd warning, file parameters.conf should be 644
		filepath.Join(parametersDir, parametersFileName),
		[]byte(fmt.Sprintf(parametersFormat, params.StartInterval, params.StartBurst, params.RestartInterval)),
		0o644); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (runner *Runner) removeRunParameters(unitName string) error {
	if err := os.RemoveAll(filepath.Join(systemdDropInsDir, unitName+".d")); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}
