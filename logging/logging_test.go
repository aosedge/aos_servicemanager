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

package logging_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	log "github.com/sirupsen/logrus"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/servicemanager"
	"google.golang.org/protobuf/types/known/timestamppb"

	"aos_servicemanager/config"
	"aos_servicemanager/launcher"
	"aos_servicemanager/logging"
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

type testServiceProvider struct {
	services map[string]*launcher.Service
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var systemd *dbus.Conn

var serviceProvider = testServiceProvider{services: make(map[string]*launcher.Service)}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	ret := m.Run()

	cleanup()

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestGetServiceLog(t *testing.T) {
	logging, err := logging.New(&config.Config{Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10}}, &serviceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	from := time.Now()

	if err = createService("logservice0"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	if err = startService("logservice0"); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(10 * time.Second)

	if err = stopService("logservice0"); err != nil {
		t.Fatalf("Can't stop service: %s", err)
	}

	till := from.Add(5 * time.Second)

	logging.GetServiceLog(&pb.ServiceLogRequest{
		ServiceId: "logservice0",
		LogId:     "log0",
		From:      timestamppb.New(from),
		Till:      timestamppb.New(till)})

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &till)

	logging.GetServiceLog(&pb.ServiceLogRequest{
		ServiceId: "logservice0",
		LogId:     "log0",
		From:      timestamppb.New(from)})

	currentTime := time.Now()
	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &currentTime)
}

func TestGetWrongServiceLog(t *testing.T) {
	logging, err := logging.New(&config.Config{Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10}}, &serviceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	till := time.Now()
	from := till.Add(-1 * time.Hour)

	logging.GetServiceLog(&pb.ServiceLogRequest{
		ServiceId: "nonExisting",
		LogId:     "log1",
		From:      timestamppb.New(from),
		Till:      timestamppb.New(till)})

	select {
	case result := <-logging.GetLogsDataChannel():
		if result.Error == "" {
			log.Error("Expect log error")
		}

	case <-time.After(5 * time.Second):
		log.Errorf("Receive log timeout")
	}
}

func TestGetSystemLog(t *testing.T) {
	logging, err := logging.New(&config.Config{Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10}}, &serviceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	from := time.Now()

	for i := 0; i < 20; i++ {
		cmd := exec.Command("logger", "Hello World")
		if err := cmd.Run(); err != nil {
			t.Error(err)
		}
	}

	time.Sleep(7 * time.Second)

	till := from.Add(5 * time.Second)

	logging.GetSystemLog(&pb.SystemLogRequest{
		LogId: "log10",
		From:  timestamppb.New(from),
		Till:  timestamppb.New(till)})

	checkReceivedLog(t, logging.GetLogsDataChannel(), nil, nil)
}

func TestGetEmptyLog(t *testing.T) {
	logging, err := logging.New(&config.Config{Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10}}, &serviceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	if err = createService("logservice2"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	from := time.Now()

	time.Sleep(5 * time.Second)

	till := time.Now()

	logging.GetServiceLog(&pb.ServiceLogRequest{
		ServiceId: "logservice2",
		LogId:     "log0",
		From:      timestamppb.New(from),
		Till:      timestamppb.New(till)})

	checkEmptyLog(t, logging.GetLogsDataChannel())
}

func TestGetServiceCrashLog(t *testing.T) {
	logging, err := logging.New(&config.Config{Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10}}, &serviceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	if err = createService("logservice3"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	from := time.Now()

	if err = startService("logservice3"); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(5 * time.Second)

	crashService("logservice3")

	till := time.Now()

	time.Sleep(1 * time.Second)

	logging.GetServiceCrashLog(&pb.ServiceLogRequest{
		ServiceId: "logservice3",
		LogId:     "log2"})

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &till)

	if err = createService("logservice5"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	from = time.Now()

	if err = startService("logservice5"); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(5 * time.Second)

	crashService("logservice5")

	time.Sleep(1 * time.Second)

	till = time.Now()

	time.Sleep(1 * time.Second)

	if err = startService("logservice5"); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(5 * time.Second)

	crashService("logservice5")

	logging.GetServiceCrashLog(&pb.ServiceLogRequest{
		ServiceId: "logservice5",
		LogId:     "log5",
		From:      timestamppb.New(from),
		Till:      timestamppb.New(till),
	})

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &till)
}

func TestMaxPartCountLog(t *testing.T) {
	logging, err := logging.New(&config.Config{Logging: config.Logging{MaxPartSize: 512, MaxPartCount: 2}}, &serviceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	from := time.Now()

	if err = createService("logservice4"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	if err = startService("logservice4"); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(20 * time.Second)

	if err = stopService("logservice4"); err != nil {
		t.Fatalf("Can't stop service: %s", err)
	}

	till := from.Add(20 * time.Second)

	logging.GetServiceLog(&pb.ServiceLogRequest{
		ServiceId: "logservice4",
		LogId:     "log0",
		From:      timestamppb.New(from),
		Till:      timestamppb.New(till)})

	for {
		select {
		case result := <-logging.GetLogsDataChannel():
			if result.Error != "" {
				t.Errorf("Error log received: %s", result.Error)
				return
			}

			if result.Data == nil {
				t.Error("No data")
				return
			}

			if result.PartCount == 0 {
				t.Error("Missing part count")
				return
			}

			if result.PartCount != 2 {
				t.Errorf("Wrong part count received: %d", result.PartCount)
				return
			}

			if result.Part == 0 {
				t.Error("Missing part")
				return
			}

			if result.Part > result.PartCount {
				t.Errorf("Wrong part received: %d", result.Part)
				return
			}

		case <-time.After(1 * time.Second):
			return
		}
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (serviceProvider *testServiceProvider) GetService(serviceID string) (service launcher.Service, err error) {
	s, ok := serviceProvider.services[serviceID]
	if !ok {
		return service, fmt.Errorf("service %s does not exist", serviceID)
	}

	return *s, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	if systemd, err = dbus.NewSystemConnection(); err != nil {
		return err
	}

	return nil
}

func cleanup() {
	for _, service := range serviceProvider.services {
		if err := stopService(service.ID); err != nil {
			log.Errorf("Can't stop service: %s", err)
		}

		if _, err := systemd.DisableUnitFiles([]string{service.UnitName}, false); err != nil {
			log.Errorf("Can't disable service: %s", err)
		}
	}

	systemd.Close()

	if err := os.RemoveAll("tmp"); err != nil {
		log.Errorf("Can't remove tmp folder: %s", err)
	}
}

func createService(serviceID string) (err error) {
	serviceContent := `[Unit]
	Description=AOS Service
	After=network.target
	
	[Service]
	Type=simple
	Restart=always
	RestartSec=1
	ExecStart=/bin/bash -c 'while true; do echo "[$(date --rfc-3339=ns)] This is log"; sleep 0.1; done'
	
	[Install]
	WantedBy=multi-user.target
`

	serviceName := "aos_" + serviceID + ".service"

	if _, ok := serviceProvider.services[serviceID]; ok {
		return errors.New("service already exists")
	}

	serviceProvider.services[serviceID] = &launcher.Service{ID: serviceID, UnitName: serviceName}

	fileName, err := filepath.Abs(path.Join("tmp", serviceName))
	if err != nil {
		return err
	}

	if err = ioutil.WriteFile(fileName, []byte(serviceContent), 0644); err != nil {
		return err
	}

	if _, err = systemd.LinkUnitFiles([]string{fileName}, false, true); err != nil {
		return err
	}

	if err = systemd.Reload(); err != nil {
		return err
	}

	return nil
}

func startService(serviceID string) (err error) {
	channel := make(chan string)

	if _, err = systemd.RestartUnit("aos_"+serviceID+".service", "replace", channel); err != nil {
		return err
	}

	<-channel

	return nil
}

func stopService(serviceID string) (err error) {
	channel := make(chan string)

	if _, err = systemd.StopUnit("aos_"+serviceID+".service", "replace", channel); err != nil {
		return err
	}

	<-channel

	return nil
}

func crashService(serviceID string) {
	systemd.KillUnit("aos_"+serviceID+".service", int32(syscall.SIGSEGV))
}

func getTimeRange(logData string) (from, till time.Time, err error) {
	list := strings.Split(logData, "\n")

	if len(list) < 2 || len(list[0]) < 37 || len(list[len(list)-2]) < 37 {
		return from, till, errors.New("bad log data")
	}

	fromLog := list[0][strings.IndexByte(list[0], '['):]

	if from, err = time.Parse("2006-01-02 15:04:05.999999999Z07:00", fromLog[1:36]); err != nil {
		return from, till, err
	}

	tillLog := list[len(list)-2][strings.IndexByte(list[len(list)-2], '['):]

	if till, err = time.Parse("2006-01-02 15:04:05.999999999Z07:00", tillLog[1:36]); err != nil {
		return from, till, err
	}

	return from, till, nil
}

func checkReceivedLog(t *testing.T, logChannel <-chan *pb.LogData, from, till *time.Time) {
	receivedLog := ""

	for {
		select {
		case result := <-logChannel:
			if result.Error != "" {
				t.Errorf("Error log received: %s", result.Error)
				return
			}

			if result.Data == nil {
				t.Error("No data")
				return
			}

			zr, err := gzip.NewReader(bytes.NewBuffer(result.Data))
			if err != nil {
				t.Errorf("gzip error: %s", err)
				return
			}

			data, err := ioutil.ReadAll(zr)
			if err != nil {
				t.Errorf("gzip error: %s", err)
				return
			}

			receivedLog += string(data)

			if result.Part == result.PartCount {
				if from == nil || till == nil {
					return
				}

				logFrom, logTill, err := getTimeRange(receivedLog)
				if err != nil {
					t.Errorf("Can't get log time range: %s", err)
					return
				}

				if logFrom.Before(*from) {
					t.Error("Log range out of requested")
				}

				if logTill.After(*till) {
					t.Error("Log range out of requested")
				}

				return
			}

		case <-time.After(5 * time.Second):
			t.Error("Receive log timeout")
			return
		}
	}
}

func checkEmptyLog(t *testing.T, logChannel <-chan *pb.LogData) {
	for {
		select {
		case result := <-logChannel:
			if result.Error != "" {
				t.Errorf("Error log received: %s", result.Error)
				return
			}

			if (result.Data == nil) || len(result.Data) != 0 {
				t.Error("Empty log expected")
				return
			}

			return

		case <-time.After(5 * time.Second):
			t.Error("Receive log timeout")
			return
		}
	}
}
