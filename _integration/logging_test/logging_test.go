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

package logging_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/api/cloudprotocol"
	"github.com/coreos/go-systemd/v22/dbus"
	log "github.com/sirupsen/logrus"

	"github.com/aosedge/aos_servicemanager/config"
	"github.com/aosedge/aos_servicemanager/logging"
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

type testInstanceIDProvider struct {
	instances map[string]cloudprotocol.InstanceFilter
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var systemd *dbus.Conn

/***********************************************************************************************************************
 * Main
 **********************************************************************************************************************/

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
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	logging, err := logging.New(&config.Config{Logging: config.Logging{
		MaxPartSize: 1024, MaxPartCount: 10,
	}}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	from := time.Now()

	subject := "subject0"

	instance := uint64(0)

	serviceID := "logservice0"

	instanceFilter := cloudprotocol.InstanceFilter{ServiceID: &serviceID, SubjectID: &subject, Instance: &instance}

	instanceID := instanceProvider.AddFilter(instanceFilter)

	if err = createService(instanceID); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	if err = startService(instanceID); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(10 * time.Second)

	if err = stopService(instanceID); err != nil {
		t.Fatalf("Can't stop service: %s", err)
	}
	defer instanceProvider.RemoveFilter(instanceID)

	till := from.Add(5 * time.Second)

	if err = logging.GetInstanceLog(cloudprotocol.RequestLog{
		LogID: "log0",
		Filter: cloudprotocol.LogFilter{
			From:           &from,
			Till:           &till,
			InstanceFilter: instanceFilter,
		},
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &till)

	if err = logging.GetInstanceLog(cloudprotocol.RequestLog{
		LogID: "log0",
		Filter: cloudprotocol.LogFilter{
			InstanceFilter: instanceFilter,
			From:           &from,
		},
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	currentTime := time.Now()

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &currentTime)
}

func TestGetWrongServiceLog(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	logging, err := logging.New(
		&config.Config{Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10}}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	subject := "subject1"

	instance := uint64(0)

	serviceID := "nonExisting"

	instanceFilter := cloudprotocol.InstanceFilter{ServiceID: &serviceID, SubjectID: &subject, Instance: &instance}

	if err = logging.GetInstanceLog(cloudprotocol.RequestLog{
		LogID: "log1",
		Filter: cloudprotocol.LogFilter{
			InstanceFilter: instanceFilter,
		},
	}); err == nil {
		t.Errorf("Should be error : no instance ids for log request")
	}

	instanceID := instanceProvider.AddFilter(instanceFilter)
	defer instanceProvider.RemoveFilter(instanceID)

	till := time.Now()
	from := till.Add(-1 * time.Hour)

	if err = logging.GetInstanceLog(cloudprotocol.RequestLog{
		LogID: "log1",
		Filter: cloudprotocol.LogFilter{
			InstanceFilter: instanceFilter,
			From:           &from,
			Till:           &till,
		},
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	select {
	case result := <-logging.GetLogsDataChannel():
		if string(result.Content) != "" {
			t.Error("Should be no logs")
		}

	case <-time.After(5 * time.Second):
		t.Error("Receive log timeout")
	}
}

func TestGetSystemLog(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	logging, err := logging.New(&config.Config{Logging: config.Logging{
		MaxPartSize: 1024, MaxPartCount: 10,
	}}, &instanceProvider)
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

	logging.GetSystemLog(cloudprotocol.RequestLog{
		LogID: "log10",
		Filter: cloudprotocol.LogFilter{
			From: &from,
			Till: &till,
		},
	})

	checkReceivedLog(t, logging.GetLogsDataChannel(), nil, nil)
}

func TestGetEmptyLog(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	logging, err := logging.New(
		&config.Config{Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10}}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	subject := "subject2"

	instance := uint64(0)

	serviceID := "logservice2"

	instanceFilter := cloudprotocol.InstanceFilter{ServiceID: &serviceID, SubjectID: &subject, Instance: &instance}

	instanceID := instanceProvider.AddFilter(instanceFilter)
	defer instanceProvider.RemoveFilter(instanceID)

	if err = createService(instanceID); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	from := time.Now()

	time.Sleep(5 * time.Second)

	till := time.Now()

	if err = logging.GetInstanceLog(cloudprotocol.RequestLog{
		LogID: "log0",
		Filter: cloudprotocol.LogFilter{
			InstanceFilter: instanceFilter,
			From:           &from,
			Till:           &till,
		},
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	checkEmptyLog(t, logging.GetLogsDataChannel())
}

func TestGetServiceCrashLog(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	logging, err := logging.New(&config.Config{
		Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10},
	}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	subject := "subject3"

	instance := uint64(0)

	serviceID := "logservice3"

	instanceFilter := cloudprotocol.InstanceFilter{ServiceID: &serviceID, SubjectID: &subject, Instance: &instance}

	instanceID := instanceProvider.AddFilter(instanceFilter)

	if err = createService(instanceID); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	from := time.Now()

	if err = startService(instanceID); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(5 * time.Second)

	crashService(instanceID)

	till := time.Now()

	time.Sleep(1 * time.Second)

	if err := logging.GetInstanceCrashLog(cloudprotocol.RequestLog{
		Filter: cloudprotocol.LogFilter{
			InstanceFilter: instanceFilter,
		},
	}); err != nil {
		t.Fatalf("Can't get instance crash log: %s", err)
	}

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &till)

	serviceID = "logservice5"

	instanceFilter = cloudprotocol.InstanceFilter{ServiceID: &serviceID, SubjectID: &subject, Instance: &instance}

	instanceID = instanceProvider.AddFilter(instanceFilter)

	if err = createService(instanceID); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	from = time.Now()

	if err = startService(instanceID); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(5 * time.Second)

	crashService(instanceID)

	time.Sleep(1 * time.Second)

	till = time.Now()

	time.Sleep(1 * time.Second)

	if err := logging.GetInstanceCrashLog(cloudprotocol.RequestLog{
		Filter: cloudprotocol.LogFilter{
			InstanceFilter: instanceFilter,
			From:           &from, Till: &till,
		},
	}); err != nil {
		t.Fatalf("Can't get instance crash log: %s", err)
	}

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &till)
}

func TestMaxPartCountLog(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	logging, err := logging.New(&config.Config{
		Logging: config.Logging{MaxPartSize: 512, MaxPartCount: 2},
	}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	subject := "subject4"

	instance := uint64(0)

	serviceID := "logservice4"

	instanceFilter := cloudprotocol.InstanceFilter{ServiceID: &serviceID, SubjectID: &subject, Instance: &instance}

	instanceID := instanceProvider.AddFilter(instanceFilter)

	if err = createService(instanceID); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	from := time.Now()

	if err = startService(instanceID); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(20 * time.Second)

	if err = stopService(instanceID); err != nil {
		t.Fatalf("Can't stop service: %s", err)
	}
	defer instanceProvider.RemoveFilter(instanceID)

	till := from.Add(20 * time.Second)

	if err = logging.GetInstanceLog(cloudprotocol.RequestLog{
		LogID: "log0",
		Filter: cloudprotocol.LogFilter{
			InstanceFilter: instanceFilter,
			From:           &from,
			Till:           &till,
		},
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	for {
		select {
		case result := <-logging.GetLogsDataChannel():
			if result.ErrorInfo != nil && result.ErrorInfo.Message != "" {
				t.Errorf("Error log received: %s", result.ErrorInfo.Message)
				return
			}

			if result.Content == nil {
				t.Error("No data")
				return
			}

			if result.PartsCount == 0 {
				t.Error("Missing part count")
				return
			}

			if result.PartsCount != 2 {
				t.Errorf("Wrong part count received: %d", result.PartsCount)
				return
			}

			if result.Part == 0 {
				t.Error("Missing part")
				return
			}

			if result.Part > result.PartsCount {
				t.Errorf("Wrong part received: %d", result.Part)
				return
			}

		case <-time.After(1 * time.Second):
			return
		}
	}
}

/***********************************************************************************************************************
 * Interfaces
 **********************************************************************************************************************/

func (provider *testInstanceIDProvider) GetInstanceIDs(
	filter cloudprotocol.InstanceFilter,
) (instances []string, err error) {
	for key, value := range provider.instances {
		if filter.ServiceID != value.ServiceID {
			continue
		}

		if filter.SubjectID != nil && (*filter.SubjectID != *value.SubjectID) {
			continue
		}

		if filter.Instance != nil && (*filter.Instance != *value.Instance) {
			continue
		}

		instances = append(instances, key)
	}

	return instances, nil
}

func (provider *testInstanceIDProvider) AddFilter(filter cloudprotocol.InstanceFilter) (instanceID string) {
	instanceID = instanceFormFilter(filter)

	provider.instances[instanceID] = filter

	return instanceID
}

func (provider *testInstanceIDProvider) RemoveFilter(instanceID string) {
	delete(provider.instances, instanceID)
}

func (provider *testInstanceIDProvider) Close() {
	for instanceID := range provider.instances {
		if err := stopService(instanceID); err != nil {
			log.Errorf("Can't stop service: %s", err)
		}

		if _, err := systemd.DisableUnitFilesContext(context.Background(),
			[]string{"aos-service@" + instanceID + ".service"}, false); err != nil {
			log.Errorf("Can't disable service: %s", err)
		}
	}
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func instanceFormFilter(filter cloudprotocol.InstanceFilter) string {
	return fmt.Sprintf("%s.%s.%s", *filter.ServiceID, *filter.SubjectID, strconv.FormatUint(*filter.Instance, 10))
}

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	if systemd, err = dbus.NewSystemConnectionContext(context.Background()); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func cleanup() {
	systemd.Close()

	if err := os.RemoveAll("tmp"); err != nil {
		log.Errorf("Can't remove tmp folder: %s", err)
	}
}

func createService(instanceID string) (err error) {
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

	serviceName := "aos-service@" + instanceID + ".service"

	fileName, err := filepath.Abs(path.Join("tmp", serviceName))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = ioutil.WriteFile(fileName, []byte(serviceContent), 0o600); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = systemd.LinkUnitFilesContext(context.Background(), []string{fileName}, false, true); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = systemd.ReloadContext(context.Background()); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func startService(instanceID string) (err error) {
	channel := make(chan string)

	if _, err = systemd.RestartUnitContext(context.Background(),
		"aos-service@"+instanceID+".service", "replace", channel); err != nil {
		return aoserrors.Wrap(err)
	}

	<-channel

	return nil
}

func stopService(instanceID string) (err error) {
	channel := make(chan string)

	if _, err = systemd.StopUnitContext(context.Background(),
		"aos-service@"+instanceID+".service", "replace", channel); err != nil {
		return aoserrors.Wrap(err)
	}

	<-channel

	return nil
}

func crashService(instanceID string) {
	systemd.KillUnitContext(context.Background(), "aos-service@"+instanceID+".service", int32(syscall.SIGSEGV))
}

func getTimeRange(logData string) (from, till time.Time, err error) {
	list := strings.Split(logData, "\n")

	if len(list) < 2 || len(list[0]) < 37 || len(list[len(list)-2]) < 37 {
		return from, till, aoserrors.New("bad log data")
	}

	fromLog := list[0][strings.IndexByte(list[0], '['):]

	if from, err = time.Parse("2006-01-02 15:04:05.999999999Z07:00", fromLog[1:36]); err != nil {
		return from, till, aoserrors.Wrap(err)
	}

	tillLog := list[len(list)-2][strings.IndexByte(list[len(list)-2], '['):]

	if till, err = time.Parse("2006-01-02 15:04:05.999999999Z07:00", tillLog[1:36]); err != nil {
		return from, till, aoserrors.Wrap(err)
	}

	return from, till, nil
}

func checkReceivedLog(t *testing.T, logChannel <-chan cloudprotocol.PushLog, from, till *time.Time) {
	t.Helper()

	receivedLog := ""

	for {
		select {
		case result := <-logChannel:
			if result.ErrorInfo != nil && result.ErrorInfo.Message != "" {
				t.Errorf("Error log received: %s", result.ErrorInfo.Message)
				return
			}

			if result.Content == nil {
				t.Error("No data")
				return
			}

			zr, err := gzip.NewReader(bytes.NewBuffer(result.Content))
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

			if result.Part == result.PartsCount {
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

func checkEmptyLog(t *testing.T, logChannel <-chan cloudprotocol.PushLog) {
	t.Helper()

	for {
		select {
		case result := <-logChannel:
			if result.ErrorInfo != nil && result.ErrorInfo.Message != "" {
				t.Errorf("Error log received: %s", result.ErrorInfo.Message)
				return
			}

			if (result.Content == nil) || len(result.Content) != 0 {
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
