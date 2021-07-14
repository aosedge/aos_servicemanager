// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2021 Renesas Inc.
// Copyright 2021 EPAM Systems Inc.
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
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"

	amqp "aos_servicemanager/amqphandler"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type EnvVarsStorage interface {
	GetAllOverrideEnvVars() (vars []amqp.OverrideEnvsFromCloud, err error)
	UpdateOverrideEnvVars(subjects []string, serviceID string, vars []amqp.EnvVarInfo) (err error)
}

type envVarStatusSender interface {
	SendOverrideEnvVarsStatus(envs []amqp.EnvVarInfoStatus) (err error)
}

type envVarsProvider struct {
	storage        EnvVarsStorage
	sender         envVarStatusSender
	currentEnvVars []amqp.OverrideEnvsFromCloud
	sync.Mutex
}

type subjectServicePair struct {
	subjectID string
	serviseID string
}

/*******************************************************************************
 * private
 ******************************************************************************/

func createEnvVarsProvider(storage EnvVarsStorage, sender envVarStatusSender) (provider *envVarsProvider, err error) {
	provider = &envVarsProvider{storage: storage, sender: sender}

	if err = provider.syncEnvVarsWithStorage(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if _, err = provider.validateEnvVarsTTL(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return provider, nil
}

func (provider *envVarsProvider) getEnvVars(request subjectServicePair) (vars []amqp.EnvVarInfo, err error) {
	provider.Lock()
	defer provider.Unlock()

	for i := range provider.currentEnvVars {
		if provider.currentEnvVars[i].SubjectID == request.subjectID &&
			provider.currentEnvVars[i].ServiceID == request.serviseID {
			return provider.currentEnvVars[i].EnvVars, nil
		}
	}

	return vars, aoserrors.Errorf("env vars for service %s doesn't exist", request.serviseID)
}

func (provider *envVarsProvider) processOverrideEnvVars(vars []amqp.OverrideEnvsFromCloud) (
	servicesToRestart []subjectServicePair, err error) {
	provider.Lock()
	defer provider.Unlock()

	var resultError error
	statuses := []amqp.EnvVarInfoStatus{}

	for currentIndex, currentVar := range provider.currentEnvVars {
		presentInDesired := false

		for desIndex, desValue := range vars {
			if desValue.ServiceID == currentVar.ServiceID && desValue.SubjectID == currentVar.SubjectID {
				presentInDesired = true

				status := amqp.EnvVarInfoStatus{SubjectID: desValue.SubjectID, ServiceID: desValue.ServiceID}

				if provider.isEnvVarsEqual(desValue.EnvVars, currentVar.EnvVars) == false {
					provider.currentEnvVars[currentIndex].EnvVars = desValue.EnvVars

					err = provider.storage.UpdateOverrideEnvVars([]string{desValue.SubjectID}, desValue.ServiceID,
						desValue.EnvVars)
					if err != nil {
						log.Error("Can't update env vars in storage: ", err)

						if resultError == nil {
							resultError = aoserrors.Wrap(err)
						}
					}

					servicesToRestart = append(servicesToRestart,
						subjectServicePair{subjectID: currentVar.SubjectID, serviseID: currentVar.ServiceID})
				}

				for _, env := range desValue.EnvVars {
					varStatus := amqp.EnvVarStatus{ID: env.ID}

					if err != nil {
						varStatus.Error = err.Error()
					}

					status.Statuses = append(status.Statuses, varStatus)
				}

				statuses = append(statuses, status)

				vars = append(vars[:desIndex], vars[desIndex+1:]...)

				break
			}
		}

		if presentInDesired == false {
			provider.currentEnvVars[currentIndex].EnvVars = []amqp.EnvVarInfo{}

			if err = provider.storage.UpdateOverrideEnvVars([]string{currentVar.SubjectID}, currentVar.ServiceID,
				[]amqp.EnvVarInfo{}); err != nil {
				if resultError == nil {
					resultError = aoserrors.Wrap(err)
				}
			}

			if len(currentVar.EnvVars) != 0 {
				servicesToRestart = append(servicesToRestart,
					subjectServicePair{subjectID: currentVar.SubjectID, serviseID: currentVar.ServiceID})
			}
		}
	}

	if len(vars) != 0 {
		for _, notExistVar := range vars {
			status := amqp.EnvVarInfoStatus{SubjectID: notExistVar.SubjectID, ServiceID: notExistVar.ServiceID}

			for _, env := range notExistVar.EnvVars {
				status.Statuses = append(status.Statuses, amqp.EnvVarStatus{ID: env.ID, Error: "Non-existent subject serviceId pair"})
			}

			statuses = append(statuses, status)
		}

		if resultError == nil {
			resultError = aoserrors.New("desired env var list contain Non-existent subject serviceId pair")
		}
	}

	if err := provider.sender.SendOverrideEnvVarsStatus(statuses); err != nil {
		log.Error("Can't send env vars status: ", err)

		if resultError == nil {
			resultError = aoserrors.Wrap(err)
		}
	}

	return servicesToRestart, aoserrors.Wrap(resultError)
}

func (provider *envVarsProvider) syncEnvVarsWithStorage() (err error) {
	provider.Lock()
	defer provider.Unlock()

	if provider.currentEnvVars, err = provider.storage.GetAllOverrideEnvVars(); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (provider *envVarsProvider) validateEnvVarsTTL() (servicesToRestart []subjectServicePair, err error) {
	provider.Lock()
	defer provider.Unlock()

	currentTime := time.Now()

	for i := range provider.currentEnvVars {
		wasChanged := false
		actualEnv := []amqp.EnvVarInfo{}

		for _, value := range provider.currentEnvVars[i].EnvVars {
			if value.TTL == nil {
				actualEnv = append(actualEnv, value)
				continue
			}

			if currentTime.Before(*value.TTL) {
				actualEnv = append(actualEnv, value)
				continue
			}

			wasChanged = true
		}

		if wasChanged {
			provider.currentEnvVars[i].EnvVars = actualEnv
			if err = provider.storage.UpdateOverrideEnvVars([]string{provider.currentEnvVars[i].SubjectID},
				provider.currentEnvVars[i].ServiceID, actualEnv); err != nil {
				return servicesToRestart, aoserrors.Wrap(err)
			}

			provider.currentEnvVars[i].EnvVars = actualEnv

			servicesToRestart = append(servicesToRestart, subjectServicePair{provider.currentEnvVars[i].SubjectID,
				provider.currentEnvVars[i].ServiceID})
		}
	}

	return servicesToRestart, nil
}

func (provider *envVarsProvider) isEnvVarsEqual(desiredVars, currentVars []amqp.EnvVarInfo) (isEqual bool) {
	if len(desiredVars) != len(currentVars) {
		return false
	}

	for _, value := range desiredVars {
		elementWasFound := false

		for _, currentEl := range currentVars {
			if value == currentEl {
				elementWasFound = true
				break
			}
		}

		if elementWasFound == false {
			return false
		}
	}

	return true
}
