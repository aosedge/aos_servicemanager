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
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/servicemanager/v1"
	"google.golang.org/protobuf/proto"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type EnvVarsStorage interface {
	GetAllOverrideEnvVars() (vars []pb.OverrideEnvVar, err error)
	UpdateOverrideEnvVars(subjects []string, serviceID string, vars []*pb.EnvVarInfo) (err error)
}

type envVarsProvider struct {
	storage        EnvVarsStorage
	currentEnvVars []pb.OverrideEnvVar
	sync.Mutex
}

type subjectServicePair struct {
	subjectID string
	serviseID string
}

/*******************************************************************************
 * private
 ******************************************************************************/

func createEnvVarsProvider(storage EnvVarsStorage) (provider *envVarsProvider, err error) {
	provider = &envVarsProvider{storage: storage}

	if err = provider.syncEnvVarsWithStorage(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if _, err = provider.validateEnvVarsTTL(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return provider, nil
}

func (provider *envVarsProvider) getEnvVars(request subjectServicePair) (vars []*pb.EnvVarInfo, err error) {
	provider.Lock()
	defer provider.Unlock()

	for i := range provider.currentEnvVars {
		if provider.currentEnvVars[i].SubjectId == request.subjectID &&
			provider.currentEnvVars[i].ServiceId == request.serviseID {
			return provider.currentEnvVars[i].Vars, nil
		}
	}

	return vars, aoserrors.Errorf("env vars for service %s doesn't exist", request.serviseID)
}

func (provider *envVarsProvider) processOverrideEnvVars(vars []*pb.OverrideEnvVar) (
	servicesToRestart []subjectServicePair, statuses []*pb.EnvVarStatus, err error) {
	provider.Lock()
	defer provider.Unlock()

	var resultError error

	for currentIndex, currentVar := range provider.currentEnvVars {
		presentInDesired := false

		for desIndex, desValue := range vars {
			if desValue.ServiceId == currentVar.ServiceId && desValue.SubjectId == currentVar.SubjectId {
				presentInDesired = true

				status := pb.EnvVarStatus{SubjectId: desValue.SubjectId, ServiceId: desValue.ServiceId}

				if !provider.isEnvVarsEqual(desValue.Vars, currentVar.Vars) {
					provider.currentEnvVars[currentIndex].Vars = desValue.Vars

					err = provider.storage.UpdateOverrideEnvVars([]string{desValue.SubjectId}, desValue.ServiceId,
						desValue.Vars)
					if err != nil {
						log.Error("Can't update env vars in storage: ", err)

						if resultError == nil {
							resultError = aoserrors.Wrap(err)
						}
					}

					servicesToRestart = append(servicesToRestart,
						subjectServicePair{subjectID: currentVar.SubjectId, serviseID: currentVar.ServiceId})
				}

				for _, env := range desValue.Vars {
					varStatus := pb.VarStatus{VarId: env.VarId}

					if err != nil {
						varStatus.Error = err.Error()
					}

					status.VarStatus = append(status.VarStatus, &varStatus)
				}

				statuses = append(statuses, &status)

				vars = append(vars[:desIndex], vars[desIndex+1:]...)

				break
			}
		}

		if !presentInDesired {
			provider.currentEnvVars[currentIndex].Vars = []*pb.EnvVarInfo{}

			if err = provider.storage.UpdateOverrideEnvVars([]string{currentVar.SubjectId}, currentVar.ServiceId,
				[]*pb.EnvVarInfo{}); err != nil {
				if resultError == nil {
					resultError = aoserrors.Wrap(err)
				}
			}

			if len(currentVar.Vars) != 0 {
				servicesToRestart = append(servicesToRestart,
					subjectServicePair{subjectID: currentVar.SubjectId, serviseID: currentVar.ServiceId})
			}
		}
	}

	if len(vars) != 0 {
		for _, notExistVar := range vars {
			status := pb.EnvVarStatus{SubjectId: notExistVar.SubjectId, ServiceId: notExistVar.ServiceId}

			for _, env := range notExistVar.Vars {
				status.VarStatus = append(status.VarStatus, &pb.VarStatus{VarId: env.VarId, Error: "Non-existent subject serviceId pair"})
			}

			statuses = append(statuses, &status)
		}

		if resultError == nil {
			resultError = aoserrors.New("desired env var list contain Non-existent subject serviceId pair")
		}
	}

	return servicesToRestart, statuses, aoserrors.Wrap(resultError)
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
		actualEnv := []*pb.EnvVarInfo{}

		for _, value := range provider.currentEnvVars[i].Vars {
			if value.Ttl == nil {
				actualEnv = append(actualEnv, value)
				continue
			}

			if currentTime.Before(value.Ttl.AsTime()) {
				actualEnv = append(actualEnv, value)
				continue
			}

			wasChanged = true
		}

		if wasChanged {
			provider.currentEnvVars[i].Vars = actualEnv
			if err = provider.storage.UpdateOverrideEnvVars([]string{provider.currentEnvVars[i].SubjectId},
				provider.currentEnvVars[i].ServiceId, actualEnv); err != nil {
				return servicesToRestart, aoserrors.Wrap(err)
			}

			provider.currentEnvVars[i].Vars = actualEnv

			servicesToRestart = append(servicesToRestart, subjectServicePair{provider.currentEnvVars[i].SubjectId,
				provider.currentEnvVars[i].ServiceId})
		}
	}

	return servicesToRestart, nil
}

func (provider *envVarsProvider) isEnvVarsEqual(desiredVars, currentVars []*pb.EnvVarInfo) (isEqual bool) {
	if len(desiredVars) != len(currentVars) {
		return false
	}

	for _, value := range desiredVars {
		elementWasFound := false

		for _, currentEl := range currentVars {
			if proto.Equal(value, currentEl) {
				elementWasFound = true
				break
			}
		}

		if !elementWasFound {
			return false
		}
	}

	return true
}
