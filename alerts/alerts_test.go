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

package alerts_test

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log/syslog"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"aos_servicemanager/alerts"
	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/launcher"
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

type testCursorStorage struct {
	cursor string
}

type validateAlert struct {
	source  string
	message map[string][]error
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var systemd *dbus.Conn

var errTimeout = errors.New("timeout")

var serviceProvider = testServiceProvider{services: make(map[string]*launcher.Service)}
var cursorStorage testCursorStorage

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

	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 32}}, &serviceProvider, &cursorStorage)
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

	if err = waitAlerts(alertsHandler.AlertsChannel, 5*time.Second,
		amqp.AlertTagSystemError, "system", nil, messages); err != nil {
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

	if err = waitResult(alertsHandler.AlertsChannel, 5*time.Second,
		func(alert amqp.AlertItem) (success bool, err error) {
			if alert.Tag == amqp.AlertTagSystemError {
				for _, originMessage := range messages {
					systemAlert, ok := (alert.Payload.(amqp.SystemAlert))
					if !ok {
						return false, errors.New("wrong alert type")
					}

					if originMessage == systemAlert.Message {
						return false, fmt.Errorf("unexpected message: %s", systemAlert.Message)
					}
				}
			}

			return false, nil
		}); err != nil && err != errTimeout {
		t.Errorf("Result failed: %s", err)
	}
}

func TestGetOfflineSystemError(t *testing.T) {
	const numMessages = 5

	// Open and close to store cursor
	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 32}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	alertsHandler.Close()

	// Wait at least 1 poll period cursor to be stored
	time.Sleep(2 * time.Second)

	sysLog, err := syslog.New(syslog.LOG_CRIT, "")
	if err != nil {
		t.Fatalf("Can't create syslog: %s", err)
	}
	defer sysLog.Close()

	// Send offline messages

	messages := make([]string, 0, numMessages)

	for i := 0; i < numMessages; i++ {
		messages = append(messages, uuid.New().String())

		if err = sysLog.Emerg(messages[len(messages)-1]); err != nil {
			t.Errorf("Can't write to syslog: %s", err)
		}
	}

	// Open again
	alertsHandler, err = alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 32}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	// Check all offline messages are handled
	if err = waitAlerts(alertsHandler.AlertsChannel, 5*time.Second, amqp.AlertTagSystemError, "system", nil, messages); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}

func TestGetServiceError(t *testing.T) {
	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 32}}, &serviceProvider, &cursorStorage)
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
		"aos_alertservice0.service: Failed with result 'core-dump'."}

	var version uint64 = 0

	if err = waitAlerts(alertsHandler.AlertsChannel, 5*time.Second,
		amqp.AlertTagSystemError, "alertservice0", &version, messages); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}

func TestGetResourceAlerts(t *testing.T) {
	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 32}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	if err = createService("alertservice1"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	type resourceAlert struct {
		source   string
		resource string
		time     time.Time
		value    uint64
	}

	resourceAlerts := []resourceAlert{
		resourceAlert{"alertservice1", "cpu", time.Now(), 89},
		resourceAlert{"alertservice1", "cpu", time.Now(), 90},
		resourceAlert{"alertservice1", "cpu", time.Now(), 91},
		resourceAlert{"alertservice1", "cpu", time.Now(), 92},
		resourceAlert{"system", "ram", time.Now(), 93},
		resourceAlert{"system", "ram", time.Now(), 1500},
		resourceAlert{"system", "ram", time.Now(), 1600}}

	for _, alert := range resourceAlerts {
		alertsHandler.SendResourceAlert(alert.source, alert.resource, alert.time, alert.value)
	}

	if err = waitResult(alertsHandler.AlertsChannel, 5*time.Second,
		func(alert amqp.AlertItem) (success bool, err error) {
			if alert.Tag != amqp.AlertTagResource {
				return false, nil
			}

			for i, originItem := range resourceAlerts {
				receivedAlert, ok := (alert.Payload.(amqp.ResourceAlert))
				if !ok {
					return false, errors.New("wrong alert type")
				}

				receivedItem := resourceAlert{
					source:   alert.Source,
					resource: receivedAlert.Parameter,
					time:     alert.Timestamp,
					value:    receivedAlert.Value}

				if receivedItem == originItem {
					resourceAlerts = append(resourceAlerts[:i], resourceAlerts[i+1:]...)

					if len(resourceAlerts) == 0 {
						return true, nil
					}

					return false, nil
				}
			}

			return false, nil
		}); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}

func TestGetValidateResourceAlerts(t *testing.T) {
	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     2048,
		MaxOfflineMessages: 32}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	if err = createService("alertservice2"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	device1 := "test123"
	device2 := "test234"
	device3 := "test345"

	error1 := fmt.Errorf("device: %s is unavailable", device1)
	error2 := fmt.Errorf("device: %s was not provided for %s service", device2, "service234")
	error3 := fmt.Errorf("device: %s error is %s", device3, "error345")
	error4 := fmt.Errorf("system error")

	message1 := make(map[string][]error)
	message1[device1] = []error{error1, error2}

	message2 := make(map[string][]error)
	message2[device2] = []error{error2, error3}
	message2[device3] = []error{error3, error4}

	validateAlerts := []validateAlert{
		validateAlert{"alertservice2", message1},
		validateAlert{"alertservice2", message2},
		validateAlert{"system", message1},
		validateAlert{"system", message2}}

	for _, alert := range validateAlerts {
		alertsHandler.SendValidateResourceAlert(alert.source, alert.message)
	}

	if err = waitResult(alertsHandler.AlertsChannel, 5*time.Second,
		func(alert amqp.AlertItem) (success bool, err error) {
			if alert.Tag != amqp.AlertTagAosCore {
				return false, nil
			}

			for i, originItem := range validateAlerts {
				receivedAlert, ok := (alert.Payload.(amqp.ResourseValidatePayload))
				if !ok {
					return false, errors.New("wrong alert type")
				}

				receivedMessage := make(map[string][]error)

				for _, item := range receivedAlert.Errors {
					var errors []error

					for _, message := range item.Errors {
						errors = append(errors, fmt.Errorf(message))
					}

					receivedMessage[item.Name] = errors
				}

				receivedItem := validateAlert{
					source:  alert.Source,
					message: receivedMessage}

				if compareValidateAlerts(receivedItem, originItem) {
					validateAlerts = append(validateAlerts[:i], validateAlerts[i+1:]...)

					if len(validateAlerts) == 0 {
						return true, nil
					}

					return false, nil
				}
			}

			return false, nil
		}); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}

func TestGetRequestResourceAlerts(t *testing.T) {
	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 32}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	if err = createService("alertservice3"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	type requestAlert struct {
		source  string
		message string
	}

	message1 := "device: test123 is unavailable"
	message2 := "device: test234 is unavailable"

	requestAlerts := []requestAlert{
		requestAlert{"alertservice3", message1},
		requestAlert{"alertservice3", message2},
		requestAlert{"system", message1},
		requestAlert{"system", message2}}

	for _, alert := range requestAlerts {
		alertsHandler.SendRequestResourceAlert(alert.source, alert.message)
	}

	if err = waitResult(alertsHandler.AlertsChannel, 5*time.Second,
		func(alert amqp.AlertItem) (success bool, err error) {
			if alert.Tag != amqp.AlertTagAosCore {
				return false, nil
			}

			for i, originItem := range requestAlerts {
				receivedAlert, ok := (alert.Payload.(amqp.SystemAlert))
				if !ok {
					return false, errors.New("wrong alert type")
				}

				receivedItem := requestAlert{
					source:  alert.Source,
					message: receivedAlert.Message}

				if receivedItem == originItem {
					requestAlerts = append(requestAlerts[:i], requestAlerts[i+1:]...)

					if len(requestAlerts) == 0 {
						return true, nil
					}

					return false, nil
				}
			}

			return false, nil
		}); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}

func TestGetDowloadsStatusAlerts(t *testing.T) {
	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     2048,
		MaxOfflineMessages: 32}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	type downloadAlert struct {
		source          string
		message         string
		progress        string
		url             string
		downloadedBytes string
		totalBytes      string
	}

	downloadStatus := alerts.DownloadAlertStatus{
		Source:     "downloader",
		URL:        "https://testurl",
		TotalBytes: 157_286_400} // 150 MB in binary format

	// send download started alert status
	originAlert := downloadAlert{
		source:          "downloader",
		message:         "Download started",
		progress:        "0%",
		url:             downloadStatus.URL,
		downloadedBytes: "0B",
		totalBytes:      "150M"}

	proccessAlertFunc := func(alert amqp.AlertItem) (success bool, err error) {
		if alert.Tag != amqp.AlertTagAosCore {
			return false, nil
		}

		receivedAlert, ok := (alert.Payload.(amqp.DownloadAlert))
		if !ok {
			return false, errors.New("wrong alert type")
		}

		receivedItem := downloadAlert{
			source:          alert.Source,
			message:         receivedAlert.Message,
			progress:        receivedAlert.Progress,
			url:             receivedAlert.URL,
			downloadedBytes: receivedAlert.DownloadedBytes,
			totalBytes:      receivedAlert.TotalBytes}

		if receivedItem == originAlert {
			return true, nil
		}
		return false, nil
	}

	alertsHandler.SendDownloadStartedStatusAlert(downloadStatus)

	if err = waitResult(alertsHandler.AlertsChannel, 5*time.Second,
		proccessAlertFunc); err != nil {
		t.Errorf("Result failed: %s", err)
	}

	// send download interrupted alert status
	reason := "problem with Internet connection"
	downloadStatus.DownloadedBytes = 15_728_640 // 15 MB
	downloadStatus.Progress = 10

	originAlert = downloadAlert{
		source:          "downloader",
		message:         "Download interrupted reason: " + reason,
		progress:        strconv.Itoa(downloadStatus.Progress) + "%",
		url:             downloadStatus.URL,
		downloadedBytes: "15M",
		totalBytes:      "150M"}

	alertsHandler.SendDownloadInterruptedStatusAlert(downloadStatus, reason)

	if err = waitResult(alertsHandler.AlertsChannel, 5*time.Second,
		proccessAlertFunc); err != nil {
		t.Errorf("Result failed: %s", err)
	}

	// send download resume alert status
	reason = "Internet connection has been fixed"
	originAlert.message = "Download resumed reason: " + reason
	alertsHandler.SendDownloadResumedStatusAlert(downloadStatus, reason)

	if err = waitResult(alertsHandler.AlertsChannel, 5*time.Second,
		proccessAlertFunc); err != nil {
		t.Errorf("Result failed: %s", err)
	}

	// send status download alert
	downloadStatus.DownloadedBytes = 31_457_280 // 30 MB
	downloadStatus.Progress = 20

	originAlert = downloadAlert{
		source:          "downloader",
		message:         "Download status",
		progress:        strconv.Itoa(downloadStatus.Progress) + "%",
		url:             downloadStatus.URL,
		downloadedBytes: "30M",
		totalBytes:      "150M"}

	alertsHandler.SendDownloadStatusAlert(downloadStatus)

	if err = waitResult(alertsHandler.AlertsChannel, 5*time.Second,
		proccessAlertFunc); err != nil {
		t.Errorf("Result failed: %s", err)
	}

	// send success download finished alert status
	downloadCode := 200
	downloadStatus.DownloadedBytes = 157_286_400 // 150 MB
	downloadStatus.Progress = 100

	originAlert = downloadAlert{
		source:          "downloader",
		message:         "Download finished code: " + strconv.Itoa(downloadCode),
		progress:        strconv.Itoa(downloadStatus.Progress) + "%",
		url:             downloadStatus.URL,
		downloadedBytes: "150M",
		totalBytes:      "150M"}

	alertsHandler.SendDownloadFinishedStatusAlert(downloadStatus, downloadCode)

	if err = waitResult(alertsHandler.AlertsChannel, 5*time.Second,
		proccessAlertFunc); err != nil {
		t.Errorf("Result failed: %s", err)
	}

	// send failed download finished alert status
	downloadCode = 301
	downloadStatus.DownloadedBytes = 52_428_800 // 50 MB
	downloadStatus.Progress = 50

	originAlert = downloadAlert{
		source:          "downloader",
		message:         "Download finished code: " + strconv.Itoa(downloadCode),
		progress:        strconv.Itoa(downloadStatus.Progress) + "%",
		url:             downloadStatus.URL,
		downloadedBytes: "50M",
		totalBytes:      "150M"}

	alertsHandler.SendDownloadFinishedStatusAlert(downloadStatus, downloadCode)

	if err = waitResult(alertsHandler.AlertsChannel, 5*time.Second,
		proccessAlertFunc); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}

func TestGetServiceManagerAlerts(t *testing.T) {
	const numMessages = 5

	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 32}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	messages := make([]string, 0, numMessages)

	for i := 0; i < numMessages; i++ {
		messages = append(messages, uuid.New().String())
		log.Error(messages[len(messages)-1])
	}

	if err = waitAlerts(alertsHandler.AlertsChannel, 5*time.Second, amqp.AlertTagAosCore, "servicemanager", nil, messages); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}

func TestAlertsMaxMessageSize(t *testing.T) {
	const numMessages = 5
	// the size of one message ~154 bytes:
	// {"timestamp":"2019-03-28T16:54:58.500221705+02:00","tag":"aosCore","source":"servicemanager","payload":{"message":"884a0472-5ce3-4da6-acff-088ce3959cd3"}}
	// Set MaxMessageSize to 500 to allow only 3 messages to come
	const numExpectedMessages = 3

	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     500,
		MaxOfflineMessages: 32}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	for i := 0; i < numMessages; i++ {
		// One message size is: timestamp 24 + tag "aosCore" 7 + source "servicemanager" 14 + uuid 36 = 81
		log.Error(uuid.New().String())
	}

	select {
	case alerts := <-alertsHandler.AlertsChannel:
		if len(alerts) != numExpectedMessages {
			t.Errorf("Wrong message count received: %d", len(alerts))
		}

	case <-time.After(5 * time.Second):
		t.Errorf("Result failed: %s", errTimeout)
	}
}

func TestAlertsMaxOfflineMessages(t *testing.T) {
	const numMessages = 5

	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 3}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	messages := make([]string, 0, numMessages)

	for i := 0; i < numMessages; i++ {
		messages = append(messages, uuid.New().String())
		log.Error(messages[len(messages)-1])
		time.Sleep(1500 * time.Millisecond)
	}

	messageCount := 0

	for {
		select {
		case <-alertsHandler.AlertsChannel:
			messageCount++

		case <-time.After(1 * time.Second):
			if messageCount != 3 {
				t.Errorf("Wrong message count received: %d", messageCount)
			}
			return
		}
	}
}

func TestDuplicateAlerts(t *testing.T) {
	const numMessages = 5

	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 25}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	messages := make([]string, 0, numMessages)

	for i := 0; i < numMessages; i++ {
		messages = append(messages, "This is error message")
		log.Error(messages[len(messages)-1])
	}

	select {
	case alerts := <-alertsHandler.AlertsChannel:
		if len(alerts) != 1 {
			t.Errorf("Wrong message count received: %d", len(alerts))
		}

	case <-time.After(5 * time.Second):
		t.Errorf("Result failed: %s", errTimeout)
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

func (serviceProvider *testServiceProvider) GetServiceByUnitName(unitName string) (service launcher.Service, err error) {
	for _, s := range serviceProvider.services {
		if s.UnitName == unitName {
			return *s, nil
		}
	}

	return service, fmt.Errorf("service with unit %s does not exist", unitName)
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

func waitResult(alertsChannel <-chan amqp.Alerts, timeout time.Duration, checkAlert func(alert amqp.AlertItem) (success bool, err error)) (err error) {
	for {
		select {
		case alerts := <-alertsChannel:
			for _, alert := range alerts {
				success, err := checkAlert(alert)
				if err != nil {
					return err
				}

				if success {
					return nil
				}
			}

		case <-time.After(timeout):
			return errTimeout
		}
	}
}

func waitAlerts(alertsChannel <-chan amqp.Alerts, timeout time.Duration, tag, source string, version *uint64, data []string) (err error) {
	return waitResult(alertsChannel, timeout, func(alert amqp.AlertItem) (success bool, err error) {
		if alert.Tag != tag {
			return false, nil
		}

		systemAlert, ok := (alert.Payload.(amqp.SystemAlert))
		if !ok {
			return false, errors.New("wrong alert type")
		}

		for i, message := range data {
			if systemAlert.Message == message {
				data = append(data[:i], data[i+1:]...)

				if alert.Source != source {
					return false, fmt.Errorf("wrong alert source: %s", alert.Source)
				}

				if !reflect.DeepEqual(alert.Version, version) {
					if alert.Version != nil {
						return false, fmt.Errorf("wrong alert version: %d", *alert.Version)
					}

					return false, errors.New("version field missing")
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

func TestMessageFilter(t *testing.T) {
	const numMessages = 3

	filter := []string{"test", "regexp"}

	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 32,
		Filter:             filter}}, &serviceProvider, &cursorStorage)
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

	for i := 0; i < 3; i++ {
		err := waitResult(alertsHandler.AlertsChannel, 1*time.Second,
			func(alert amqp.AlertItem) (success bool, err error) {
				systemAlert, ok := (alert.Payload.(amqp.SystemAlert))
				if !ok {
					return false, errors.New("wrong alert type")
				}

				if systemAlert.Message != validMessage {
					return false, errors.New("Receive unexpected alert mesage")
				}
				return true, nil
			})

		if err == nil {
			foundCount++
			continue
		}

		if err != errTimeout {
			t.Errorf("Result failed: %s", err)
		}
	}

	if foundCount != 1 {
		t.Errorf("Incorrect count of received alerts count = %d", foundCount)
	}
}

func TestWrongFilter(t *testing.T) {
	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{
		SendPeriod:         config.Duration{Duration: 1 * time.Second},
		MaxMessageSize:     1024,
		MaxOfflineMessages: 32,
		Filter:             []string{"", "*(test)^"}}}, &serviceProvider, &cursorStorage)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()
}

func compareValidateAlerts(first validateAlert, second validateAlert) (result bool) {
	if first.source != second.source {
		return false
	}

	return reflect.DeepEqual(first.message, second.message)
}
