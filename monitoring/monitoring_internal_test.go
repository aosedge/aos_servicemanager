// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
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

package monitoring

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strconv"
	"testing"
	"time"

	"github.com/docker/docker/pkg/reexec"
	"github.com/docker/docker/pkg/stringid"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/networkmanager"
)

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

/*******************************************************************************
 * Types
 ******************************************************************************/

type testAlerts struct {
	callback func(serviceID, resource string, time time.Time, value uint64)
}

type chainData struct {
	timestamp time.Time
	value     uint64
}

type testTrafficStorage struct {
	chainData map[string]chainData
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var trafficStorage = testTrafficStorage{
	chainData: make(map[string]chainData)}

var networkManager *networkmanager.NetworkManager
var tmpDir string

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	if reexec.Init() {
		return
	}

	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	ret := m.Run()

	if err := cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestAlertProcessor(t *testing.T) {
	var sourceValue uint64
	destination := make([]uint64, 0, 2)

	alert := createAlertProcessor(
		"Test",
		&sourceValue,
		func(time time.Time, value uint64) {
			log.Debugf("T: %s, %d", time, value)
			destination = append(destination, value)
		},
		config.AlertRule{
			MinTimeout:   config.Duration{Duration: 3 * time.Second},
			MinThreshold: 80,
			MaxThreshold: 90})

	values := []uint64{50, 91, 79, 92, 93, 94, 95, 94, 79, 91, 92, 93, 94, 32, 91, 92, 93, 94, 95, 96}
	alertsCount := []int{0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 3, 3, 3}

	currentTime := time.Now()

	for i, value := range values {
		sourceValue = value
		alert.checkAlertDetection(currentTime)
		if alertsCount[i] != len(destination) {
			t.Errorf("Wrong alert count %d at %d", len(destination), i)
		}
		currentTime = currentTime.Add(time.Second)
	}
}

func TestPeriodicReport(t *testing.T) {
	sendDuration := 1 * time.Second

	monitor, err := New(&config.Config{
		WorkingDir: ".",
		Monitoring: config.Monitoring{
			MaxOfflineMessages: 10,
			SendPeriod:         config.Duration{Duration: sendDuration},
			PollPeriod:         config.Duration{Duration: 1 * time.Second}}},
		&trafficStorage, nil)
	if err != nil {
		t.Fatalf("Can't create monitoring instance: %s", err)
	}
	defer monitor.Close()

	timer := time.NewTimer(sendDuration * 2)
	numSends := 3
	sendTime := time.Now()

	for {
		select {
		case <-monitor.DataChannel:
			currentTime := time.Now()

			period := currentTime.Sub(sendTime)
			// check is period in +-10% range
			if period > sendDuration*110/100 || period < sendDuration*90/100 {
				t.Errorf("Period mismatch: %s", period)
			}

			sendTime = currentTime
			timer.Reset(sendDuration * 2)
			numSends--
			if numSends == 0 {
				return
			}

		case <-timer.C:
			t.Fatal("Monitoring data timeout")
		}
	}
}

func TestSystemAlerts(t *testing.T) {
	sendDuration := 1 * time.Second

	alertMap := make(map[string]int)

	monitor, err := New(
		&config.Config{
			WorkingDir: ".",
			Monitoring: config.Monitoring{
				MaxOfflineMessages: 10,
				SendPeriod:         config.Duration{Duration: sendDuration},
				PollPeriod:         config.Duration{Duration: 1 * time.Second},
				CPU: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				RAM: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				UsedDisk: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				InTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				OutTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0}}},
		&trafficStorage, &testAlerts{callback: func(serviceID, resource string, time time.Time, value uint64) {
			alertMap[resource] = alertMap[resource] + 1
		}})
	if err != nil {
		t.Fatalf("Can't create monitoring instance: %s", err)
	}
	defer monitor.Close()

	for {
		select {
		case <-monitor.DataChannel:
			for resource, numAlerts := range alertMap {
				if numAlerts != 1 {
					t.Errorf("Wrong number of %s alerts: %d", resource, numAlerts)
				}
			}

			if len(alertMap) != 5 {
				t.Error("Not enough alerts")
			}

			return

		case <-time.After(sendDuration * 2):
			t.Fatal("Monitoring data timeout")
		}
	}
}

func TestServices(t *testing.T) {
	sendDuration := 2 * time.Second

	alertMap := make(map[string]int)

	monitor, err := New(
		&config.Config{
			WorkingDir: ".",
			Monitoring: config.Monitoring{
				MaxOfflineMessages: 10,
				SendPeriod:         config.Duration{Duration: sendDuration},
				PollPeriod:         config.Duration{Duration: 1 * time.Second}}},
		&trafficStorage, &testAlerts{callback: func(serviceID, resource string, time time.Time, value uint64) {
			alertMap[resource] = alertMap[resource] + 1
		}})
	if err != nil {
		t.Fatalf("Can't create monitoring instance: %s", err)
	}
	defer monitor.Close()

	cmd1, err := runContainerCmd(path.Join(tmpDir, "service1"), "service1")
	if err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	cmd2, err := runContainerCmd(path.Join(tmpDir, "service2"), "service2")
	if err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	// Wait while .ip amd .pid files are created
	time.Sleep(1 * time.Second)

	err = monitor.StartMonitorService("Service1",
		ServiceMonitoringConfig{
			ServiceDir: "tmp/service1",
			UID:        5000,
			GID:        5000,
			ServiceRules: &amqp.ServiceAlertRules{
				CPU: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				RAM: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				UsedDisk: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				InTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				OutTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0}}})
	if err != nil {
		t.Fatalf("Can't start monitoring service: %s", err)
	}

	monitor.StartMonitorService("Service2",
		ServiceMonitoringConfig{
			ServiceDir: "tmp/service2",
			UID:        5002,
			GID:        5002,
			ServiceRules: &amqp.ServiceAlertRules{
				CPU: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				RAM: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				UsedDisk: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				InTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				OutTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0}}})
	if err != nil {
		t.Fatalf("Can't start monitoring service: %s", err)
	}

	terminate := false

	for terminate != true {
		select {
		case data := <-monitor.DataChannel:
			if len(data.ServicesData) != 2 {
				t.Errorf("Wrong number of services: %d", len(data.ServicesData))
			}

			for resource, numAlerts := range alertMap {
				if numAlerts != 2 {
					t.Errorf("Wrong number of %s alerts: %d", resource, numAlerts)
				}
			}

			if len(alertMap) != 5 {
				t.Error("Not enough alerts")
			}

			terminate = true

		case <-time.After(sendDuration * 2):
			t.Fatal("Monitoring data timeout")
		}
	}

	alertMap = make(map[string]int)

	err = monitor.StopMonitorService("Service1")
	if err != nil {
		t.Fatalf("Can't stop monitoring service: %s", err)
	}

	terminate = false

	for terminate != true {
		select {
		case data := <-monitor.DataChannel:
			if len(data.ServicesData) != 1 {
				t.Errorf("Wrong number of services: %d", len(data.ServicesData))
			}

			if len(alertMap) != 0 {
				t.Error("Not enough alerts")
			}

			terminate = true

		case <-time.After(sendDuration * 2):
			t.Fatal("Monitoring data timeout")
		}
	}

	err = monitor.StopMonitorService("Service2")
	if err != nil {
		t.Fatalf("Can't stop monitoring service: %s", err)
	}

	runContainerWait("service1", cmd1)
	runContainerWait("service2", cmd2)
}

func TestTrafficLimit(t *testing.T) {
	sendDuration := 2 * time.Second

	monitor, err := New(
		&config.Config{
			WorkingDir: ".",
			Monitoring: config.Monitoring{
				MaxOfflineMessages: 256,
				SendPeriod:         config.Duration{Duration: sendDuration},
				PollPeriod:         config.Duration{Duration: 1 * time.Second}}},
		&trafficStorage, nil)
	if err != nil {
		t.Fatalf("Can't create monitoring instance: %s", err)
	}
	defer monitor.Close()

	monitor.trafficPeriod = MinutePeriod

	// wait for beginning of next minute
	time.Sleep(time.Duration((60 - time.Now().Second())) * time.Second)

	cmd1, err := runContainerCmd(path.Join(tmpDir, "service1"), "service1")
	if err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	// Wait while .ip amd .pid files are created
	time.Sleep(1 * time.Second)

	ipAddress, err := networkManager.GetServiceIP("service1", "default")
	if err != nil {
		t.Fatalf("Can't get service IP: %s", err)
	}

	err = monitor.StartMonitorService("Service1",
		ServiceMonitoringConfig{
			ServiceDir:    "tmp/service1",
			IPAddress:     ipAddress,
			UID:           5001,
			GID:           5001,
			UploadLimit:   300,
			DownloadLimit: 300})
	if err != nil {
		t.Fatalf("Can't start monitoring service: %s", err)
	}

	if err := runContainerWait("service1", cmd1); err == nil {
		t.Error("Ping should fail")
	}

	err = monitor.StopMonitorService("Service1")
	if err != nil {
		t.Fatalf("Can't stop monitoring service: %s", err)
	}

	// wait for beginning of next minute
	time.Sleep(time.Duration((60 - time.Now().Second())) * time.Second)

	// Start again

	if cmd1, err = runContainerCmd(path.Join(tmpDir, "service1"), "service1"); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	if ipAddress, err = networkManager.GetServiceIP("service1", "default"); err != nil {
		t.Fatalf("Can't get service IP: %s", err)
	}

	// Wait while .ip amd .pid files are created
	time.Sleep(1 * time.Second)

	err = monitor.StartMonitorService("Service1",
		ServiceMonitoringConfig{
			ServiceDir:    "tmp/service1",
			IPAddress:     ipAddress,
			UID:           5001,
			GID:           5001,
			UploadLimit:   2000,
			DownloadLimit: 2000})
	if err != nil {
		t.Fatalf("Can't start monitoring service: %s", err)
	}

	if err = runContainerWait("service1", cmd1); err != nil {
		t.Errorf("Wait for service error: %s", err)
	}

	err = monitor.StopMonitorService("Service1")
	if err != nil {
		t.Fatalf("Can't stop monitoring service: %s", err)
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (instance *testAlerts) SendResourceAlert(source, resource string, time time.Time, value uint64) {
	instance.callback(source, resource, time, value)
}

func (storage *testTrafficStorage) SetTrafficMonitorData(chain string, timestamp time.Time, value uint64) (err error) {
	storage.chainData[chain] = chainData{timestamp, value}

	return nil
}

func (storage *testTrafficStorage) GetTrafficMonitorData(chain string) (timestamp time.Time, value uint64, err error) {
	data, ok := storage.chainData[chain]
	if !ok {
		return timestamp, value, errors.New("chain does not exist")
	}

	return data.timestamp, data.value, nil
}

func (storage *testTrafficStorage) RemoveTrafficMonitorData(chain string) (err error) {
	if _, ok := storage.chainData[chain]; !ok {
		return errors.New("chain does not exist")
	}

	delete(storage.chainData, chain)

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return err
	}

	if err = os.MkdirAll(tmpDir, 0755); err != nil {
		return err
	}

	if networkManager, err = networkmanager.New(&config.Config{WorkingDir: tmpDir}); err != nil {
		return err
	}

	if err = networkManager.CreateNetwork("default"); err != nil {
		return err
	}

	if err = createOCIContainer(path.Join(tmpDir, "service1"), "service1", []string{"ping", "8.8.8.8", "-c10", "-w10"}); err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	if err := networkManager.DeleteAllNetworks(); err != nil {
		log.Errorf("Can't remove networks: %s", err)
	}

	networkManager.Close()

	if err := os.RemoveAll(tmpDir); err != nil {
		log.Errorf("Can't remove tmp dir: %s", err)
	}

	return nil
}

func createOCIContainer(imagePath string, containerID string, args []string) (err error) {
	if err = os.RemoveAll(imagePath); err != nil {
		return err
	}

	if err = os.MkdirAll(path.Join(imagePath, "rootfs"), 0755); err != nil {
		return err
	}

	out, err := exec.Command("runc", "spec", "-b", imagePath).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	specJSON, err := ioutil.ReadFile(path.Join(imagePath, "config.json"))
	if err != nil {
		return err
	}

	var spec runtimespec.Spec

	if err = json.Unmarshal(specJSON, &spec); err != nil {
		return err
	}

	spec.Process.Terminal = false

	spec.Process.Args = args

	for _, mount := range []string{"/bin", "/sbin", "/lib", "/lib64", "/usr"} {
		spec.Mounts = append(spec.Mounts, runtimespec.Mount{Destination: mount,
			Type: "bind", Source: mount, Options: []string{"bind", "ro"}})
	}

	for _, mount := range []string{"hosts", "resolv.conf"} {
		spec.Mounts = append(spec.Mounts, runtimespec.Mount{Destination: path.Join("/etc", mount),
			Type: "bind", Source: path.Join(imagePath, "etc", mount), Options: []string{"bind", "ro"}})
	}

	spec.Hooks = new(runtimespec.Hooks)

	spec.Hooks.Prestart = append(spec.Hooks.Poststart, runtimespec.Hook{
		Path: path.Join("/proc", strconv.Itoa(os.Getpid()), "exe"),
		Args: []string{
			"libnetwork-setkey",
			"-exec-root=/run/aos",
			containerID,
			stringid.TruncateID(networkManager.GetID()),
		}})

	spec.Process.Capabilities.Bounding = append(spec.Process.Capabilities.Bounding, "CAP_NET_RAW")
	spec.Process.Capabilities.Effective = append(spec.Process.Capabilities.Effective, "CAP_NET_RAW")
	spec.Process.Capabilities.Inheritable = append(spec.Process.Capabilities.Inheritable, "CAP_NET_RAW")
	spec.Process.Capabilities.Permitted = append(spec.Process.Capabilities.Permitted, "CAP_NET_RAW")
	spec.Process.Capabilities.Ambient = append(spec.Process.Capabilities.Ambient, "CAP_NET_RAW")

	if specJSON, err = json.Marshal(&spec); err != nil {
		return err
	}

	if err = ioutil.WriteFile(path.Join(imagePath, "config.json"), specJSON, 0644); err != nil {
		return err
	}

	return nil
}

func runContainerCmd(imagePath string, containerID string) (cmd *exec.Cmd, err error) {
	if err = networkManager.AddServiceToNetwork(containerID, "default", imagePath,
		networkmanager.NetworkParams{}); err != nil {
		return nil, err
	}

	cmd = exec.Command("runc", "run", "--pid-file", path.Join(imagePath, ".pid"), "-b", imagePath, containerID)

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return cmd, nil
}

func runContainerWait(containerID string, cmd *exec.Cmd) (err error) {
	err = cmd.Wait()

	networkManager.RemoveServiceFromNetwork(containerID, "default")

	return err
}
