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

// Package alerts provides set of API to send system and services alerts
package alerts

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/servicemanager/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"aos_servicemanager/config"
	"aos_servicemanager/launcher"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	AlertTagSystemError = "systemError"
	AlertTagAosCore     = "aosCore"
	AlertTagResource    = "resourceAlert"
	AlertDeviceErrors   = "deviceErrors"
)

const (
	waitJournalTimeout = 1 * time.Second
	journalSavePeriod  = 10 * time.Second
)

const alertChannelSize = 50

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
	alertsChannel   chan *pb.Alert
	config          config.Alerts
	cursorStorage   CursorStorage
	serviceProvider ServiceProvider
	filterRegexp    []*regexp.Regexp

	sync.Mutex

	journal      *sdjournal.Journal
	ticker       *time.Ticker
	closeChannel chan bool
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
	"aos-communicationmanager.service",
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new alerts object
func New(config *config.Config, serviceProvider ServiceProvider,
	cursorStorage CursorStorage) (instance *Alerts, err error) {
	log.Debug("New alerts")

	if config.Alerts.Disabled {
		return nil, ErrDisabled
	}

	instance = &Alerts{config: config.Alerts, cursorStorage: cursorStorage,
		serviceProvider: serviceProvider}

	instance.alertsChannel = make(chan *pb.Alert, alertChannelSize)

	instance.closeChannel = make(chan bool)

	instance.ticker = time.NewTicker(journalSavePeriod)

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
		return nil, aoserrors.Wrap(err)
	}

	return instance, nil
}

// Close closes logging
func (instance *Alerts) Close() {
	log.Debug("Close Alerts")

	instance.closeChannel <- true

	instance.ticker.Stop()

	instance.storeCurrentCursor()

	instance.journal.Close()
}

//GetAlertsChannel returns channel with alerts to be sent
func (instance *Alerts) GetAlertsChannel() (channel <-chan *pb.Alert) {
	return instance.alertsChannel
}

// SendValidateResourceAlert sends request/releases resource alert
func (instance *Alerts) SendValidateResourceAlert(source string, errors map[string][]error) {
	time := time.Now()

	log.WithFields(log.Fields{
		"timestamp": time,
		"source":    source,
		"errors":    errors}).Debug("Validate Resource alert")

	var convertedErrors []*pb.ResourceValidateErrors

	for name, reason := range errors {
		var messages []string

		for _, item := range reason {
			messages = append(messages, item.Error())
		}

		resourceError := pb.ResourceValidateErrors{
			Name:     name,
			ErrorMsg: messages}

		convertedErrors = append(convertedErrors, &resourceError)
	}

	alert := pb.Alert{
		Timestamp: timestamppb.New(time),
		Tag:       AlertTagAosCore,
		Source:    source,
		Payload: &pb.Alert_ResourceValidateAlert{
			ResourceValidateAlert: &pb.ResourceValidateAlert{
				Type:   AlertDeviceErrors,
				Errors: convertedErrors}},
	}

	instance.pushAlert(&alert)
}

// SendResourceAlert sends resource alert
func (instance *Alerts) SendResourceAlert(source, resource string, time time.Time, value uint64) {
	log.WithFields(log.Fields{
		"timestamp": time,
		"source":    source,
		"resource":  resource,
		"value":     value}).Debug("Resource alert")

	alert := pb.Alert{
		Timestamp: timestamppb.New(time),
		Tag:       AlertTagResource,
		Source:    source,
		Payload: &pb.Alert_ResourceAlert{ResourceAlert: &pb.ResourceAlert{
			Parameter: resource,
			Value:     value}},
	}

	instance.pushAlert(&alert)
}

//SendRequestResourceAlert send request resource alert
func (instance *Alerts) SendRequestResourceAlert(source string, message string) {
	instance.pushAlert(&pb.Alert{
		Timestamp: timestamppb.Now(),
		Tag:       AlertTagAosCore,
		Source:    source,
		Payload: &pb.Alert_SystemAlert{
			SystemAlert: &pb.SystemAlert{
				Message: message}}})
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (instance *Alerts) setupJournal() (err error) {
	if instance.journal, err = sdjournal.NewJournal(); err != nil {
		return aoserrors.Wrap(err)
	}

	for priorityLevel := 0; priorityLevel <= instance.config.SystemAlertPriority; priorityLevel++ {
		if err = instance.journal.AddMatch(fmt.Sprintf("PRIORITY=%d", priorityLevel)); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if err = instance.journal.AddDisjunction(); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = instance.journal.AddMatch("_SYSTEMD_UNIT=init.scope"); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = instance.journal.SeekTail(); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = instance.journal.Previous(); err != nil {
		return aoserrors.Wrap(err)
	}

	cursor, err := instance.cursorStorage.GetJournalCursor()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if cursor != "" {
		if err = instance.journal.SeekCursor(cursor); err != nil {
			return aoserrors.Wrap(err)
		}

		if _, err = instance.journal.Next(); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	go func() {
		result := sdjournal.SD_JOURNAL_APPEND

		for {
			select {
			case <-instance.ticker.C:
				if err = instance.storeCurrentCursor(); err != nil {
					log.Error("Can't store journal cursor: ", err)
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
			return aoserrors.Wrap(err)
		}

		if count == 0 {
			return nil
		}

		entry, err := instance.journal.GetEntry()
		if err != nil {
			return aoserrors.Wrap(err)
		}

		var version uint64
		source := "system"
		tag := AlertTagSystemError
		unit := entry.Fields[sdjournal.SD_JOURNAL_FIELD_SYSTEMD_UNIT]

		if unit == "init.scope" {
			if priority, err := strconv.Atoi(entry.Fields[sdjournal.SD_JOURNAL_FIELD_PRIORITY]); err != nil || priority > instance.config.ServiceAlertPriority {
				continue
			}

			unit = entry.Fields["UNIT"]
		}

		if strings.HasPrefix(unit, "aos") {
			service, err := instance.serviceProvider.GetServiceByUnitName(unit)
			if err == nil {
				source = service.ID
				version = service.AosVersion
			} else {
				for _, aosService := range aosServices {
					if unit == aosService {
						source = unit
						tag = AlertTagAosCore
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

			instance.pushAlert(&pb.Alert{
				Timestamp:  timestamppb.New(t),
				Tag:        tag,
				Source:     source,
				AosVersion: version,
				Payload: &pb.Alert_SystemAlert{
					SystemAlert: &pb.SystemAlert{
						Message: entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE]}}})
		}
	}
}

func (instance *Alerts) storeCurrentCursor() (err error) {
	cursor, err := instance.journal.GetCursor()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = instance.cursorStorage.SetJournalCursor(cursor); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (instance *Alerts) pushAlert(alert *pb.Alert) {
	if len(instance.alertsChannel) >= cap(instance.alertsChannel) {
		log.Warn("Skip alert, channel is full")

		return
	}

	instance.alertsChannel <- alert
}
