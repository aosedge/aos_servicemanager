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

// Package logging provides set of API to retrieve system and instance log
package logging

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/coreos/go-systemd/v22/sdjournal"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	logChannelSize = 32

	cgroupField      = "_SYSTEMD_CGROUP"
	unitField        = "UNIT"
	aosServicePrefix = "aos-service@"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// InstanceIDProvider provides instances ID.
type InstanceIDProvider interface {
	GetInstanceIDs(cloudprotocol.InstanceFilter) ([]string, error)
}

// Logging instance.
type Logging struct {
	logChannel       chan cloudprotocol.PushLog
	instanceProvider InstanceIDProvider
	config           config.Logging
}

type JournalInterface interface {
	Close() error
	AddMatch(match string) error
	AddDisjunction() error
	SeekTail() error
	SeekHead() error
	SeekRealtimeUsec(usec uint64) error
	Previous() (uint64, error)
	Next() (uint64, error)
	GetEntry() (*sdjournal.JournalEntry, error)
}

type getLogRequest struct {
	instanceIDs []string
	logID       string
	from        *time.Time
	till        *time.Time
}

/***********************************************************************************************************************
 * Variable
 **********************************************************************************************************************/

// SDJournal is using to mock systemd journal in unit tests.
var SDJournal JournalInterface // nolint:gochecknoglobals

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new logging object.
func New(config *config.Config, instanceProvider InstanceIDProvider) (instance *Logging, err error) {
	log.Debug("New logging")

	instance = &Logging{
		instanceProvider: instanceProvider,
		config:           config.Logging,
		logChannel:       make(chan cloudprotocol.PushLog, logChannelSize),
	}

	return instance, nil
}

// Close closes logging.
func (instance *Logging) Close() {
	log.Debug("Close logging")
}

// GetInstanceLog returns instance log.
func (instance *Logging) GetInstanceLog(request cloudprotocol.RequestLog) error {
	log.WithField("request", logRequestToString(request)).Debug("Get instance log")

	logRequest, err := instance.prepareInstanceLogRequest(request)
	if err != nil {
		instance.sendErrorResponse(err.Error(), request.LogID)

		return err
	}

	go func() {
		if err := instance.getLog(logRequest); err != nil {
			log.Errorf("Can't get instanace logs: %s", err)

			instance.sendErrorResponse(err.Error(), logRequest.logID)
		}
	}()

	return nil
}

// GetServiceCrashLog returns instance crash log.
func (instance *Logging) GetInstanceCrashLog(request cloudprotocol.RequestLog) error {
	log.WithField("request", logRequestToString(request)).Debug("Get instance crash log")

	logRequest, err := instance.prepareInstanceLogRequest(request)
	if err != nil {
		instance.sendErrorResponse(err.Error(), request.LogID)

		return err
	}

	go func() {
		if err := instance.getInstanceCrashLog(logRequest); err != nil {
			log.Errorf("Can't get instance crash logs: %s", err)

			instance.sendErrorResponse(err.Error(), logRequest.logID)
		}
	}()

	return nil
}

// GetSystemLog returns system log.
func (instance *Logging) GetSystemLog(request cloudprotocol.RequestLog) {
	log.WithField("request", logRequestToString(request)).Debug("Get system log")

	logRequest := getLogRequest{
		logID: request.LogID,
		from:  request.Filter.From,
		till:  request.Filter.Till,
	}

	go func() {
		if err := instance.getLog(logRequest); err != nil {
			log.Errorf("Can't get system logs: %s", err)

			instance.sendErrorResponse(err.Error(), logRequest.logID)
		}
	}()
}

// GetLogsDataChannel returns channel with logs that are ready to send.
func (instance *Logging) GetLogsDataChannel() (channel <-chan cloudprotocol.PushLog) {
	return instance.logChannel
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (instance *Logging) getLog(request getLogRequest) (err error) {
	journal := SDJournal
	if journal == nil {
		if journal, err = sdjournal.NewJournal(); err != nil {
			return aoserrors.Wrap(err)
		}
	}
	defer journal.Close()

	needUnitField := true

	if len(request.instanceIDs) != 0 {
		needUnitField = false

		if err = instance.addServiceCgroupFilter(journal, request.instanceIDs); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if err = instance.seekToTime(journal, request.from); err != nil {
		return aoserrors.Wrap(err)
	}

	var tillRealtime uint64

	if request.till != nil {
		tillRealtime = uint64(request.till.UnixNano() / 1000)
	}

	var archInstance *archivator

	if archInstance, err = newArchivator(instance.logChannel,
		instance.config.MaxPartSize, instance.config.MaxPartCount); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = instance.processJournalToGetInstanceLog(archInstance, journal, tillRealtime, needUnitField); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = archInstance.sendLog(request.logID); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (instance *Logging) processJournalToGetInstanceLog(
	archInstance *archivator, journal JournalInterface, tillRealtime uint64, needUnitField bool,
) error {
	for {
		rowCount, err := journal.Next()
		if err != nil {
			return aoserrors.Wrap(err)
		}

		// end of log
		if rowCount == 0 {
			break
		}

		logEntry, err := journal.GetEntry()
		if err != nil {
			return aoserrors.Wrap(err)
		}

		if logEntry == nil {
			break
		}

		// till time reached
		if tillRealtime != 0 && logEntry.RealtimeTimestamp > tillRealtime {
			break
		}

		if err = archInstance.addLog(createLogString(logEntry, needUnitField)); err != nil {
			if errors.Is(err, errMaxPartCount) {
				log.Warn(err)
				break
			}

			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (instance *Logging) getInstanceCrashLog(request getLogRequest) (err error) {
	journal := SDJournal
	if journal == nil {
		if journal, err = sdjournal.NewJournal(); err != nil {
			return aoserrors.Wrap(err)
		}
	}
	defer journal.Close()

	if err = instance.addUnitFilter(journal, request.instanceIDs); err != nil {
		return aoserrors.Wrap(err)
	}

	if request.till == nil {
		if err = journal.SeekTail(); err != nil {
			return aoserrors.Wrap(err)
		}
	} else {
		if err = journal.SeekRealtimeUsec(uint64(request.till.UnixNano() / 1000)); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	var crashTime uint64

	crashTime, err = instance.getCrashTime(journal, request.from)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if crashTime == 0 {
		return nil
	}

	if err = journal.AddDisjunction(); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = instance.addServiceCgroupFilter(journal, request.instanceIDs); err != nil {
		return aoserrors.Wrap(err)
	}

	archInstance, err := instance.archivateCrashLog(journal, crashTime, request.instanceIDs)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = archInstance.sendLog(request.logID); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (instance *Logging) getCrashTime(journal JournalInterface, from *time.Time) (crashTime uint64, err error) {
	for {
		var rowCount uint64

		if rowCount, err = journal.Previous(); err != nil {
			return crashTime, aoserrors.Wrap(err)
		}

		// end of log
		if rowCount == 0 {
			break
		}

		var logEntry *sdjournal.JournalEntry

		if logEntry, err = journal.GetEntry(); err != nil {
			return crashTime, aoserrors.Wrap(err)
		}

		if from != nil {
			if logEntry.RealtimeTimestamp <= uint64(from.UnixNano()/1000) {
				break
			}
		}

		if crashTime == 0 {
			if strings.Contains(logEntry.Fields["MESSAGE"], "process exited") {
				crashTime = logEntry.MonotonicTimestamp

				log.WithFields(log.Fields{
					"time": getLogDate(logEntry),
				}).Debug("Crash detected")
			}
		} else {
			if strings.HasPrefix(logEntry.Fields["MESSAGE"], "Started") {
				break
			}
		}
	}

	return crashTime, nil
}

func (instance *Logging) archivateCrashLog(
	journal JournalInterface, crashTime uint64, instanceIDs []string,
) (archivator *archivator, err error) {
	archivator, err = newArchivator(instance.logChannel, instance.config.MaxPartSize, instance.config.MaxPartCount)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	for {
		var rowCount uint64

		if rowCount, err = journal.Next(); err != nil {
			return archivator, aoserrors.Wrap(err)
		}

		// end of log
		if rowCount == 0 {
			break
		}

		var logEntry *sdjournal.JournalEntry

		if logEntry, err = journal.GetEntry(); err != nil {
			return archivator, aoserrors.Wrap(err)
		}

		if logEntry.MonotonicTimestamp > crashTime {
			break
		}

		for _, instanceID := range instanceIDs {
			if strings.Contains(getUnitNameFromLog(logEntry), makeUnitNameFromInstanceID(instanceID)) {
				if err = archivator.addLog(createLogString(logEntry, false)); err != nil {
					return archivator, aoserrors.Wrap(err)
				}
			}
		}
	}

	return archivator, nil
}

func (instance *Logging) sendErrorResponse(errorStr, logID string) {
	response := cloudprotocol.PushLog{
		LogID: logID,
		ErrorInfo: cloudprotocol.ErrorInfo{
			Message: errorStr,
		},
	}

	instance.logChannel <- response
}

func (instance *Logging) addServiceCgroupFilter(journal JournalInterface, instanceIDs []string) (err error) {
	for _, instanceID := range instanceIDs {
		// for supporting cgroup v1
		// format: /system.slice/system-aos@service.slice/aos-service@AOS_INSTANCE_ID.service
		if err = journal.AddMatch(
			cgroupField +
				"=/system.slice/system-aos\\x2dservice.slice/" + aosServicePrefix + instanceID + ".service"); err != nil {
			return aoserrors.Wrap(err)
		}

		// for supporting cgroup v2
		// format: /system.slice/system-aos@service.slice/AOS_INSTANCE_ID
		if err = journal.AddMatch(cgroupField + "=/system.slice/system-aos\\x2dservice.slice/" + instanceID); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (instance *Logging) addUnitFilter(journal JournalInterface, instancesIDs []string) (err error) {
	for _, instanceID := range instancesIDs {
		if err = journal.AddMatch(unitField + "=" + makeUnitNameFromInstanceID(instanceID)); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (instance *Logging) seekToTime(journal JournalInterface, from *time.Time) (err error) {
	if from != nil {
		return aoserrors.Wrap(journal.SeekRealtimeUsec(uint64(from.UnixNano() / 1000)))
	}

	return aoserrors.Wrap(journal.SeekHead())
}

func (instance *Logging) prepareInstanceLogRequest(
	request cloudprotocol.RequestLog,
) (logRequest getLogRequest, err error) {
	instances, err := instance.instanceProvider.GetInstanceIDs(request.Filter.InstanceFilter)
	if err != nil {
		return logRequest, aoserrors.Wrap(err)
	}

	if len(instances) == 0 {
		return logRequest, aoserrors.New("no instance ids for log request")
	}

	return getLogRequest{
		instanceIDs: instances,
		logID:       request.LogID,
		from:        request.Filter.From,
		till:        request.Filter.Till,
	}, nil
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

func getUnitNameFromLog(logEntry *sdjournal.JournalEntry) (unitName string) {
	systemdCgroup := logEntry.Fields[sdjournal.SD_JOURNAL_FIELD_SYSTEMD_CGROUP]

	if len(systemdCgroup) == 0 {
		return ""
	}

	unitName = filepath.Base(systemdCgroup)

	if !strings.Contains(unitName, aosServicePrefix) { // nolint:wsl
		// with cgroup v2 logs from container do not contains _SYSTEMD_UNIT due to restrictions
		// that's why id should be checked via _SYSTEMD_CGROUP
		// format: /system.slice/system-aos@service.slice/AOS_INSTANCE_ID

		return fmt.Sprintf("%s%s.service", aosServicePrefix, unitName)
	}

	return unitName
}

func makeUnitNameFromInstanceID(instanceID string) string {
	return fmt.Sprintf("%s%s.service", aosServicePrefix, instanceID)
}

func logRequestToString(log interface{}) string {
	if data, err := json.Marshal(log); err == nil {
		return string(data)
	}

	return ""
}
