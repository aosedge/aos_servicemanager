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

// Package logging provides set of API to retrieve system and services log
package logging

import (
	"fmt"
	"strings"
	"time"

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
	logChannelSize = 32

	serviceField = "_SYSTEMD_UNIT"
	unitField    = "UNIT"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// ServiceProvider provides service info
type ServiceProvider interface {
	GetService(serviceID string) (service launcher.Service, err error)
}

// Logging instance
type Logging struct {
	LogChannel chan amqp.PushServiceLog

	serviceProvider ServiceProvider
	config          config.Logging
}

type getLogRequest struct {
	serviceID string
	logID     string
	from      *time.Time
	till      *time.Time
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new logging object
func New(config *config.Config, serviceProvider ServiceProvider) (instance *Logging, err error) {
	log.Debug("New logging")

	instance = &Logging{serviceProvider: serviceProvider, config: config.Logging}

	instance.LogChannel = make(chan amqp.PushServiceLog, logChannelSize)

	return instance, nil
}

// Close closes logging
func (instance *Logging) Close() {
	log.Debug("Close logging")
}

// GetServiceLog returns service log
func (instance *Logging) GetServiceLog(request amqp.RequestServiceLog) {
	log.WithFields(log.Fields{
		"serviceID": request.ServiceID,
		"logID":     request.LogID,
		"dateFrom":  request.From,
		"dateTill":  request.Till}).Debug("Get service log")

	logRequest := getLogRequest{
		serviceID: request.ServiceID,
		logID:     request.LogID,
		from:      request.From,
		till:      request.Till,
	}

	go instance.getLog(logRequest)
}

// GetServiceCrashLog returns service crash log
func (instance *Logging) GetServiceCrashLog(request amqp.RequestServiceCrashLog) {
	log.WithFields(log.Fields{
		"serviceID": request.ServiceID,
		"logID":     request.LogID}).Debug("Get service crash log")

	go instance.getServiceCrashLog(request)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (instance *Logging) getLog(request getLogRequest) {
	var err error

	// error handling
	defer func() {
		if r := recover(); r != nil {
			errorStr := fmt.Sprintf("%s: %s", r, err)

			log.Error(errorStr)

			instance.sendErrorResponse(errorStr, request.logID)
		}
	}()

	var journal *sdjournal.Journal

	journal, err = sdjournal.NewJournal()
	if err != nil {
		panic("Can't open sd journal")
	}
	defer journal.Close()

	if _, err = instance.addServiceIDFilter(journal, serviceField, request.serviceID); err != nil {
		panic("Can't add filter")
	}

	if err = instance.seekToTime(journal, request.from); err != nil {
		panic("Can't seek log")
	}

	var tillRealtime uint64

	if request.till != nil {
		tillRealtime = uint64(request.till.UnixNano() / 1000)
	}

	var archInstance *archivator

	if archInstance, err = newArchivator(instance.LogChannel,
		instance.config.MaxPartSize,
		instance.config.MaxPartCount); err != nil {
		panic("Can't create archivator")
	}

	for {
		var rowCount uint64

		if rowCount, err = journal.Next(); err != nil {
			panic("Can't seek log")
		}

		// end of log
		if rowCount == 0 {
			break
		}

		var logEntry *sdjournal.JournalEntry

		if logEntry, err = journal.GetEntry(); err != nil {
			panic("Can't get entry")
		}

		// till time reached
		if tillRealtime != 0 && logEntry.RealtimeTimestamp > tillRealtime {
			break
		}

		if err = archInstance.addLog(logEntry.Fields["MESSAGE"] + "\n"); err != nil {
			if err == errMaxPartCount {
				log.Warn(err)
				break
			}

			panic("Can't archive log")
		}
	}

	if err = archInstance.sendLog(request.logID); err != nil {
		panic("Can't send log")
	}
}

func (instance *Logging) getServiceCrashLog(request amqp.RequestServiceCrashLog) {
	var err error

	// error handling
	defer func() {
		if r := recover(); r != nil {
			errorStr := fmt.Sprintf("%s: %s", r, err)

			log.Error(errorStr)

			instance.sendErrorResponse(errorStr, request.LogID)
		}
	}()

	var journal *sdjournal.Journal

	journal, err = sdjournal.NewJournal()
	if err != nil {
		panic("Can't open sd journal")
	}
	defer journal.Close()

	if _, err = instance.addServiceIDFilter(journal, unitField, request.ServiceID); err != nil {
		panic("Can't add filter")
	}

	if err = journal.SeekTail(); err != nil {
		panic("Can't seek log")
	}

	var crashTime uint64

	for {
		var rowCount uint64

		if rowCount, err = journal.Previous(); err != nil {
			panic("Can't seek log")
		}

		// end of log
		if rowCount == 0 {
			break
		}

		var logEntry *sdjournal.JournalEntry

		if logEntry, err = journal.GetEntry(); err != nil {
			panic("Can't get entry")
		}

		if crashTime == 0 {
			if strings.Contains(logEntry.Fields["MESSAGE"], "process exited") {
				crashTime = logEntry.MonotonicTimestamp

				log.WithFields(log.Fields{
					"serviceID": request.ServiceID,
					"time": time.Unix(int64(logEntry.RealtimeTimestamp/1000000),
						int64((logEntry.RealtimeTimestamp%1000000))*1000)}).Debug("Crash detected")
			}
		} else {
			if strings.HasPrefix(logEntry.Fields["MESSAGE"], "Started") {
				break
			}
		}
	}

	var archInstance *archivator

	if archInstance, err = newArchivator(instance.LogChannel,
		instance.config.MaxPartSize,
		instance.config.MaxPartCount); err != nil {
		panic("Can't create archivator")
	}

	if crashTime > 0 {
		if err = journal.AddDisjunction(); err != nil {
			panic("Can't add filter")
		}

		var unitName string

		unitName, err = instance.addServiceIDFilter(journal, serviceField, request.ServiceID)
		if err != nil {
			panic("Can't add filter")
		}

		for {
			var rowCount uint64

			if rowCount, err = journal.Next(); err != nil {
				panic("Can't seek log")
			}

			// end of log
			if rowCount == 0 {
				break
			}

			var logEntry *sdjournal.JournalEntry

			if logEntry, err = journal.GetEntry(); err != nil {
				panic("Can't get entry")
			}

			if logEntry.MonotonicTimestamp > crashTime {
				break
			}

			if serviceName, ok := logEntry.Fields[serviceField]; ok && unitName == serviceName {
				if err = archInstance.addLog(logEntry.Fields["MESSAGE"] + "\n"); err != nil {
					panic("Can't archive log")
				}
			}
		}
	}

	if err = archInstance.sendLog(request.LogID); err != nil {
		panic("Can't send log")
	}
}

func (instance *Logging) sendErrorResponse(errorStr, logID string) {
	response := amqp.PushServiceLog{
		LogID: logID,
		Error: &errorStr}

	instance.LogChannel <- response
}

func (instance *Logging) addServiceIDFilter(journal *sdjournal.Journal,
	fieldName, serviceID string) (unitName string, err error) {
	if serviceID == "" {
		return unitName, nil
	}

	service, err := instance.serviceProvider.GetService(serviceID)
	if err != nil {
		return unitName, nil
	}

	if err = journal.AddMatch(fieldName + "=" + service.UnitName); err != nil {
		return unitName, err
	}

	return service.UnitName, nil
}

func (instance *Logging) seekToTime(journal *sdjournal.Journal, from *time.Time) (err error) {
	if from != nil {
		return journal.SeekRealtimeUsec(uint64(from.UnixNano() / 1000))
	}

	return journal.SeekHead()
}
