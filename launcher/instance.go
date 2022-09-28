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
	"encoding/hex"
	"path/filepath"

	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/aoscloud/aos_servicemanager/runner"
	log "github.com/sirupsen/logrus"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type instanceInfo struct {
	InstanceInfo
	service         *serviceInfo
	runStatus       runner.InstanceStatus
	isStarted       bool
	runtimeDir      string
	secret          string
	storagePath     string
	statePath       string
	stateChecksum   []byte
	overrideEnvVars []string
}

type byPriority []*instanceInfo

/***********************************************************************************************************************
 * Sort instance priority
 **********************************************************************************************************************/

func (instances byPriority) Len() int { return len(instances) }

func (instances byPriority) Less(i, j int) bool {
	return instances[i].SubjectID < instances[j].SubjectID ||
		instances[i].ServiceID < instances[j].ServiceID ||
		instances[i].Instance < instances[j].Instance
}
func (instances byPriority) Swap(i, j int) { instances[i], instances[j] = instances[j], instances[i] }

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func newInstanceInfo(info InstanceInfo) *instanceInfo {
	return &instanceInfo{InstanceInfo: info, runtimeDir: filepath.Join(RuntimeDir, info.InstanceID)}
}

func (instance *instanceInfo) getCloudStatus() cloudprotocol.InstanceStatus {
	status := cloudprotocol.InstanceStatus{
		InstanceIdent: instance.InstanceIdent,
		AosVersion:    instance.AosVersion,
		StateChecksum: hex.EncodeToString(instance.stateChecksum),
		RunState:      instance.runStatus.State,
	}

	if status.RunState == cloudprotocol.InstanceStateFailed {
		status.ErrorInfo = &cloudprotocol.ErrorInfo{ExitCode: instance.runStatus.ExitCode}

		if instance.runStatus.Err != nil {
			status.ErrorInfo.Message = instance.runStatus.Err.Error()
		}
	}

	return status
}

func (instance *instanceInfo) setRunStatus(runStatus runner.InstanceStatus) {
	instance.runStatus = runStatus

	if runStatus.State == cloudprotocol.InstanceStateFailed {
		log.WithFields(instanceIdentLogFields(instance.InstanceIdent,
			log.Fields{"instanceID": runStatus.InstanceID})).Errorf("Instance failed: %v", runStatus.Err)

		return
	}

	log.WithFields(instanceIdentLogFields(instance.InstanceIdent,
		log.Fields{"instanceID": runStatus.InstanceID})).Info("Instance successfully started")
}

func (launcher *Launcher) getCurrentInstance(instanceIdent cloudprotocol.InstanceIdent) (InstanceInfo, error) {
	for _, currentInstance := range launcher.currentInstances {
		if currentInstance.InstanceIdent == instanceIdent {
			return currentInstance.InstanceInfo, nil
		}
	}

	return InstanceInfo{}, ErrNotExist
}

func (launcher *Launcher) instanceFailed(instance *instanceInfo, err error) {
	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	log.WithFields(instanceIdentLogFields(instance.InstanceIdent, nil)).Errorf("Instance failed: %v", err)

	instance.runStatus.State = cloudprotocol.InstanceStateFailed
	instance.runStatus.Err = err
}

func instanceIdentLogFields(instance cloudprotocol.InstanceIdent, extraFields log.Fields) log.Fields {
	logFields := log.Fields{
		"serviceID": instance.ServiceID,
		"subjectID": instance.SubjectID,
		"instance":  instance.Instance,
	}

	for k, v := range extraFields {
		logFields[k] = v
	}

	return logFields
}

func instanceFilterLogFields(filter cloudprotocol.InstanceFilter, extraFields log.Fields) log.Fields {
	logFields := log.Fields{"serviceID": filter.ServiceID}

	if filter.SubjectID != nil {
		logFields["subjectID"] = *filter.SubjectID
	} else {
		logFields["subjectID"] = "*"
	}

	if filter.Instance != nil {
		logFields["Instance"] = *filter.Instance
	} else {
		logFields["Instance"] = "*"
	}

	for k, v := range extraFields {
		logFields[k] = v
	}

	return logFields
}

func appendInstances(instances []*instanceInfo, instance ...*instanceInfo) []*instanceInfo {
	var newInstances []*instanceInfo

instanceLoop:
	for _, newInstance := range instance {
		for _, existingInstance := range instances {
			if newInstance == existingInstance {
				continue instanceLoop
			}
		}

		newInstances = append(newInstances, newInstance)
	}

	return newInstances
}
