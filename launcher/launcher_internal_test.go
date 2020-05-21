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

package launcher

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jlaffaye/ftp"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/sha3"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/monitoring"
	"aos_servicemanager/platform"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Generates test image with python script
type pythonImage struct {
}

// Generates test image with iperf server
type iperfImage struct {
}

// Generates test image with ftp server
type ftpImage struct {
	storageLimit uint64
	stateLimit   uint64
}

// Test monitor info
type testMonitorInfo struct {
	serviceID string
	config    monitoring.ServiceMonitoringConfig
}

// Test monitor
type testMonitor struct {
	startChannel chan *testMonitorInfo
	stopChannel  chan string
}

type stateRequest struct {
	serviceID    string
	defaultState bool
}

// Test sender
type testSender struct {
	statusChannel       chan amqp.ServiceInfo
	stateRequestChannel chan stateRequest
}

type testServiceProvider struct {
	sync.Mutex
	services      map[string]*Service
	usersServices []*UsersService
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var serviceProvider = testServiceProvider{services: make(map[string]*Service)}

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
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error creating service images: %s", err)
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

func TestInstallRemove(t *testing.T) {
	sender := newTestSender()

	launcher, err := newTestLauncher(new(pythonImage), sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	numInstallServices := 10
	numUninstallServices := 5

	// install services
	for i := 0; i < numInstallServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}
	// remove services
	for i := 0; i < numUninstallServices; i++ {
		launcher.UninstallService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numInstallServices+numUninstallServices; i++ {
		if status := <-sender.statusChannel; status.Error != "" {
			t.Errorf("%s, service ID %s", status.Error, status.ID)
		}
	}

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != numInstallServices-numUninstallServices {
		t.Errorf("Wrong service quantity")
	}
	for _, service := range services {
		if service.Status != "OK" {
			t.Errorf("Service %s error status: %s", service.ID, service.Status)
		}
	}

	time.Sleep(time.Second * 2)

	// remove remaining services
	for i := numUninstallServices; i < numInstallServices; i++ {
		launcher.UninstallService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numInstallServices-numUninstallServices; i++ {
		if status := <-sender.statusChannel; status.Error != "" {
			t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
		}
	}

	services, err = launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service quantity")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestAutoStart(t *testing.T) {
	sender := newTestSender()

	launcher, err := newTestLauncher(new(pythonImage), sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	numServices := 5

	// install services
	for i := 0; i < numServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}

	for i := 0; i < numServices; i++ {
		if status := <-sender.statusChannel; status.Error != "" {
			t.Errorf("%s, service ID %s", status.Error, status.ID)
		}
	}

	launcher.Close()

	time.Sleep(time.Second * 2)

	launcher, err = newTestLauncher(new(pythonImage), sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	time.Sleep(time.Second * 2)

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != numServices {
		t.Errorf("Wrong service quantity")
	}
	for _, service := range services {
		if service.Status != "OK" {
			t.Errorf("Service %s error status: %s", service.ID, service.Status)
		}
	}

	// remove services
	for i := 0; i < numServices; i++ {
		launcher.UninstallService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numServices; i++ {
		if status := <-sender.statusChannel; status.Error != "" {
			t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
		}
	}

	services, err = launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service quantity")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestErrors(t *testing.T) {
	sender := newTestSender()

	launcher, err := newTestLauncher(new(pythonImage), sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	// test version mistmatch

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 5})
	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 4})
	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 6})

	for i := 0; i < 3; i++ {
		status := <-sender.statusChannel
		switch {
		case status.Version == 5 && status.Error != "":
			t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
		case status.Version == 4 && status.Error == "":
			t.Errorf("Service %s version %d should not be installed", status.ID, status.Version)
		case status.Version == 6 && status.Error != "":
			t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
		}
	}

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != 1 {
		t.Errorf("Wrong service quantity: %d", len(services))
	} else if services[0].Version != 6 {
		t.Errorf("Wrong service version")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestUpdate(t *testing.T) {
	sender := newTestSender()

	launcher, err := newTestLauncher(new(pythonImage), sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	serverAddr, err := net.ResolveUDPAddr("udp", ":10001")
	if err != nil {
		t.Fatalf("Can't create resolve UDP address: %s", err)
	}

	serverConn, err := net.ListenUDP("udp", serverAddr)
	if err != nil {
		t.Fatalf("Can't listen UDP: %s", err)
	}
	defer serverConn.Close()

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 0})

	if status := <-sender.statusChannel; status.Error != "" {
		t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
	}

	if err := serverConn.SetReadDeadline(time.Now().Add(time.Second * 30)); err != nil {
		t.Fatalf("Can't set read deadline: %s", err)
	}

	buf := make([]byte, 1024)

	n, _, err := serverConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("Can't read from UDP: %s", err)
	} else {
		message := string(buf[:n])

		if message != "service0, version: 0" {
			t.Fatalf("Wrong service content: %s", message)
		}
	}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 1})

	if status := <-sender.statusChannel; status.Error != "" {
		t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
	}

	n, _, err = serverConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("Can't read from UDP: %s", err)
	} else {
		message := string(buf[:n])

		if message != "service0, version: 1" {
			t.Fatalf("Wrong service content: %s", message)
		}
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestNetworkSpeed(t *testing.T) {
	sender := newTestSender()

	launcher, err := newTestLauncher(new(iperfImage), sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	numServices := 2

	for i := 0; i < numServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}

	for i := 0; i < numServices; i++ {
		if status := <-sender.statusChannel; status.Error != "" {
			t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
		}
	}

	for i := 0; i < numServices; i++ {
		serviceID := fmt.Sprintf("service%d", i)

		service, err := launcher.serviceProvider.GetService(serviceID)
		if err != nil {
			t.Errorf("Can't get service: %s", err)
			continue
		}

		addr, err := monitoring.GetServiceIPAddress(service.Path)
		if err != nil {
			t.Errorf("Can't get ip address: %s", err)
			continue
		}
		output, err := exec.Command("iperf", "-c"+addr, "-d", "-r", "-t2", "-yc").Output()
		if err != nil {
			t.Errorf("Iperf failed: %s", err)
			continue
		}

		ulSpeed := -1
		dlSpeed := -1

		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			result := strings.Split(line, ",")
			if len(result) >= 9 {
				if result[4] == "5001" {
					value, err := strconv.ParseInt(result[8], 10, 64)
					if err != nil {
						t.Errorf("Can't parse ul speed: %s", err)
						continue
					}
					ulSpeed = int(value) / 1000
				} else {
					value, err := strconv.ParseUint(result[8], 10, 64)
					if err != nil {
						t.Errorf("Can't parse ul speed: %s", err)
						continue
					}
					dlSpeed = int(value) / 1000
				}
			}
		}

		if ulSpeed == -1 || dlSpeed == -1 {
			t.Error("Can't determine ul/dl speed")
		}

		if ulSpeed > 4096*1.5 || dlSpeed > 8192*1.5 {
			t.Errorf("Speed limit exceeds: dl %d, ul %d", dlSpeed, ulSpeed)
		}
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestVisPermissions(t *testing.T) {
	sender := newTestSender()

	launcher, err := newTestLauncher(new(pythonImage), sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 0})

	if status := <-sender.statusChannel; status.Error != "" {
		t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
	}

	service, ok := serviceProvider.services["service0"]
	if !ok {
		t.Fatalf("Service not found")
	}

	if service.Permissions != `{"*": "rw", "123": "rw"}` {
		t.Fatalf("Permissions mismatch")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestUsersServices(t *testing.T) {
	sender := newTestSender()

	launcher, err := newTestLauncher(new(pythonImage), sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	numUsers := 3
	numServices := 3

	for i := 0; i < numUsers; i++ {
		users := []string{fmt.Sprintf("user%d", i)}

		if err = launcher.SetUsers(users); err != nil {
			t.Fatalf("Can't set users: %s", err)
		}

		services, err := launcher.serviceProvider.GetUsersServices(users)
		if err != nil {
			t.Fatalf("Can't get users services: %s", err)
		}
		if len(services) != 0 {
			t.Fatalf("Wrong service quantity")
		}

		// install services
		for j := 0; j < numServices; j++ {
			launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("user%d_service%d", i, j)})
		}
		for i := 0; i < numServices; i++ {
			if status := <-sender.statusChannel; status.Error != "" {
				t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
			}
		}

		time.Sleep(time.Second * 2)

		services, err = launcher.serviceProvider.GetServices()
		if err != nil {
			t.Fatalf("Can't get services: %s", err)
		}

		count := 0
		for _, service := range services {
			if service.State == stateRunning {
				_, err = launcher.serviceProvider.GetUsersService(users, service.ID)
				if err != nil && !strings.Contains(err.Error(), "not exist") {
					t.Errorf("Can't check users service: %s", err)
				}

				if err != nil {
					t.Errorf("Service doesn't belong to users: %s", service.ID)
				}

				count++
			}
		}

		if count != numServices {
			t.Fatalf("Wrong running services count")
		}
	}

	for i := 0; i < numUsers; i++ {
		users := []string{fmt.Sprintf("user%d", i)}

		if err = launcher.SetUsers(users); err != nil {
			t.Fatalf("Can't set users: %s", err)
		}

		time.Sleep(time.Second * 2)

		services, err := launcher.serviceProvider.GetServices()
		if err != nil {
			t.Fatalf("Can't get services: %s", err)
		}

		count := 0
		for _, service := range services {
			if service.State == stateRunning {
				_, err = launcher.serviceProvider.GetUsersService(users, service.ID)
				if err != nil && !strings.Contains(err.Error(), "not exist") {
					t.Errorf("Can't check users service: %s", err)
				}

				if err != nil {
					t.Errorf("Service doesn't belong to users: %s", service.ID)
				}

				count++
			}
		}

		if count != numServices {
			t.Fatalf("Wrong running services count")
		}
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestServiceTTL(t *testing.T) {
	sender := newTestSender()

	launcher, err := newTestLauncher(new(pythonImage), sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	numServices := 3

	if err = launcher.SetUsers([]string{"user0"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	// install services
	for i := 0; i < numServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}
	for i := 0; i < numServices; i++ {
		if status := <-sender.statusChannel; status.Error != "" {
			t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
		}
	}

	services, err := launcher.serviceProvider.GetServices()
	if err != nil {
		t.Fatalf("Can't get services: %s", err)
	}

	for _, service := range services {
		if err = launcher.serviceProvider.SetServiceStartTime(service.ID, service.StartAt.Add(-time.Hour*24*30)); err != nil {
			t.Errorf("Can't set service start time: %s", err)
		}
	}

	if err = launcher.SetUsers([]string{"user1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	services, err = launcher.serviceProvider.GetServices()
	if err != nil {
		t.Fatalf("Can't get services: %s", err)
	}

	if len(services) != 0 {
		t.Fatal("Wrong service quantity")
	}

	if len(serviceProvider.usersServices) != 0 {
		t.Fatalf("Wrong users quantity: %d", len(serviceProvider.usersServices))
	}
}

func TestServiceMonitoring(t *testing.T) {
	sender := newTestSender()

	monitor, err := newTestMonitor()
	if err != nil {
		t.Fatalf("Can't create monitor: %s", err)
	}

	launcher, err := newTestLauncher(new(pythonImage), sender, monitor)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"user0"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	serviceAlerts := amqp.ServiceAlertRules{
		RAM: &config.AlertRule{
			MinTimeout:   config.Duration{Duration: 30 * time.Second},
			MinThreshold: 0,
			MaxThreshold: 80},
		CPU: &config.AlertRule{
			MinTimeout:   config.Duration{Duration: 1 * time.Minute},
			MinThreshold: 0,
			MaxThreshold: 20},
		UsedDisk: &config.AlertRule{
			MinTimeout:   config.Duration{Duration: 5 * time.Minute},
			MinThreshold: 0,
			MaxThreshold: 20}}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "Service1", AlertRules: &serviceAlerts})
	if status := <-sender.statusChannel; status.Error != "" {
		t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
	}

	select {
	case info := <-monitor.startChannel:
		if info.serviceID != "Service1" {
			t.Fatalf("Wrong service ID: %s", info.serviceID)
		}

		if !reflect.DeepEqual(info.config.ServiceRules, &serviceAlerts) {
			t.Fatalf("Wrong service alert rules")
		}

	case <-time.After(1000 * time.Millisecond):
		t.Errorf("Waiting for service monitor timeout")
	}

	launcher.UninstallService("Service1")
	if status := <-sender.statusChannel; status.Error != "" {
		t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
	}

	select {
	case serviceID := <-monitor.stopChannel:
		if serviceID != "Service1" {
			t.Fatalf("Wrong service ID: %s", serviceID)
		}

	case <-time.After(2000 * time.Millisecond):
		t.Errorf("Waiting for service monitor timeout")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestServiceStorage(t *testing.T) {
	if os.Getenv("CI") != "" {
		log.Debug("Skip TestServiceStorage")
		return
	}

	sender := newTestSender()

	// Set limit for 2 files 8192 bytes length + 1 folder 4k
	launcher, err := newTestLauncher(&ftpImage{8192*2 + 4096, 0}, sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 0})
	if status := <-sender.statusChannel; status.Error != "" {
		t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
	}

	// Wait ftp server ready
	time.Sleep(2 * time.Second)

	ftp, err := launcher.connectToFtp("service0")
	if err != nil {
		t.Fatalf("Can't connect to ftp: %s", err)
	}
	defer ftp.Quit()

	service, err := launcher.serviceProvider.GetService("service0")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}

	testData := make([]byte, 8192)

	if err := ftp.Stor("test1.dat", bytes.NewReader(testData)); err != nil {
		t.Errorf("Can't write file: %s", err)
	}

	diskUsage, err := platform.GetUserFSQuotaUsage(launcher.config.WorkingDir, service.UserName)
	if err != nil {
		t.Errorf("Can't get disk usage: %s", err)
	}

	// file size + 1 block 4k for dir
	if diskUsage != (8192 + 4096) {
		t.Errorf("Wrong disk usage value: %d", diskUsage)
	}

	if err := ftp.Stor("test2.dat", bytes.NewReader(testData)); err != nil {
		t.Errorf("Can't write file: %s", err)
	}

	diskUsage, err = platform.GetUserFSQuotaUsage(launcher.config.WorkingDir, service.UserName)
	if err != nil {
		t.Errorf("Can't get disk usage: %s", err)
	}

	// 2 files size + 1 block 4k for dir
	if diskUsage != (8192*2 + 4096) {
		t.Errorf("Wrong disk usage value: %d", diskUsage)
	}

	if err := ftp.Stor("test3.dat", bytes.NewReader(testData)); err == nil {
		t.Errorf("Unexpected nil error")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestServiceState(t *testing.T) {
	if os.Getenv("CI") != "" {
		log.Debug("Skip TestServiceState")
		return
	}

	sender := newTestSender()

	launcher, err := newTestLauncher(&ftpImage{1024 * 12, 256}, sender, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 0})
	if status := <-sender.statusChannel; status.Error != "" {
		t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
	}

	ftp, err := launcher.connectToFtp("service0")
	if err != nil {
		t.Fatalf("Can't connect to ftp: %s", err)
	}
	defer ftp.Quit()

	// Check new state accept

	stateData := ""

	if err := ftp.Stor("state.dat", bytes.NewReader([]byte(stateData))); err != nil {
		t.Errorf("Can't write file: %s", err)
	}

	time.Sleep(500 * time.Millisecond)

	stateData = "Hello"

	if err := ftp.Stor("state.dat", bytes.NewReader([]byte(stateData))); err != nil {
		t.Errorf("Can't write file: %s", err)
	}

	select {
	case newState := <-launcher.NewStateChannel:
		if newState.State != stateData {
			t.Errorf("Wrong state: %s", newState.State)
		}

		launcher.StateAcceptance(amqp.StateAcceptance{Result: "accepted"}, newState.CorrelationID)

	case <-time.After(2 * time.Second):
		t.Error("No new state event")
	}

	// Check new state reject

	stateData = "Hello again"

	if err := ftp.Stor("state.dat", bytes.NewReader([]byte(stateData))); err != nil {
		t.Errorf("Can't write file: %s", err)
	}

	select {
	case newState := <-launcher.NewStateChannel:
		if newState.State != stateData {
			t.Errorf("Wrong state: %s", newState.State)
		}

		launcher.StateAcceptance(amqp.StateAcceptance{Result: "rejected", Reason: "just because"}, newState.CorrelationID)

	case <-time.After(2 * time.Second):
		t.Error("No new state event")
	}

	select {
	case <-sender.stateRequestChannel:
		stateData = "Hello"
		calcSum := sha3.Sum224([]byte(stateData))

		launcher.UpdateState(amqp.UpdateState{
			ServiceID: "service0",
			State:     stateData,
			Checksum:  hex.EncodeToString(calcSum[:])})

		time.Sleep(1 * time.Second)

	case <-time.After(2 * time.Second):
		t.Error("No state request event")
	}

	if ftp, err = launcher.connectToFtp("service0"); err != nil {
		t.Fatalf("Can't connect to ftp: %s", err)
	}
	defer ftp.Quit()

	response, err := ftp.Retr("state.dat")
	if err != nil {
		t.Errorf("Can't retrieve state file: %s", err)
	} else {
		serviceState, err := ioutil.ReadAll(response)
		if err != nil {
			t.Errorf("Can't retrieve state file: %s", err)
		}

		if string(serviceState) != stateData {
			t.Errorf("Wrong state: %s", serviceState)
		}

		response.Close()
	}

	// Check state after update

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 1})
	if status := <-sender.statusChannel; status.Error != "" {
		t.Errorf("%s, service ID %s, version: %d", status.Error, status.ID, status.Version)
	}

	if ftp, err = launcher.connectToFtp("service0"); err != nil {
		t.Fatalf("Can't connect to ftp: %s", err)
	}
	defer ftp.Quit()

	if response, err = ftp.Retr("state.dat"); err != nil {
		t.Errorf("Can't retrieve state file: %s", err)
	} else {
		serviceState, err := ioutil.ReadAll(response)
		if err != nil {
			t.Errorf("Can't retrieve state file: %s", err)
		}

		if string(serviceState) != stateData {
			t.Errorf("Wrong state: %s", serviceState)
		}

		response.Close()
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestSpec(t *testing.T) {
	if err := generateConfig("tmp"); err != nil {
		t.Fatalf("Can't generate service spec: %s", err)
	}

	spec, err := loadServiceSpec(path.Join("tmp", ocConfigFile))
	if err != nil {
		t.Fatalf("Can't load service spec: %s", err)
	}
	defer func() {
		if err := spec.save(); err != nil {
			t.Fatalf("Can't save service spec: %s", err)
		}
	}()

	// add device

	deviceName := "/dev/random"

	if err = spec.addHostDevice(deviceName); err != nil {
		t.Fatalf("Can't add host device: %s", err)
	}

	found := false

	var device runtimespec.LinuxDevice

	for _, device = range spec.ocSpec.Linux.Devices {
		if device.Path == deviceName {
			found = true

			break
		}
	}

	if !found {
		t.Fatal("Device not found")
	}

	found = false

	for _, resource := range spec.ocSpec.Linux.Resources.Devices {
		if resource.Major == nil || resource.Minor == nil {
			continue
		}

		if *resource.Major == device.Major && *resource.Minor == device.Minor {
			found = true

			if !resource.Allow {
				t.Error("Resource is not allowed")
			}
		}
	}

	if !found {
		t.Fatal("Resource not found")
	}

	// add group

	groupName := "audio"

	group, err := user.LookupGroup(groupName)
	if err != nil {
		t.Fatalf("Can't lookup group: %s", err)
	}

	gid, err := strconv.ParseUint(group.Gid, 10, 32)
	if err != nil {
		t.Fatalf("Can't parse GID: %s", err)
	}

	if err = spec.addGroup(groupName); err != nil {
		t.Fatalf("Can't add group: %s", err)
	}

	found = false

	for _, serviceGID := range spec.ocSpec.Process.User.AdditionalGids {
		if uint32(gid) == serviceGID {
			found = true

			break
		}
	}

	if !found {
		t.Error("Group not found")
	}
}

func TestSpecFromImageConfig(t *testing.T) {
	_, err := generateSpecFromImageConfig("no_file", "tmp/config.json")
	if err == nil {
		t.Errorf("Should be error no such file or director")
	}

	imgConfig, err := generateImageConfig()
	if err != nil {
		log.Fatalf("Error creating OCI Image config %s", err)
	}

	imgConfig.OS = "Windows"
	configFile, err := saveImageConfig("tmp", imgConfig)
	if err != nil {
		log.Fatalf("Error save OCI Image config %s", err)
	}

	_, err = generateSpecFromImageConfig(configFile, "tmp/config.json")
	if err == nil {
		t.Errorf("Should be error unsupported OS in image config")
	}

	imgConfig.OS = "linux"
	configFile, err = saveImageConfig("tmp", imgConfig)
	if err != nil {
		log.Fatalf("Error save OCI Image config %s", err)
	}

	runtimeSpec, err := generateSpecFromImageConfig(configFile, "tmp/config.json")
	if err != nil {
		t.Errorf("Error generating OCI runtime spec %s", err)
	}

	originalCmd := []string{"/bin/my-app-binary", "--foreground", "--config", "/etc/my-app.d/default.cfg"}
	if false == reflect.DeepEqual(runtimeSpec.ocSpec.Process.Args, originalCmd) {
		t.Errorf("Error crating args from config")
	}

	origEnv := []string{"PATH=/usr/local/sbin", "TERM", "FOO=oci_is_a", "BAR=well_written_spec", "MY_VAR"}
	if false == reflect.DeepEqual(runtimeSpec.ocSpec.Process.Env, origEnv) {
		t.Errorf("Error crating env from config")
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func newTestLauncher(downloader downloader, sender Sender, monitor ServiceMonitor) (launcher *Launcher, err error) {
	launcher, err = New(&config.Config{WorkingDir: "tmp", StorageDir: "tmp/storage", DefaultServiceTTL: 30}, sender, &serviceProvider, monitor)
	if err != nil {
		return launcher, err
	}

	launcher.downloader = downloader

	return launcher, err
}

func (downloader pythonImage) downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		log.Error("Can't create image dir : ", err)
		return outputFile, err
	}
	defer os.RemoveAll(imageDir)

	// create dir
	if err := os.MkdirAll(path.Join(imageDir, "rootfs", "home"), 0755); err != nil {
		return outputFile, err
	}

	if err := generatePythonContent(imageDir); err != nil {
		return outputFile, err
	}

	if err := generateConfig(imageDir); err != nil {
		return outputFile, err
	}

	spec, err := loadServiceSpec(path.Join(imageDir, "config.json"))
	if err != nil {
		return outputFile, err
	}

	spec.ocSpec.Process.Args = []string{"python3", "/home/service.py", serviceInfo.ID, fmt.Sprintf("%d", serviceInfo.Version)}

	if spec.ocSpec.Annotations == nil {
		spec.ocSpec.Annotations = make(map[string]string)
	}
	spec.ocSpec.Annotations[aosProductPrefix+"vis.permissions"] = `{"*": "rw", "123": "rw"}`

	if err = spec.save(); err != nil {
		return outputFile, err
	}

	imageFile, err := ioutil.TempFile("", "aos_")
	if err != nil {
		return outputFile, err
	}
	outputFile = imageFile.Name()
	imageFile.Close()

	if err = packImage(imageDir, outputFile); err != nil {
		return outputFile, err
	}

	return outputFile, nil
}

func (downloader iperfImage) downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		log.Error("Can't create image dir : ", err)
		return outputFile, err
	}
	defer os.RemoveAll(imageDir)

	// create dir
	if err := os.MkdirAll(path.Join(imageDir, "rootfs", "home"), 0755); err != nil {
		return outputFile, err
	}

	if err := generateConfig(imageDir); err != nil {
		return outputFile, err
	}

	spec, err := loadServiceSpec(path.Join(imageDir, "config.json"))
	if err != nil {
		return outputFile, err
	}

	spec.ocSpec.Process.Args = []string{"iperf", "-s"}

	if spec.ocSpec.Annotations == nil {
		spec.ocSpec.Annotations = make(map[string]string)
	}
	spec.ocSpec.Annotations[aosProductPrefix+"network.uploadSpeed"] = "4096"
	spec.ocSpec.Annotations[aosProductPrefix+"network.downloadSpeed"] = "8192"

	if err = spec.save(); err != nil {
		return outputFile, err
	}

	imageFile, err := ioutil.TempFile("", "aos_")
	if err != nil {
		return outputFile, err
	}
	outputFile = imageFile.Name()
	imageFile.Close()

	if err = packImage(imageDir, outputFile); err != nil {
		return outputFile, err
	}

	return outputFile, nil
}

func (downloader ftpImage) downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		log.Error("Can't create image dir : ", err)
		return outputFile, err
	}
	defer os.RemoveAll(imageDir)

	// create dir
	if err := os.MkdirAll(path.Join(imageDir, "rootfs", "home"), 0755); err != nil {
		return outputFile, err
	}

	if err := generateFtpContent(imageDir); err != nil {
		return outputFile, err
	}

	if err := generateConfig(imageDir); err != nil {
		return outputFile, err
	}

	spec, err := loadServiceSpec(path.Join(imageDir, ocConfigFile))
	if err != nil {
		return outputFile, err
	}

	spec.ocSpec.Process.Args = []string{"python3", "/home/service.py", serviceInfo.ID, fmt.Sprintf("%d", serviceInfo.Version)}

	if spec.ocSpec.Annotations == nil {
		spec.ocSpec.Annotations = make(map[string]string)
	}
	spec.ocSpec.Annotations[aosProductPrefix+"storage.limit"] = strconv.FormatUint(downloader.storageLimit, 10)
	spec.ocSpec.Annotations[aosProductPrefix+"state.limit"] = strconv.FormatUint(downloader.stateLimit, 10)

	if err = spec.save(); err != nil {
		return outputFile, err
	}

	imageFile, err := ioutil.TempFile("", "aos_")
	if err != nil {
		return outputFile, err
	}
	outputFile = imageFile.Name()
	imageFile.Close()

	if err = packImage(imageDir, outputFile); err != nil {
		return outputFile, err
	}

	return outputFile, nil
}

func newTestMonitor() (monitor *testMonitor, err error) {
	monitor = &testMonitor{}

	monitor.startChannel = make(chan *testMonitorInfo, 100)
	monitor.stopChannel = make(chan string, 100)

	return monitor, nil
}

func (monitor *testMonitor) StartMonitorService(serviceID string, monitorConfig monitoring.ServiceMonitoringConfig) (err error) {

	monitor.startChannel <- &testMonitorInfo{serviceID, monitorConfig}

	return nil
}

func (monitor *testMonitor) StopMonitorService(serviceID string) (err error) {
	monitor.stopChannel <- serviceID

	return nil
}

func newTestSender() (sender *testSender) {
	sender = &testSender{}

	sender.statusChannel = make(chan amqp.ServiceInfo, maxExecutedActions)
	sender.stateRequestChannel = make(chan stateRequest, 32)

	return sender
}

func (sender *testSender) SendServiceStatus(serviceStatus amqp.ServiceInfo) (err error) {
	sender.statusChannel <- serviceStatus

	return nil
}

func (sender *testSender) SendStateRequest(serviceID string, defaultState bool) (err error) {
	sender.stateRequestChannel <- stateRequest{serviceID, defaultState}

	return nil
}

func (launcher *Launcher) removeAllServices() (err error) {
	services, err := launcher.serviceProvider.GetServices()
	if err != nil {
		return err
	}

	statusChannel := make(chan error, len(services))

	for _, service := range services {
		go func(service Service) {
			err := launcher.removeService(service)
			if err != nil {
				log.Errorf("Can't remove service %s: %s", service.ID, err)
			}
			statusChannel <- err
		}(service)
	}

	// Wait all services are deleted
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}

	err = launcher.systemd.Reload()
	if err != nil {
		return err
	}

	services, err = launcher.serviceProvider.GetServices()
	if err != nil {
		return err
	}
	if len(services) != 0 {
		return errors.New("can't remove all services")
	}

	if len(serviceProvider.usersServices) != 0 {
		return errors.New("can't remove all users")
	}

	return err
}

func (serviceProvider *testServiceProvider) AddService(service Service) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	if _, ok := serviceProvider.services[service.ID]; ok {
		return fmt.Errorf("service %s already exists", service.ID)
	}

	serviceProvider.services[service.ID] = &service

	return nil
}

func (serviceProvider *testServiceProvider) UpdateService(service Service) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	if _, ok := serviceProvider.services[service.ID]; !ok {
		return fmt.Errorf("service %s does not exist", service.ID)
	}

	serviceProvider.services[service.ID] = &service

	return nil
}

func (serviceProvider *testServiceProvider) RemoveService(serviceID string) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	if _, ok := serviceProvider.services[serviceID]; !ok {
		return fmt.Errorf("service %s does not exist", serviceID)
	}

	delete(serviceProvider.services, serviceID)

	return nil
}

func (serviceProvider *testServiceProvider) GetService(serviceID string) (service Service, err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	servicePtr, ok := serviceProvider.services[serviceID]
	if !ok {
		return service, fmt.Errorf("service %s does not exist", serviceID)
	}

	return *servicePtr, nil
}

func (serviceProvider *testServiceProvider) GetServices() (services []Service, err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	services = make([]Service, 0, len(serviceProvider.services))

	for _, servicePtr := range serviceProvider.services {
		services = append(services, *servicePtr)
	}

	return services, nil
}

func (serviceProvider *testServiceProvider) GetServiceByUnitName(unitName string) (service Service, err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	for _, servicePtr := range serviceProvider.services {
		if service.UnitName == unitName {
			return *servicePtr, nil
		}
	}

	return service, fmt.Errorf("service with unit %s does not exist", unitName)
}

func (serviceProvider *testServiceProvider) SetServiceStatus(serviceID string, status ServiceStatus) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	if _, ok := serviceProvider.services[serviceID]; !ok {
		return fmt.Errorf("service %s does not exist", serviceID)
	}

	serviceProvider.services[serviceID].Status = status

	return nil
}

func (serviceProvider *testServiceProvider) SetServiceState(serviceID string, state ServiceState) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	if _, ok := serviceProvider.services[serviceID]; !ok {
		return fmt.Errorf("service %s does not exist", serviceID)
	}

	serviceProvider.services[serviceID].State = state

	return nil
}

func (serviceProvider *testServiceProvider) SetServiceStartTime(serviceID string, time time.Time) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	if _, ok := serviceProvider.services[serviceID]; !ok {
		return fmt.Errorf("service %s does not exist", serviceID)
	}

	serviceProvider.services[serviceID].StartAt = time

	return nil
}

func (serviceProvider *testServiceProvider) AddServiceToUsers(users []string, serviceID string) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	for _, usersServicePtr := range serviceProvider.usersServices {
		if reflect.DeepEqual(usersServicePtr.Users, users) && usersServicePtr.ServiceID == serviceID {
			return fmt.Errorf("service %s already in users", serviceID)
		}
	}

	serviceProvider.usersServices = append(serviceProvider.usersServices, &UsersService{Users: users, ServiceID: serviceID})

	return nil
}

func (serviceProvider *testServiceProvider) RemoveServiceFromUsers(users []string, serviceID string) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	i := 0

	for _, usersServicePtr := range serviceProvider.usersServices {
		if !reflect.DeepEqual(usersServicePtr.Users, users) || usersServicePtr.ServiceID != serviceID {
			serviceProvider.usersServices[i] = usersServicePtr
			i++
		}
	}

	serviceProvider.usersServices = serviceProvider.usersServices[:i]

	return nil
}

func (serviceProvider *testServiceProvider) GetUsersServices(users []string) (services []Service, err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	for _, usersService := range serviceProvider.usersServices {
		if reflect.DeepEqual(usersService.Users, users) {
			service, ok := serviceProvider.services[usersService.ServiceID]
			if !ok {
				return nil, fmt.Errorf("service %s does not exist", usersService.ServiceID)
			}

			services = append(services, *service)
		}
	}

	return services, nil
}

func (serviceProvider *testServiceProvider) RemoveServiceFromAllUsers(serviceID string) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	i := 0

	for _, usersService := range serviceProvider.usersServices {
		if usersService.ServiceID != serviceID {
			serviceProvider.usersServices[i] = usersService
			i++
		}
	}

	serviceProvider.usersServices = serviceProvider.usersServices[:i]

	return nil
}

func (serviceProvider *testServiceProvider) GetUsersService(users []string, serviceID string) (userService UsersService, err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	for _, usersServicePtr := range serviceProvider.usersServices {
		if reflect.DeepEqual(usersServicePtr.Users, users) && usersServicePtr.ServiceID == serviceID {
			return *usersServicePtr, nil
		}
	}

	return userService, fmt.Errorf("service %s does not exist in users", serviceID)
}

func (serviceProvider *testServiceProvider) GetUsersServicesByServiceID(serviceID string) (userServices []UsersService, err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	for _, usersServicePtr := range serviceProvider.usersServices {
		userServices = append(userServices, *usersServicePtr)
	}

	return userServices, nil
}

func (serviceProvider *testServiceProvider) SetUsersStorageFolder(users []string, serviceID string, storageFolder string) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	for _, usersServicePtr := range serviceProvider.usersServices {
		if reflect.DeepEqual(usersServicePtr.Users, users) && usersServicePtr.ServiceID == serviceID {
			usersServicePtr.StorageFolder = storageFolder

			return nil
		}
	}

	return fmt.Errorf("service %s does not exist in users", serviceID)
}

func (serviceProvider *testServiceProvider) SetUsersStateChecksum(users []string, serviceID string, checksum []byte) (err error) {
	serviceProvider.Lock()
	defer serviceProvider.Unlock()

	for _, usersServicePtr := range serviceProvider.usersServices {
		if reflect.DeepEqual(usersServicePtr.Users, users) && usersServicePtr.ServiceID == serviceID {
			usersServicePtr.StateChecksum = checksum

			return nil
		}
	}

	return fmt.Errorf("service %s does not exist in users", serviceID)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	launcher, err := newTestLauncher(new(pythonImage), nil, nil)
	if err != nil {
		return err
	}
	defer launcher.Close()

	if err := launcher.removeAllServices(); err != nil {
		return err
	}

	if err := os.RemoveAll("tmp"); err != nil {
		return err
	}

	return nil
}

func generatePythonContent(imagePath string) (err error) {
	serviceContent := `#!/usr/bin/python

import time
import socket
import sys

i = 0
serviceName = sys.argv[1]
serviceVersion = sys.argv[2]

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM) 
message = serviceName + ", version: " + serviceVersion
sock.sendto(str.encode(message), ("172.19.0.1", 10001))
sock.close()

print(">>>> Start", serviceName, "version", serviceVersion)
while True:
	print(">>>> aos", serviceName, "version", serviceVersion, "count", i)
	i = i + 1
	time.sleep(5)`

	if err := ioutil.WriteFile(path.Join(imagePath, "rootfs", "home", "service.py"), []byte(serviceContent), 0644); err != nil {
		return err
	}

	return nil
}

func generateFtpContent(imagePath string) (err error) {
	serviceContent := `#!/usr/bin/python

from pyftpdlib.authorizers import DummyAuthorizer
from pyftpdlib.handlers import FTPHandler
from pyftpdlib.servers import FTPServer

authorizer = DummyAuthorizer()
authorizer.add_anonymous("/home/service/storage", perm="elradfmw")

handler = FTPHandler
handler.authorizer = authorizer

server = FTPServer(("", 21), handler)
server.serve_forever()`

	if err := ioutil.WriteFile(path.Join(imagePath, "rootfs", "home", "service.py"), []byte(serviceContent), 0644); err != nil {
		return err
	}

	return nil
}

func generateConfig(imagePath string) (err error) {
	// remove json
	if err := os.Remove(path.Join(imagePath, "config.json")); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}

	// generate config spec
	out, err := exec.Command("runc", "spec", "-b", imagePath).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	return nil
}

func (launcher *Launcher) connectToFtp(serviceID string) (ftpConnection *ftp.ServerConn, err error) {
	service, err := launcher.serviceProvider.GetService(serviceID)
	if err != nil {
		return nil, err
	}

	ip, err := monitoring.GetServiceIPAddress(service.Path)
	if err != nil {
		return nil, err
	}

	ftpConnection, err = ftp.DialTimeout(ip+":21", 5*time.Second)
	if err != nil {
		return nil, err
	}

	if err = ftpConnection.Login("anonymous", "anonymous"); err != nil {
		ftpConnection.Quit()
		return nil, err
	}

	return ftpConnection, nil
}

func generateImageConfig() (config *imagespec.Image, err error) {
	configStr := `{
		"created": "2015-10-31T22:22:56.015925234Z",
		"author": "Alyssa P. Hacker <alyspdev@example.com>",
		"architecture": "amd64",
		"os": "Linux",
		"config": {
			"ExposedPorts": {
				"8080/tcp": {}
			},
			"Env": [
				"PATH=/usr/local/sbin",
				"FOO=oci_is_a",
				"BAR=well_written_spec",
				"MY_VAR",
				"TERM"
			],
			"Entrypoint": [
				"/bin/my-app-binary"
			],
			"Cmd": [
				"--foreground",
				"--config",
				"/etc/my-app.d/default.cfg"
			],
			"Volumes": {
				"/var/job-result-data": {},
				"/var/log/my-app-logs": {}
			},
			"WorkingDir": "/home/alice",
			"Labels": {
				"com.example.project.git.url": "https://example.com/project.git",
				"com.example.project.git.commit": "45a939b2999782a3f005621a8d0f29aa387e1d6b"
			}
		},
		"rootfs": {
		  "diff_ids": [
			"sha256:c6f988f4874bb0add23a778f753c65efe992244e148a1d2ec2a8b664fb66bbd1",
			"sha256:5f70bf18a086007016e948b04aed3b82103a36bea41755b6cddfaf10ace3c6ef"
		  ],
		  "type": "layers"
		},
		"history": [
		  {
			"created": "2015-10-31T22:22:54.690851953Z",
			"created_by": "/bin/sh -c #(nop) ADD file:a3bc1e842b69636f9df5256c49c5374fb4eef1e281fe3f282c65fb853ee171c5 in /"
		  },
		  {
			"created": "2015-10-31T22:22:55.613815829Z",
			"created_by": "/bin/sh -c #(nop) CMD [\"sh\"]",
			"empty_layer": true
		  }
		]
	}
	`
	var imageConfig imagespec.Image
	if err = json.Unmarshal([]byte(configStr), &imageConfig); err != nil {
		return nil, err
	}

	return &imageConfig, nil
}

func saveImageConfig(folderPath string, config *imagespec.Image) (filePath string, err error) {
	filePath = path.Join(folderPath, "imageConfig.json")

	if err := os.Remove(filePath); err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
	}

	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}

	jsonFile, err := os.Create(filePath)
	if err != nil {
		return "", err
	}

	if _, err := jsonFile.Write(data); err != nil {
		return "", err
	}

	return filePath, err
}
