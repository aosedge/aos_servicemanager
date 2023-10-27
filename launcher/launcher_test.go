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
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/aoscloud/aos_common/resourcemonitor"
	"github.com/google/uuid"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/launcher"
	"github.com/aoscloud/aos_servicemanager/layermanager"
	"github.com/aoscloud/aos_servicemanager/networkmanager"
	"github.com/aoscloud/aos_servicemanager/resourcemanager"
	"github.com/aoscloud/aos_servicemanager/runner"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
)

/***********************************************************************************************************************
 * Const
 **********************************************************************************************************************/

const (
	imageConfigFile   = "image.json"
	serviceConfigFile = "service.json"
	instanceRootFS    = "rootfs"
	runtimeConfigFile = "config.json"
	servicesDir       = "services"
	layersDir         = "layers"
	storagesDir       = "storages"
	statesDir         = "states"
)

const defaultStatusTimeout = 5 * time.Second

var defaultEnvVars = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TERM=xterm"}

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testStorage struct {
	sync.RWMutex
	instances  map[string]launcher.InstanceInfo
	envVars    []cloudprotocol.EnvVarsInstanceInfo
	onlineTime time.Time
}

type testServiceProvider struct {
	services     map[string]servicemanager.ServiceInfo
	layerDigests map[string][]string
}

type testLayerProvider struct {
	layers map[string]layermanager.LayerInfo
}

type testRunner struct {
	sync.Mutex
	statusChannel chan []runner.InstanceStatus
	startFunc     func(instanceID string) runner.InstanceStatus
	stopFunc      func(instanceID string) error
}

type testResourceManager struct {
	sync.RWMutex
	allocatedDevices map[string][]string
	devices          map[string]aostypes.DeviceInfo
	resources        map[string]aostypes.ResourceInfo
}

type testNetworkManager struct {
	sync.Mutex
	instances map[string]networkmanager.NetworkParams
}

type testRegistrar struct {
	sync.Mutex
	secrets map[aostypes.InstanceIdent]string
}

type testInstanceMonitor struct {
	sync.Mutex
	instances map[string]resourcemonitor.ResourceMonitorParams
}

type testMounter struct {
	sync.Mutex
	mounts map[string]mountInfo
}

type serviceInfo struct {
	aostypes.ServiceInfo
	gid           uint32
	imageConfig   *imagespec.Image
	serviceConfig *aostypes.ServiceConfig
	layerDigests  []string
}

type mountInfo struct {
	lowerDirs []string
	upperDir  string
	workDir   string
}

type testItem struct {
	services  []serviceInfo
	layers    []aostypes.LayerInfo
	instances []aostypes.InstanceInfo
	err       []error
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

type testAlertSender struct {
	alerts []cloudprotocol.DeviceAllocateAlert
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var (
	tmpDir  string
	mounter = newTestMounter()
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
	data := []testItem{
		// start from scratch
		{
			services: []serviceInfo{
				{ServiceInfo: aostypes.ServiceInfo{ID: "service0"}},
				{ServiceInfo: aostypes.ServiceInfo{ID: "service1"}},
				{ServiceInfo: aostypes.ServiceInfo{ID: "service2"}},
			},
			instances: []aostypes.InstanceInfo{
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0}},
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 0}},
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject0", Instance: 0}},
			},
		},
		// start the same instances
		{
			services: []serviceInfo{
				{ServiceInfo: aostypes.ServiceInfo{ID: "service0"}},
				{ServiceInfo: aostypes.ServiceInfo{ID: "service1"}},
				{ServiceInfo: aostypes.ServiceInfo{ID: "service2"}},
			},
			instances: []aostypes.InstanceInfo{
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0}},
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 0}},
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject0", Instance: 0}},
			},
		},
		// stop and start some instances
		{
			services: []serviceInfo{
				{ServiceInfo: aostypes.ServiceInfo{ID: "service0"}},
				{ServiceInfo: aostypes.ServiceInfo{ID: "service2"}},
				{ServiceInfo: aostypes.ServiceInfo{ID: "service3"}},
			},
			instances: []aostypes.InstanceInfo{
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0}},
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject0", Instance: 1}},
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service3", SubjectID: "subject0", Instance: 2}},
			},
		},
		// new service version
		{
			services: []serviceInfo{
				{ServiceInfo: aostypes.ServiceInfo{ID: "service0", VersionInfo: aostypes.VersionInfo{AosVersion: 1}}},
				{ServiceInfo: aostypes.ServiceInfo{ID: "service2"}},
				{ServiceInfo: aostypes.ServiceInfo{ID: "service3"}},
			},
			instances: []aostypes.InstanceInfo{
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0}},
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject0", Instance: 1}},
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service3", SubjectID: "subject0", Instance: 2}},
			},
		},
		// start error
		{
			services: []serviceInfo{
				{ServiceInfo: aostypes.ServiceInfo{ID: "service0"}},
				{ServiceInfo: aostypes.ServiceInfo{ID: "service1"}},
				{ServiceInfo: aostypes.ServiceInfo{ID: "service2"}},
			},
			instances: []aostypes.InstanceInfo{
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject1", Instance: 0}},
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject1", Instance: 0}},
				{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject1", Instance: 0}},
			},
			err: []error{errors.New("error1"), errors.New("error2")}, //nolint:goerr113
		},
		// stop all instances
		{},
	}

	var currentTestItem testItem

	runningInstances := make(map[string]runner.InstanceStatus)

	storage := newTestStorage()
	serviceProvider := newTestServiceProvider()
	layerProvider := newTestLayerProvider()
	instanceRunner := newTestRunner(
		func(instanceID string) runner.InstanceStatus {
			status := getRunnerStatus(instanceID, currentTestItem, storage)
			runningInstances[instanceID] = status

			return status
		},
		func(instanceID string) error {
			delete(runningInstances, instanceID)

			return nil
		},
	)

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider,
		layerProvider, instanceRunner, newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(),
		newTestInstanceMonitor(), newTestAlertSender())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{}}, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	for i, item := range data {
		t.Logf("Run instances: %d", i)

		currentTestItem = item

		if err = serviceProvider.installServices(item.services); err != nil {
			t.Fatalf("Can't install services: %v", err)
		}

		if err = layerProvider.installLayers(item.layers); err != nil {
			t.Fatalf("Can't install layers: %v", err)
		}

		if err = testLauncher.RunInstances(item.instances, false); err != nil {
			t.Fatalf("Can't run instances: %v", err)
		}

		runtimeStatus := launcher.RuntimeStatus{
			RunStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(item)},
		}

		if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
			runtimeStatus, defaultStatusTimeout); err != nil {
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
	layerProvider := newTestLayerProvider()
	instanceRunner := newTestRunner(nil, nil)

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider, layerProvider,
		instanceRunner, newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(),
		newTestInstanceMonitor(), newTestAlertSender())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{}}, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	runItem := testItem{
		services: []serviceInfo{
			{ServiceInfo: aostypes.ServiceInfo{ID: "service0"}},
			{ServiceInfo: aostypes.ServiceInfo{ID: "service1"}},
			{ServiceInfo: aostypes.ServiceInfo{ID: "service2"}},
		},
		instances: []aostypes.InstanceInfo{
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject0", Instance: 0}},
		},
	}

	if err = serviceProvider.installServices(runItem.services); err != nil {
		t.Fatalf("Can't install services: %v", err)
	}

	if err = layerProvider.installLayers(runItem.layers); err != nil {
		t.Fatalf("Can't install layers: %v", err)
	}

	if err = testLauncher.RunInstances(runItem.instances, false); err != nil {
		t.Fatalf("Can't run instances: %v", err)
	}

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(runItem)}},
		defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	// Check update status on runtime state changed

	changedItem := testItem{
		instances: []aostypes.InstanceInfo{
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject0", Instance: 0}},
		},
		err: []error{errors.New("error0"), errors.New("error1")}, //nolint:goerr113
	}

	instanceRunner.statusChannel <- createRunStatus(storage, changedItem)

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(), launcher.RuntimeStatus{
		UpdateStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(changedItem)},
	}, defaultStatusTimeout); err != nil {
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
	layerProvider := newTestLayerProvider()
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
		layerProvider, instanceRunner, newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(),
		newTestInstanceMonitor(), newTestAlertSender())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{}}, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	runItem := testItem{
		services: []serviceInfo{
			{ServiceInfo: aostypes.ServiceInfo{ID: "service0"}},
			{ServiceInfo: aostypes.ServiceInfo{ID: "service1"}},
			{ServiceInfo: aostypes.ServiceInfo{ID: "service2"}},
		},
		instances: []aostypes.InstanceInfo{
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 1}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 2}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 3}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 1}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 2}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject0", Instance: 1}},
		},
	}

	if err = serviceProvider.installServices(runItem.services); err != nil {
		t.Fatalf("Can't install services: %v", err)
	}

	if err = layerProvider.installLayers(runItem.layers); err != nil {
		t.Fatalf("Can't install layers: %v", err)
	}

	if err = testLauncher.RunInstances(runItem.instances, false); err != nil {
		t.Fatalf("Can't run instances: %v", err)
	}

	runtimeStatus := launcher.RuntimeStatus{
		RunStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(runItem)},
	}

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(), runtimeStatus, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	if err = testLauncher.RunInstances(runItem.instances, true); err != nil {
		t.Errorf("Can't stop instances: %v", err)
	}

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(), runtimeStatus, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	if len(restartMap) != 9 {
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

func TestHostFSDir(t *testing.T) {
	hostFSBinds := []string{"bin", "sbin", "lib", "lib64", "usr"}

	testLauncher, err := launcher.New(&config.Config{
		WorkingDir: tmpDir,
		HostBinds:  hostFSBinds,
	},
		newTestStorage(), newTestServiceProvider(), newTestLayerProvider(), newTestRunner(nil, nil),
		newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(), newTestInstanceMonitor(),
		newTestAlertSender())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	rootItems, err := os.ReadDir("/")
	if err != nil {
		t.Fatalf("Can't read root dir: %v", err)
	}

	whiteoutItems, err := os.ReadDir(filepath.Join(tmpDir, "hostfs", "whiteouts"))
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
				info, err := whiteoutItem.Info()
				if err != nil {
					t.Fatalf("Can't get whiteout info: %v", err)
				}

				if info.Mode() != 0o410000000 {
					t.Errorf("Wrong white out mode 0o%o", info.Mode())
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
	layerProvider := newTestLayerProvider()
	storage := newTestStorage()
	resourceManager := newTestResourceManager()
	networkManager := newTestNetworkManager()
	testRegistrar := newTestRegistrar()

	runItem := testItem{
		services: []serviceInfo{
			{
				ServiceInfo: aostypes.ServiceInfo{ID: "service0"},
				gid:         3456,
				imageConfig: &imagespec.Image{
					OS: "linux",
					Config: imagespec.ImageConfig{
						Entrypoint: []string{"entry1", "entry2", "entry3"},
						Cmd:        []string{"cmd1", "cmd2", "cmd3"},
						WorkingDir: "/working/dir",
						Env:        []string{"env1=val1", "env2=val2", "env3=val3"},
					},
				},
				serviceConfig: &aostypes.ServiceConfig{
					Hostname: newString("testHostName"),
					Sysctl:   map[string]string{"key1": "val1", "key2": "val2", "key3": "val3"},
					Quotas: aostypes.ServiceQuotas{
						CPULimit:    newUint64(42),
						RAMLimit:    newUint64(1024),
						PIDsLimit:   newUint64(10),
						NoFileLimit: newUint64(3),
						TmpLimit:    newUint64(512),
					},
					Devices: []aostypes.ServiceDevice{
						{Name: "input", Permissions: "r"},
						{Name: "video", Permissions: "rw"},
						{Name: "sound", Permissions: "rwm"},
					},
					Resources:   []string{"resource1", "resource2", "resource3"},
					Permissions: map[string]map[string]string{"perm1": {"key1": "val1"}},
				},
			},
		},
		instances: []aostypes.InstanceInfo{
			{
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0},
				StatePath:     "state.dat",
				StoragePath:   "storage",
				UID:           9483,
			},
		},
	}

	testLauncher, err := launcher.New(&config.Config{
		WorkingDir: tmpDir,
		StorageDir: filepath.Join(tmpDir, "storages"),
		StateDir:   filepath.Join(tmpDir, "states"),
	}, storage, serviceProvider,
		newTestLayerProvider(), newTestRunner(nil, nil), resourceManager, networkManager, testRegistrar,
		newTestInstanceMonitor(), newTestAlertSender())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{}}, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
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

	resourceManager.addDevice(aostypes.DeviceInfo{
		Name: "input", HostDevices: []string{fmt.Sprintf("%s/input:/dev/input", hostDeviceDir)},
		Groups: hostGroups[:2],
	})
	resourceManager.addDevice(aostypes.DeviceInfo{
		Name: "video", HostDevices: []string{fmt.Sprintf("%s/video:/dev/video", hostDeviceDir)},
		Groups: hostGroups[2:4],
	})
	resourceManager.addDevice(aostypes.DeviceInfo{
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

	hostMounts := []aostypes.FileSystemMount{
		{Source: filepath.Join(hostDirs, "dir0"), Destination: "/dir0", Type: "bind", Options: []string{"opt0, opt1"}},
		{Source: filepath.Join(hostDirs, "dir1"), Destination: "/dir1", Type: "bind", Options: []string{"opt2, opt3"}},
		{Source: filepath.Join(hostDirs, "dir2"), Destination: "/dir2", Type: "bind", Options: []string{"opt4, opt5"}},
		{Source: filepath.Join(hostDirs, "dir3"), Destination: "/dir3", Type: "bind", Options: []string{"opt6, opt7"}},
		{Source: filepath.Join(hostDirs, "dir4"), Destination: "/dir4", Type: "bind", Options: []string{"opt8, opt9"}},
	}

	envVars := []string{"var0=0", "var1=1", "var2=2", "var3=3", "var4=4", "var5=5", "var6=6", "var7=7"}

	resourceManager.addResource(aostypes.ResourceInfo{
		Name: "resource1", Mounts: hostMounts[:2], Env: envVars[:2], Groups: hostGroups[6:7],
	})
	resourceManager.addResource(aostypes.ResourceInfo{
		Name: "resource2", Mounts: hostMounts[2:4], Env: envVars[2:5], Groups: hostGroups[7:8],
	})
	resourceManager.addResource(aostypes.ResourceInfo{
		Name: "resource3", Mounts: hostMounts[4:], Env: envVars[5:], Groups: hostGroups[8:],
	})

	if err = serviceProvider.installServices(runItem.services); err != nil {
		t.Fatalf("Can't install services: %v", err)
	}

	if err = layerProvider.installLayers(runItem.layers); err != nil {
		t.Fatalf("Can't install layers: %v", err)
	}

	if err = testLauncher.RunInstances(runItem.instances, false); err != nil {
		t.Fatalf("Can't run instances: %v", err)
	}

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(), launcher.RuntimeStatus{
		RunStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(runItem)},
	}, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	instance, err := storage.getInstanceByIdent(runItem.instances[0].InstanceIdent)
	if err != nil {
		t.Fatalf("Can't get instance info: %v", err)
	}

	runtimeSpec, err := getInstanceRuntimeSpec(instance.InstanceID)
	if err != nil {
		t.Fatalf("Can't get instance runtime spec: %v", err)
	}

	imageConfig := runItem.services[0].imageConfig
	serviceConfig := runItem.services[0].serviceConfig

	// Check terminal is false

	if runtimeSpec.Process.Terminal {
		t.Error("Terminal setting should be disabled")
	}

	// Check args

	expectedArgs := make([]string, 0,
		len(imageConfig.Config.Entrypoint)+len(imageConfig.Config.Cmd))

	expectedArgs = append(expectedArgs, imageConfig.Config.Entrypoint...)
	expectedArgs = append(expectedArgs, imageConfig.Config.Cmd...)

	if !compareArrays(len(expectedArgs), len(runtimeSpec.Process.Args), func(index1, index2 int) bool {
		return expectedArgs[index1] == runtimeSpec.Process.Args[index2]
	}) {
		t.Errorf("Wrong args value: %v", runtimeSpec.Process.Args)
	}

	// Check working dir

	if runtimeSpec.Process.Cwd != imageConfig.Config.WorkingDir {
		t.Errorf("Wrong working dir value: %s", runtimeSpec.Process.Cwd)
	}

	// Check host name

	if runtimeSpec.Hostname != *serviceConfig.Hostname {
		t.Errorf("Wrong host name value: %s", runtimeSpec.Hostname)
	}

	// Check sysctl

	if !reflect.DeepEqual(runtimeSpec.Linux.Sysctl, serviceConfig.Sysctl) {
		t.Errorf("Wrong sysctl value: %s", runtimeSpec.Linux.Sysctl)
	}

	// Check CPU limit

	if *runtimeSpec.Linux.Resources.CPU.Period != 100000 {
		t.Errorf("Wrong CPU period value: %d", *runtimeSpec.Linux.Resources.CPU.Period)
	}

	if *runtimeSpec.Linux.Resources.CPU.Quota !=
		int64(*serviceConfig.Quotas.CPULimit*(*runtimeSpec.Linux.Resources.CPU.Period)/100) {
		t.Errorf("Wrong CPU quota value: %d", *runtimeSpec.Linux.Resources.CPU.Quota)
	}

	// Check RAM limit

	if *runtimeSpec.Linux.Resources.Memory.Limit != int64(*serviceConfig.Quotas.RAMLimit) {
		t.Errorf("Wrong RAM limit value: %d", *runtimeSpec.Linux.Resources.Memory.Limit)
	}

	// Check PIDs limit

	if runtimeSpec.Linux.Resources.Pids.Limit != int64(*serviceConfig.Quotas.PIDsLimit) {
		t.Errorf("Wrong PIDs limit value: %d", runtimeSpec.Linux.Resources.Pids.Limit)
	}

	// Check RLimits
	expectedRLimits := []runtimespec.POSIXRlimit{
		{
			Type: "RLIMIT_NPROC",
			Hard: *serviceConfig.Quotas.PIDsLimit,
			Soft: *serviceConfig.Quotas.PIDsLimit,
		},
		{
			Type: "RLIMIT_NOFILE",
			Hard: *serviceConfig.Quotas.NoFileLimit,
			Soft: *serviceConfig.Quotas.NoFileLimit,
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
			"size=" + strconv.FormatUint(*serviceConfig.Quotas.TmpLimit, 10),
		},
	})
	expectedMounts = append(expectedMounts,
		runtimespec.Mount{
			Source:      filepath.Join(tmpDir, "states", runItem.instances[0].StatePath),
			Destination: "/state.dat",
			Type:        "bind",
			Options:     []string{"bind", "rw"},
		},
		runtimespec.Mount{
			Source:      filepath.Join(tmpDir, "storages", runItem.instances[0].StoragePath),
			Destination: "/storage",
			Type:        "bind",
			Options:     []string{"bind", "rw"},
		},
	)

	if !compareArrays(len(expectedMounts), len(runtimeSpec.Mounts), func(index1, index2 int) bool {
		return reflect.DeepEqual(expectedMounts[index1], runtimeSpec.Mounts[index2])
	}) {
		t.Errorf("Wrong mounts value: %v", runtimeSpec.Mounts)
	}

	// Check env vars
	envVars = append(envVars, defaultEnvVars...)
	envVars = append(envVars, imageConfig.Config.Env...)
	envVars = append(envVars, getAosEnvVars(instance)...)
	envVars = append(envVars, fmt.Sprintf("AOS_SECRET=%s", testRegistrar.secrets[instance.InstanceIdent]))

	if !compareArrays(len(envVars), len(runtimeSpec.Process.Env), func(index1, index2 int) bool {
		return envVars[index1] == runtimeSpec.Process.Env[index2]
	}) {
		t.Errorf("Wrong env variables value: %v", runtimeSpec.Process.Env)
	}

	// Check UID/GID

	if runtimeSpec.Process.User.UID != instance.UID {
		t.Errorf("Wrong UID: %d", runtimeSpec.Process.User.UID)
	}

	if runtimeSpec.Process.User.GID != serviceProvider.services[instance.ServiceID].GID {
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
	layerDigest1, layerDigest2, layerDigest3, layerDigest4 := uuid.NewString(), uuid.NewString(),
		uuid.NewString(), uuid.NewString()

	serviceProvider := newTestServiceProvider()
	layerProvider := newTestLayerProvider()
	storage := newTestStorage()
	resourceManager := newTestResourceManager()
	networkManager := newTestNetworkManager()
	registrar := newTestRegistrar()
	instanceMonitor := newTestInstanceMonitor()

	resourceHosts := []aostypes.Host{
		{IP: "10.0.0.1", Hostname: "host1"},
		{IP: "10.0.0.2", Hostname: "host2"},
		{IP: "10.0.0.3", Hostname: "host3"},
	}

	resourceManager.addResource(aostypes.ResourceInfo{Name: "resource0", Hosts: resourceHosts[:1]})
	resourceManager.addResource(aostypes.ResourceInfo{Name: "resource1", Hosts: resourceHosts[1:2]})
	resourceManager.addResource(aostypes.ResourceInfo{Name: "resource2", Hosts: resourceHosts[2:]})
	resourceManager.addDevice(aostypes.DeviceInfo{Name: "device0"})
	resourceManager.addDevice(aostypes.DeviceInfo{Name: "device1"})
	resourceManager.addDevice(aostypes.DeviceInfo{Name: "device2"})

	runItem := testItem{
		services: []serviceInfo{
			{
				ServiceInfo:  aostypes.ServiceInfo{ID: "service0"},
				gid:          3456,
				layerDigests: []string{layerDigest1, layerDigest2, layerDigest3, layerDigest4},
				imageConfig: &imagespec.Image{
					OS: "linux",
					Config: imagespec.ImageConfig{
						ExposedPorts: map[string]struct{}{"port0": {}, "port1": {}, "port2": {}},
					},
				},
				serviceConfig: &aostypes.ServiceConfig{
					Hostname:    newString("host1"),
					Permissions: map[string]map[string]string{"perm1": {"key1": "val1"}},
					Quotas: aostypes.ServiceQuotas{
						DownloadSpeed: newUint64(4096),
						UploadSpeed:   newUint64(8192),
						DownloadLimit: newUint64(16384),
						UploadLimit:   newUint64(32768),
						StorageLimit:  newUint64(2048),
						StateLimit:    newUint64(1024),
					},
					Resources: []string{"resource0", "resource1", "resource2"},
					Devices:   []aostypes.ServiceDevice{{Name: "device0"}, {Name: "device1"}, {Name: "device2"}},
					AlertRules: &aostypes.AlertRules{
						RAM: &aostypes.AlertRuleParam{
							MinTimeout:   aostypes.Duration{Duration: 1 * time.Second},
							MinThreshold: 10, MaxThreshold: 100,
						},
						CPU: &aostypes.AlertRuleParam{
							MinTimeout:   aostypes.Duration{Duration: 2 * time.Second},
							MinThreshold: 20, MaxThreshold: 200,
						},
						UsedDisks: []aostypes.PartitionAlertRuleParam{
							{
								Name: "storage",
								AlertRuleParam: aostypes.AlertRuleParam{
									MinTimeout:   aostypes.Duration{Duration: 3 * time.Second},
									MinThreshold: 30, MaxThreshold: 300,
								},
							},
						},
						InTraffic: &aostypes.AlertRuleParam{
							MinTimeout:   aostypes.Duration{Duration: 4 * time.Second},
							MinThreshold: 40, MaxThreshold: 400,
						},
						OutTraffic: &aostypes.AlertRuleParam{
							MinTimeout:   aostypes.Duration{Duration: 5 * time.Second},
							MinThreshold: 50, MaxThreshold: 500,
						},
					},
				},
			},
		},
		layers: []aostypes.LayerInfo{
			{Digest: layerDigest1}, {Digest: layerDigest2}, {Digest: layerDigest3}, {Digest: layerDigest4},
		},
		instances: []aostypes.InstanceInfo{
			{
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0},
				StoragePath:   "storage",
				StatePath:     "state.dat",
				UID:           9483,
			},
		},
	}

	testLauncher, err := launcher.New(&config.Config{
		WorkingDir: tmpDir,
		StorageDir: filepath.Join(tmpDir, "storages"),
		StateDir:   filepath.Join(tmpDir, "states"),
	}, storage, serviceProvider, layerProvider,
		newTestRunner(nil, nil), resourceManager, networkManager, registrar, instanceMonitor, newTestAlertSender())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{}}, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	if err = serviceProvider.installServices(runItem.services); err != nil {
		t.Fatalf("Can't install services: %v", err)
	}

	if err = layerProvider.installLayers(runItem.layers); err != nil {
		t.Fatalf("Can't install layers: %v", err)
	}

	if err = testLauncher.RunInstances(runItem.instances, false); err != nil {
		t.Fatalf("Can't run instances: %v", err)
	}

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(runItem)}},
		defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	instance, err := storage.getInstanceByIdent(runItem.instances[0].InstanceIdent)
	if err != nil {
		t.Fatalf("Can't get instance info: %v", err)
	}

	// Check registrar

	if _, ok := registrar.secrets[instance.InstanceIdent]; !ok {
		t.Error("Instance should be registered")
	}

	// Check network

	netParams, ok := networkManager.instances[instance.InstanceID]
	if !ok {
		t.Error("Instance should be registered to network")
	}

	serviceConfig := runItem.services[0].serviceConfig
	imageConfig := runItem.services[0].imageConfig

	if !compareNetParams(netParams, networkmanager.NetworkParams{
		InstanceIdent:      instance.InstanceIdent,
		Hostname:           *serviceConfig.Hostname,
		Hosts:              resourceHosts,
		ExposedPorts:       convertMapToStringList(imageConfig.Config.ExposedPorts),
		HostsFilePath:      filepath.Join(launcher.RuntimeDir, instance.InstanceID, "mounts", "etc", "hosts"),
		ResolvConfFilePath: filepath.Join(launcher.RuntimeDir, instance.InstanceID, "mounts", "etc", "resolv.conf"),
		IngressKbit:        *serviceConfig.Quotas.DownloadSpeed,
		EgressKbit:         *serviceConfig.Quotas.UploadSpeed,
		DownloadLimit:      *serviceConfig.Quotas.DownloadLimit,
		UploadLimit:        *serviceConfig.Quotas.UploadLimit,
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

	if !reflect.DeepEqual(monitorPrams, resourcemonitor.ResourceMonitorParams{
		InstanceIdent: instance.InstanceIdent,
		UID:           int(instance.UID),
		GID:           int(runItem.services[0].gid),
		AlertRules:    serviceConfig.AlertRules,
		Partitions: []resourcemonitor.PartitionParam{
			{
				Name: "storage",
				Path: filepath.Join(tmpDir, storagesDir, instance.StoragePath),
			},
			{
				Name: "state",
				Path: filepath.Join(tmpDir, statesDir, instance.StatePath),
			},
		},
	}) {
		t.Errorf("Wrong monitor params: %v", monitorPrams)
	}

	// Check mount

	mountInfo, ok := mounter.mounts[filepath.Join(launcher.RuntimeDir, instance.InstanceID, instanceRootFS)]
	if !ok {
		t.Error("Instance root FS should be mounted")
	}

	if mountInfo.upperDir != "" {
		t.Errorf("Wrong upper dir value: %v", mountInfo.upperDir)
	}

	if mountInfo.workDir != "" {
		t.Errorf("Wrong work dir value: %v", mountInfo.workDir)
	}

	service, err := serviceProvider.GetServiceInfo(instance.ServiceID)
	if err != nil {
		t.Fatalf("Can't get service info: %v", err)
	}

	imageParts, err := serviceProvider.GetImageParts(service)
	if err != nil {
		t.Fatalf("Can't get image parts: %v", err)
	}

	expectedLowerDirs := []string{
		filepath.Join(launcher.RuntimeDir, instance.InstanceID, "mounts"), imageParts.ServiceFSPath,
	}

	for _, digest := range imageParts.LayersDigest {
		layer, err := layerProvider.GetLayerInfoByDigest(digest)
		if err != nil {
			t.Fatalf("Can't get layer info: %v", err)
		}

		expectedLowerDirs = append(expectedLowerDirs, layer.Path)
	}

	expectedLowerDirs = append(expectedLowerDirs, filepath.Join(tmpDir, "hostfs", "whiteouts"), "/")

	if !reflect.DeepEqual(mountInfo.lowerDirs, expectedLowerDirs) {
		t.Errorf("Wrong lower dirs value: %v", mountInfo.lowerDirs)
	}

	// Stop instances and check runtime release

	if err = testLauncher.RunInstances(nil, false); err != nil {
		t.Fatalf("Can't stop instances: %v", err)
	}

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{}}, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	// Check registrar

	if _, ok := registrar.secrets[instance.InstanceIdent]; ok {
		t.Error("Instance should be unregistered")
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

	// Check mount

	if _, ok := mounter.mounts[filepath.Join(launcher.RuntimeDir, instance.InstanceID, instanceRootFS)]; ok {
		t.Error("Instance root FS should be unmounted")
	}
}

func TestOverrideEnvVars(t *testing.T) {
	defaultTTLPeriod := launcher.CheckTTLsPeriod

	launcher.CheckTTLsPeriod = 1 * time.Second

	t.Cleanup(func() { launcher.CheckTTLsPeriod = defaultTTLPeriod })

	type instanceEnvVars struct {
		aostypes.InstanceIdent
		envVars []string
	}

	type testData struct {
		envVars      []cloudprotocol.EnvVarsInstanceInfo
		status       []cloudprotocol.EnvVarsInstanceStatus
		instances    []instanceEnvVars
		waitDuration time.Duration
	}

	data := []testData{
		// Override env var for all instances of service0
		{
			envVars: []cloudprotocol.EnvVarsInstanceInfo{
				{
					InstanceFilter: cloudprotocol.InstanceFilter{ServiceID: newString("service0")},
					EnvVars: []cloudprotocol.EnvVarInfo{
						{ID: "id0", Variable: "VAR0=VAL0"},
						{ID: "id1", Variable: "VAR1=VAL1"},
						{ID: "id2", Variable: "VAR2=VAL2"},
					},
				},
			},
			status: []cloudprotocol.EnvVarsInstanceStatus{
				{
					InstanceFilter: cloudprotocol.InstanceFilter{ServiceID: newString("service0")},
					Statuses: []cloudprotocol.EnvVarStatus{
						{ID: "id0"}, {ID: "id1"}, {ID: "id2"},
					},
				},
			},
			instances: []instanceEnvVars{
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 0,
					},
					envVars: []string{"VAR0=VAL0", "VAR1=VAL1", "VAR2=VAL2"},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 1,
					},
					envVars: []string{"VAR0=VAL0", "VAR1=VAL1", "VAR2=VAL2"},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 2,
					},
					envVars: []string{"VAR0=VAL0", "VAR1=VAL1", "VAR2=VAL2"},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject1", Instance: 0,
					},
					envVars: []string{"VAR0=VAL0", "VAR1=VAL1", "VAR2=VAL2"},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject1", Instance: 1,
					},
					envVars: []string{"VAR0=VAL0", "VAR1=VAL1", "VAR2=VAL2"},
				},
			},
		},
		// Override env var for all instances of service0 subject0
		{
			envVars: []cloudprotocol.EnvVarsInstanceInfo{
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject0"),
					},
					EnvVars: []cloudprotocol.EnvVarInfo{
						{ID: "id3", Variable: "VAR3=VAL3"},
						{ID: "id4", Variable: "VAR4=VAL4"},
					},
				},
			},
			status: []cloudprotocol.EnvVarsInstanceStatus{
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject0"),
					},
					Statuses: []cloudprotocol.EnvVarStatus{
						{ID: "id3"}, {ID: "id4"},
					},
				},
			},
			instances: []instanceEnvVars{
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 0,
					},
					envVars: []string{"VAR3=VAL3", "VAR4=VAL4"},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 1,
					},
					envVars: []string{"VAR3=VAL3", "VAR4=VAL4"},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 2,
					},
					envVars: []string{"VAR3=VAL3", "VAR4=VAL4"},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject1", Instance: 0,
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject1", Instance: 1,
					},
				},
			},
		},
		// Override env var for instance of service0 subject0 instance 1
		{
			envVars: []cloudprotocol.EnvVarsInstanceInfo{
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject0"), Instance: newUint64(1),
					},
					EnvVars: []cloudprotocol.EnvVarInfo{{ID: "id5", Variable: "VAR5=VAL5"}},
				},
			},
			status: []cloudprotocol.EnvVarsInstanceStatus{
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject0"), Instance: newUint64(1),
					},
					Statuses: []cloudprotocol.EnvVarStatus{{ID: "id5"}},
				},
			},
			instances: []instanceEnvVars{
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 0,
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 1,
					},
					envVars: []string{"VAR5=VAL5"},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 2,
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject1", Instance: 0,
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject1", Instance: 1,
					},
				},
			},
		},
		// Set expired env vars for service0 subject0
		{
			envVars: []cloudprotocol.EnvVarsInstanceInfo{
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject0"),
					},
					EnvVars: []cloudprotocol.EnvVarInfo{
						{ID: "id6", Variable: "VAR6=VAL6", TTL: newTime(time.Now().Add(-10 * time.Second))},
					},
				},
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject1"),
					},
					EnvVars: []cloudprotocol.EnvVarInfo{
						{ID: "id7", Variable: "VAR7=VAL7", TTL: newTime(time.Now().Add(10 * time.Second))},
					},
				},
			},
			status: []cloudprotocol.EnvVarsInstanceStatus{
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject0"),
					},
					Statuses: []cloudprotocol.EnvVarStatus{{ID: "id6", Error: "environment variable expired"}},
				},
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject1"),
					},
					Statuses: []cloudprotocol.EnvVarStatus{{ID: "id7"}},
				},
			},
			instances: []instanceEnvVars{
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 0,
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 1,
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 2,
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject1", Instance: 0,
					},
					envVars: []string{"VAR7=VAL7"},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject1", Instance: 1,
					},
					envVars: []string{"VAR7=VAL7"},
				},
			},
		},
		// Check runtime env var expiration
		{
			envVars: []cloudprotocol.EnvVarsInstanceInfo{
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject0"),
					},
					EnvVars: []cloudprotocol.EnvVarInfo{
						{ID: "id8", Variable: "VAR8=VAL8", TTL: newTime(time.Now().Add(2 * time.Second))},
					},
				},
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject1"),
					},
					EnvVars: []cloudprotocol.EnvVarInfo{
						{ID: "id9", Variable: "VAR9=VAL9"},
					},
				},
			},
			status: []cloudprotocol.EnvVarsInstanceStatus{
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject0"),
					},
					Statuses: []cloudprotocol.EnvVarStatus{{ID: "id8"}},
				},
				{
					InstanceFilter: cloudprotocol.InstanceFilter{
						ServiceID: newString("service0"), SubjectID: newString("subject1"),
					},
					Statuses: []cloudprotocol.EnvVarStatus{{ID: "id9"}},
				},
			},
			instances: []instanceEnvVars{
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 0,
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 1,
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject0", Instance: 2,
					},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject1", Instance: 0,
					},
					envVars: []string{"VAR9=VAL9"},
				},
				{
					InstanceIdent: aostypes.InstanceIdent{
						ServiceID: "service0", SubjectID: "subject1", Instance: 1,
					},
					envVars: []string{"VAR9=VAL9"},
				},
			},
			waitDuration: 4 * time.Second,
		},
	}

	runItem := testItem{
		services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: "service0"}}},
		instances: []aostypes.InstanceInfo{
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 1}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 2}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject1", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject1", Instance: 1}},
		},
	}

	serviceProvider := newTestServiceProvider()
	layerProvider := newTestLayerProvider()
	storage := newTestStorage()

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider, layerProvider,
		newTestRunner(nil, nil), newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(),
		newTestInstanceMonitor(), newTestAlertSender())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{}}, defaultStatusTimeout); err != nil {
		t.Fatalf("Check runtime status error: %v", err)
	}

	if err = serviceProvider.installServices(runItem.services); err != nil {
		t.Fatalf("Can't install services: %v", err)
	}

	if err = layerProvider.installLayers(runItem.layers); err != nil {
		t.Fatalf("Can't install layers: %v", err)
	}

	if err = testLauncher.RunInstances(runItem.instances, false); err != nil {
		t.Fatalf("Can't run instances: %v", err)
	}

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(runItem)}},
		defaultStatusTimeout); err != nil {
		t.Fatalf("Check runtime status error: %v", err)
	}

	for i, item := range data {
		t.Logf("Override env vars: %d", i)

		status, err := testLauncher.OverrideEnvVars(item.envVars)
		if err != nil {
			t.Fatalf("Error override env vars: %v", err)
		}

		if err = compareOverrideEnvVarsStatus(item.status, status); err != nil {
			t.Errorf("Check override env vars status error: %v", err)
		}

		time.Sleep(item.waitDuration)

		for _, checkInstance := range item.instances {
			instanceInfo, err := storage.getInstanceByIdent(checkInstance.InstanceIdent)
			if err != nil {
				t.Fatalf("Can't get instance: %v", err)
			}

			runtimeSpec, err := getInstanceRuntimeSpec(instanceInfo.InstanceID)
			if err != nil {
				t.Fatalf("Can't get instance runtime spec: %v", err)
			}

			expectedEnvVars := append(getAosEnvVars(instanceInfo), checkInstance.envVars...)
			expectedEnvVars = append(expectedEnvVars, defaultEnvVars...)

			if !compareArrays(len(expectedEnvVars), len(runtimeSpec.Process.Env), func(index1, index2 int) bool {
				return expectedEnvVars[index1] == runtimeSpec.Process.Env[index2]
			}) {
				t.Errorf("Wrong env variables value: %v", runtimeSpec.Process.Env)
			}
		}
	}
}

func TestRestartStoredInstancesOnStart(t *testing.T) {
	storage := newTestStorage()
	serviceProvider := newTestServiceProvider()

	runItem := testItem{
		services: []serviceInfo{
			{ServiceInfo: aostypes.ServiceInfo{ID: "service0"}},
			{ServiceInfo: aostypes.ServiceInfo{ID: "service1"}},
			{ServiceInfo: aostypes.ServiceInfo{ID: "service2"}},
		},
		instances: []aostypes.InstanceInfo{
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 1}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 2}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject1", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject1", Instance: 1}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject1", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject3", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject3", Instance: 1}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject4", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject4", Instance: 1}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject4", Instance: 2}},
		},
	}

	storage.fromTestItem(runItem)

	if err := serviceProvider.installServices(runItem.services); err != nil {
		t.Fatalf("Can't install services: %v", err)
	}

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider,
		newTestLayerProvider(), newTestRunner(nil, nil), newTestResourceManager(), newTestNetworkManager(),
		newTestRegistrar(), newTestInstanceMonitor(), newTestAlertSender())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(runItem)}},
		defaultStatusTimeout); err != nil {
		t.Fatalf("Check runtime status error: %v", err)
	}
}

func TestInstancePriorities(t *testing.T) {
	const (
		service = "service0"
		subject = "subject0"
	)

	type testPriorityItem struct {
		testItem
		startedInstances []aostypes.InstanceInfo
		stoppedInstances []aostypes.InstanceInfo
	}

	instancePriority := func(index, priority uint64) aostypes.InstanceInfo {
		return aostypes.InstanceInfo{
			InstanceIdent: aostypes.InstanceIdent{
				ServiceID: service, SubjectID: subject, Instance: index,
			},
			Priority: priority,
		}
	}

	data := []testPriorityItem{
		// start from scratch
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 400),
					instancePriority(2, 400),
					instancePriority(3, 300),
					instancePriority(4, 300),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 500),
					instancePriority(9, 200),
				},
			},
			startedInstances: []aostypes.InstanceInfo{
				instancePriority(5, 500),
				instancePriority(6, 500),
				instancePriority(7, 500),
				instancePriority(8, 500),
				instancePriority(0, 400),
				instancePriority(1, 400),
				instancePriority(2, 400),
				instancePriority(3, 300),
				instancePriority(4, 300),
				instancePriority(9, 200),
			},
		},
		// change instance 4 to the highest priority
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 400),
					instancePriority(2, 400),
					instancePriority(3, 300),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 500),
					instancePriority(9, 200),
				},
			},
			startedInstances: []aostypes.InstanceInfo{
				instancePriority(4, 600),
				instancePriority(5, 500),
				instancePriority(6, 500),
				instancePriority(7, 500),
				instancePriority(8, 500),
				instancePriority(0, 400),
				instancePriority(1, 400),
				instancePriority(2, 400),
				instancePriority(3, 300),
				instancePriority(9, 200),
			},
			stoppedInstances: []aostypes.InstanceInfo{
				instancePriority(0, 400),
				instancePriority(1, 400),
				instancePriority(2, 400),
				instancePriority(3, 300),
				instancePriority(4, 300),
				instancePriority(5, 500),
				instancePriority(6, 500),
				instancePriority(7, 500),
				instancePriority(8, 500),
				instancePriority(9, 200),
			},
		},
		// change instance 8 to the lowest priority
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 400),
					instancePriority(2, 400),
					instancePriority(3, 300),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 100),
					instancePriority(9, 200),
				},
			},
			startedInstances: []aostypes.InstanceInfo{
				instancePriority(8, 100),
			},
			stoppedInstances: []aostypes.InstanceInfo{
				instancePriority(8, 500),
			},
		},
		// change instance 1 to priority 500
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 500),
					instancePriority(2, 400),
					instancePriority(3, 300),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 100),
					instancePriority(9, 200),
				},
			},
			startedInstances: []aostypes.InstanceInfo{
				instancePriority(1, 500),
				instancePriority(0, 400),
				instancePriority(2, 400),
				instancePriority(3, 300),
				instancePriority(9, 200),
				instancePriority(8, 100),
			},
			stoppedInstances: []aostypes.InstanceInfo{
				instancePriority(0, 400),
				instancePriority(1, 400),
				instancePriority(2, 400),
				instancePriority(3, 300),
				instancePriority(8, 100),
				instancePriority(9, 200),
			},
		},
		// Add new instances with the highest priority
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 500),
					instancePriority(2, 400),
					instancePriority(3, 300),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 100),
					instancePriority(9, 200),
					instancePriority(10, 700),
					instancePriority(11, 700),
				},
			},
			startedInstances: []aostypes.InstanceInfo{
				instancePriority(10, 700),
				instancePriority(11, 700),
				instancePriority(4, 600),
				instancePriority(1, 500),
				instancePriority(5, 500),
				instancePriority(6, 500),
				instancePriority(7, 500),
				instancePriority(0, 400),
				instancePriority(2, 400),
				instancePriority(3, 300),
				instancePriority(9, 200),
				instancePriority(8, 100),
			},
			stoppedInstances: []aostypes.InstanceInfo{
				instancePriority(0, 400),
				instancePriority(1, 500),
				instancePriority(2, 400),
				instancePriority(3, 300),
				instancePriority(4, 600),
				instancePriority(5, 500),
				instancePriority(6, 500),
				instancePriority(7, 500),
				instancePriority(8, 100),
				instancePriority(9, 200),
			},
		},
		// Add new instances with the lowest priority
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 500),
					instancePriority(2, 400),
					instancePriority(3, 300),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 100),
					instancePriority(9, 200),
					instancePriority(10, 700),
					instancePriority(11, 700),
					instancePriority(13, 0),
					instancePriority(14, 0),
				},
			},
			startedInstances: []aostypes.InstanceInfo{
				instancePriority(13, 0),
				instancePriority(14, 0),
			},
			stoppedInstances: []aostypes.InstanceInfo{},
		},
		// Add new instances with the middle priority
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 500),
					instancePriority(2, 400),
					instancePriority(3, 300),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 100),
					instancePriority(9, 200),
					instancePriority(10, 700),
					instancePriority(11, 700),
					instancePriority(13, 0),
					instancePriority(14, 0),
					instancePriority(15, 300),
					instancePriority(16, 300),
				},
			},
			startedInstances: []aostypes.InstanceInfo{
				instancePriority(15, 300),
				instancePriority(16, 300),
				instancePriority(9, 200),
				instancePriority(8, 100),
				instancePriority(13, 0),
				instancePriority(14, 0),
			},
			stoppedInstances: []aostypes.InstanceInfo{
				instancePriority(8, 100),
				instancePriority(9, 200),
				instancePriority(13, 0),
				instancePriority(14, 0),
			},
		},
		// Remove instances with the highest priority
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 500),
					instancePriority(2, 400),
					instancePriority(3, 300),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 100),
					instancePriority(9, 200),
					instancePriority(13, 0),
					instancePriority(14, 0),
					instancePriority(15, 300),
					instancePriority(16, 300),
				},
			},
			startedInstances: []aostypes.InstanceInfo{},
			stoppedInstances: []aostypes.InstanceInfo{
				instancePriority(10, 700),
				instancePriority(11, 700),
			},
		},
		// Remove instances with the lowest priority
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 500),
					instancePriority(2, 400),
					instancePriority(3, 300),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 100),
					instancePriority(9, 200),
					instancePriority(15, 300),
					instancePriority(16, 300),
				},
			},
			startedInstances: []aostypes.InstanceInfo{},
			stoppedInstances: []aostypes.InstanceInfo{
				instancePriority(13, 0),
				instancePriority(14, 0),
			},
		},
		// Remove instances with the middle priority
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 500),
					instancePriority(2, 400),
					instancePriority(3, 300),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 100),
					instancePriority(9, 200),
				},
			},
			startedInstances: []aostypes.InstanceInfo{},
			stoppedInstances: []aostypes.InstanceInfo{
				instancePriority(15, 300),
				instancePriority(16, 300),
			},
		},
		// Change instance 3 priority to the same order
		{
			testItem: testItem{
				services: []serviceInfo{{ServiceInfo: aostypes.ServiceInfo{ID: service}}},
				instances: []aostypes.InstanceInfo{
					instancePriority(0, 400),
					instancePriority(1, 500),
					instancePriority(2, 400),
					instancePriority(3, 350),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 100),
					instancePriority(9, 200),
				},
				err: []error{nil, nil, nil, errors.New("some error")}, //nolint:goerr113
			},
			startedInstances: []aostypes.InstanceInfo{
				instancePriority(3, 350),
				instancePriority(9, 200),
				instancePriority(8, 100),
			},
			stoppedInstances: []aostypes.InstanceInfo{
				instancePriority(3, 300),
				instancePriority(8, 100),
				instancePriority(9, 200),
			},
		},
		// Remove instances with the middle priority and there is not successfully started instance
		{
			testItem: testItem{
				services: []serviceInfo{
					{ServiceInfo: aostypes.ServiceInfo{ID: service}},
				},
				instances: []aostypes.InstanceInfo{
					instancePriority(1, 500),
					instancePriority(2, 400),
					instancePriority(3, 350),
					instancePriority(4, 600),
					instancePriority(5, 500),
					instancePriority(6, 500),
					instancePriority(7, 500),
					instancePriority(8, 100),
					instancePriority(9, 200),
				},
			},
			startedInstances: []aostypes.InstanceInfo{
				instancePriority(3, 350),
				instancePriority(9, 200),
				instancePriority(8, 100),
			},
			stoppedInstances: []aostypes.InstanceInfo{
				instancePriority(0, 400),
				instancePriority(3, 350),
				instancePriority(8, 100),
				instancePriority(9, 200),
			},
		},
	}

	var (
		currentTestItem     testItem
		startedInstances    = make([]aostypes.InstanceInfo, 0)
		stoppedInstances    = make([]aostypes.InstanceInfo, 0)
		oldStorageInstances map[string]launcher.InstanceInfo
	)

	serviceProvider := newTestServiceProvider()
	layerProvider := newTestLayerProvider()
	storage := newTestStorage()
	instanceRunner := newTestRunner(
		func(instanceID string) runner.InstanceStatus {
			if instance, ok := storage.instances[instanceID]; ok {
				startedInstances = append(startedInstances, instance.InstanceInfo)
			}

			return getRunnerStatus(instanceID, currentTestItem, storage)
		},
		func(instanceID string) error {
			if instance, ok := oldStorageInstances[instanceID]; ok {
				stoppedInstances = append(stoppedInstances, instance.InstanceInfo)
			}

			return nil
		},
	)

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider, layerProvider,
		instanceRunner, newTestResourceManager(), newTestNetworkManager(), newTestRegistrar(), newTestInstanceMonitor(),
		newTestAlertSender())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{}}, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	for i, item := range data {
		t.Logf("Run instances: %d", i)

		currentTestItem = item.testItem
		startedInstances = make([]aostypes.InstanceInfo, 0)
		stoppedInstances = make([]aostypes.InstanceInfo, 0)

		if err = serviceProvider.installServices(item.services); err != nil {
			t.Fatalf("Can't install services: %v", err)
		}

		if err = layerProvider.installLayers(item.layers); err != nil {
			t.Fatalf("Can't install layers: %v", err)
		}

		if err = testLauncher.RunInstances(item.instances, false); err != nil {
			t.Fatalf("Can't run instances: %v", err)
		}

		runtimeStatus := launcher.RuntimeStatus{
			RunStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(item.testItem)},
		}

		oldStorageInstances = make(map[string]launcher.InstanceInfo)

		for instanceID, instance := range storage.instances {
			oldStorageInstances[instanceID] = instance
		}

		if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
			runtimeStatus, defaultStatusTimeout); err != nil {
			t.Errorf("Check runtime status error: %v", err)
		}

		if err = checkInstancesByPriority(startedInstances, item.startedInstances); err != nil {
			t.Errorf("Check instances by priority error: %v", err)
		}

		if !compareArrays(len(stoppedInstances), len(item.stoppedInstances), func(index1, index2 int) bool {
			return reflect.DeepEqual(stoppedInstances[index1], item.stoppedInstances[index2])
		}) {
			t.Errorf("Wrong stopped instances: %v", stoppedInstances)
		}
	}
}

func TestResourceAlerts(t *testing.T) {
	type testAlertItem struct {
		testItem
		alerts []cloudprotocol.DeviceAllocateAlert
	}

	serviceConfig := &aostypes.ServiceConfig{Devices: []aostypes.ServiceDevice{{Name: "device0", Permissions: "rw"}}}

	data := []testAlertItem{
		// Try to allocate device3 (shared count 1) by 3 instances. Instance with index 0 should allocate the device as
		// it has higher priority.
		{
			testItem: testItem{
				services: []serviceInfo{
					{
						ServiceInfo:   aostypes.ServiceInfo{ID: "service0"},
						serviceConfig: serviceConfig,
					},
				},
				instances: []aostypes.InstanceInfo{
					{
						InstanceIdent: aostypes.InstanceIdent{
							ServiceID: "service0", SubjectID: "subject0", Instance: 0,
						},
						Priority: 100,
					},
					{
						InstanceIdent: aostypes.InstanceIdent{
							ServiceID: "service0", SubjectID: "subject0", Instance: 1,
						},
					},
					{
						InstanceIdent: aostypes.InstanceIdent{
							ServiceID: "service0", SubjectID: "subject0", Instance: 2,
						},
					},
				},
				err: []error{nil, resourcemanager.ErrNoAvailableDevice, resourcemanager.ErrNoAvailableDevice},
			},
			alerts: []cloudprotocol.DeviceAllocateAlert{
				createDeviceAllocateAlert(aostypes.InstanceIdent{
					ServiceID: "service0", SubjectID: "subject0", Instance: 1,
				}, "device0"),
				createDeviceAllocateAlert(aostypes.InstanceIdent{
					ServiceID: "service0", SubjectID: "subject0", Instance: 2,
				}, "device0"),
			},
		},
		// Try to allocate device3 (shared count 1) by 3 instances. Instance with index 0 should allocate the device as
		// it has higher priority.
		{
			testItem: testItem{
				services: []serviceInfo{
					{
						ServiceInfo:   aostypes.ServiceInfo{ID: "service0"},
						serviceConfig: serviceConfig,
					},
				},
				instances: []aostypes.InstanceInfo{
					{
						InstanceIdent: aostypes.InstanceIdent{
							ServiceID: "service0", SubjectID: "subject0", Instance: 0,
						},
						Priority: 100,
					},
					{
						InstanceIdent: aostypes.InstanceIdent{
							ServiceID: "service0", SubjectID: "subject0", Instance: 1,
						},
					},
					{
						InstanceIdent: aostypes.InstanceIdent{
							ServiceID: "service0", SubjectID: "subject0", Instance: 2,
						},
					},
					{
						InstanceIdent: aostypes.InstanceIdent{
							ServiceID: "service0", SubjectID: "subject0", Instance: 3,
						},
						Priority: 200,
					},
				},
				err: []error{
					resourcemanager.ErrNoAvailableDevice, resourcemanager.ErrNoAvailableDevice,
					resourcemanager.ErrNoAvailableDevice, nil,
				},
			},
			alerts: []cloudprotocol.DeviceAllocateAlert{
				createDeviceAllocateAlert(aostypes.InstanceIdent{
					ServiceID: "service0", SubjectID: "subject0", Instance: 0,
				}, "device0"),
				createDeviceAllocateAlert(aostypes.InstanceIdent{
					ServiceID: "service0", SubjectID: "subject0", Instance: 1,
				}, "device0"),
				createDeviceAllocateAlert(aostypes.InstanceIdent{
					ServiceID: "service0", SubjectID: "subject0", Instance: 2,
				}, "device0"),
			},
		},
	}

	serviceProvider := newTestServiceProvider()
	layerProvider := newTestLayerProvider()
	resourceManager := newTestResourceManager()
	alertSender := newTestAlertSender()

	resourceManager.addDevice(aostypes.DeviceInfo{Name: "device0", SharedCount: 1})

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, newTestStorage(), serviceProvider,
		newTestLayerProvider(), newTestRunner(nil, nil), resourceManager, newTestNetworkManager(),
		newTestRegistrar(), newTestInstanceMonitor(), alertSender)
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{}}, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	for i, item := range data {
		t.Logf("Run instances: %d", i)

		alertSender.alerts = nil

		if err = serviceProvider.installServices(item.services); err != nil {
			t.Fatalf("Can't install services: %v", err)
		}

		if err = layerProvider.installLayers(item.layers); err != nil {
			t.Fatalf("Can't install layers: %v", err)
		}

		if err = testLauncher.RunInstances(item.instances, false); err != nil {
			t.Fatalf("Can't run instances: %v", err)
		}

		runtimeStatus := launcher.RuntimeStatus{
			RunStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(item.testItem)},
		}

		if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(), runtimeStatus,
			defaultStatusTimeout); err != nil {
			t.Errorf("Check runtime status error: %v", err)
		}

		if err = compareDeviceAllocateAlerts(item.alerts, alertSender.alerts); err != nil {
			t.Errorf("Compare device allocation alerts error: %v", err)
		}
	}
}

func TestOfflineTimeout(t *testing.T) {
	launcher.CheckTTLsPeriod = 1 * time.Second

	serviceProvider := newTestServiceProvider()
	storage := newTestStorage()

	testLauncher, err := launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider,
		newTestLayerProvider(), newTestRunner(nil, nil), newTestResourceManager(), newTestNetworkManager(),
		newTestRegistrar(), newTestInstanceMonitor(), newTestAlertSender())
	if err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{}}, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	item := testItem{
		services: []serviceInfo{
			{
				ServiceInfo: aostypes.ServiceInfo{ID: "service0"},
			},
			{
				ServiceInfo: aostypes.ServiceInfo{ID: "service1"},
				serviceConfig: &aostypes.ServiceConfig{
					OfflineTTL: aostypes.Duration{Duration: 5 * time.Second},
				},
			},
			{
				ServiceInfo: aostypes.ServiceInfo{ID: "service2"},
			},
			{
				ServiceInfo: aostypes.ServiceInfo{ID: "service3"},
				serviceConfig: &aostypes.ServiceConfig{
					OfflineTTL: aostypes.Duration{Duration: 10 * time.Second},
				},
			},
		},
		instances: []aostypes.InstanceInfo{
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 1}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 1}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 2}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service3", SubjectID: "subject0", Instance: 0}},
			{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service3", SubjectID: "subject0", Instance: 1}},
		},
	}

	if err = serviceProvider.installServices(item.services); err != nil {
		t.Fatalf("Can't install services: %v", err)
	}

	if err := testLauncher.CloudConnection(true); err != nil {
		t.Errorf("Can't set cloud connection: %v", err)
	}

	if err = testLauncher.RunInstances(item.instances, false); err != nil {
		t.Fatalf("Can't run instances: %v", err)
	}

	runtimeStatus := launcher.RuntimeStatus{
		RunStatus: &launcher.InstancesStatus{Instances: createInstancesStatuses(item)},
	}

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(), runtimeStatus, defaultStatusTimeout); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	// Should not be stopped instance during max offline timeout (> 15 sec)

	select {
	case <-testLauncher.RuntimeStatusChannel():
		t.Error("Unexpected runtime status")

	case <-time.After(20 * time.Second):
	}

	// Set cloud connection offline

	if err := testLauncher.CloudConnection(false); err != nil {
		t.Errorf("Can't set cloud connection: %v", err)
	}

	errOfflineTimeout := errors.New("offline timeout") //nolint:goerr113

	// We should receive offline timeout status from service1 instances first

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{UpdateStatus: &launcher.InstancesStatus{
			Instances: createInstancesStatuses(testItem{
				instances: []aostypes.InstanceInfo{
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 0}},
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 1}},
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 2}},
				},
				err: []error{
					errOfflineTimeout, errOfflineTimeout, errOfflineTimeout, errOfflineTimeout, errOfflineTimeout,
				},
			}),
		}}, 20*time.Second); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	// Then we should receive offline timeout status from service3 instances

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{UpdateStatus: &launcher.InstancesStatus{
			Instances: createInstancesStatuses(testItem{
				instances: []aostypes.InstanceInfo{
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service3", SubjectID: "subject0", Instance: 0}},
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service3", SubjectID: "subject0", Instance: 1}},
				},
				err: []error{
					errOfflineTimeout, errOfflineTimeout, errOfflineTimeout, errOfflineTimeout, errOfflineTimeout,
				},
			}),
		}}, 20*time.Second); err != nil {
		t.Errorf("Check runtime status error: %v", err)
	}

	// Check offline timeout after launcher restart

	testLauncher.Close()

	if testLauncher, err = launcher.New(&config.Config{WorkingDir: tmpDir}, storage, serviceProvider,
		newTestLayerProvider(), newTestRunner(nil, nil), newTestResourceManager(), newTestNetworkManager(),
		newTestRegistrar(), newTestInstanceMonitor(), newTestAlertSender()); err != nil {
		t.Fatalf("Can't create launcher: %v", err)
	}
	defer testLauncher.Close()

	if err = checkRuntimeStatus(testLauncher.RuntimeStatusChannel(),
		launcher.RuntimeStatus{RunStatus: &launcher.InstancesStatus{
			Instances: createInstancesStatuses(testItem{
				instances: []aostypes.InstanceInfo{
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 0}},
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 1}},
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service1", SubjectID: "subject0", Instance: 2}},
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service3", SubjectID: "subject0", Instance: 0}},
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service3", SubjectID: "subject0", Instance: 1}},
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 0}},
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service0", SubjectID: "subject0", Instance: 1}},
					{InstanceIdent: aostypes.InstanceIdent{ServiceID: "service2", SubjectID: "subject0", Instance: 0}},
				},
				err: []error{
					errOfflineTimeout, errOfflineTimeout, errOfflineTimeout, errOfflineTimeout, errOfflineTimeout,
				},
			}),
		}}, defaultStatusTimeout); err != nil {
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

func (storage *testStorage) GetAllInstances() (instances []launcher.InstanceInfo, err error) {
	storage.RLock()
	defer storage.RUnlock()

	for _, instance := range storage.instances {
		instances = append(instances, instance)
	}

	return instances, nil
}

func (storage *testStorage) GetOverrideEnvVars() ([]cloudprotocol.EnvVarsInstanceInfo, error) {
	storage.RLock()
	defer storage.RUnlock()

	return storage.envVars, nil
}

func (storage *testStorage) SetOverrideEnvVars(envVarsInfo []cloudprotocol.EnvVarsInstanceInfo) error {
	storage.Lock()
	defer storage.Unlock()

	storage.envVars = envVarsInfo

	return nil
}

func (storage *testStorage) GetOnlineTime() (time.Time, error) {
	storage.Lock()
	defer storage.Unlock()

	return storage.onlineTime, nil
}

func (storage *testStorage) SetOnlineTime(onlineTime time.Time) error {
	storage.Lock()
	defer storage.Unlock()

	storage.onlineTime = onlineTime

	return nil
}

func (storage *testStorage) fromTestItem(item testItem) {
	storage.Lock()
	defer storage.Unlock()

	storage.instances = make(map[string]launcher.InstanceInfo)

	for _, instance := range item.instances {
		instanceID := uuid.New().String()

		storage.instances[instanceID] = launcher.InstanceInfo{
			InstanceInfo: instance,
			InstanceID:   instanceID,
		}
	}
}

/***********************************************************************************************************************
 * testServiceProvider
 **********************************************************************************************************************/

func newTestServiceProvider() *testServiceProvider {
	return &testServiceProvider{}
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
	if _, ok := provider.services[service.ServiceID]; !ok {
		return servicemanager.ImageParts{}, servicemanager.ErrNotExist
	}

	return servicemanager.ImageParts{
		ImageConfigPath:   filepath.Join(service.ImagePath, imageConfigFile),
		ServiceConfigPath: filepath.Join(service.ImagePath, serviceConfigFile),
		ServiceFSPath:     filepath.Join(service.ImagePath, instanceRootFS),
		LayersDigest:      provider.layerDigests[service.ServiceID],
	}, nil
}

func (provider *testServiceProvider) ValidateService(service servicemanager.ServiceInfo) error {
	return nil
}

func (provider *testServiceProvider) installServices(services []serviceInfo) error {
	if err := os.RemoveAll(filepath.Join(tmpDir, servicesDir)); err != nil {
		return aoserrors.Wrap(err)
	}

	provider.services = make(map[string]servicemanager.ServiceInfo)
	provider.layerDigests = map[string][]string{}

	for _, service := range services {
		servicePath := filepath.Join(tmpDir, servicesDir, service.ID)

		provider.services[service.ID] = servicemanager.ServiceInfo{
			VersionInfo:     service.VersionInfo,
			ServiceID:       service.ID,
			ServiceProvider: service.ProviderID,
			ImagePath:       servicePath,
			GID:             service.gid,
		}

		provider.layerDigests[service.ID] = service.layerDigests

		if err := os.MkdirAll(filepath.Join(servicePath, instanceRootFS), 0o755); err != nil {
			return aoserrors.Wrap(err)
		}

		imageConfig := &imagespec.Image{OS: "linux"}

		if service.imageConfig != nil {
			imageConfig = service.imageConfig
		}

		if err := writeConfig(filepath.Join(tmpDir, servicesDir, service.ID, imageConfigFile),
			imageConfig); err != nil {
			return err
		}

		if service.serviceConfig != nil {
			if err := writeConfig(filepath.Join(tmpDir, servicesDir, service.ID, serviceConfigFile),
				service.serviceConfig); err != nil {
				return err
			}
		}
	}

	return nil
}

func (storage *testStorage) getInstanceByIdent(instanceIdent aostypes.InstanceIdent) (launcher.InstanceInfo, error) {
	for _, instance := range storage.instances {
		if instance.InstanceIdent == instanceIdent {
			return instance, nil
		}
	}

	return launcher.InstanceInfo{}, launcher.ErrNotExist
}

/***********************************************************************************************************************
 * testLayerProvider
 **********************************************************************************************************************/

func newTestLayerProvider() *testLayerProvider {
	return &testLayerProvider{
		layers: make(map[string]layermanager.LayerInfo),
	}
}

func (provider *testLayerProvider) GetLayerInfoByDigest(digest string) (layermanager.LayerInfo, error) {
	layer, ok := provider.layers[digest]
	if !ok {
		return layermanager.LayerInfo{}, layermanager.ErrNotExist
	}

	return layer, nil
}

func (provider *testLayerProvider) installLayers(layers []aostypes.LayerInfo) error {
	provider.layers = make(map[string]layermanager.LayerInfo)

	if err := os.RemoveAll(filepath.Join(tmpDir, layersDir)); err != nil {
		return aoserrors.Wrap(err)
	}

	for _, layer := range layers {
		layerPath := filepath.Join(tmpDir, layersDir, layer.Digest)

		provider.layers[layer.Digest] = layermanager.LayerInfo{
			VersionInfo: layer.VersionInfo,
			Digest:      layer.Digest,
			Path:        layerPath,
		}

		if err := os.MkdirAll(layerPath, 0o755); err != nil {
			return aoserrors.Wrap(err)
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
	instanceID, runtimeDir string, params runner.RunParameters,
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
		devices:          make(map[string]aostypes.DeviceInfo),
		resources:        make(map[string]aostypes.ResourceInfo),
	}
}

func (manager *testResourceManager) GetDeviceInfo(device string) (aostypes.DeviceInfo, error) {
	manager.RLock()
	defer manager.RUnlock()

	deviceInfo, ok := manager.devices[device]
	if !ok {
		return aostypes.DeviceInfo{}, aoserrors.New("device info not found")
	}

	return deviceInfo, nil
}

func (manager *testResourceManager) GetResourceInfo(resource string) (aostypes.ResourceInfo, error) {
	manager.RLock()
	defer manager.RUnlock()

	resourceInfo, ok := manager.resources[resource]
	if !ok {
		return aostypes.ResourceInfo{}, aoserrors.New("resource info not found")
	}

	return resourceInfo, nil
}

func (manager *testResourceManager) AllocateDevice(device, instanceID string) error {
	manager.Lock()
	defer manager.Unlock()

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
		return resourcemanager.ErrNoAvailableDevice
	}

	manager.allocatedDevices[device] = append(manager.allocatedDevices[device], instanceID)

	return nil
}

func (manager *testResourceManager) ReleaseDevice(device, instanceID string) error {
	manager.Lock()
	defer manager.Unlock()

	if _, ok := manager.devices[device]; !ok {
		return aoserrors.New("device info not found")
	}

	for i, allocatedInstanceID := range manager.allocatedDevices[device] {
		if allocatedInstanceID == instanceID {
			manager.allocatedDevices[device] = append(manager.allocatedDevices[device][:i],
				manager.allocatedDevices[device][i+1:]...)

			return nil
		}
	}

	return aoserrors.Errorf("device %s is not allocated for instance %s", device, instanceID)
}

func (manager *testResourceManager) ReleaseDevices(instanceID string) error {
	manager.Lock()
	defer manager.Unlock()

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

func (manager *testResourceManager) GetDeviceInstances(name string) (instanceIDs []string, err error) {
	manager.RLock()
	defer manager.RUnlock()

	if _, ok := manager.devices[name]; !ok {
		return nil, aoserrors.New("device info not found")
	}

	return manager.allocatedDevices[name], nil
}

func (manager *testResourceManager) addDevice(device aostypes.DeviceInfo) {
	manager.Lock()
	defer manager.Unlock()

	manager.devices[device.Name] = device
}

func (manager *testResourceManager) addResource(resource aostypes.ResourceInfo) {
	manager.Lock()
	defer manager.Unlock()

	for _, mount := range resource.Mounts {
		if err := os.MkdirAll(mount.Source, 0o755); err != nil {
			log.Errorf("Can't create mount dir: %v", err)

			return
		}
	}

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
	return &testRegistrar{secrets: make(map[aostypes.InstanceIdent]string)}
}

func (registrar *testRegistrar) RegisterInstance(
	instance aostypes.InstanceIdent, permissions map[string]map[string]string,
) (secret string, err error) {
	registrar.Lock()
	defer registrar.Unlock()

	if _, ok := registrar.secrets[instance]; ok {
		return "", aoserrors.New("instance already registered")
	}

	secret = uuid.NewString()

	registrar.secrets[instance] = secret

	return secret, nil
}

func (registrar *testRegistrar) UnregisterInstance(instance aostypes.InstanceIdent) error {
	registrar.Lock()
	defer registrar.Unlock()

	if _, ok := registrar.secrets[instance]; !ok {
		return aoserrors.New("instance is not registered")
	}

	delete(registrar.secrets, instance)

	return nil
}

/***********************************************************************************************************************
 * testInstanceMonitor
 **********************************************************************************************************************/

func newTestInstanceMonitor() *testInstanceMonitor {
	return &testInstanceMonitor{instances: make(map[string]resourcemonitor.ResourceMonitorParams)}
}

func (monitor *testInstanceMonitor) StartInstanceMonitor(
	instanceID string, params resourcemonitor.ResourceMonitorParams,
) error {
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
 * testMounter
 **********************************************************************************************************************/

func newTestMounter() *testMounter {
	return &testMounter{mounts: make(map[string]mountInfo)}
}

func (mounter *testMounter) Mount(mountPoint string, lowerDirs []string, workDir, upperDir string) error {
	mounter.Lock()
	defer mounter.Unlock()

	if _, ok := mounter.mounts[mountPoint]; ok {
		return aoserrors.Errorf("folder %s already mounted", mountPoint)
	}

	if _, err := os.Stat(mountPoint); err != nil {
		return aoserrors.Errorf("mount point err: %v", err)
	}

	for _, lowerDir := range lowerDirs {
		if _, err := os.Stat(lowerDir); err != nil {
			return aoserrors.Errorf("lower dir err: %v", err)
		}
	}

	if workDir != "" {
		if _, err := os.Stat(workDir); err != nil {
			return aoserrors.Errorf("work dir err: %v", err)
		}
	}

	if upperDir != "" {
		if _, err := os.Stat(upperDir); err != nil {
			return aoserrors.Errorf("upper dir err: %v", err)
		}
	}

	mounter.mounts[mountPoint] = mountInfo{
		lowerDirs: lowerDirs,
		workDir:   workDir,
		upperDir:  upperDir,
	}

	return nil
}

func (mounter *testMounter) Unmount(mountPoint string) error {
	mounter.Lock()
	defer mounter.Unlock()

	delete(mounter.mounts, mountPoint)

	return nil
}

/***********************************************************************************************************************
 * testAlertSender
 **********************************************************************************************************************/

func newTestAlertSender() *testAlertSender {
	return &testAlertSender{}
}

func (sender *testAlertSender) SendAlert(alertItem cloudprotocol.AlertItem) {
	if alert, ok := alertItem.Payload.(cloudprotocol.DeviceAllocateAlert); ok {
		sender.alerts = append(sender.alerts, alert)
	}
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func setup() (err error) {
	tmpDir, err = os.MkdirTemp("", "sm_")
	if err != nil {
		return aoserrors.Wrap(err)
	}

	launcher.RuntimeDir = filepath.Join(tmpDir, "runtime")
	launcher.MountFunc = mounter.Mount
	launcher.UnmountFunc = mounter.Unmount

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
		if !compareArrays(len(status1.RunStatus.Instances), len(status2.RunStatus.Instances),
			func(index1, index2 int) bool {
				return isInstanceStatusesEqual(status1.RunStatus.Instances[index1], status2.RunStatus.Instances[index2])
			}) {
			return aoserrors.New("run instances statuses mismatch")
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

func checkRuntimeStatus(statusChannel <-chan launcher.RuntimeStatus,
	refStatus launcher.RuntimeStatus, timeout time.Duration,
) error {
	select {
	case runtimeStatus := <-statusChannel:
		if err := compareRuntimeStatus(refStatus, runtimeStatus); err != nil {
			return err
		}

	case <-time.After(timeout):
		return aoserrors.New("Wait for runtime status timeout")
	}

	return nil
}

func checkInstancesByPriority(compInstances, refInstances []aostypes.InstanceInfo) error {
	if len(compInstances) != len(refInstances) {
		return aoserrors.New("wrong instances len")
	}

	if len(refInstances) == 0 {
		return nil
	}

	currentPriority := refInstances[0].Priority
	startPriorityIndex := 0

	for i, instance := range refInstances {
		if instance.Priority != currentPriority {
			compPriorityInstances := compInstances[startPriorityIndex:i]
			refPriorityInstances := refInstances[startPriorityIndex:i]

			if !compareArrays(len(compPriorityInstances), len(refPriorityInstances), func(index1, index2 int) bool {
				return reflect.DeepEqual(compPriorityInstances[index1], refPriorityInstances[index2])
			}) {
				return aoserrors.New("instance priorities mismatch")
			}

			currentPriority = instance.Priority
			startPriorityIndex = i
		}
	}

	return nil
}

func createInstancesStatuses(item testItem) (instancesStatuses []cloudprotocol.InstanceStatus) {
	for i, instance := range item.instances {
		instanceStatus := cloudprotocol.InstanceStatus{
			InstanceIdent: item.instances[i].InstanceIdent,
			RunState:      cloudprotocol.InstanceStateActive,
		}

		for _, service := range item.services {
			if instance.ServiceID == service.ID {
				instanceStatus.AosVersion = service.AosVersion
			}
		}

		if i < len(item.err) && item.err[i] != nil {
			instanceStatus.RunState = cloudprotocol.InstanceStateFailed
			instanceStatus.ErrorInfo = &cloudprotocol.ErrorInfo{
				Message: item.err[i].Error(),
			}
		}

		instancesStatuses = append(instancesStatuses, instanceStatus)
	}

	return instancesStatuses
}

func getRunnerStatus(instanceID string, item testItem, storage *testStorage) runner.InstanceStatus {
	activeStatus := runner.InstanceStatus{InstanceID: instanceID, State: cloudprotocol.InstanceStateActive}

	curInstance, ok := storage.instances[instanceID]
	if !ok {
		return activeStatus
	}

	for i, testInstance := range item.instances {
		if curInstance.InstanceIdent == testInstance.InstanceIdent && i < len(item.err) && item.err[i] != nil {
			return runner.InstanceStatus{
				InstanceID: instanceID,
				State:      cloudprotocol.InstanceStateFailed,
				Err:        item.err[i],
			}
		}
	}

	return activeStatus
}

func createRunStatus(storage *testStorage, item testItem) []runner.InstanceStatus {
	var runStatus []runner.InstanceStatus

	for i, instance := range item.instances {
		for _, storedInstance := range storage.instances {
			if storedInstance.InstanceIdent != instance.InstanceIdent {
				continue
			}

			status := runner.InstanceStatus{
				InstanceID: storedInstance.InstanceID,
				State:      cloudprotocol.InstanceStateActive,
			}

			if i < len(item.err) {
				status.Err = item.err[i]

				if status.Err != nil {
					status.State = cloudprotocol.InstanceStateFailed
				}
			}

			runStatus = append(runStatus, status)
		}
	}

	return runStatus
}

func writeConfig(fileName string, config interface{}) error {
	data, err := json.Marshal(config)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = os.WriteFile(fileName, data, 0o600); err != nil {
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

func newTime(value time.Time) *time.Time {
	return &value
}

func createSpecMounts(mounts []aostypes.FileSystemMount) (specMounts []runtimespec.Mount) {
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
	runtimeData, err := os.ReadFile(filepath.Join(launcher.RuntimeDir, instanceID, runtimeConfigFile))
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

func compareNetParams(p1, p2 networkmanager.NetworkParams) bool {
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

func isInstanceFilterEqual(filter1, filter2 cloudprotocol.InstanceFilter) bool {
	switch {
	case (filter1.ServiceID == nil && filter2.ServiceID != nil) ||
		(filter1.ServiceID != nil && filter2.ServiceID == nil):
		return false

	case (filter1.SubjectID == nil && filter2.SubjectID != nil) ||
		(filter1.SubjectID != nil && filter2.SubjectID == nil):
		return false

	case filter1.SubjectID != nil && filter2.SubjectID != nil && *filter1.SubjectID != *filter2.SubjectID:
		return false

	case (filter1.Instance == nil && filter2.Instance != nil) ||
		(filter1.Instance != nil && filter2.Instance == nil):
		return false

	case filter1.Instance != nil && filter2.Instance != nil && *filter1.Instance != *filter2.Instance:
		return false
	}

	return true
}

func compareOverrideEnvVarsStatus(status1, status2 []cloudprotocol.EnvVarsInstanceStatus) error {
	if !compareArrays(len(status1), len(status2),
		func(index1, index2 int) bool {
			status1, status2 := status1[index1], status2[index2]

			if !isInstanceFilterEqual(status1.InstanceFilter, status2.InstanceFilter) {
				return false
			}

			return compareArrays(len(status1.Statuses), len(status2.Statuses), func(index1, index2 int) bool {
				return status1.Statuses[index1].ID == status2.Statuses[index2].ID &&
					(strings.HasPrefix(status1.Statuses[index1].Error, status2.Statuses[index2].Error) ||
						(strings.HasPrefix(status2.Statuses[index2].Error, status1.Statuses[index1].Error)))
			})
		}) {
		return aoserrors.New("override env vars status mismatch")
	}

	return nil
}

func createDeviceAllocateAlert(
	instanceIdent aostypes.InstanceIdent, device string,
) cloudprotocol.DeviceAllocateAlert {
	return cloudprotocol.DeviceAllocateAlert{
		InstanceIdent: instanceIdent,
		Device:        device,
		Message:       resourcemanager.ErrNoAvailableDevice.Error(),
	}
}

func compareDeviceAllocateAlerts(alerts1, alerts2 []cloudprotocol.DeviceAllocateAlert) error {
	if !compareArrays(len(alerts1), len(alerts2),
		func(index1, index2 int) bool {
			if alerts1[index1].InstanceIdent != alerts2[index2].InstanceIdent {
				return false
			}

			if alerts1[index1].Device != alerts2[index2].Device {
				return false
			}

			return (strings.HasPrefix(alerts1[index1].Message, alerts2[index2].Message) ||
				(strings.HasPrefix(alerts2[index2].Message, alerts1[index1].Message)))
		}) {
		return aoserrors.New("device allocate alerts mismatch")
	}

	return nil
}
