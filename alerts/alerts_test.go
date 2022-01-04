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

package alerts_test

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log/syslog"
	"os"
	"path"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	pb "github.com/aoscloud/aos_common/api/servicemanager/v1"
	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aoscloud/aos_servicemanager/alerts"
	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/launcher"
)

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Types
 ******************************************************************************/

type testServiceProvider struct {
	services map[string]*launcher.Service
}

type testCursorStorage struct {
	cursor string
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var systemd *dbus.Conn

var errTimeout = aoserrors.New("timeout")

var (
	serviceProvider = testServiceProvider{services: make(map[string]*launcher.Service)}
	cursorStorage   testCursorStorage
)

var tmpDir string

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

func TestGetSystemError(t *testing.T) {
	const numMessages = 5

	alertsHandler, err := alerts.New(&config.Config{
		Alerts: config.Alerts{
			ServiceAlertPriority: 4,
			SystemAlertPriority:  3,
		},
	},
		&serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	sysLog, err := syslog.New(syslog.LOG_CRIT, "")
	if err != nil {
		t.Fatalf("Can't create syslog: %s", err)
	}
	defer sysLog.Close()

	// Check crit message received

	messages := make([]string, 0, numMessages)

	for i := 0; i < numMessages; i++ {
		messages = append(messages, uuid.New().String())

		if err = sysLog.Crit(messages[len(messages)-1]); err != nil {
			t.Errorf("Can't write to syslog: %s", err)
		}

		time.Sleep(500 * time.Millisecond)
	}

	if err = waitAlerts(alertsHandler.GetAlertsChannel(), 5*time.Second,
		alerts.AlertTagSystemError, "system", 0, messages); err != nil {
		t.Errorf("Result failed: %s", err)
	}

	// Check non crit message not received

	messages = make([]string, 0, numMessages)

	messages = append(messages, uuid.New().String())
	if err = sysLog.Warning(messages[len(messages)-1]); err != nil {
		t.Errorf("Can't write to syslog: %s", err)
	}

	messages = append(messages, uuid.New().String())
	if err = sysLog.Notice(messages[len(messages)-1]); err != nil {
		t.Errorf("Can't write to syslog: %s", err)
	}

	messages = append(messages, uuid.New().String())
	if err = sysLog.Info(messages[len(messages)-1]); err != nil {
		t.Errorf("Can't write to syslog: %s", err)
	}

	messages = append(messages, uuid.New().String())
	if err = sysLog.Debug(messages[len(messages)-1]); err != nil {
		t.Errorf("Can't write to syslog: %s", err)
	}

	if err = waitResult(alertsHandler.GetAlertsChannel(), 5*time.Second,
		func(alert *pb.Alert) (success bool, err error) {
			if alert.Tag == alerts.AlertTagSystemError {
				for _, originMessage := range messages {
					systemAlert := alert.GetSystemAlert()
					if systemAlert == nil {
						return false, aoserrors.New("wrong alert type")
					}

					if originMessage == systemAlert.Message {
						return false, aoserrors.New("unexpected message: " + systemAlert.Message)
					}
				}
			}

			return false, nil
		}); err != nil && !errors.Is(err, errTimeout) {
		t.Errorf("Result failed: %s", err)
	}
}

func TestGetServiceError(t *testing.T) {
	alertsHandler, err := alerts.New(&config.Config{
		Alerts: config.Alerts{
			ServiceAlertPriority: 4,
			SystemAlertPriority:  3,
		},
	},
		&serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	if err = createService("alertservice0"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	if err = startService("alertservice0"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	time.Sleep(1 * time.Second)

	crashService("alertservice0")

	messages := []string{
		"aos_alertservice0.service: Main process exited, code=dumped, status=11/SEGV",
		"aos_alertservice0.service: Failed with result 'core-dump'.",
	}

	if err = waitAlerts(alertsHandler.GetAlertsChannel(), 5*time.Second,
		alerts.AlertTagSystemError, "alertservice0", 0, messages); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}

func TestGetServiceManagerAlerts(t *testing.T) {
	const numMessages = 5

	messages := make([]string, 0, numMessages)

	for i := 0; i < numMessages; i++ {
		messages = append(messages, uuid.New().String())
	}

	command := fmt.Sprintf("/usr/bin/systemd-cat -p3 /bin/bash -c 'for message in %s ; do echo $message ; done'",
		strings.Join(messages, " "))

	if err := createSystemdUnit("oneshot", command,
		path.Join(tmpDir, "aos-servicemanager.service")); err != nil {
		t.Fatalf("Can't create systemd unit: %s", err)
	}

	alertsHandler, err := alerts.New(&config.Config{
		Alerts: config.Alerts{
			ServiceAlertPriority: 4,
			SystemAlertPriority:  3,
		},
	},
		&serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	if err = startSystemdUnit("aos-servicemanager.service"); err != nil {
		t.Fatalf("Can't start systemd unit: %s", err)
	}

	if err = waitAlerts(alertsHandler.GetAlertsChannel(), 5*time.Second, alerts.AlertTagAosCore,
		"aos-servicemanager.service", 0, messages); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}

func TestMessageFilter(t *testing.T) {
	const numMessages = 3

	filter := []string{"test", "regexp"}

	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		ServiceAlertPriority: 4,
		SystemAlertPriority:  3,
		Filter:               filter,
	}},
		&serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	sysLog, err := syslog.New(syslog.LOG_CRIT, "")
	if err != nil {
		t.Fatalf("Can't create syslog: %s", err)
	}
	defer sysLog.Close()

	validMessage := "message should not be filterout"
	messages := []string{"test mesage to filterout", validMessage, "regexp mesage to filterout"}

	for _, msg := range messages {
		if err = sysLog.Crit(msg); err != nil {
			t.Errorf("Can't write to syslog: %s", err)
		}

		time.Sleep(100 * time.Millisecond)
	}

	foundCount := 0

	for i := 0; i < numMessages; i++ {
		err := waitResult(alertsHandler.GetAlertsChannel(), 1*time.Second,
			func(alert *pb.Alert) (success bool, err error) {
				systemAlert := (alert.GetSystemAlert())
				if systemAlert == nil {
					return false, aoserrors.New("wrong alert type")
				}

				if systemAlert.Message != validMessage {
					return false, aoserrors.New("Receive unexpected alert mesage")
				}

				return true, nil
			})

		if err == nil {
			foundCount++

			continue
		}

		if !errors.Is(err, errTimeout) {
			t.Errorf("Result failed: %s", err)
		}
	}

	if foundCount != 1 {
		t.Errorf("Incorrect count of received alerts count = %d", foundCount)
	}
}

func TestWrongFilter(t *testing.T) {
	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		Filter:               []string{"", "*(test)^"},
		ServiceAlertPriority: 4,
		SystemAlertPriority:  3,
	}},
		&serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()
}

func TestOtheralerts(t *testing.T) {
	alertsHandler, err := alerts.New(&config.Config{
		Alerts: config.Alerts{
			ServiceAlertPriority: 4,
			SystemAlertPriority:  3,
		},
	},
		&serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	time := time.Now()
	alertsHandler.SendResourceAlert("test", "testResource", time, 42)

	resourceAlert := pb.Alert{
		Timestamp: timestamppb.New(time),
		Tag:       alerts.AlertTagResource,
		Source:    "test",
		Payload: &pb.Alert_ResourceAlert{ResourceAlert: &pb.ResourceAlert{
			Parameter: "testResource",
			Value:     42,
		}},
	}

	if err = waitResult(alertsHandler.GetAlertsChannel(), 5, func(alert *pb.Alert) (success bool, err error) {
		if !proto.Equal(alert, &resourceAlert) {
			return false, aoserrors.New("received resource alert != send alert")
		}

		return true, nil
	}); err != nil {
		t.Errorf("Result failed: %s", err)
	}

	deviceErrors := make(map[string][]error)
	deviceErrors["dev1"] = []error{aoserrors.New("some error")}

	alertsHandler.SendValidateResourceAlert("test", deviceErrors)

	convertedErrors := make([]*pb.ResourceValidateErrors, 0)

	for name, reason := range deviceErrors {
		var messages []string

		for _, item := range reason {
			messages = append(messages, item.Error())
		}

		resourceError := pb.ResourceValidateErrors{
			Name:     name,
			ErrorMsg: messages,
		}

		convertedErrors = append(convertedErrors, &resourceError)
	}

	validationAlert := pb.Alert{
		Timestamp: timestamppb.New(time),
		Tag:       alerts.AlertTagAosCore,
		Source:    "test",
		Payload: &pb.Alert_ResourceValidateAlert{
			ResourceValidateAlert: &pb.ResourceValidateAlert{
				Type:   alerts.AlertDeviceErrors,
				Errors: convertedErrors,
			},
		},
	}

	if err = waitResult(alertsHandler.GetAlertsChannel(), 5, func(alert *pb.Alert) (success bool, err error) {
		alert.Timestamp = validationAlert.Timestamp
		if !proto.Equal(alert, &validationAlert) {
			return false, aoserrors.New("received validation alert != send alert")
		}

		return true, nil
	}); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (serviceProvider *testServiceProvider) GetService(serviceID string) (service launcher.Service, err error) {
	s, ok := serviceProvider.services[serviceID]
	if !ok {
		return service, aoserrors.New(fmt.Sprintf("service %s does not exist", serviceID))
	}

	return *s, nil
}

func (serviceProvider *testServiceProvider) GetServiceByUnitName(unitName string) (service launcher.Service,
	err error) {
	for _, s := range serviceProvider.services {
		if s.UnitName == unitName {
			return *s, nil
		}
	}

	return service, aoserrors.New(fmt.Sprintf("service with unit %s does not exist", unitName))
}

func (cursorStorage *testCursorStorage) SetJournalCursor(cursor string) (err error) {
	cursorStorage.cursor = cursor

	return nil
}

func (cursorStorage *testCursorStorage) GetJournalCursor() (cursor string, err error) {
	return cursorStorage.cursor, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	tmpDir, err = ioutil.TempDir("", "sm_")
	if err != nil {
		log.Fatalf("Error create temporary dir: %s", err)
	}

	if systemd, err = dbus.NewSystemConnectionContext(context.Background()); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func cleanup() {
	for _, service := range serviceProvider.services {
		if err := stopService(service.ID); err != nil {
			log.Errorf("Can't stop service: %s", err)
		}

		if _, err := systemd.DisableUnitFilesContext(context.Background(),
			[]string{service.UnitName}, false); err != nil {
			log.Errorf("Can't disable service: %s", err)
		}
	}

	systemd.Close()

	if err := os.RemoveAll(tmpDir); err != nil {
		log.Errorf("Error removing tmp dir: %s", err)
	}
}

func waitResult(alertsChannel <-chan *pb.Alert, timeout time.Duration,
	checkAlert func(alert *pb.Alert) (success bool, err error)) (err error) {
	for {
		select {
		case alert := <-alertsChannel:
			success, err := checkAlert(alert)
			if err != nil {
				return aoserrors.Wrap(err)
			}

			if success {
				return nil
			}

		case <-time.After(timeout):
			return errTimeout
		}
	}
}

func waitAlerts(alertsChannel <-chan *pb.Alert, timeout time.Duration,
	tag, source string, version uint64, data []string) (err error) {
	return waitResult(alertsChannel, timeout, func(alert *pb.Alert) (success bool, err error) {
		if alert.Tag != tag {
			return false, nil
		}

		systemAlert := alert.GetSystemAlert()
		if systemAlert == nil {
			return false, aoserrors.New("wrong alert type")
		}

		for i, message := range data {
			if systemAlert.Message == message {
				data = append(data[:i], data[i+1:]...)

				if alert.Source != source {
					return false, aoserrors.New("wrong alert source: " + alert.Source)
				}

				if !reflect.DeepEqual(alert.AosVersion, version) {
					return false, aoserrors.New("AosVersion field missing")
				}

				if len(data) == 0 {
					return true, nil
				}

				return false, nil
			}
		}

		return false, nil
	})
}

func createService(serviceID string) (err error) {
	serviceName := "aos_" + serviceID + ".service"

	if _, ok := serviceProvider.services[serviceID]; ok {
		return aoserrors.New("service already exists")
	}

	serviceProvider.services[serviceID] = &launcher.Service{ID: serviceID, UnitName: serviceName}

	if err = createSystemdUnit("simple",
		`/bin/bash -c 'while true; do echo "[$(date --rfc-3339=ns)] This is log"; sleep 0.1; done'`,
		path.Join(tmpDir, serviceName)); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func startService(serviceID string) (err error) {
	if err = startSystemdUnit("aos_" + serviceID + ".service"); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func createSystemdUnit(serviceType, command, fileName string) (err error) {
	serviceTemplate := `[Unit]
	After=network.target
	
	[Service]
	Type=%s
	ExecStart=%s
	
	[Install]
	WantedBy=multi-user.target
`

	serviceContent := fmt.Sprintf(serviceTemplate, serviceType, command)

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

func startSystemdUnit(name string) (err error) {
	channel := make(chan string)

	if _, err = systemd.RestartUnitContext(context.Background(), name, "replace", channel); err != nil {
		return aoserrors.Wrap(err)
	}

	<-channel

	return nil
}

func stopService(serviceID string) (err error) {
	channel := make(chan string)

	if _, err = systemd.StopUnitContext(context.Background(),
		"aos_"+serviceID+".service", "replace", channel); err != nil {
		return aoserrors.Wrap(err)
	}

	<-channel

	return nil
}

func crashService(serviceID string) {
	systemd.KillUnitContext(context.Background(), "aos_"+serviceID+".service", int32(syscall.SIGSEGV))
}
