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

// Package alerts provides set of API to send system and services alerts
package alerts

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/coreos/go-systemd/v22/sdjournal"
	log "github.com/sirupsen/logrus"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/launcher"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	alertsDataAllocSize = 10
	waitJournalTimeout  = 1 * time.Second
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// ServiceProvider provides service info
type ServiceProvider interface {
	GetService(serviceID string) (service launcher.Service, err error)
	GetServiceByUnitName(unitName string) (service launcher.Service, err error)
}

// CursorStorage provides API to set and get journal cursor
type CursorStorage interface {
	SetJournalCursor(cursor string) (err error)
	GetJournalCursor() (cursor string, err error)
}

// Alerts instance
type Alerts struct {
	AlertsChannel chan amqp.Alerts

	config          config.Alerts
	cursorStorage   CursorStorage
	serviceProvider ServiceProvider
	filterRegexp    []*regexp.Regexp

	alertsSize       int
	skippedAlerts    uint32
	duplicatedAlerts uint32
	alerts           amqp.Alerts

	sync.Mutex

	journal      *sdjournal.Journal
	ticker       *time.Ticker
	closeChannel chan bool
}

// DownloadAlertStatus instance
type DownloadAlertStatus struct {
	Source          string
	URL             string
	Progress        int
	DownloadedBytes uint64
	TotalBytes      uint64
}

/*******************************************************************************
 * Variable
 ******************************************************************************/

// ErrDisabled indicates that alerts is disable in the config
var ErrDisabled = errors.New("alerts is disabled")

var aosServices = []string{
	"aos-servicemanager.service",
	"aos-updatemanager.service",
	"aos-iamanager.service",
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new alerts object
func New(config *config.Config,
	serviceProvider ServiceProvider,
	cursorStorage CursorStorage) (instance *Alerts, err error) {
	log.Debug("New alerts")

	if config.Alerts.Disabled {
		return nil, ErrDisabled
	}

	instance = &Alerts{config: config.Alerts, cursorStorage: cursorStorage, serviceProvider: serviceProvider}

	instance.AlertsChannel = make(chan amqp.Alerts, instance.config.MaxOfflineMessages)
	instance.closeChannel = make(chan bool)

	instance.ticker = time.NewTicker(instance.config.SendPeriod.Duration)

	instance.alerts = make([]amqp.AlertItem, 0, alertsDataAllocSize)

	for _, substr := range instance.config.Filter {
		if len(substr) == 0 {
			log.Warning("Filter value has an empty string")
			continue
		}

		tmpRegexp, err := regexp.Compile(substr)
		if err != nil {
			log.Errorf("Regexp compile error. Incorrect regexp: %s, error is: %s", substr, err)
			continue
		}

		instance.filterRegexp = append(instance.filterRegexp, tmpRegexp)
	}

	if err = instance.setupJournal(); err != nil {
		return nil, err
	}

	return instance, nil
}

// Close closes logging
func (instance *Alerts) Close() {
	log.Debug("Close Alerts")

	instance.closeChannel <- true

	instance.ticker.Stop()

	instance.journal.Close()
}

// SendResourceAlert sends resource alert
func (instance *Alerts) SendResourceAlert(source, resource string, time time.Time, value uint64) {
	log.WithFields(log.Fields{
		"timestamp": time,
		"source":    source,
		"resource":  resource,
		"value":     value}).Debug("Resource alert")

	var version *uint64

	if service, err := instance.serviceProvider.GetService(source); err == nil {
		version = &service.AosVersion
	}

	instance.addAlert(amqp.AlertItem{
		Timestamp:  time,
		Tag:        amqp.AlertTagResource,
		Source:     source,
		AosVersion: version,
		Payload: amqp.ResourceAlert{
			Parameter: resource,
			Value:     value}})
}

// SendValidateResourceAlert sends request/releases resource alert
func (instance *Alerts) SendValidateResourceAlert(source string, errors map[string][]error) {
	time := time.Now()

	log.WithFields(log.Fields{
		"timestamp": time,
		"source":    source,
		"errors":    errors}).Debug("Validate Resource alert")

	var convertedErrors []amqp.ResourceValidateErrors

	for name, reason := range errors {
		var messages []string

		for _, item := range reason {
			messages = append(messages, item.Error())
		}

		err := amqp.ResourceValidateErrors{
			Name:   name,
			Errors: messages}

		convertedErrors = append(convertedErrors, err)
	}

	instance.addAlert(amqp.AlertItem{
		Timestamp: time,
		Tag:       amqp.AlertTagAosCore,
		Source:    source,
		Payload: amqp.ResourseValidatePayload{
			Type:   amqp.DeviceErrors,
			Errors: convertedErrors}})
}

// SendRequestResourceAlert sends request resource alert
func (instance *Alerts) SendRequestResourceAlert(source string, message string) {
	time := time.Now()

	log.WithFields(log.Fields{
		"timestamp": time,
		"source":    source,
		"error":     message}).Debug("Request Resource alert")

	var version *uint64

	if service, err := instance.serviceProvider.GetService(source); err == nil {
		version = &service.AosVersion
	}

	instance.addAlert(amqp.AlertItem{
		Timestamp:  time,
		Tag:        amqp.AlertTagAosCore,
		Source:     source,
		AosVersion: version,
		Payload: amqp.SystemAlert{
			Message: message}})
}

// SendDownloadStartedStatusAlert sends download started status alert
func (instance *Alerts) SendDownloadStartedStatusAlert(downloadStatus DownloadAlertStatus) {
	message := "Download started"
	payload := instance.prepareDownloadAlert(downloadStatus, message)

	instance.sendDownloadStatusAlertMessage(downloadStatus.Source, payload)
}

// SendDownloadFinishedStatusAlert sends download finished status alert
func (instance *Alerts) SendDownloadFinishedStatusAlert(downloadStatus DownloadAlertStatus, code int) {
	message := "Download finished code: " + strconv.Itoa(code)
	payload := instance.prepareDownloadAlert(downloadStatus, message)

	instance.sendDownloadStatusAlertMessage(downloadStatus.Source, payload)
}

// SendDownloadInterruptedStatusAlert sends download interrupted status alert
func (instance *Alerts) SendDownloadInterruptedStatusAlert(downloadStatus DownloadAlertStatus, reason string) {
	message := "Download interrupted reason: " + reason
	payload := instance.prepareDownloadAlert(downloadStatus, message)

	instance.sendDownloadStatusAlertMessage(downloadStatus.Source, payload)
}

// SendDownloadResumedStatusAlert sends download resumed status alert
func (instance *Alerts) SendDownloadResumedStatusAlert(downloadStatus DownloadAlertStatus, reason string) {
	message := "Download resumed reason: " + reason
	payload := instance.prepareDownloadAlert(downloadStatus, message)

	instance.sendDownloadStatusAlertMessage(downloadStatus.Source, payload)
}

// SendDownloadStatusAlert sends download status alert
func (instance *Alerts) SendDownloadStatusAlert(downloadStatus DownloadAlertStatus) {
	message := "Download status"
	payload := instance.prepareDownloadAlert(downloadStatus, message)

	instance.sendDownloadStatusAlertMessage(downloadStatus.Source, payload)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (instance *Alerts) prepareDownloadAlert(downloadStatus DownloadAlertStatus, message string) amqp.DownloadAlert {
	payload := amqp.DownloadAlert{
		Message:         message,
		Progress:        strconv.Itoa(downloadStatus.Progress) + "%",
		URL:             downloadStatus.URL,
		DownloadedBytes: bytefmt.ByteSize(downloadStatus.DownloadedBytes),
		TotalBytes:      bytefmt.ByteSize(downloadStatus.TotalBytes)}

	return payload
}

func (instance *Alerts) sendDownloadStatusAlertMessage(source string, payload amqp.DownloadAlert) {
	time := time.Now()

	log.WithFields(log.Fields{
		"timestamp":       time,
		"source":          source,
		"download status": payload.Message,
		"progress":        payload.Progress,
		"url":             payload.URL,
		"downloadedBytes": payload.DownloadedBytes,
		"totalBytes":      payload.TotalBytes}).Debug(payload.Message)

	instance.addAlert(amqp.AlertItem{
		Timestamp: time,
		Tag:       amqp.AlertTagAosCore,
		Source:    source,
		Payload:   payload})
}

func (instance *Alerts) setupJournal() (err error) {
	if instance.journal, err = sdjournal.NewJournal(); err != nil {
		return err
	}

	if err = instance.journal.AddMatch("PRIORITY=0"); err != nil {
		return err
	}

	if err = instance.journal.AddMatch("PRIORITY=1"); err != nil {
		return err
	}

	if err = instance.journal.AddMatch("PRIORITY=2"); err != nil {
		return err
	}

	if err = instance.journal.AddMatch("PRIORITY=3"); err != nil {
		return err
	}

	if err = instance.journal.AddDisjunction(); err != nil {
		return err
	}

	if err = instance.journal.AddMatch("_SYSTEMD_UNIT=init.scope"); err != nil {
		return err
	}

	if err = instance.journal.SeekTail(); err != nil {
		return err
	}

	if _, err = instance.journal.Previous(); err != nil {
		return err
	}

	cursor, err := instance.cursorStorage.GetJournalCursor()
	if err != nil {
		return err
	}

	if cursor != "" {
		if err = instance.journal.SeekCursor(cursor); err != nil {
			return err
		}

		if _, err = instance.journal.Next(); err != nil {
			return err
		}
	}

	go func() {
		result := sdjournal.SD_JOURNAL_APPEND

		for {
			select {
			case <-instance.ticker.C:
				if err = instance.sendAlerts(); err != nil {
					log.Errorf("Send alerts error: %s", err)
				}

			case <-instance.closeChannel:
				return

			default:
				if result != sdjournal.SD_JOURNAL_NOP {
					if err = instance.processJournal(); err != nil {
						log.Errorf("Journal process error: %s", err)
					}
				}

				if result = instance.journal.Wait(waitJournalTimeout); result < 0 {
					log.Errorf("Wait journal error: %s", syscall.Errno(-result))
				}
			}
		}
	}()

	return nil
}

func (instance *Alerts) processJournal() (err error) {
	for {
		count, err := instance.journal.Next()
		if err != nil {
			return err
		}

		if count == 0 {
			return nil
		}

		entry, err := instance.journal.GetEntry()
		if err != nil {
			return err
		}

		var version *uint64
		source := "system"
		tag := amqp.AlertTagSystemError
		unit := entry.Fields[sdjournal.SD_JOURNAL_FIELD_SYSTEMD_UNIT]

		if unit == "init.scope" {
			if priority, err := strconv.Atoi(entry.Fields[sdjournal.SD_JOURNAL_FIELD_PRIORITY]); err != nil || priority > 4 {
				continue
			}

			unit = entry.Fields["UNIT"]
		}

		if strings.HasPrefix(unit, "aos") {
			service, err := instance.serviceProvider.GetServiceByUnitName(unit)
			if err == nil {
				source = service.ID
				version = &service.AosVersion
			} else {
				for _, aosService := range aosServices {
					if unit == aosService {
						source = unit
						tag = amqp.AlertTagAosCore
					}
				}
			}
		}

		t := time.Unix(int64(entry.RealtimeTimestamp/1000000),
			int64((entry.RealtimeTimestamp%1000000)*1000))

		skipsend := false

		for _, substr := range instance.filterRegexp {
			skipsend = substr.MatchString(entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE])

			if skipsend {
				break
			}
		}

		if !skipsend {
			log.WithFields(log.Fields{
				"time":    t,
				"message": entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE],
				"tag":     tag,
				"source":  source,
			}).Debug("System alert")

			instance.addAlert(amqp.AlertItem{
				Timestamp:  t,
				Tag:        tag,
				Source:     source,
				AosVersion: version,
				Payload:    amqp.SystemAlert{Message: entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE]}})
		}
	}
}

func (instance *Alerts) addAlert(item amqp.AlertItem) {
	instance.Lock()
	defer instance.Unlock()

	if len(instance.alerts) != 0 &&
		reflect.DeepEqual(instance.alerts[len(instance.alerts)-1].Payload, item.Payload) {
		instance.duplicatedAlerts++
		return
	}

	data, _ := json.Marshal(item)
	instance.alertsSize += len(data)

	if int(instance.alertsSize) <= instance.config.MaxMessageSize {
		instance.alerts = append(instance.alerts, item)
	} else {
		instance.skippedAlerts++
	}
}

func (instance *Alerts) sendAlerts() (err error) {
	instance.Lock()
	defer instance.Unlock()

	if instance.alertsSize != 0 {
		if len(instance.AlertsChannel) < cap(instance.AlertsChannel) {
			instance.AlertsChannel <- instance.alerts

			if instance.skippedAlerts != 0 {
				log.WithField("count", instance.skippedAlerts).Warn("Alerts skipped due to size limit")
			}
			if instance.duplicatedAlerts != 0 {
				log.WithField("count", instance.duplicatedAlerts).Warn("Alerts skipped due to duplication")
			}
		} else {
			log.Warn("Skip sending alerts due to channel is full")
		}

		instance.alerts = make([]amqp.AlertItem, 0, alertsDataAllocSize)
		instance.skippedAlerts = 0
		instance.duplicatedAlerts = 0
		instance.alertsSize = 0

		if err = instance.storeCurrentCursor(); err != nil {
			return err
		}
	}

	return nil
}

func (instance *Alerts) storeCurrentCursor() (err error) {
	cursor, err := instance.journal.GetCursor()
	if err != nil {
		return err
	}

	if err = instance.cursorStorage.SetJournalCursor(cursor); err != nil {
		return err
	}

	return nil
}
