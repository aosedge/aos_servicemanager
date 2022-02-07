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
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/google/uuid"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/launcher"
	"github.com/aoscloud/aos_servicemanager/monitoring"
	"github.com/aoscloud/aos_servicemanager/networkmanager"
	"github.com/aoscloud/aos_servicemanager/resourcemanager"
	"github.com/aoscloud/aos_servicemanager/runner"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
	"github.com/aoscloud/aos_servicemanager/storagestate"
)

/***********************************************************************************************************************
 * Const
 **********************************************************************************************************************/

const (
	imageConfigFile   = "image.json"
	serviceConfigFile = "service.json"
	runtimeConfigFile = "config.json"
	servicesDir       = "services"
	storagesDir       = "storages"
	statesDir         = "states"
)

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

type testResourceManager struct {
	allocatedDevices map[string][]string
	devices          map[string]resourcemanager.DeviceInfo
	resources        map[string]resourcemanager.ResourceInfo
}

type testNetworkManager struct {
	sync.Mutex
	instances map[string]networkmanager.NetworkParams
}

type testRegistrar struct {
	sync.Mutex
	secrets map[string]string
}

type testStorageStateProvider struct {
	sync.Mutex
	testInstances []testInstance
	infos         map[string]storageStateInfo
	stateChannel  chan storagestate.StateChangedInfo
}

type testInstanceMonitor struct {
	sync.Mutex
	instances map[string]monitoring.MonitorParams
}

type storageStateInfo struct {
	storagestate.SetupParams
	storagePath, statePath string
}

type testInstance struct {
	serviceID       string
	serviceVersion  uint64
	serviceProvider string
	subjectID       string
	serviceGID      int
	numInstances    uint64
	unitSubject     bool
	imageConfig     *imagespec.Image
	serviceConfig   *serviceConfig
	stateChecksum   [][]byte
	err             []error
}

type serviceDevice struct {
	Name        string `json:"name"`
	Permissions string `json:"permissions"`
}

type serviceQuotas struct {
	CPULimit      *uint64 `json:"cpuLimit,omitempty"`
	RAMLimit      *uint64 `json:"ramLimit,omitempty"`
	PIDsLimit     *uint64 `json:"pidsLimit,omitempty"`
	NoFileLimit   *uint64 `json:"noFileLimit,omitempty"`
	TmpLimit      *uint64 `json:"tmpLimit,omitempty"`
	StateLimit    *uint64 `json:"stateLimit,omitempty"`
	StorageLimit  *uint64 `json:"storageLimit,omitempty"`
	UploadSpeed   *uint64 `json:"uploadSpeed,omitempty"`
	DownloadSpeed *uint64 `json:"downloadSpeed,omitempty"`
	UploadLimit   *uint64 `json:"uploadLimit,omitempty"`
	DownloadLimit *uint64 `json:"downloadLimit,omitempty"`
}

type serviceConfig struct {
	Created            time.Time                    `json:"created"`
	Author             string                       `json:"author"`
	Hostname           *string                      `json:"hostname,omitempty"`
	Sysctl             map[string]string            `json:"sysctl,omitempty"`
	ServiceTTL         *uint64                      `json:"serviceTtl,omitempty"`
	Quotas             serviceQuotas                `json:"quotas"`
	AllowedConnections map[string]struct{}          `json:"allowedConnections,omitempty"`
	Devices            []serviceDevice              `json:"devices,omitempty"`
	Resources          []string                     `json:"resources,omitempty"`
	Permissions        map[string]map[string]string `json:"permissions,omitempty"`
	AlertRules         *aostypes.ServiceAlertRules  `json:"alertRules,omitempty"`
}

type testDevice struct {
	hostPath      string
	containerPath string
	deviceType    string
	major         int64
	minor         int64
	uid           uint32
	gid           uint32
	mode          os.FileMode
	permissions   string
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var tmpDir string

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
 * Main
 **********************************************************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Setup error: %v", err)
	}

	ret := m.Run()

	if err := cleanup(); err != nil {
		log.Errorf("Cleanup error: %v", err)
	}

	os.Exit(ret)
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

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider, instanceRunner,
		newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(), newTestStorageStateProvider(nil),
		newTestInstanceMonitor())
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
	storageStateProvider := newTestStorageStateProvider(nil)

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider, instanceRunner,
		newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(), storageStateProvider,
		newTestInstanceMonitor())
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

	// Check update status on runtime state changed

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

	// Check checksum on runtime state changed

	newChecksum := []byte("new checksum")

	changedInstances = []testInstance{
		{
			serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 1,
			stateChecksum: [][]byte{newChecksum}, err: []error{errors.New("some error")}, // nolint:goerr113
		},
	}

	instance, err := storage.GetInstanceByIdent(cloudprotocol.InstanceIdent{
		ServiceID: changedInstances[0].serviceID, SubjectID: changedInstances[0].subjectID, Instance: 0,
	})
	if err != nil {
		t.Fatalf("Can't get instance info: %v", err)
	}

	storageStateProvider.stateChannel <- storagestate.StateChangedInfo{
		InstanceID: instance.InstanceID, Checksum: newChecksum,
	}

	// Wait for state event processed by launcher
	time.Sleep(1 * time.Second)

	if runStatus, err = createRunStatus(storage, changedInstances); err != nil {
		t.Fatalf("Can't create run status: %v", err)
	}

	instanceRunner.statusChannel <- runStatus

	runtimeStatus = launcher.RuntimeStatus{
		UpdateStatus: &launcher.UpdateInstancesStatus{Instances: createInstancesStatuses(changedInstances)},
	}

	if err = checkRuntimeStatus(runtimeStatus, testLauncher.RuntimeStatusChannel()); err != nil {
		t.Fatalf("Check runtime status error: %v", err)
	}
}

func TestSendCurrentRuntimeStatus(t *testing.T) {
	serviceProvider := newTestServiceProvider()

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, newTestStorage(), serviceProvider,
		newTestRunner(nil, nil), newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(),
		newTestStorageStateProvider(nil), newTestInstanceMonitor())
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

func TestRestartInstances(t *testing.T) {
	type startStopCount struct {
		startCount int
		stopCount  int
	}

	restartMap := make(map[string]startStopCount)

	serviceProvider := newTestServiceProvider()
	instanceRunner := newTestRunner(
		func(instanceID string) runner.InstanceStatus {
			status := runner.InstanceStatus{InstanceID: instanceID, State: cloudprotocol.InstanceStateActive}

			currentValue := restartMap[instanceID]

			currentValue.startCount++
			restartMap[instanceID] = currentValue

			return status
		},
		func(instanceID string) error {
			currentValue := restartMap[instanceID]

			currentValue.stopCount++
			restartMap[instanceID] = currentValue

			return nil
		},
	)

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, newTestStorage(), serviceProvider,
		instanceRunner, newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(),
		newTestStorageStateProvider(nil), newTestInstanceMonitor())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	testInstances := []testInstance{
		{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
		{serviceID: "service0", serviceVersion: 0, subjectID: "subject1", numInstances: 2},
		{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 1},
		{serviceID: "service1", serviceVersion: 1, subjectID: "subject2", numInstances: 2},
		{serviceID: "service2", serviceVersion: 2, subjectID: "subject3", numInstances: 2},
		{serviceID: "service2", serviceVersion: 2, subjectID: "subject4", numInstances: 3},
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

	if err = testLauncher.RestartInstances(); err != nil {
		t.Errorf("Can't stop instances: %v", err)
	}

	if err = checkRuntimeStatus(runtimeStatus, testLauncher.RuntimeStatusChannel()); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	if len(restartMap) != 13 {
		t.Errorf("Wrong running instances count: %d", len(restartMap))
	}

	for instanceID, counters := range restartMap {
		if counters.stopCount != 1 {
			t.Errorf("Wrong stop count for instance %s: %d", instanceID, counters.stopCount)
		}

		if counters.startCount != 2 {
			t.Errorf("Wrong start count for instance %s: %d", instanceID, counters.startCount)
		}
	}
}

func TestSubjectsChanged(t *testing.T) {
	type testData struct {
		initialInstances []testInstance
		subjects         []string
		resultInstances  []testInstance
	}

	data := []testData{
		{
			initialInstances: []testInstance{
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject1", numInstances: 2, unitSubject: true},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 1, unitSubject: true},
				{serviceID: "service2", serviceVersion: 2, subjectID: "subject2", numInstances: 4, unitSubject: true},
			},
			subjects: []string{"subject2"},
			resultInstances: []testInstance{
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
				{serviceID: "service2", serviceVersion: 2, subjectID: "subject2", numInstances: 4},
			},
		},
		{
			initialInstances: []testInstance{
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject1", numInstances: 2, unitSubject: true},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 1, unitSubject: true},
				{serviceID: "service2", serviceVersion: 2, subjectID: "subject2", numInstances: 4, unitSubject: true},
			},
			subjects: []string{"subject1"},
			resultInstances: []testInstance{
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject1", numInstances: 2},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 1},
			},
		},
		{
			initialInstances: []testInstance{
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject1", numInstances: 2, unitSubject: true},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 1, unitSubject: true},
				{serviceID: "service2", serviceVersion: 2, subjectID: "subject2", numInstances: 4, unitSubject: true},
			},
			subjects: []string{"subject1", "subject2"},
			resultInstances: []testInstance{
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject1", numInstances: 2},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 1},
				{serviceID: "service2", serviceVersion: 2, subjectID: "subject2", numInstances: 4},
			},
		},
		{
			initialInstances: []testInstance{
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject1", numInstances: 2, unitSubject: true},
				{serviceID: "service1", serviceVersion: 1, subjectID: "subject1", numInstances: 1, unitSubject: true},
				{serviceID: "service2", serviceVersion: 2, subjectID: "subject2", numInstances: 4, unitSubject: true},
			},
			subjects: []string{"subject3"},
			resultInstances: []testInstance{
				{serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 3},
			},
		},
	}

	storage := newTestStorage()
	serviceProvider := newTestServiceProvider()

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider,
		newTestRunner(nil, nil), newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(),
		newTestStorageStateProvider(nil), newTestInstanceMonitor())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	for i, item := range data {
		t.Logf("Subjects changed: %d", i)

		if err = serviceProvider.fromTestInstances(item.initialInstances); err != nil {
			t.Fatalf("Can't create test services: %v", err)
		}

		storage.fromTestInstances(item.initialInstances, true)

		if err = testLauncher.SubjectsChanged(item.subjects); err != nil {
			t.Fatalf("Subjects changed error: %v", err)
		}

		select {
		case runtimeStatus := <-testLauncher.RuntimeStatusChannel():
			runStatus := &launcher.RunInstancesStatus{
				UnitSubjects: item.subjects,
				Instances:    createInstancesStatuses(item.resultInstances),
			}

			if err = compareRuntimeStatus(launcher.RuntimeStatus{RunStatus: runStatus}, runtimeStatus); err != nil {
				t.Errorf("Compare runtime status failed: %v", err)
			}

		case <-time.After(5 * time.Second):
			t.Error("Wait for runtime status timeout")
		}
	}
}

func TestHostFSDir(t *testing.T) {
	hostFSBinds := []string{"bin", "sbin", "lib", "lib64", "usr"}

	testLauncher, err := launcher.New(&config.Config{
		WorkingDir: tmpDir,
		HostBinds:  hostFSBinds,
	},
		newTestStorage(), newTestServiceProvider(), newTestRunner(nil, nil), newTestResourceManager(),
		newTestNetworkManager(), newTestRegistrar(), newTestStorageStateProvider(nil), newTestInstanceMonitor())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	rootItems, err := ioutil.ReadDir("/")
	if err != nil {
		t.Fatalf("Can't read root dir: %v", err)
	}

	whiteoutItems, err := ioutil.ReadDir(filepath.Join(tmpDir, "hostfs", "whiteouts"))
	if err != nil {
		t.Fatalf("Can't read root dir: %v", err)
	}

	for _, rootItem := range rootItems {
		bind := false

		for _, hostFSBind := range hostFSBinds {
			if rootItem.Name() == hostFSBind {
				bind = true
				break
			}
		}

		whiteout := false

		for i, whiteoutItem := range whiteoutItems {
			if rootItem.Name() == whiteoutItem.Name() {
				if whiteoutItem.Mode() != 0o410000000 {
					t.Errorf("Wrong white out mode 0o%o", whiteoutItem.Mode())
				}

				whiteoutItems = append(whiteoutItems[:i], whiteoutItems[i+1:]...)
				whiteout = true

				break
			}
		}

		if bind && whiteout {
			t.Errorf("Bind item %s should not be whiteouted", rootItem.Name())
		}

		if !bind && !whiteout {
			t.Errorf("Not bind item %s should be whiteouted", rootItem.Name())
		}
	}
}

func TestRuntimeSpec(t *testing.T) {
	serviceProvider := newTestServiceProvider()
	storage := newTestStorage()
	resourceManager := newTestResourceManager()
	networkManager := newTestNetworkManager()
	testRegistrar := newTestRegistrar()
	storageStateProvider := newTestStorageStateProvider(nil)

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider,
		newTestRunner(nil, nil), resourceManager, networkManager, testRegistrar, storageStateProvider,
		newTestInstanceMonitor())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	testInstaces := []testInstance{
		{
			serviceID: "service0", serviceVersion: 0, subjectID: "subject0", numInstances: 1, serviceGID: 9999,
			imageConfig: &imagespec.Image{
				OS: "linux",
				Config: imagespec.ImageConfig{
					Entrypoint: []string{"entry1", "entry2", "entry3"},
					Cmd:        []string{"cmd1", "cmd2", "cmd3"},
					WorkingDir: "/working/dir",
					Env:        []string{"env1=val1", "env2=val2", "env3=val3"},
				},
			},
			serviceConfig: &serviceConfig{
				Hostname: newString("testHostName"),
				Sysctl:   map[string]string{"key1": "val1", "key2": "val2", "key3": "val3"},
				Quotas: serviceQuotas{
					CPULimit:    newUint64(42),
					RAMLimit:    newUint64(1024),
					PIDsLimit:   newUint64(10),
					NoFileLimit: newUint64(3),
					TmpLimit:    newUint64(512),
				},
				Devices: []serviceDevice{
					{Name: "input", Permissions: "r"},
					{Name: "video", Permissions: "rw"},
					{Name: "sound", Permissions: "rwm"},
				},
				Resources:   []string{"resource1", "resource2", "resource3"},
				Permissions: map[string]map[string]string{"perm1": {"key1": "val1"}},
			},
		},
	}

	hostGroups, err := getSystemGroups(9)
	if err != nil {
		t.Fatalf("Can't get system groups: %v", err)
	}

	// Create devices

	hostDeviceDir := filepath.Join(tmpDir, "dev")

	if err = os.RemoveAll(hostDeviceDir); err != nil {
		t.Fatalf("Can't remove host device dir: %v", err)
	}

	resourceManager.addDevice(resourcemanager.DeviceInfo{
		Name: "input", HostDevices: []string{fmt.Sprintf("%s/input:/dev/input", hostDeviceDir)},
		Groups: hostGroups[:2],
	})
	resourceManager.addDevice(resourcemanager.DeviceInfo{
		Name: "video", HostDevices: []string{fmt.Sprintf("%s/video:/dev/video", hostDeviceDir)},
		Groups: hostGroups[2:4],
	})
	resourceManager.addDevice(resourcemanager.DeviceInfo{
		Name: "sound", HostDevices: []string{fmt.Sprintf("%s/sound:/dev/sound", hostDeviceDir)},
		Groups: hostGroups[4:6],
	})

	testDevices := []testDevice{
		{
			hostPath: filepath.Join(hostDeviceDir, "input"), containerPath: "/dev/input",
			deviceType: "c", major: 1, minor: 1, uid: 0, gid: 1, mode: 0o644, permissions: "r",
		},
		{
			hostPath: filepath.Join(hostDeviceDir, "video", "video1"), containerPath: "/dev/video/video1",
			deviceType: "b", major: 2, minor: 1, uid: 1, gid: 1, mode: 0o640, permissions: "rw",
		},
		{
			hostPath: filepath.Join(hostDeviceDir, "video", "video2"), containerPath: "/dev/video/video2",
			deviceType: "b", major: 2, minor: 2, uid: 1, gid: 2, mode: 0o640, permissions: "rw",
		},
		{
			hostPath: filepath.Join(hostDeviceDir, "video", "video3"), containerPath: "/dev/video/video3",
			deviceType: "b", major: 2, minor: 3, uid: 1, gid: 3, mode: 0o640, permissions: "rw",
		},
		{
			hostPath: filepath.Join(hostDeviceDir, "real_sound", "sound1"), containerPath: "/dev/sound/sound1",
			deviceType: "b", major: 3, minor: 1, uid: 2, gid: 1, mode: 0o600, permissions: "rwm",
		},
		{
			hostPath: filepath.Join(hostDeviceDir, "real_sound", "sound2"), containerPath: "/dev/sound/sound2",
			deviceType: "b", major: 3, minor: 2, uid: 2, gid: 2, mode: 0o600, permissions: "rwm",
		},
	}

	if err = createTestDevices(testDevices); err != nil {
		t.Fatalf("Can't create test devices: %v", err)
	}

	// create symlink to test if it is resolved
	if err := os.Symlink(filepath.Join(hostDeviceDir, "real_sound"),
		filepath.Join(hostDeviceDir, "sound")); err != nil {
		t.Fatalf("Can't create symlink: %v", err)
	}

	// Create resource

	hostDirs := filepath.Join(tmpDir, "mount")

	hostMounts := []resourcemanager.FileSystemMount{
		{Source: filepath.Join(hostDirs, "dir0"), Destination: "/dir0", Type: "bind", Options: []string{"opt0, opt1"}},
		{Source: filepath.Join(hostDirs, "dir1"), Destination: "/dir1", Type: "bind", Options: []string{"opt2, opt3"}},
		{Source: filepath.Join(hostDirs, "dir2"), Destination: "/dir2", Type: "bind", Options: []string{"opt4, opt5"}},
		{Source: filepath.Join(hostDirs, "dir3"), Destination: "/dir3", Type: "bind", Options: []string{"opt6, opt7"}},
		{Source: filepath.Join(hostDirs, "dir4"), Destination: "/dir4", Type: "bind", Options: []string{"opt8, opt9"}},
	}

	envVars := []string{"var0=0", "var1=1", "var2=2", "var3=3", "var4=4", "var5=5", "var6=6", "var7=7"}

	resourceManager.addResource(resourcemanager.ResourceInfo{
		Name: "resource1", Mounts: hostMounts[:2], Env: envVars[:2], Groups: hostGroups[6:7],
	})
	resourceManager.addResource(resourcemanager.ResourceInfo{
		Name: "resource2", Mounts: hostMounts[2:4], Env: envVars[2:5], Groups: hostGroups[7:8],
	})
	resourceManager.addResource(resourcemanager.ResourceInfo{
		Name: "resource3", Mounts: hostMounts[4:], Env: envVars[5:], Groups: hostGroups[8:],
	})

	if err = serviceProvider.fromTestInstances(testInstaces); err != nil {
		t.Fatalf("Can't create test services: %v", err)
	}

	if err = testLauncher.RunInstances(createInstancesInfos(testInstaces)); err != nil {
		t.Fatalf("Can't run instances: %v", err)
	}

	if err = checkRuntimeStatus(launcher.RuntimeStatus{
		RunStatus: &launcher.RunInstancesStatus{Instances: createInstancesStatuses(testInstaces)},
	}, testLauncher.RuntimeStatusChannel()); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	instance, err := storage.GetInstanceByIdent(cloudprotocol.InstanceIdent{
		ServiceID: testInstaces[0].serviceID, SubjectID: testInstaces[0].subjectID, Instance: 0,
	})
	if err != nil {
		t.Fatalf("Can't get instance info: %v", err)
	}

	runtimeSpec, err := getInstanceRuntimeSpec(instance.InstanceID)
	if err != nil {
		t.Fatalf("Can't get instance runtime spec: %v", err)
	}

	// Check terminal is false

	if runtimeSpec.Process.Terminal {
		t.Error("Terminal setting should be disabled")
	}

	// Check args

	expectedArgs := make([]string, 0,
		len(testInstaces[0].imageConfig.Config.Entrypoint)+len(testInstaces[0].imageConfig.Config.Cmd))

	expectedArgs = append(expectedArgs, testInstaces[0].imageConfig.Config.Entrypoint...)
	expectedArgs = append(expectedArgs, testInstaces[0].imageConfig.Config.Cmd...)

	if !compareArrays(len(expectedArgs), len(runtimeSpec.Process.Args), func(index1, index2 int) bool {
		return expectedArgs[index1] == runtimeSpec.Process.Args[index2]
	}) {
		t.Errorf("Wrong args value: %v", runtimeSpec.Process.Args)
	}

	// Check working dir

	if runtimeSpec.Process.Cwd != testInstaces[0].imageConfig.Config.WorkingDir {
		t.Errorf("Wrong working dir value: %s", runtimeSpec.Process.Cwd)
	}

	// Check host name

	if runtimeSpec.Hostname != *testInstaces[0].serviceConfig.Hostname {
		t.Errorf("Wrong host name value: %s", runtimeSpec.Hostname)
	}

	// Check sysctl

	if !reflect.DeepEqual(runtimeSpec.Linux.Sysctl, testInstaces[0].serviceConfig.Sysctl) {
		t.Errorf("Wrong sysctl value: %s", runtimeSpec.Linux.Sysctl)
	}

	// Check CPU limit

	if *runtimeSpec.Linux.Resources.CPU.Period != 100000 {
		t.Errorf("Wrong CPU period value: %d", *runtimeSpec.Linux.Resources.CPU.Period)
	}

	if *runtimeSpec.Linux.Resources.CPU.Quota !=
		int64(*testInstaces[0].serviceConfig.Quotas.CPULimit*(*runtimeSpec.Linux.Resources.CPU.Period)/100) {
		t.Errorf("Wrong CPU quota value: %d", *runtimeSpec.Linux.Resources.CPU.Quota)
	}

	// Check RAM limit

	if *runtimeSpec.Linux.Resources.Memory.Limit != int64(*testInstaces[0].serviceConfig.Quotas.RAMLimit) {
		t.Errorf("Wrong RAM limit value: %d", *runtimeSpec.Linux.Resources.Memory.Limit)
	}

	// Check PIDs limit

	if runtimeSpec.Linux.Resources.Pids.Limit != int64(*testInstaces[0].serviceConfig.Quotas.PIDsLimit) {
		t.Errorf("Wrong PIDs limit value: %d", runtimeSpec.Linux.Resources.Pids.Limit)
	}

	// Check RLimits
	expectedRLimits := []runtimespec.POSIXRlimit{
		{
			Type: "RLIMIT_NPROC",
			Hard: *testInstaces[0].serviceConfig.Quotas.PIDsLimit,
			Soft: *testInstaces[0].serviceConfig.Quotas.PIDsLimit,
		},
		{
			Type: "RLIMIT_NOFILE",
			Hard: *testInstaces[0].serviceConfig.Quotas.NoFileLimit,
			Soft: *testInstaces[0].serviceConfig.Quotas.NoFileLimit,
		},
	}

	if !compareArrays(len(expectedRLimits), len(runtimeSpec.Process.Rlimits), func(index1, index2 int) bool {
		return expectedRLimits[index1] == runtimeSpec.Process.Rlimits[index2]
	}) {
		t.Errorf("Wrong resource limits value: %v", runtimeSpec.Process.Rlimits)
	}

	// Check network namespace

	namespaceFound := false

	for _, namespace := range runtimeSpec.Linux.Namespaces {
		if namespace.Type == runtimespec.NetworkNamespace {
			if namespace.Path != networkManager.GetNetnsPath(instance.InstanceID) {
				t.Errorf("Wrong network namespace path: %s", namespace.Path)
			}

			namespaceFound = true
		}
	}

	if !namespaceFound {
		t.Error("Network namespace not found")
	}

	// Check devices

	expectedDevices := createSpecDevices(testDevices)

	if !compareArrays(len(expectedDevices), len(runtimeSpec.Linux.Devices), func(index1, index2 int) bool {
		val1, val2 := expectedDevices[index1], runtimeSpec.Linux.Devices[index2]

		return val1.Path == val2.Path && val1.Type == val2.Type &&
			val1.Major == val2.Major && val1.Minor == val2.Minor &&
			*val1.FileMode == *val2.FileMode && *val1.UID == *val2.UID && *val1.GID == *val2.GID
	}) {
		t.Errorf("Wrong linux devices value: %v", runtimeSpec.Linux.Devices)
	}

	expectedCgroupDevices := createCgroupDevices(testDevices)

	if !compareArrays(len(expectedCgroupDevices), len(runtimeSpec.Linux.Resources.Devices),
		func(index1, index2 int) bool {
			val1, val2 := expectedCgroupDevices[index1], runtimeSpec.Linux.Resources.Devices[index2]

			if val1.Allow != val2.Allow || val1.Type != val2.Type || val1.Access != val2.Access {
				return false
			}

			if val1.Major != nil && val2.Major != nil {
				if *val1.Major != *val2.Major {
					return false
				}
			}

			if val1.Minor != nil && val2.Minor != nil {
				if *val1.Minor != *val2.Minor {
					return false
				}
			}

			return true
		}) {
		t.Errorf("Wrong cgroup devices value: %v", runtimeSpec.Linux.Resources.Devices)
	}

	// Check mounts

	expectedMounts := createSpecMounts(hostMounts)
	expectedMounts = append(expectedMounts, runtimespec.Mount{
		Source:      "tmpfs",
		Destination: "/tmp",
		Type:        "tmpfs",
		Options: []string{
			"nosuid", "strictatime", "mode=1777",
			"size=" + strconv.FormatUint(*testInstaces[0].serviceConfig.Quotas.TmpLimit, 10),
		},
	})
	expectedMounts = append(expectedMounts, runtimespec.Mount{
		Source:      storageStateProvider.infos[instance.InstanceID].statePath,
		Destination: "/state.dat",
		Type:        "bind",
		Options:     []string{"bind", "rw"},
	})

	if !compareArrays(len(expectedMounts), len(runtimeSpec.Mounts), func(index1, index2 int) bool {
		return reflect.DeepEqual(expectedMounts[index1], runtimeSpec.Mounts[index2])
	}) {
		t.Errorf("Wrong mounts value: %v", runtimeSpec.Mounts)
	}

	// Check env vars

	envVars = append(envVars, testInstaces[0].imageConfig.Config.Env...)
	envVars = append(envVars, getAosEnvVars(instance)...)
	envVars = append(envVars, fmt.Sprintf("AOS_SECRET=%s", testRegistrar.secrets[instance.InstanceID]))

	if !compareArrays(len(envVars), len(runtimeSpec.Process.Env), func(index1, index2 int) bool {
		return envVars[index1] == runtimeSpec.Process.Env[index2]
	}) {
		t.Errorf("Wrong env variables value: %v", runtimeSpec.Process.Env)
	}

	// Check UID/GID

	if runtimeSpec.Process.User.UID != uint32(instance.UID) {
		t.Errorf("Wrong UID: %d", runtimeSpec.Process.User.UID)
	}

	if runtimeSpec.Process.User.GID != uint32(testInstaces[0].serviceGID) {
		t.Errorf("Wrong GID: %d", runtimeSpec.Process.User.GID)
	}

	// Check additional groups

	expectedGroups, err := createAdditionalGroups(hostGroups)
	if err != nil {
		t.Fatalf("Can't create additional groups: %v", err)
	}

	if !compareArrays(len(expectedGroups), len(runtimeSpec.Process.User.AdditionalGids), func(index1, index2 int) bool {
		return expectedGroups[index1] == runtimeSpec.Process.User.AdditionalGids[index2]
	}) {
		t.Errorf("Wrong additional GIDs value: %v", runtimeSpec.Process.User.AdditionalGids)
	}
}

func TestRuntimeEnvironment(t *testing.T) {
	testInstaces := []testInstance{
		{
			serviceID: "service0", serviceVersion: 0, serviceProvider: "sp0", serviceGID: 1234,
			subjectID: "subject0", numInstances: 1,
			imageConfig: &imagespec.Image{
				OS: "linux",
				Config: imagespec.ImageConfig{
					ExposedPorts: map[string]struct{}{"port0": {}, "port1": {}, "port2": {}},
				},
			},
			serviceConfig: &serviceConfig{
				Hostname:           newString("host1"),
				Permissions:        map[string]map[string]string{"perm1": {"key1": "val1"}},
				AllowedConnections: map[string]struct{}{"connection0": {}, "connection1": {}, "connection2": {}},
				Quotas: serviceQuotas{
					DownloadSpeed: newUint64(4096),
					UploadSpeed:   newUint64(8192),
					DownloadLimit: newUint64(16384),
					UploadLimit:   newUint64(32768),
					StorageLimit:  newUint64(2048),
					StateLimit:    newUint64(1024),
				},
				Resources: []string{"resource0", "resource1", "resource2"},
				Devices:   []serviceDevice{{Name: "device0"}, {Name: "device1"}, {Name: "device2"}},
				AlertRules: &aostypes.ServiceAlertRules{
					RAM: &aostypes.AlertRule{
						MinTimeout:   aostypes.Duration{Duration: 1 * time.Second},
						MinThreshold: 10, MaxThreshold: 100,
					},
					CPU: &aostypes.AlertRule{
						MinTimeout:   aostypes.Duration{Duration: 2 * time.Second},
						MinThreshold: 20, MaxThreshold: 200,
					},
					UsedDisk: &aostypes.AlertRule{
						MinTimeout:   aostypes.Duration{Duration: 3 * time.Second},
						MinThreshold: 30, MaxThreshold: 300,
					},
					InTraffic: &aostypes.AlertRule{
						MinTimeout:   aostypes.Duration{Duration: 4 * time.Second},
						MinThreshold: 40, MaxThreshold: 400,
					},
					OutTraffic: &aostypes.AlertRule{
						MinTimeout:   aostypes.Duration{Duration: 5 * time.Second},
						MinThreshold: 50, MaxThreshold: 500,
					},
				},
			},
		},
	}

	serviceProvider := newTestServiceProvider()
	storage := newTestStorage()
	resourceManager := newTestResourceManager()
	networkManager := newTestNetworkManager()
	registrar := newTestRegistrar()
	storageStateProvider := newTestStorageStateProvider(testInstaces)
	instanceMonitor := newTestInstanceMonitor()

	resourceHosts := []config.Host{
		{IP: "10.0.0.1", Hostname: "host1"},
		{IP: "10.0.0.2", Hostname: "host2"},
		{IP: "10.0.0.3", Hostname: "host3"},
	}

	resourceManager.addResource(resourcemanager.ResourceInfo{Name: "resource0", Hosts: resourceHosts[:1]})
	resourceManager.addResource(resourcemanager.ResourceInfo{Name: "resource1", Hosts: resourceHosts[1:2]})
	resourceManager.addResource(resourcemanager.ResourceInfo{Name: "resource2", Hosts: resourceHosts[2:]})
	resourceManager.addDevice(resourcemanager.DeviceInfo{Name: "device0"})
	resourceManager.addDevice(resourcemanager.DeviceInfo{Name: "device1"})
	resourceManager.addDevice(resourcemanager.DeviceInfo{Name: "device2"})

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider,
		newTestRunner(nil, nil), resourceManager, networkManager, registrar, storageStateProvider, instanceMonitor)
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = serviceProvider.fromTestInstances(testInstaces); err != nil {
		t.Fatalf("Can't create test services: %v", err)
	}

	if err = testLauncher.RunInstances(createInstancesInfos(testInstaces)); err != nil {
		t.Fatalf("Can't run instances: %v", err)
	}

	if err = checkRuntimeStatus(launcher.RuntimeStatus{
		RunStatus: &launcher.RunInstancesStatus{Instances: createInstancesStatuses(testInstaces)},
	}, testLauncher.RuntimeStatusChannel()); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	instance, err := storage.GetInstanceByIdent(cloudprotocol.InstanceIdent{
		ServiceID: testInstaces[0].serviceID, SubjectID: testInstaces[0].subjectID, Instance: 0,
	})
	if err != nil {
		t.Fatalf("Can't get instance info: %v", err)
	}

	// Check registrar

	if _, ok := registrar.secrets[instance.InstanceID]; !ok {
		t.Error("Instance should be registered")
	}

	// Check storage state

	storageStateInfo, ok := storageStateProvider.infos[instance.InstanceID]
	if !ok {
		t.Error("Storage & state should be setup")
	}

	if storageStateInfo.InstanceIdent != instance.InstanceIdent {
		t.Errorf("Wrong storage & state instance ident: %v", storageStateInfo.InstanceIdent)
	}

	if storageStateInfo.StorageQuota != *testInstaces[0].serviceConfig.Quotas.StorageLimit {
		t.Errorf("Wrong storage quota value: %d", storageStateInfo.StorageQuota)
	}

	if storageStateInfo.StateQuota != *testInstaces[0].serviceConfig.Quotas.StateLimit {
		t.Errorf("Wrong state quota value: %d", storageStateInfo.StateQuota)
	}

	if storageStateInfo.UID != instance.UID {
		t.Errorf("Wrong storage & state UID: %d", storageStateInfo.UID)
	}

	if storageStateInfo.GID != testInstaces[0].serviceGID {
		t.Errorf("Wrong storage & state GID: %d", storageStateInfo.GID)
	}

	// Check network

	netParams, ok := networkManager.instances[instance.InstanceID]
	if !ok {
		t.Error("Instance should be registered to network")
	}

	if !compateNetParams(netParams, networkmanager.NetworkParams{
		InstanceIdent:      instance.InstanceIdent,
		Hostname:           *testInstaces[0].serviceConfig.Hostname,
		Hosts:              resourceHosts,
		ExposedPorts:       convertMapToStringList(testInstaces[0].imageConfig.Config.ExposedPorts),
		AllowedConnections: convertMapToStringList(testInstaces[0].serviceConfig.AllowedConnections),
		HostsFilePath:      filepath.Join(launcher.RuntimeDir, instance.InstanceID, "mounts", "etc", "hosts"),
		ResolvConfFilePath: filepath.Join(launcher.RuntimeDir, instance.InstanceID, "mounts", "etc", "resolv.conf"),
		IngressKbit:        *testInstaces[0].serviceConfig.Quotas.DownloadSpeed,
		EgressKbit:         *testInstaces[0].serviceConfig.Quotas.UploadSpeed,
		DownloadLimit:      *testInstaces[0].serviceConfig.Quotas.DownloadLimit,
		UploadLimit:        *testInstaces[0].serviceConfig.Quotas.UploadLimit,
	}) {
		t.Errorf("Wrong network params: %v", netParams)
	}

	// Check allocated devices

	for name, allocatedDevice := range resourceManager.allocatedDevices {
		if allocatedDevice[0] != instance.InstanceID {
			t.Errorf("Device %s should be allocated", name)
		}
	}

	// Check monitor

	monitorPrams, ok := instanceMonitor.instances[instance.InstanceID]
	if !ok {
		t.Error("Instance monitor should be stated")
	}

	if !reflect.DeepEqual(monitorPrams, monitoring.MonitorParams{
		InstanceIdent: instance.InstanceIdent,
		UID:           instance.UID,
		GID:           testInstaces[0].serviceGID,
		AlertRules:    testInstaces[0].serviceConfig.AlertRules,
	}) {
		t.Errorf("Wrong monitor params: %v", monitorPrams)
	}

	// Stop instances and check runtime release

	if err = testLauncher.RunInstances(nil); err != nil {
		t.Fatalf("Can't stop instances: %v", err)
	}

	if err = checkRuntimeStatus(launcher.RuntimeStatus{
		RunStatus: &launcher.RunInstancesStatus{},
	}, testLauncher.RuntimeStatusChannel()); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	// Check registrar

	if _, ok := registrar.secrets[instance.InstanceID]; ok {
		t.Error("Instance should be unregistered")
	}

	// Check storage state

	if _, ok := storageStateProvider.infos[instance.InstanceID]; ok {
		t.Error("Storage state should be cleanup")
	}

	// Check network

	if _, ok := networkManager.instances[instance.InstanceID]; ok {
		t.Error("Instance should be removed from network")
	}

	// Check allocated devices

	for name, allocatedDevice := range resourceManager.allocatedDevices {
		if len(allocatedDevice) != 0 {
			t.Errorf("Device %s should be released", name)
		}
	}

	// Check monitor

	if _, ok := instanceMonitor.instances[instance.InstanceID]; ok {
		t.Error("Instance monitor should be stopped")
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

func (storage *testStorage) GetAllInstances() (instances []launcher.InstanceInfo, err error) {
	storage.RLock()
	defer storage.RUnlock()

	for _, instance := range storage.instances {
		instances = append(instances, instance)
	}

	return instances, nil
}

func (storage *testStorage) fromTestInstances(testInstances []testInstance, running bool) {
	storage.Lock()
	defer storage.Unlock()

	storage.instances = make(map[string]launcher.InstanceInfo)

	uid := 5000

	for _, testInstance := range testInstances {
		for i := uint64(0); i < testInstance.numInstances; i++ {
			newInstanceID := uuid.New().String()

			for instanceID, instance := range storage.instances {
				if instance.ServiceID == testInstance.serviceID && instance.SubjectID == testInstance.subjectID &&
					instance.Instance == i {
					newInstanceID = instanceID
				}
			}

			storage.instances[newInstanceID] = launcher.InstanceInfo{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: testInstance.serviceID,
					SubjectID: testInstance.subjectID,
					Instance:  i,
				},
				InstanceID:  newInstanceID,
				UnitSubject: testInstance.unitSubject,
				Running:     running,
				UID:         uid,
			}

			uid++
		}
	}
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

func (provider *testServiceProvider) GetImageParts(
	service servicemanager.ServiceInfo,
) (servicemanager.ImageParts, error) {
	return servicemanager.ImageParts{
		ImageConfigPath:   filepath.Join(service.ImagePath, imageConfigFile),
		ServiceConfigPath: filepath.Join(service.ImagePath, serviceConfigFile),
	}, nil
}

func (provider *testServiceProvider) fromTestInstances(testInstances []testInstance) error {
	provider.services = make(map[string]servicemanager.ServiceInfo)

	if err := os.RemoveAll(filepath.Join(tmpDir, servicesDir)); err != nil {
		return aoserrors.Wrap(err)
	}

	for _, testInstance := range testInstances {
		provider.services[testInstance.serviceID] = servicemanager.ServiceInfo{
			ServiceID:       testInstance.serviceID,
			AosVersion:      testInstance.serviceVersion,
			ServiceProvider: testInstance.serviceProvider,
			ImagePath:       filepath.Join(tmpDir, servicesDir, testInstance.serviceID),
			GID:             testInstance.serviceGID,
		}

		if err := os.MkdirAll(filepath.Join(tmpDir, servicesDir, testInstance.serviceID), 0o755); err != nil {
			return aoserrors.Wrap(err)
		}

		imageConfig := &imagespec.Image{OS: "linux"}

		if testInstance.imageConfig != nil {
			imageConfig = testInstance.imageConfig
		}

		if err := writeConfig(filepath.Join(tmpDir, servicesDir, testInstance.serviceID, imageConfigFile),
			imageConfig); err != nil {
			return err
		}

		if testInstance.serviceConfig != nil {
			if err := writeConfig(filepath.Join(tmpDir, servicesDir, testInstance.serviceID, serviceConfigFile),
				testInstance.serviceConfig); err != nil {
				return err
			}
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
 * testResourceManager
 **********************************************************************************************************************/

func newTestResourceManager() *testResourceManager {
	return &testResourceManager{
		allocatedDevices: map[string][]string{},
		devices:          make(map[string]resourcemanager.DeviceInfo),
		resources:        make(map[string]resourcemanager.ResourceInfo),
	}
}

func (manager *testResourceManager) GetDeviceInfo(device string) (resourcemanager.DeviceInfo, error) {
	deviceInfo, ok := manager.devices[device]
	if !ok {
		return resourcemanager.DeviceInfo{}, aoserrors.New("device info not found")
	}

	return deviceInfo, nil
}

func (manager *testResourceManager) GetResourceInfo(resource string) (resourcemanager.ResourceInfo, error) {
	resourceInfo, ok := manager.resources[resource]
	if !ok {
		return resourcemanager.ResourceInfo{}, aoserrors.New("resource info not found")
	}

	return resourceInfo, nil
}

func (manager *testResourceManager) AllocateDevice(device, instanceID string) error {
	deviceInfo, ok := manager.devices[device]
	if !ok {
		return aoserrors.New("device info not found")
	}

	for _, allocatedInstanceID := range manager.allocatedDevices[device] {
		if allocatedInstanceID == instanceID {
			return aoserrors.Errorf("device %s already allocated by %s", device, instanceID)
		}
	}

	if deviceInfo.SharedCount != 0 && len(manager.allocatedDevices[device]) >= deviceInfo.SharedCount {
		return aoserrors.Errorf("device %s shared count exceeds", device)
	}

	manager.allocatedDevices[device] = append(manager.allocatedDevices[device], instanceID)

	return nil
}

func (manager *testResourceManager) ReleaseDevices(instanceID string) error {
	for device, instanceIDs := range manager.allocatedDevices {
		for i, allocatedInstanceID := range instanceIDs {
			if allocatedInstanceID == instanceID {
				manager.allocatedDevices[device] = append(instanceIDs[:i], instanceIDs[i+1:]...)
				break
			}
		}

		if len(manager.allocatedDevices[device]) == 0 {
			delete(manager.allocatedDevices, device)
		}
	}

	return nil
}

func (manager *testResourceManager) addDevice(device resourcemanager.DeviceInfo) {
	manager.devices[device.Name] = device
}

func (manager *testResourceManager) addResource(resource resourcemanager.ResourceInfo) {
	manager.resources[resource.Name] = resource
}

/***********************************************************************************************************************
 * testNetworkManager
 **********************************************************************************************************************/

func newTestNetworkManager() *testNetworkManager {
	return &testNetworkManager{instances: make(map[string]networkmanager.NetworkParams)}
}

func (manager *testNetworkManager) GetNetnsPath(instanceID string) string {
	return filepath.Join("/run/netns", instanceID)
}

func (manager *testNetworkManager) AddInstanceToNetwork(
	instanceID, networkID string, params networkmanager.NetworkParams,
) error {
	manager.Lock()
	defer manager.Unlock()

	if _, ok := manager.instances[instanceID]; ok {
		return aoserrors.Errorf("instance %s already added to network", instanceID)
	}

	manager.instances[instanceID] = params

	return nil
}

func (manager *testNetworkManager) RemoveInstanceFromNetwork(instanceID, networkID string) error {
	manager.Lock()
	defer manager.Unlock()

	delete(manager.instances, instanceID)

	return nil
}

/***********************************************************************************************************************
 * testRegistrar
 **********************************************************************************************************************/

func newTestRegistrar() *testRegistrar {
	return &testRegistrar{secrets: make(map[string]string)}
}

func (registrar *testRegistrar) RegisterInstance(
	instanceID string, permissions map[string]map[string]string,
) (secret string, err error) {
	registrar.Lock()
	defer registrar.Unlock()

	if _, ok := registrar.secrets[instanceID]; ok {
		return "", aoserrors.Errorf("instance %s already registered", instanceID)
	}

	secret = uuid.NewString()

	registrar.secrets[instanceID] = secret

	return secret, nil
}

func (registrar *testRegistrar) UnregisterInstance(instanceID string) error {
	registrar.Lock()
	defer registrar.Unlock()

	if _, ok := registrar.secrets[instanceID]; !ok {
		return aoserrors.Errorf("instance %s is not registered", instanceID)
	}

	delete(registrar.secrets, instanceID)

	return nil
}

/***********************************************************************************************************************
 * testStateStorageProvider
 **********************************************************************************************************************/

func newTestStorageStateProvider(testInstances []testInstance) *testStorageStateProvider {
	return &testStorageStateProvider{
		testInstances: testInstances,
		infos:         make(map[string]storageStateInfo),
		stateChannel:  make(chan storagestate.StateChangedInfo, 1),
	}
}

func (provider *testStorageStateProvider) Setup(
	instanceID string, params storagestate.SetupParams,
) (storagePath, statePath string, stateChecksum []byte, err error) {
	provider.Lock()
	defer provider.Unlock()

	storagePath = filepath.Join(tmpDir, storagesDir, instanceID)
	statePath = filepath.Join(tmpDir, statesDir, instanceID)

	provider.infos[instanceID] = storageStateInfo{
		SetupParams: params,
		storagePath: storagePath,
		statePath:   statePath,
	}

	if len(provider.testInstances) > 0 {
		for _, testInstance := range provider.testInstances {
			if testInstance.serviceID == params.ServiceID && testInstance.subjectID == params.SubjectID &&
				params.Instance < uint64(len(testInstance.stateChecksum)) {
				stateChecksum = testInstance.stateChecksum[params.Instance]
			}
		}
	}

	return storagePath, statePath, stateChecksum, nil
}

func (provider *testStorageStateProvider) Cleanup(instanceID string) error {
	provider.Lock()
	defer provider.Unlock()

	delete(provider.infos, instanceID)

	return nil
}

func (provider *testStorageStateProvider) Remove(instanceID string) error {
	return nil
}

func (provider *testStorageStateProvider) StateChangedChannel() <-chan storagestate.StateChangedInfo {
	return provider.stateChannel
}

/***********************************************************************************************************************
 * testInstanceMonitor
 **********************************************************************************************************************/

func newTestInstanceMonitor() *testInstanceMonitor {
	return &testInstanceMonitor{instances: make(map[string]monitoring.MonitorParams)}
}

func (monitor *testInstanceMonitor) StartInstanceMonitor(instanceID string, params monitoring.MonitorParams) error {
	monitor.Lock()
	defer monitor.Unlock()

	if _, ok := monitor.instances[instanceID]; ok {
		return aoserrors.Errorf("monitor for instance %s already started", instanceID)
	}

	monitor.instances[instanceID] = params

	return nil
}

func (monitor *testInstanceMonitor) StopInstanceMonitor(instanceID string) error {
	monitor.Lock()
	defer monitor.Unlock()

	delete(monitor.instances, instanceID)

	return nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func setup() (err error) {
	tmpDir, err = ioutil.TempDir("", "sm_")
	if err != nil {
		return aoserrors.Wrap(err)
	}

	launcher.RuntimeDir = filepath.Join(tmpDir, "runtime")

	return nil
}

func cleanup() (err error) {
	if err = os.RemoveAll(tmpDir); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

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

			if index < uint64(len(testInstance.stateChecksum)) {
				instanceStatus.StateChecksum = hex.EncodeToString(testInstance.stateChecksum[index])
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

func writeConfig(fileName string, config interface{}) error {
	data, err := json.Marshal(config)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = ioutil.WriteFile(fileName, data, 0o600); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func createDevice(path, deviceType string, major, minor int64, mode os.FileMode, uid, gid uint32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	output, err := exec.Command("mknod", path, deviceType,
		strconv.FormatInt(major, 10), strconv.FormatInt(minor, 10)).CombinedOutput()
	if err != nil {
		return aoserrors.Errorf("%v: %s", err, output)
	}

	if output, err = exec.Command("chown", fmt.Sprintf("%d:%d", uid, gid), path).CombinedOutput(); err != nil {
		return aoserrors.Errorf("%v: %s", err, output)
	}

	if output, err = exec.Command("chmod", fmt.Sprintf("%o", mode), path).CombinedOutput(); err != nil {
		return aoserrors.Errorf("%v: %s", err, output)
	}

	return nil
}

func createTestDevices(testDevices []testDevice) error {
	for _, device := range testDevices {
		if err := createDevice(device.hostPath, device.deviceType, device.major, device.minor,
			device.mode, device.uid, device.gid); err != nil {
			return err
		}
	}

	return nil
}

func createSpecDevices(testDevices []testDevice) (specDevices []runtimespec.LinuxDevice) {
	for _, device := range testDevices {
		specDevices = append(specDevices, runtimespec.LinuxDevice{
			Path:     device.containerPath,
			Type:     device.deviceType,
			Major:    device.major,
			Minor:    device.minor,
			FileMode: newFileMode(device.mode),
			UID:      newUIDGID(device.uid),
			GID:      newUIDGID(device.gid),
		})
	}

	return specDevices
}

func createCgroupDevices(testDevices []testDevice) (cgroupDevices []runtimespec.LinuxDeviceCgroup) {
	cgroupDevices = append(cgroupDevices, runtimespec.LinuxDeviceCgroup{
		Allow:  false,
		Access: "rwm",
	})

	for _, device := range testDevices {
		cgroupDevices = append(cgroupDevices, runtimespec.LinuxDeviceCgroup{
			Allow:  true,
			Type:   device.deviceType,
			Major:  newMajorMinor(device.major),
			Minor:  newMajorMinor(device.minor),
			Access: device.permissions,
		})
	}

	return cgroupDevices
}

func newFileMode(mode os.FileMode) *os.FileMode {
	return &mode
}

func newUIDGID(id uint32) *uint32 {
	return &id
}

func newMajorMinor(value int64) *int64 {
	return &value
}

func newString(value string) *string {
	return &value
}

func newUint64(value uint64) *uint64 {
	return &value
}

func createSpecMounts(mounts []resourcemanager.FileSystemMount) (specMounts []runtimespec.Mount) {
	// Manadatory mounts
	specMounts = append(specMounts, runtimespec.Mount{Source: "proc", Destination: "/proc", Type: "proc"})
	specMounts = append(specMounts, runtimespec.Mount{
		Source: "tmpfs", Destination: "/dev", Type: "tmpfs",
		Options: []string{"nosuid", "strictatime", "mode=755", "size=65536k"},
	})
	specMounts = append(specMounts, runtimespec.Mount{
		Source: "devpts", Destination: "/dev/pts", Type: "devpts",
		Options: []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620", "gid=5"},
	})
	specMounts = append(specMounts, runtimespec.Mount{
		Source: "shm", Destination: "/dev/shm", Type: "tmpfs",
		Options: []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"},
	})
	specMounts = append(specMounts, runtimespec.Mount{
		Source: "mqueue", Destination: "/dev/mqueue", Type: "mqueue",
		Options: []string{"nosuid", "noexec", "nodev"},
	})
	specMounts = append(specMounts, runtimespec.Mount{
		Source: "sysfs", Destination: "/sys", Type: "sysfs",
		Options: []string{"nosuid", "noexec", "nodev", "ro"},
	})
	specMounts = append(specMounts, runtimespec.Mount{
		Source: "cgroup", Destination: "/sys/fs/cgroup", Type: "cgroup",
		Options: []string{"nosuid", "noexec", "nodev", "relatime", "ro"},
	})

	// Aos mounts

	specMounts = append(specMounts, runtimespec.Mount{
		Source: "/etc/nsswitch.conf", Destination: "/etc/nsswitch.conf", Type: "bind",
		Options: []string{"bind", "ro"},
	})
	specMounts = append(specMounts, runtimespec.Mount{
		Source: "/etc/ssl", Destination: "/etc/ssl", Type: "bind",
		Options: []string{"bind", "ro"},
	})

	// Resource mounts

	for _, mount := range mounts {
		specMounts = append(specMounts, runtimespec.Mount{
			Source:      mount.Source,
			Destination: mount.Destination,
			Type:        mount.Type,
			Options:     mount.Options,
		})
	}

	return specMounts
}

func getGroupsMap() (groupsMap map[string]uint32, err error) {
	groupsMap = make(map[string]uint32)

	output, err := exec.Command("getent", "group").CombinedOutput()
	if err != nil {
		return nil, aoserrors.Errorf("%v: %s", err, output)
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")

		if len(fields) < 3 {
			return nil, aoserrors.Errorf("invalid group line: %s", scanner.Text())
		}

		gid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return nil, aoserrors.Wrap(err)
		}

		groupsMap[fields[0]] = uint32(gid)
	}

	return groupsMap, nil
}

func getSystemGroups(n int) (groups []string, err error) {
	groupsMap, err := getGroupsMap()
	if err != nil {
		return nil, err
	}

	if len(groupsMap) == 0 {
		return nil, aoserrors.New("no system groups found")
	}

	systemGroups := make([]string, 0, len(groupsMap))

	for group := range groupsMap {
		systemGroups = append(systemGroups, group)
	}

	for i := 0; i < n; i++ {
		groups = append(groups, systemGroups[i%len(systemGroups)])
	}

	return groups, nil
}

func createAdditionalGroups(groups []string) (gids []uint32, err error) {
	groupsMap, err := getGroupsMap()
	if err != nil {
		return nil, err
	}

groupLoop:
	for _, group := range groups {
		gid, ok := groupsMap[group]
		if !ok {
			return nil, aoserrors.Errorf("group %s not found", group)
		}

		for _, existingGID := range gids {
			if gid == existingGID {
				continue groupLoop
			}
		}

		gids = append(gids, gid)
	}

	return gids, nil
}

func getInstanceRuntimeSpec(instanceID string) (runtimespec.Spec, error) {
	runtimeData, err := ioutil.ReadFile(filepath.Join(launcher.RuntimeDir, instanceID, runtimeConfigFile))
	if err != nil {
		return runtimespec.Spec{}, aoserrors.Wrap(err)
	}

	var runtimeSpec runtimespec.Spec

	if err = json.Unmarshal(runtimeData, &runtimeSpec); err != nil {
		return runtimespec.Spec{}, aoserrors.Wrap(err)
	}

	return runtimeSpec, nil
}

func getAosEnvVars(instance launcher.InstanceInfo) (aosEnvVars []string) {
	aosEnvVars = append(aosEnvVars, fmt.Sprintf("AOS_SERVICE_ID=%s", instance.ServiceID))
	aosEnvVars = append(aosEnvVars, fmt.Sprintf("AOS_SUBJECT_ID=%s", instance.SubjectID))
	aosEnvVars = append(aosEnvVars, fmt.Sprintf("AOS_INSTANCE_INDEX=%d", instance.Instance))
	aosEnvVars = append(aosEnvVars, fmt.Sprintf("AOS_INSTANCE_ID=%s", instance.InstanceID))

	return aosEnvVars
}

func convertMapToStringList(m map[string]struct{}) (result []string) {
	result = make([]string, 0, len(m))

	for key := range m {
		result = append(result, key)
	}

	return result
}

func compateNetParams(p1, p2 networkmanager.NetworkParams) bool {
	if p1.InstanceIdent != p2.InstanceIdent {
		return false
	}

	if p1.Hostname != p2.Hostname {
		return false
	}

	if !compareArrays(len(p1.Aliases), len(p2.Aliases), func(index1, index2 int) bool {
		return p1.Aliases[index1] == p2.Aliases[index2]
	}) {
		return false
	}

	if !compareArrays(len(p1.Hosts), len(p2.Hosts), func(index1, index2 int) bool {
		return p1.Hosts[index1] == p2.Hosts[index2]
	}) {
		return false
	}

	if !compareArrays(len(p1.DNSSevers), len(p2.DNSSevers), func(index1, index2 int) bool {
		return p1.DNSSevers[index1] == p2.DNSSevers[index2]
	}) {
		return false
	}

	if !compareArrays(len(p1.ExposedPorts), len(p2.ExposedPorts), func(index1, index2 int) bool {
		return p1.ExposedPorts[index1] == p2.ExposedPorts[index2]
	}) {
		return false
	}

	if !compareArrays(len(p1.AllowedConnections), len(p2.AllowedConnections), func(index1, index2 int) bool {
		return p1.AllowedConnections[index1] == p2.AllowedConnections[index2]
	}) {
		return false
	}

	if p1.HostsFilePath != p2.HostsFilePath {
		return false
	}

	if p1.ResolvConfFilePath != p2.ResolvConfFilePath {
		return false
	}

	if p1.IngressKbit != p2.IngressKbit || p1.EgressKbit != p2.EgressKbit {
		return false
	}

	if p1.DownloadLimit != p2.DownloadLimit || p1.UploadLimit != p2.UploadLimit {
		return false
	}

	return true
}
