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
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	amqp "aos_servicemanager/amqphandler"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type testEnvVarsStorage struct {
	envVarsData []amqp.OverrideEnvsFromCloud
}

type testEnvVarsSender struct {
	statuses []amqp.EnvVarInfoStatus
}

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

func TestEnvVarsProviderCreate(t *testing.T) {
	testStorage := testEnvVarsStorage{}
	testSender := testEnvVarsSender{}

	time1 := time.Now().Add(time.Duration(1 * time.Hour))
	time2 := time.Now().Add(time.Duration(-1 * time.Hour))

	testStorage.envVarsData = []amqp.OverrideEnvsFromCloud{{SubjectID: "subject1", ServiceID: "service1",
		EnvVars: []amqp.EnvVarInfo{{ID: "1234", Variable: "LEVEL=Debug", TTL: &time1}, {ID: "0000", Variable: "HELLO", TTL: &time2}}}}

	_, err := createEnvVarsProvider(&testStorage, &testSender)
	if err != nil {
		t.Fatal("Can't create env vars provider: ", err)
	}

	if len(testStorage.envVarsData[0].EnvVars) != 1 {
		t.Error("Count of env vars should be 1")
	}

	if testStorage.envVarsData[0].EnvVars[0].ID != "1234" {
		t.Error("Incorrect env var ID: ", testStorage.envVarsData[0].EnvVars[0].ID)
	}
}

func TestProcessprocessOverrideEnvVars(t *testing.T) {
	testStorage := testEnvVarsStorage{}
	testSender := testEnvVarsSender{}

	time1 := time.Now().Add(time.Duration(1 * time.Hour))

	testStorage.envVarsData = []amqp.OverrideEnvsFromCloud{
		{SubjectID: "subject0", ServiceID: "service0",
			EnvVars: []amqp.EnvVarInfo{}},
		{SubjectID: "subject1", ServiceID: "service1",
			EnvVars: []amqp.EnvVarInfo{{ID: "1111", Variable: "LEVEL=Debug", TTL: &time1}, {ID: "2222", Variable: "HELLO", TTL: &time1}}},
		{SubjectID: "subject2", ServiceID: "service2",
			EnvVars: []amqp.EnvVarInfo{{ID: "3333", Variable: "LEVEL2=Debug", TTL: &time1}, {ID: "4444", Variable: "HELLO2", TTL: &time1}}},
		{SubjectID: "subject3", ServiceID: "service3",
			EnvVars: []amqp.EnvVarInfo{{ID: "5555", Variable: "LEVEL2=Debug", TTL: &time1}, {ID: "6666", Variable: "HELLO2", TTL: &time1}}},
	}

	provider, err := createEnvVarsProvider(&testStorage, &testSender)
	if err != nil {
		t.Fatal("Can't create env vars provider: ", err)
	}

	desiredEnv := []amqp.OverrideEnvsFromCloud{
		{SubjectID: "subject0", ServiceID: "service0",
			EnvVars: []amqp.EnvVarInfo{{ID: "0_new", Variable: "LEVEL_NEW=Debug", TTL: &time1}, {ID: "00_new", Variable: "NEW", TTL: &time1}}},
		{SubjectID: "subject2", ServiceID: "service2",
			EnvVars: []amqp.EnvVarInfo{{ID: "3333_new", Variable: "LEVEL_NEW=Debug", TTL: &time1}, {ID: "4444_new", Variable: "NEW", TTL: &time1}}},
		{SubjectID: "subject3", ServiceID: "service3",
			EnvVars: []amqp.EnvVarInfo{{ID: "5555", Variable: "LEVEL2=Debug", TTL: &time1}, {ID: "6666", Variable: "HELLO2", TTL: &time1}}},
	}

	result, err := provider.processOverrideEnvVars(desiredEnv)
	if err != nil {
		t.Error("Can't process override env vars: ", err)
	}

	servicesToRestart := []subjectServicePair{{"subject0", "service0"},
		{"subject1", "service1"},
		{"subject2", "service2"}}

	if reflect.DeepEqual(result, servicesToRestart) == false {
		t.Error("incorrect services to restart")
	}

	validsStatus := []amqp.EnvVarInfoStatus{
		{SubjectID: "subject0", ServiceID: "service0", Statuses: []amqp.EnvVarStatus{{ID: "0_new"}, {ID: "00_new"}}},
		{SubjectID: "subject2", ServiceID: "service2", Statuses: []amqp.EnvVarStatus{{ID: "3333_new"}, {ID: "4444_new"}}},
		{SubjectID: "subject3", ServiceID: "service3", Statuses: []amqp.EnvVarStatus{{ID: "5555"}, {ID: "6666"}}},
	}

	if reflect.DeepEqual(validsStatus, testSender.statuses) == false {
		t.Error("incorrect env vars status")
	}

	// test desired env var list contain Non-existent subject serviceId pair
	desiredEnv = []amqp.OverrideEnvsFromCloud{
		{SubjectID: "subject0", ServiceID: "service0",
			EnvVars: []amqp.EnvVarInfo{{ID: "0_new", Variable: "LEVEL_NEW=Debug", TTL: &time1}, {ID: "00_new", Variable: "NEW", TTL: &time1}}},
		{SubjectID: "subject2", ServiceID: "service2",
			EnvVars: []amqp.EnvVarInfo{{ID: "3333_new", Variable: "LEVEL_NEW=Debug", TTL: &time1}, {ID: "4444_new", Variable: "NEW", TTL: &time1}}},
		{SubjectID: "subject3", ServiceID: "service3",
			EnvVars: []amqp.EnvVarInfo{{ID: "5555", Variable: "LEVEL2=Debug", TTL: &time1}, {ID: "6666", Variable: "HELLO2", TTL: &time1}}},
		{SubjectID: "NOT_EXIST", ServiceID: "NOT_EXIST",
			EnvVars: []amqp.EnvVarInfo{{ID: "5555", Variable: "LEVEL2=Debug", TTL: &time1}, {ID: "6666", Variable: "HELLO2", TTL: &time1}}},
	}

	result, err = provider.processOverrideEnvVars(desiredEnv)
	if err == nil {
		t.Error("should be error: desired env var list contain Non-existent subject serviceId pair")
	}

	log.Debug(testStorage.envVarsData)

	if len(result) != 0 {
		t.Error("No need to restart services")
	}
	validsStatus = []amqp.EnvVarInfoStatus{
		{SubjectID: "subject0", ServiceID: "service0", Statuses: []amqp.EnvVarStatus{{ID: "0_new"}, {ID: "00_new"}}},
		{SubjectID: "subject2", ServiceID: "service2", Statuses: []amqp.EnvVarStatus{{ID: "3333_new"}, {ID: "4444_new"}}},
		{SubjectID: "subject3", ServiceID: "service3", Statuses: []amqp.EnvVarStatus{{ID: "5555"}, {ID: "6666"}}},
		{SubjectID: "NOT_EXIST", ServiceID: "NOT_EXIST", Statuses: []amqp.EnvVarStatus{
			{ID: "5555", Error: "Non-existent subject serviceId pair"}, {ID: "6666", Error: "Non-existent subject serviceId pair"}}},
	}

	if reflect.DeepEqual(validsStatus, testSender.statuses) == false {
		t.Error("incorrect env vrs status")
	}

}

func (storage *testEnvVarsStorage) GetAllOverrideEnvVars() (vars []amqp.OverrideEnvsFromCloud, err error) {
	retValue := make([]amqp.OverrideEnvsFromCloud, len(storage.envVarsData))
	copy(retValue, storage.envVarsData)

	return storage.envVarsData, nil
}

func (storage *testEnvVarsStorage) UpdateOverrideEnvVars(subjects []string, serviceID string, vars []amqp.EnvVarInfo) (err error) {
	for i := range storage.envVarsData {
		if storage.envVarsData[i].SubjectID == subjects[0] && storage.envVarsData[i].ServiceID == serviceID {
			storage.envVarsData[i].EnvVars = vars
			return nil
		}
	}

	return fmt.Errorf("Env doesn't exist")
}

func (sender *testEnvVarsSender) SendOverrideEnvVarsStatus(envs []amqp.EnvVarInfoStatus) (err error) {
	sender.statuses = envs

	return nil
}
