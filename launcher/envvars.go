// SPX-License-Identifier: Apache-2.0
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

package launcher

import (
	"reflect"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/runner"
)

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (launcher *Launcher) setEnvVars(
	envVarsInfo []cloudprotocol.EnvVarsInstanceInfo,
) []cloudprotocol.EnvVarsInstanceStatus {
	envVarsStatus := make([]cloudprotocol.EnvVarsInstanceStatus, len(envVarsInfo))

	now := time.Now()

	for i, envVarInfo := range envVarsInfo {
		envVarStatus := cloudprotocol.EnvVarsInstanceStatus{
			InstanceFilter: envVarInfo.InstanceFilter,
		}

		for _, envVar := range envVarInfo.EnvVars {
			log.WithFields(
				instanceFilterLogFields(envVarInfo.InstanceFilter, log.Fields{
					"var": envVar.Variable,
					"ttl": envVar.TTL,
					"id":  envVar.ID,
				}),
			).Debug("Override env var")

			if envVar.TTL != nil && envVar.TTL.Before(now) {
				err := aoserrors.New("environment variable expired")

				envVarStatus.Statuses = append(envVarStatus.Statuses,
					cloudprotocol.EnvVarStatus{ID: envVar.ID, Error: err.Error()})

				log.WithField("id", envVar.ID).Errorf("Error overriding environment variable: %s", err)

				continue
			}

			envVarStatus.Statuses = append(envVarStatus.Statuses, cloudprotocol.EnvVarStatus{ID: envVar.ID})
		}

		envVarsStatus[i] = envVarStatus
	}

	launcher.currentEnvVars = envVarsInfo

	if err := launcher.storage.SetOverrideEnvVars(envVarsInfo); err != nil {
		return setEnvVarsErr(envVarsStatus, err)
	}

	return envVarsStatus
}

func setEnvVarsErr(
	envVarsStatus []cloudprotocol.EnvVarsInstanceStatus, err error,
) []cloudprotocol.EnvVarsInstanceStatus {
	var errStr string

	if err != nil {
		errStr = err.Error()
	}

	for _, envVarStatus := range envVarsStatus {
		for i, status := range envVarStatus.Statuses {
			if status.Error == "" {
				envVarStatus.Statuses[i] = cloudprotocol.EnvVarStatus{
					ID: status.ID, Error: errStr,
				}
			}
		}
	}

	return envVarsStatus
}

func (launcher *Launcher) getInstanceEnvVars(instance InstanceInfo) (envVars []string) {
	for _, envVarInfo := range launcher.currentEnvVars {
		if (envVarInfo.ServiceID == instance.ServiceID) &&
			(envVarInfo.SubjectID == nil || *envVarInfo.SubjectID == instance.SubjectID) &&
			(envVarInfo.Instance == nil || *envVarInfo.Instance == instance.Instance) {
			for _, envVar := range envVarInfo.EnvVars {
				envVars = append(envVars, envVar.Variable)
			}
		}
	}

	return envVars
}

func (launcher *Launcher) removeOutdatedEnvVars() {
	var (
		now            = time.Now()
		updatedEnvVars = make([]cloudprotocol.EnvVarsInstanceInfo, 0, len(launcher.currentEnvVars))
		updated        = false
	)

	for _, item := range launcher.currentEnvVars {
		var updatedItems []cloudprotocol.EnvVarInfo

		for _, envVar := range item.EnvVars {
			if envVar.TTL != nil && envVar.TTL.Before(now) {
				log.WithFields(log.Fields{
					"id": envVar.ID, "variable": envVar.Variable,
				}).Debug("Remove expired overridden environment variable")

				updated = true

				continue
			}

			updatedItems = append(updatedItems, envVar)
		}

		if len(updatedItems) == 0 {
			continue
		}

		updatedEnvVars = append(updatedEnvVars, cloudprotocol.EnvVarsInstanceInfo{
			InstanceFilter: item.InstanceFilter,
			EnvVars:        updatedItems,
		})
	}

	if updated {
		launcher.currentEnvVars = updatedEnvVars

		if err := launcher.storage.SetOverrideEnvVars(updatedEnvVars); err != nil {
			log.Errorf("Can't set override env vars: %s", err)
		}
	}
}

func (launcher *Launcher) updateInstancesEnvVars() {
	launcher.removeOutdatedEnvVars()

	statusMap := make(map[string]runner.InstanceStatus)

instancesLoop:
	for _, instance := range launcher.currentInstances {
		if !reflect.DeepEqual(launcher.getInstanceEnvVars(instance.InstanceInfo), instance.overrideEnvVars) {
			log.WithFields(
				instanceLogFields(instance, nil)).Debug("Restart instance due to environment variables change")

			statusMap[instance.InstanceID] = instance.runStatus

			launcher.doStopAction(instance)
			launcher.doStartAction(instance)

			continue instancesLoop
		}
	}

	launcher.actionHandler.Wait()

	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	// Send updated status if it is changed after env vars applying

	updateInstancesStatus := &InstancesStatus{Instances: make([]cloudprotocol.InstanceStatus, 0)}

	for instanceID, runStatus := range statusMap {
		currentInstance, ok := launcher.currentInstances[instanceID]
		if !ok {
			log.WithFields(log.Fields{"instanceID": instanceID}).Errorf("Instance not found after overriding env vars")

			continue
		}

		if runStatus.State != currentInstance.runStatus.State {
			updateInstancesStatus.Instances = append(updateInstancesStatus.Instances, currentInstance.getCloudStatus())
		}
	}

	if len(updateInstancesStatus.Instances) > 0 {
		launcher.runtimeStatusChannel <- RuntimeStatus{UpdateStatus: updateInstancesStatus}
	}
}
