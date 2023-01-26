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
	"path/filepath"

	"github.com/aoscloud/aos_common/api/cloudprotocol"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/runner"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type runtimeInstanceInfo struct {
	InstanceInfo
	service         *serviceInfo
	runStatus       runner.InstanceStatus
	runtimeDir      string
	secret          string
	overrideEnvVars []string
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func newRuntimeInstanceInfo(info InstanceInfo) *runtimeInstanceInfo {
	return &runtimeInstanceInfo{InstanceInfo: info, runtimeDir: filepath.Join(RuntimeDir, info.InstanceID)}
}

func (instance *runtimeInstanceInfo) getCloudStatus() cloudprotocol.InstanceStatus {
	status := cloudprotocol.InstanceStatus{
		InstanceIdent: instance.InstanceIdent,
		RunState:      instance.runStatus.State,
	}

	if instance.service != nil {
		status.AosVersion = instance.service.AosVersion
	}

	if status.RunState == cloudprotocol.InstanceStateFailed {
		status.ErrorInfo = &cloudprotocol.ErrorInfo{ExitCode: instance.runStatus.ExitCode}

		if instance.runStatus.Err != nil {
			status.ErrorInfo.Message = instance.runStatus.Err.Error()
		}
	}

	return status
}

func (instance *runtimeInstanceInfo) setRunStatus(runStatus runner.InstanceStatus) {
	instance.runStatus = runStatus

	if runStatus.State == cloudprotocol.InstanceStateFailed {
		log.WithFields(instanceLogFields(instance, nil)).Errorf("Instance failed: %v", runStatus.Err)

		return
	}

	log.WithFields(instanceLogFields(instance, nil)).Info("Instance successfully started")
}

func (launcher *Launcher) instanceFailed(instance *runtimeInstanceInfo, err error) {
	log.WithFields(instanceLogFields(instance, nil)).Errorf("Instance failed: %v", err)

	instance.runStatus.State = cloudprotocol.InstanceStateFailed
	instance.runStatus.Err = err
}

func instanceLogFields(instance *runtimeInstanceInfo, extraFields log.Fields) log.Fields {
	logFields := log.Fields{
		"serviceID":     instance.ServiceID,
		"subjectID":     instance.SubjectID,
		"instanceIndex": instance.Instance,
		"instanceID":    instance.InstanceID,
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
