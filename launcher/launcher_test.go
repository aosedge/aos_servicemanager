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

package launcher_test

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/launcher"
	"github.com/aoscloud/aos_servicemanager/runner"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
)

/***********************************************************************************************************************
 * Init
 **********************************************************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testStorage struct {
	sync.RWMutex
	instances map[string]launcher.InstanceInfo
}

type testServiceProvider struct {
	services map[string]servicemanager.ServiceInfo
}

type testRunner struct {
	sync.Mutex
	statusChannel chan []runner.InstanceStatus
	startFunc     func(instanceID string) runner.InstanceStatus
	stopFunc      func(instanceID string) error
}

type testInstance struct {
	serviceID      string
	serviceVersion uint64
	subjectID      string
	numInstances   uint64
	err            []error
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestRunInstances(t *testing.T) {
	type testData struct {
		instances []testInstance
	}

	data := []testData{
		// start from scretch
		{
			instances: []testInstance{
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject1", numInstances: 2},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 1},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject2", numInstances: 2},
			},
		},
		// start the same instances
		{
			instances: []testInstance{
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject1", numInstances: 2},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 1},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject2", numInstances: 2},
			},
		},
		// stop and start some instances
		{
			instances: []testInstance{
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 1},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject2", numInstances: 2},
				{serviceID: "service2", serviceVersion: 2, subjectID: "subject1", numInstances: 3},
				{serviceID: "service2", serviceVersion: 2, subjectID: "subject2", numInstances: 4},
			},
		},
		// new service version
		{
			instances: []testInstance{
				{serviceID: "service1", serviceVersion: 2, subjectID: "subject1", numInstances: 1},
				{serviceID: "service1", serviceVersion: 2, subjectID: "subject2", numInstances: 2},
				{serviceID: "service2", serviceVersion: 3, subjectID: "subject1", numInstances: 3},
				{serviceID: "service2", serviceVersion: 3, subjectID: "subject2", numInstances: 4},
			},
		},
		// start error
		{
			instances: []testInstance{
				{
					serviceID: "service3", serviceVersion: 3, subjectID: "subject3", numInstances: 3,
					err: []error{errors.New("error0"), errors.New("error1")}, // nolint:goerr113
				},
			},
		},
		// stop all instances
		{
			instances: []testInstance{},
		},
	}

	var currentTestInstances []testInstance

	runningInstances := make(map[string]runner.InstanceStatus)

	storage := newTestStorage()
	serviceProvider := newTestServiceProvider()
	instanceRunner := newTestRunner(
		func(instanceID string) runner.InstanceStatus {
			status := getRunnerStatus(instanceID, currentTestInstances, storage)
			runningInstances[instanceID] = status

			return status
		},
		func(instanceID string) error {
			delete(runningInstances, instanceID)

			return nil
		},
	)

	testLauncher, err := launcher.New(&config.Config{}, storage, serviceProvider, instanceRunner)
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	for i, item := range data {
		t.Logf("Run instances: %d", i)

		currentTestInstances = item.instances

		if err = serviceProvider.fromTestInstances(currentTestInstances); err != nil {
			t.Fatalf("Can't create test services: %v", err)
		}

		if err = testLauncher.RunInstances(createInstancesInfos(currentTestInstances)); err != nil {
			t.Fatalf("Can't run instances: %v", err)
		}

		runtimeStatus := launcher.RuntimeStatus{
			RunStatus: &launcher.RunInstancesStatus{Instances: createInstancesStatuses(currentTestInstances)},
		}

		if err = checkRuntimeStatus(runtimeStatus, testLauncher.RuntimeStatusChannel()); err != nil {
			t.Errorf("Check runtime status error: %v", err)
		}

		if len(runtimeStatus.RunStatus.Instances) != len(runningInstances) {
			t.Errorf("Wrong running instances count: %d", len(runningInstances))
		}
	}
}

func TestUpdateInstances(t *testing.T) {
	storage := newTestStorage()
	serviceProvider := newTestServiceProvider()
	instanceRunner := newTestRunner(nil, nil)

	testLauncher, err := launcher.New(&config.Config{}, storage, serviceProvider, instanceRunner)
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	testInstances := []testInstance{
		{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 1},
		{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 2},
		{serviceID: "service2", serviceVersion: 2, subjectID: "subject2", numInstances: 3},
	}

	if err = serviceProvider.fromTestInstances(testInstances); err != nil {
		t.Fatalf("Can't create test services: %v", err)
	}

	if err = testLauncher.RunInstances(createInstancesInfos(testInstances)); err != nil {
		t.Fatalf("Can't run instances: %v", err)
	}

	runtimeStatus := launcher.RuntimeStatus{
		RunStatus: &launcher.RunInstancesStatus{Instances: createInstancesStatuses(testInstances)},
	}

	if err = checkRuntimeStatus(runtimeStatus, testLauncher.RuntimeStatusChannel()); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	changedInstances := []testInstance{
		{
			serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 2,
			err: []error{errors.New("error0"), errors.New("error1")}, // nolint:goerr113
		},
	}

	runStatus, err := createRunStatus(storage, changedInstances)
	if err != nil {
		t.Fatalf("Can't create run status: %v", err)
	}

	instanceRunner.statusChannel <- runStatus

	runtimeStatus = launcher.RuntimeStatus{
		UpdateStatus: &launcher.UpdateInstancesStatus{Instances: createInstancesStatuses(changedInstances)},
	}

	if err = checkRuntimeStatus(runtimeStatus, testLauncher.RuntimeStatusChannel()); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}
}

func TestSendCurrentRuntimeStatus(t *testing.T) {
	serviceProvider := newTestServiceProvider()

	testLauncher, err := launcher.New(&config.Config{}, newTestStorage(), serviceProvider,
		newTestRunner(nil, nil))
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	testInstances := []testInstance{
		{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 1},
		{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 2},
		{serviceID: "service2", serviceVersion: 2, subjectID: "subject2", numInstances: 3},
	}

	if err = serviceProvider.fromTestInstances(testInstances); err != nil {
		t.Fatalf("Can't create test services: %v", err)
	}

	if err = testLauncher.SendCurrentRuntimeStatus(); !errors.Is(launcher.ErrNoRuntimeStatus, err) {
		t.Error("No runtime status error expected")
	}

	if err = testLauncher.RunInstances(createInstancesInfos(testInstances)); err != nil {
		t.Fatalf("Can't run instances: %v", err)
	}

	runtimeStatus := launcher.RuntimeStatus{
		RunStatus: &launcher.RunInstancesStatus{Instances: createInstancesStatuses(testInstances)},
	}

	if err = checkRuntimeStatus(runtimeStatus, testLauncher.RuntimeStatusChannel()); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	if err = testLauncher.SendCurrentRuntimeStatus(); err != nil {
		t.Errorf("Can't send current runtime status: %v", err)
	}

	if err = checkRuntimeStatus(runtimeStatus, testLauncher.RuntimeStatusChannel()); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}
}

/***********************************************************************************************************************
 * testStorage
 **********************************************************************************************************************/

func newTestStorage() *testStorage {
	return &testStorage{
		instances: make(map[string]launcher.InstanceInfo),
	}
}

func (storage *testStorage) AddInstance(instance launcher.InstanceInfo) error {
	storage.Lock()
	defer storage.Unlock()

	if _, ok := storage.instances[instance.InstanceID]; ok {
		return aoserrors.New("instance exists")
	}

	storage.instances[instance.InstanceID] = instance

	return nil
}

func (storage *testStorage) UpdateInstance(instance launcher.InstanceInfo) error {
	storage.Lock()
	defer storage.Unlock()

	if _, ok := storage.instances[instance.InstanceID]; !ok {
		return launcher.ErrNotExist
	}

	storage.instances[instance.InstanceID] = instance

	return nil
}

func (storage *testStorage) RemoveInstance(instanceID string) error {
	storage.Lock()
	defer storage.Unlock()

	if _, ok := storage.instances[instanceID]; !ok {
		return launcher.ErrNotExist
	}

	delete(storage.instances, instanceID)

	return nil
}

func (storage *testStorage) GetInstanceByIdent(
	instanceIdent cloudprotocol.InstanceIdent,
) (launcher.InstanceInfo, error) {
	storage.RLock()
	defer storage.RUnlock()

	for _, instance := range storage.instances {
		if instance.InstanceIdent == instanceIdent {
			return instance, nil
		}
	}

	return launcher.InstanceInfo{}, launcher.ErrNotExist
}

func (storage *testStorage) GetInstanceByID(instanceID string) (launcher.InstanceInfo, error) {
	storage.RLock()
	defer storage.RUnlock()

	instance, ok := storage.instances[instanceID]
	if !ok {
		return launcher.InstanceInfo{}, launcher.ErrNotExist
	}

	return instance, nil
}

func (storage *testStorage) GetRunningInstances() (instances []launcher.InstanceInfo, err error) {
	storage.RLock()
	defer storage.RUnlock()

	for _, instance := range storage.instances {
		if instance.Running {
			instances = append(instances, instance)
		}
	}

	return instances, nil
}

func (storage *testStorage) GetSubjectInstances(subjectID string) (instances []launcher.InstanceInfo, err error) {
	storage.RLock()
	defer storage.RUnlock()

	for _, instance := range storage.instances {
		if instance.SubjectID == subjectID {
			instances = append(instances, instance)
		}
	}

	return instances, nil
}

/***********************************************************************************************************************
 * testServiceProvider
 **********************************************************************************************************************/

func newTestServiceProvider() *testServiceProvider {
	return &testServiceProvider{
		services: make(map[string]servicemanager.ServiceInfo),
	}
}

func (provider *testServiceProvider) GetServiceInfo(serviceID string) (servicemanager.ServiceInfo, error) {
	service, ok := provider.services[serviceID]
	if !ok {
		return servicemanager.ServiceInfo{}, servicemanager.ErrNotExist
	}

	return service, nil
}

func (provider *testServiceProvider) fromTestInstances(testInstances []testInstance) error {
	provider.services = make(map[string]servicemanager.ServiceInfo)

	for _, testInstance := range testInstances {
		provider.services[testInstance.serviceID] = servicemanager.ServiceInfo{
			ServiceID:  testInstance.serviceID,
			AosVersion: testInstance.serviceVersion,
		}
	}

	return nil
}

/***********************************************************************************************************************
 * testRunner
 **********************************************************************************************************************/

func newTestRunner(startFunc func(instanceID string) runner.InstanceStatus,
	stopFunc func(instanceID string) error,
) *testRunner {
	return &testRunner{
		statusChannel: make(chan []runner.InstanceStatus, 1),
		startFunc:     startFunc,
		stopFunc:      stopFunc,
	}
}

func (instanceRunner *testRunner) StartInstance(
	instanceID, runtimeDir string, params runner.StartInstanceParams,
) runner.InstanceStatus {
	instanceRunner.Lock()
	defer instanceRunner.Unlock()

	if instanceRunner.startFunc == nil {
		return runner.InstanceStatus{
			InstanceID: instanceID,
			State:      cloudprotocol.InstanceStateActive,
		}
	}

	return instanceRunner.startFunc(instanceID)
}

func (instanceRunner *testRunner) StopInstance(instanceID string) error {
	instanceRunner.Lock()
	defer instanceRunner.Unlock()

	if instanceRunner.stopFunc == nil {
		return nil
	}

	return instanceRunner.stopFunc(instanceID)
}

func (instanceRunner *testRunner) InstanceStatusChannel() <-chan []runner.InstanceStatus {
	return instanceRunner.statusChannel
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func compareRuntimeStatus(status1, status2 launcher.RuntimeStatus) error {
	switch {
	case status1.RunStatus == nil && status2.RunStatus == nil:

	case status1.RunStatus != nil && status2.RunStatus != nil:
		if !compareArrays(len(status1.RunStatus.UnitSubjects), len(status2.RunStatus.UnitSubjects),
			func(index1, index2 int) bool {
				return status1.RunStatus.UnitSubjects[index1] == status2.RunStatus.UnitSubjects[index2]
			}) {
			return aoserrors.New("unit subjects mismatch")
		}

		if !compareArrays(len(status1.RunStatus.Instances), len(status2.RunStatus.Instances),
			func(index1, index2 int) bool {
				return isInstanceStatusesEqual(status1.RunStatus.Instances[index1], status2.RunStatus.Instances[index2])
			}) {
			return aoserrors.New("run instances statuses mismatch")
		}

		if !compareArrays(len(status1.RunStatus.ErrorServices), len(status2.RunStatus.ErrorServices),
			func(index1, index2 int) bool {
				return isServiceStatusesEqual(
					status1.RunStatus.ErrorServices[index1],
					status2.RunStatus.ErrorServices[index2])
			}) {
			return aoserrors.New("error services mismatch")
		}

	case status1.RunStatus == nil || status2.RunStatus == nil:
		return aoserrors.New("run status mismatch")
	}

	switch {
	case status1.UpdateStatus == nil && status2.UpdateStatus == nil:

	case status1.UpdateStatus != nil && status2.UpdateStatus != nil:
		if !compareArrays(len(status1.UpdateStatus.Instances), len(status2.UpdateStatus.Instances),
			func(index1, index2 int) bool {
				return isInstanceStatusesEqual(status1.UpdateStatus.Instances[index1],
					status2.UpdateStatus.Instances[index2])
			}) {
			return aoserrors.New("update instances statuses mismatch")
		}

	case status1.UpdateStatus == nil || status2.UpdateStatus == nil:
		return aoserrors.New("update status mismatch")
	}

	return nil
}

func isErrorInfosEqual(info1, info2 *cloudprotocol.ErrorInfo) bool {
	switch {
	case info1 == nil && info2 == nil:

	case info1 != nil && info2 != nil:
		if info1.AosCode != info2.AosCode || info1.ExitCode != info2.ExitCode ||
			!(strings.HasPrefix(info1.Message, info2.Message) || (strings.HasPrefix(info2.Message, info1.Message))) {
			return false
		}

	case info1 == nil || info2 == nil:
		return false
	}

	return true
}

func isInstanceStatusesEqual(status1, status2 cloudprotocol.InstanceStatus) bool {
	if !isErrorInfosEqual(status1.ErrorInfo, status2.ErrorInfo) {
		return false
	}

	status1.ErrorInfo, status2.ErrorInfo = nil, nil

	return status1 == status2
}

func isServiceStatusesEqual(status1, status2 cloudprotocol.ServiceStatus) bool {
	if !isErrorInfosEqual(status1.ErrorInfo, status2.ErrorInfo) {
		return false
	}

	status1.ErrorInfo, status2.ErrorInfo = nil, nil

	return status1 == status2
}

func compareArrays(len1, len2 int, isEqual func(index1, index2 int) bool) bool {
	if len1 != len2 {
		return false
	}

loop1:
	for i := 0; i < len1; i++ {
		for j := 0; j < len2; j++ {
			if isEqual(i, j) {
				continue loop1
			}
		}

		return false
	}

loop2:

	for j := 0; j < len2; j++ {
		for i := 0; i < len1; i++ {
			if isEqual(i, j) {
				continue loop2
			}
		}

		return false
	}

	return true
}

func checkRuntimeStatus(refStatus launcher.RuntimeStatus, statusChannel <-chan launcher.RuntimeStatus) error {
	select {
	case runtimeStatus := <-statusChannel:
		if err := compareRuntimeStatus(refStatus, runtimeStatus); err != nil {
			return err
		}

	case <-time.After(5 * time.Second):
		return aoserrors.New("Wait for runtime status timeout")
	}

	return nil
}

func createInstancesInfos(testInstances []testInstance) (instances []cloudprotocol.InstanceInfo) {
	for _, testInstance := range testInstances {
		instances = append(instances, cloudprotocol.InstanceInfo{
			ServiceID:    testInstance.serviceID,
			SubjectID:    testInstance.subjectID,
			NumInstances: testInstance.numInstances,
		})
	}

	return instances
}

func createInstancesStatuses(testInstances []testInstance) (instances []cloudprotocol.InstanceStatus) {
	for _, testInstance := range testInstances {
		for index := uint64(0); index < testInstance.numInstances; index++ {
			instanceStatus := cloudprotocol.InstanceStatus{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: testInstance.serviceID,
					SubjectID: testInstance.subjectID,
					Instance:  index,
				},
				AosVersion: testInstance.serviceVersion,
				RunState:   cloudprotocol.InstanceStateActive,
			}

			if index < uint64(len(testInstance.err)) && testInstance.err[index] != nil {
				instanceStatus.RunState = cloudprotocol.InstanceStateFailed
				instanceStatus.ErrorInfo = &cloudprotocol.ErrorInfo{
					Message: testInstance.err[index].Error(),
				}
			}

			instances = append(instances, instanceStatus)
		}
	}

	return instances
}

func getRunnerStatus(instanceID string, testInstances []testInstance, storage *testStorage) runner.InstanceStatus {
	activeStatus := runner.InstanceStatus{InstanceID: instanceID, State: cloudprotocol.InstanceStateActive}

	instance, err := storage.GetInstanceByID(instanceID)
	if err != nil {
		return activeStatus
	}

	for _, testInstance := range testInstances {
		if testInstance.serviceID == instance.ServiceID && testInstance.subjectID == instance.SubjectID &&
			instance.Instance < uint64(len(testInstance.err)) {
			if testInstance.err[instance.Instance] != nil {
				return runner.InstanceStatus{
					InstanceID: instanceID,
					State:      cloudprotocol.InstanceStateFailed,
					Err:        testInstance.err[instance.Instance],
				}
			}

			return activeStatus
		}
	}

	return activeStatus
}

func createRunStatus(
	storage *testStorage, testInstances []testInstance,
) (runStatus []runner.InstanceStatus, err error) {
	for _, testInstance := range testInstances {
		for i := uint64(0); i < testInstance.numInstances; i++ {
			instance, err := storage.GetInstanceByIdent(cloudprotocol.InstanceIdent{
				ServiceID: testInstance.serviceID,
				SubjectID: testInstance.subjectID,
				Instance:  i,
			})
			if err != nil {
				return nil, aoserrors.Wrap(err)
			}

			var runError error

			if i < uint64(len(testInstance.err)) {
				runError = testInstance.err[i]
			}

			state := cloudprotocol.InstanceStateActive

			if runError != nil {
				state = cloudprotocol.InstanceStateFailed
			}

			runStatus = append(runStatus, runner.InstanceStatus{
				InstanceID: instance.InstanceID,
				State:      state,
				Err:        runError,
			})
		}
	}

	return runStatus, nil
}
