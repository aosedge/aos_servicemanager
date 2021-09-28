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
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/servicemanager/v1"

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
	logChannel chan *pb.LogData

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

	instance.logChannel = make(chan *pb.LogData, logChannelSize)

	return instance, nil
}

// Close closes logging
func (instance *Logging) Close() {
	log.Debug("Close logging")
}

// GetServiceLog returns service log
func (instance *Logging) GetServiceLog(request *pb.ServiceLogRequest) {
	log.WithFields(log.Fields{
		"serviceID": request.ServiceId,
		"logID":     request.LogId,
		"dateFrom":  request.From,
		"dateTill":  request.Till}).Debug("Get service log")

	logRequest := getLogRequest{
		serviceID: request.ServiceId,
		logID:     request.LogId,
	}

	if request.From != nil {
		localTime := request.GetFrom().AsTime()
		logRequest.from = &localTime
	}

	if request.Till != nil {
		localTime := request.GetTill().AsTime()
		logRequest.till = &localTime
	}

	go instance.getLog(logRequest)
}

// GetServiceCrashLog returns service crash log
func (instance *Logging) GetServiceCrashLog(request *pb.ServiceLogRequest) {
	log.WithFields(log.Fields{
		"serviceID": request.ServiceId,
		"logID":     request.LogId}).Debug("Get service crash log")

	logRequest := getLogRequest{
		serviceID: request.ServiceId,
		logID:     request.LogId,
	}

	if request.From != nil {
		localTime := request.GetFrom().AsTime()
		logRequest.from = &localTime
	}

	if request.Till != nil {
		localTime := request.GetTill().AsTime()
		logRequest.till = &localTime
	}

	go instance.getServiceCrashLog(logRequest)
}

// GetSystemLog returns system log
func (instance *Logging) GetSystemLog(request *pb.SystemLogRequest) {
	log.WithFields(log.Fields{
		"logID":    request.LogId,
		"dateFrom": request.From,
		"dateTill": request.Till}).Debug("Get system log")

	logRequest := getLogRequest{
		logID: request.LogId,
	}

	if request.From != nil {
		localTime := request.GetFrom().AsTime()
		logRequest.from = &localTime
	}

	if request.Till != nil {
		localTime := request.GetTill().AsTime()
		logRequest.till = &localTime
	}

	go instance.getLog(logRequest)
}

func (instance *Logging) GetLogsDataChannel() (channel <-chan *pb.LogData) {
	return instance.logChannel
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (instance *Logging) getLog(request getLogRequest) {
	var err error

	// error handling
	defer func() {
		if err != nil {
			log.Error("Can't get logs: ", err)

			instance.sendErrorResponse(err.Error(), request.logID)
		}
	}()

	var journal *sdjournal.Journal

	journal, err = sdjournal.NewJournal()
	if err != nil {
		err = aoserrors.Wrap(err)
		return
	}
	defer journal.Close()

	needUnitField := true

	if request.serviceID != "" {
		needUnitField = false

		if _, err = instance.addServiceIDFilter(journal, serviceField, request.serviceID); err != nil {
			err = aoserrors.Wrap(err)
			return
		}
	}

	if err = instance.seekToTime(journal, request.from); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	var tillRealtime uint64

	if request.till != nil {
		tillRealtime = uint64(request.till.UnixNano() / 1000)
	}

	var archInstance *archivator

	if archInstance, err = newArchivator(instance.logChannel,
		instance.config.MaxPartSize,
		instance.config.MaxPartCount); err != nil {
		err = aoserrors.Wrap(err)

		return
	}

	for {
		var rowCount uint64

		if rowCount, err = journal.Next(); err != nil {
			err = aoserrors.Wrap(err)
			return
		}

		// end of log
		if rowCount == 0 {
			break
		}

		var logEntry *sdjournal.JournalEntry

		if logEntry, err = journal.GetEntry(); err != nil {
			err = aoserrors.Wrap(err)
			return
		}

		// till time reached
		if tillRealtime != 0 && logEntry.RealtimeTimestamp > tillRealtime {
			break
		}

		if err = archInstance.addLog(createLogString(logEntry, needUnitField)); err != nil {
			if err == errMaxPartCount {
				log.Warn(err)
				break
			}

			err = aoserrors.Wrap(err)

			return
		}
	}

	if err = archInstance.sendLog(request.logID); err != nil {
		err = aoserrors.Wrap(err)
		return
	}
}

func (instance *Logging) getServiceCrashLog(request getLogRequest) {
	var err error

	// error handling
	defer func() {
		if err != nil {
			log.Error("Can't get service crash logs: ", err)

			instance.sendErrorResponse(err.Error(), request.logID)
		}
	}()

	var journal *sdjournal.Journal

	journal, err = sdjournal.NewJournal()
	if err != nil {
		err = aoserrors.Wrap(err)
		return
	}
	defer journal.Close()

	if _, err = instance.addServiceIDFilter(journal, unitField, request.serviceID); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	if request.till == nil {
		if err = journal.SeekTail(); err != nil {
			err = aoserrors.Wrap(err)
			return
		}
	} else {
		if err = journal.SeekRealtimeUsec(uint64(request.till.UnixNano() / 1000)); err != nil {
			err = aoserrors.Wrap(err)
			return
		}
	}

	var crashTime uint64

	for {
		var rowCount uint64

		if rowCount, err = journal.Previous(); err != nil {
			err = aoserrors.Wrap(err)
			return
		}

		// end of log
		if rowCount == 0 {
			break
		}

		var logEntry *sdjournal.JournalEntry

		if logEntry, err = journal.GetEntry(); err != nil {
			err = aoserrors.Wrap(err)
			return
		}

		if request.from != nil {
			if logEntry.RealtimeTimestamp <= uint64(request.from.UnixNano()/1000) {
				break
			}
		}

		if crashTime == 0 {
			if strings.Contains(logEntry.Fields["MESSAGE"], "process exited") {
				crashTime = logEntry.MonotonicTimestamp

				log.WithFields(log.Fields{
					"serviceID": request.serviceID,
					"time":      getLogDate(logEntry)}).Debug("Crash detected")
			}
		} else {
			if strings.HasPrefix(logEntry.Fields["MESSAGE"], "Started") {
				break
			}
		}
	}

	var archInstance *archivator

	if archInstance, err = newArchivator(instance.logChannel,
		instance.config.MaxPartSize,
		instance.config.MaxPartCount); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	if crashTime > 0 {
		if err = journal.AddDisjunction(); err != nil {
			err = aoserrors.Wrap(err)
			return
		}

		var unitName string

		unitName, err = instance.addServiceIDFilter(journal, serviceField, request.serviceID)
		if err != nil {
			err = aoserrors.Wrap(err)
			return
		}

		for {
			var rowCount uint64

			if rowCount, err = journal.Next(); err != nil {
				err = aoserrors.Wrap(err)
				return
			}

			// end of log
			if rowCount == 0 {
				break
			}

			var logEntry *sdjournal.JournalEntry

			if logEntry, err = journal.GetEntry(); err != nil {
				err = aoserrors.Wrap(err)
				return
			}

			if logEntry.MonotonicTimestamp > crashTime {
				break
			}

			if serviceName, ok := logEntry.Fields[serviceField]; ok && unitName == serviceName {
				if err = archInstance.addLog(createLogString(logEntry, false)); err != nil {
					err = aoserrors.Wrap(err)
					return
				}
			}
		}
	}

	if err = archInstance.sendLog(request.logID); err != nil {
		err = aoserrors.Wrap(err)
		return
	}
}

func (instance *Logging) sendErrorResponse(errorStr, logID string) {
	response := &pb.LogData{
		LogId: logID,
		Error: errorStr}

	instance.logChannel <- response
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
		return unitName, aoserrors.Wrap(err)
	}

	return service.UnitName, nil
}

func (instance *Logging) seekToTime(journal *sdjournal.Journal, from *time.Time) (err error) {
	if from != nil {
		return aoserrors.Wrap(journal.SeekRealtimeUsec(uint64(from.UnixNano() / 1000)))
	}

	return aoserrors.Wrap(journal.SeekHead())
}

func createLogString(entry *sdjournal.JournalEntry, addUnit bool) (logStr string) {
	if addUnit {
		return fmt.Sprintf("%s %s %s \n", getLogDate(entry), entry.Fields[sdjournal.SD_JOURNAL_FIELD_SYSTEMD_UNIT],
			entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE])
	}

	return fmt.Sprintf("%s %s \n", getLogDate(entry), entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE])
}

func getLogDate(entry *sdjournal.JournalEntry) (date time.Time) {
	return time.Unix(int64(entry.RealtimeTimestamp/1000000), int64((entry.RealtimeTimestamp%1000000))*1000)
}
