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
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/servicemanager/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type testEnvVarsStorage struct {
	envVarsData []pb.OverrideEnvVar
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

	time1 := time.Now().Add(time.Duration(1 * time.Hour))
	time2 := time.Now().Add(time.Duration(-1 * time.Hour))

	testStorage.envVarsData = []pb.OverrideEnvVar{{SubjectId: "subject1", ServiceId: "service1",
		Vars: []*pb.EnvVarInfo{{VarId: "1234", Variable: "LEVEL=Debug", Ttl: timestamppb.New(time1)},
			{VarId: "0000", Variable: "HELLO", Ttl: timestamppb.New(time2)}}}}

	_, err := createEnvVarsProvider(&testStorage)
	if err != nil {
		t.Fatal("Can't create env vars provider: ", err)
	}

	if len(testStorage.envVarsData[0].Vars) != 1 {
		t.Error("Count of env vars should be 1")
	}

	if testStorage.envVarsData[0].Vars[0].VarId != "1234" {
		t.Error("Incorrect env var VarId: ", testStorage.envVarsData[0].Vars[0].VarId)
	}
}

func TestProcessprocessOverrideEnvVars(t *testing.T) {
	testStorage := testEnvVarsStorage{}

	time1 := time.Now().Add(time.Duration(1 * time.Hour))

	testStorage.envVarsData = []pb.OverrideEnvVar{
		{SubjectId: "subject0", ServiceId: "service0",
			Vars: []*pb.EnvVarInfo{}},
		{SubjectId: "subject1", ServiceId: "service1",
			Vars: []*pb.EnvVarInfo{{VarId: "1111", Variable: "LEVEL=Debug", Ttl: timestamppb.New(time1)}, {VarId: "2222", Variable: "HELLO", Ttl: timestamppb.New(time1)}}},
		{SubjectId: "subject2", ServiceId: "service2",
			Vars: []*pb.EnvVarInfo{{VarId: "3333", Variable: "LEVEL2=Debug", Ttl: timestamppb.New(time1)}, {VarId: "4444", Variable: "HELLO2", Ttl: timestamppb.New(time1)}}},
		{SubjectId: "subject3", ServiceId: "service3",
			Vars: []*pb.EnvVarInfo{{VarId: "5555", Variable: "LEVEL2=Debug", Ttl: timestamppb.New(time1)}, {VarId: "6666", Variable: "HELLO2", Ttl: timestamppb.New(time1)}}},
	}

	provider, err := createEnvVarsProvider(&testStorage)
	if err != nil {
		t.Fatal("Can't create env vars provider: ", err)
	}

	desiredEnv := []*pb.OverrideEnvVar{
		{SubjectId: "subject0", ServiceId: "service0",
			Vars: []*pb.EnvVarInfo{{VarId: "0_new", Variable: "LEVEL_NEW=Debug", Ttl: timestamppb.New(time1)}, {VarId: "00_new", Variable: "NEW", Ttl: timestamppb.New(time1)}}},
		{SubjectId: "subject2", ServiceId: "service2",
			Vars: []*pb.EnvVarInfo{{VarId: "3333_new", Variable: "LEVEL_NEW=Debug", Ttl: timestamppb.New(time1)}, {VarId: "4444_new", Variable: "NEW", Ttl: timestamppb.New(time1)}}},
		{SubjectId: "subject3", ServiceId: "service3",
			Vars: []*pb.EnvVarInfo{{VarId: "5555", Variable: "LEVEL2=Debug", Ttl: timestamppb.New(time1)}, {VarId: "6666", Variable: "HELLO2", Ttl: timestamppb.New(time1)}}},
	}

	result, statuses, err := provider.processOverrideEnvVars(desiredEnv)
	if err != nil {
		t.Error("Can't process override env vars: ", err)
	}

	servicesToRestart := []subjectServicePair{{"subject0", "service0"},
		{"subject1", "service1"},
		{"subject2", "service2"}}

	if reflect.DeepEqual(result, servicesToRestart) == false {
		t.Error("incorrect services to restart")
	}

	validsStatus := []*pb.EnvVarStatus{
		{SubjectId: "subject0", ServiceId: "service0", VarStatus: []*pb.VarStatus{{VarId: "0_new"}, {VarId: "00_new"}}},
		{SubjectId: "subject2", ServiceId: "service2", VarStatus: []*pb.VarStatus{{VarId: "3333_new"}, {VarId: "4444_new"}}},
		{SubjectId: "subject3", ServiceId: "service3", VarStatus: []*pb.VarStatus{{VarId: "5555"}, {VarId: "6666"}}},
	}

	if reflect.DeepEqual(validsStatus, statuses) == false {
		t.Error("incorrect env vars status")
		log.Debug("\nVALID:  ", validsStatus)
		log.Debug("\nSTATUS:  ", statuses)
	}

	// test desired env var list contain Non-existent subject serviceId pair
	desiredEnv = []*pb.OverrideEnvVar{
		{SubjectId: "subject0", ServiceId: "service0",
			Vars: []*pb.EnvVarInfo{{VarId: "0_new", Variable: "LEVEL_NEW=Debug", Ttl: timestamppb.New(time1)}, {VarId: "00_new", Variable: "NEW", Ttl: timestamppb.New(time1)}}},
		{SubjectId: "subject2", ServiceId: "service2",
			Vars: []*pb.EnvVarInfo{{VarId: "3333_new", Variable: "LEVEL_NEW=Debug", Ttl: timestamppb.New(time1)}, {VarId: "4444_new", Variable: "NEW", Ttl: timestamppb.New(time1)}}},
		{SubjectId: "subject3", ServiceId: "service3",
			Vars: []*pb.EnvVarInfo{{VarId: "5555", Variable: "LEVEL2=Debug", Ttl: timestamppb.New(time1)}, {VarId: "6666", Variable: "HELLO2", Ttl: timestamppb.New(time1)}}},
		{SubjectId: "NOT_EXIST", ServiceId: "NOT_EXIST",
			Vars: []*pb.EnvVarInfo{{VarId: "5555", Variable: "LEVEL2=Debug", Ttl: timestamppb.New(time1)}, {VarId: "6666", Variable: "HELLO2", Ttl: timestamppb.New(time1)}}},
	}

	result, statuses, err = provider.processOverrideEnvVars(desiredEnv)
	if err == nil {
		t.Error("should be error: desired env var list contain Non-existent subject serviceId pair")
	}

	log.Debug(testStorage.envVarsData)

	if len(result) != 0 {
		t.Error("No need to restart services")
	}
	validsStatus = []*pb.EnvVarStatus{
		{SubjectId: "subject0", ServiceId: "service0", VarStatus: []*pb.VarStatus{{VarId: "0_new"}, {VarId: "00_new"}}},
		{SubjectId: "subject2", ServiceId: "service2", VarStatus: []*pb.VarStatus{{VarId: "3333_new"}, {VarId: "4444_new"}}},
		{SubjectId: "subject3", ServiceId: "service3", VarStatus: []*pb.VarStatus{{VarId: "5555"}, {VarId: "6666"}}},
		{SubjectId: "NOT_EXIST", ServiceId: "NOT_EXIST", VarStatus: []*pb.VarStatus{
			{VarId: "5555", Error: "Non-existent subject serviceId pair"}, {VarId: "6666", Error: "Non-existent subject serviceId pair"}}},
	}

	if reflect.DeepEqual(validsStatus, statuses) == false {
		t.Error("incorrect env vrs status")
	}

}

func (storage *testEnvVarsStorage) GetAllOverrideEnvVars() (vars []pb.OverrideEnvVar, err error) {
	retValue := make([]pb.OverrideEnvVar, len(storage.envVarsData))
	copy(retValue, storage.envVarsData)

	return storage.envVarsData, nil
}

func (storage *testEnvVarsStorage) UpdateOverrideEnvVars(subjects []string, serviceVarId string, vars []*pb.EnvVarInfo) (err error) {
	for i := range storage.envVarsData {
		if storage.envVarsData[i].SubjectId == subjects[0] && storage.envVarsData[i].ServiceId == serviceVarId {
			storage.envVarsData[i].Vars = vars
			return nil
		}
	}

	return fmt.Errorf("Env doesn't exist")
}
