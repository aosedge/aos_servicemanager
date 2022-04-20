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
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/coreos/go-systemd/v22/sdjournal"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/logging"
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

type testSystemdJournal struct {
	sync.RWMutex
	messages       []*sdjournal.JournalEntry
	currentMessage int
	systemdMatches []string
	simulateError  bool
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestGetServiceLog(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	testJournal := testSystemdJournal{}
	logging.SDJournal = &testJournal

	logging, err := logging.New(&config.Config{Logging: config.Logging{
		MaxPartSize: 1024, MaxPartCount: 10,
	}}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	var (
		from           = time.Now()
		instanceFilter = createInstanceFilter("logservice0", "subject0", 0)
		instanceID     = instanceProvider.addFilter(instanceFilter)
		unitName       = "aos-service@" + instanceID + ".service"
		till           = from.Add(5 * time.Second)
	)

	testJournal.addMessage("This is log", unitName, "", "2")

	if err = logging.GetInstanceLog(cloudprotocol.RequestServiceLog{
		InstanceFilter: instanceFilter,
		LogID:          "log0", From: &from, Till: &till,
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &till)

	etalonMatches := []string{
		"_SYSTEMD_CGROUP=/system.slice/system-aos\\x2dservice.slice/" + unitName,
		"_SYSTEMD_CGROUP=/system.slice/system-aos\\x2dservice.slice/" + instanceID,
	}

	if err = testJournal.isMatchesEqual(etalonMatches); err != nil {
		t.Error(err)
	}

	if err = logging.GetInstanceLog(cloudprotocol.RequestServiceLog{
		InstanceFilter: instanceFilter,
		LogID:          "log0", From: &from,
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	currentTime := time.Now()

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &currentTime)
}

func TestGetSystemLog(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	testJournal := testSystemdJournal{}
	logging.SDJournal = &testJournal

	logging, err := logging.New(&config.Config{Logging: config.Logging{
		MaxPartSize: 1024, MaxPartCount: 10,
	}}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	var (
		from = time.Now()
		till = from.Add(5 * time.Second)
	)

	for i := 0; i < 20; i++ {
		testJournal.addMessage("Hello World", "logger", "", "2")
	}

	logging.GetSystemLog(cloudprotocol.RequestSystemLog{
		LogID: "log10",
		From:  &from,
		Till:  &till,
	})

	checkReceivedLog(t, logging.GetLogsDataChannel(), nil, nil)

	logging.GetSystemLog(cloudprotocol.RequestSystemLog{
		LogID: "log10",
		Till:  &till,
	})

	checkReceivedLog(t, logging.GetLogsDataChannel(), nil, nil)
}

func TestGetEmptyLog(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	testJournal := testSystemdJournal{}
	logging.SDJournal = &testJournal

	logging, err := logging.New(
		&config.Config{Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10}}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	var (
		instanceFilter = createInstanceFilter("logservice2", "subject2", 0)
		from           = time.Now()
		till           = from.Add(5 * time.Second)
	)

	_ = instanceProvider.addFilter(instanceFilter)

	if err = logging.GetInstanceLog(cloudprotocol.RequestServiceLog{
		InstanceFilter: instanceFilter,
		LogID:          "log0", From: &from, Till: &till,
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	checkEmptyLog(t, logging.GetLogsDataChannel())
}

func TestGetServiceCrashLog(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	testJournal := testSystemdJournal{}
	logging.SDJournal = &testJournal

	logging, err := logging.New(&config.Config{
		Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10},
	}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	var (
		instanceFilter = createInstanceFilter("logservice3", "subject3", 0)
		instanceID     = instanceProvider.addFilter(instanceFilter)
		unitName       = "aos-service@" + instanceID + ".service"
		from           = time.Now()
		till           = from.Add(2 * time.Second)
	)

	testJournal.addMessage("Started", unitName, "/system.slice/system-aos@service.slice/"+unitName, "2")
	testJournal.addMessage("somelog1", unitName, "/system.slice/system-aos@service.slice/"+unitName, "2")
	testJournal.addMessage("somelog2", unitName, "/system.slice/system-aos@service.slice/"+instanceID, "2")
	testJournal.addMessage("somelog3", unitName, "", "2")
	testJournal.addMessage("process exited", unitName, "/system.slice/system-aos@service.slice/"+unitName, "2")

	if err := logging.GetInstanceCrashLog(cloudprotocol.RequestServiceCrashLog{
		InstanceFilter: instanceFilter,
	}); err != nil {
		t.Fatalf("Can't get instance crash log: %s", err)
	}

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &till)

	etalonMatches := []string{
		"_SYSTEMD_CGROUP=/system.slice/system-aos\\x2dservice.slice/" + unitName,
		"_SYSTEMD_CGROUP=/system.slice/system-aos\\x2dservice.slice/" + instanceID,
		"UNIT=" + unitName,
	}

	if err = testJournal.isMatchesEqual(etalonMatches); err != nil {
		t.Error(err)
	}

	if err := logging.GetInstanceCrashLog(cloudprotocol.RequestServiceCrashLog{
		InstanceFilter: instanceFilter,
		From:           &from, Till: &till,
	}); err != nil {
		t.Fatalf("Can't get instance crash log: %s", err)
	}

	checkReceivedLog(t, logging.GetLogsDataChannel(), &from, &till)
}

func TestMaxPartCountLog(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	testJournal := testSystemdJournal{}
	logging.SDJournal = &testJournal

	logging, err := logging.New(&config.Config{
		Logging: config.Logging{MaxPartSize: 512, MaxPartCount: 2},
	}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	var (
		instanceFilter = createInstanceFilter("logservice4", "subject4", 0)
		instanceID     = instanceProvider.addFilter(instanceFilter)
		unitName       = "aos-service@" + instanceID + ".service"
		from           = time.Now()
		till           = from.Add(20 * time.Second)
	)

	for i := 0; i < 200; i++ {
		testJournal.addMessage(fmt.Sprintf("Super mega log %d", i),
			unitName, "/system.slice/system-aos@service.slice/"+unitName, "2")
	}

	if err = logging.GetInstanceLog(cloudprotocol.RequestServiceLog{
		InstanceFilter: instanceFilter,
		LogID:          "log0", From: &from, Till: &till,
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

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

func TestLogErrorCases(t *testing.T) {
	instanceProvider := testInstanceIDProvider{instances: make(map[string]cloudprotocol.InstanceFilter)}
	defer instanceProvider.Close()

	testJournal := testSystemdJournal{}
	logging.SDJournal = &testJournal

	loggingInstance, err := logging.New(&config.Config{
		Logging: config.Logging{MaxPartSize: 1024, MaxPartCount: 10},
	}, &instanceProvider)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer loggingInstance.Close()

	if err := loggingInstance.GetInstanceLog(cloudprotocol.RequestServiceLog{
		InstanceFilter: createInstanceFilter("noService", "", -1),
	}); err == nil {
		t.Error("should be error: no instance ids for log request")
	}

	if err := loggingInstance.GetInstanceCrashLog(cloudprotocol.RequestServiceCrashLog{
		InstanceFilter: createInstanceFilter("noService", "", -1),
	}); err == nil {
		t.Error("should be error: no instance ids for log request")
	}

	var (
		instanceFilter = createInstanceFilter("logservice5", "subject5", 0)
		faultTime      = time.Time{}
		unitName       = "aos-service@" + instanceProvider.addFilter(instanceFilter) + ".service"
	)

	if err = loggingInstance.GetInstanceLog(cloudprotocol.RequestServiceLog{
		InstanceFilter: instanceFilter,
		LogID:          "log0", From: &faultTime,
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	checkErrorLog(t, loggingInstance.GetLogsDataChannel())

	if err = loggingInstance.GetInstanceCrashLog(cloudprotocol.RequestServiceCrashLog{
		InstanceFilter: instanceFilter,
		LogID:          "log0", Till: &faultTime,
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	checkErrorLog(t, loggingInstance.GetLogsDataChannel())

	testJournal.simulateError = true

	testJournal.addMessage("Started", unitName, "/system.slice/system-aos@service.slice/"+unitName, "2")

	if err = loggingInstance.GetInstanceLog(cloudprotocol.RequestServiceLog{
		InstanceFilter: instanceFilter,
		LogID:          "log0",
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	checkErrorLog(t, loggingInstance.GetLogsDataChannel())

	if err = loggingInstance.GetInstanceCrashLog(cloudprotocol.RequestServiceCrashLog{
		InstanceFilter: instanceFilter,
		LogID:          "log0",
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	checkErrorLog(t, loggingInstance.GetLogsDataChannel())

	logging.SDJournal = nil

	if err = loggingInstance.GetInstanceLog(cloudprotocol.RequestServiceLog{
		InstanceFilter: instanceFilter,
		LogID:          "log0", From: &faultTime,
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
	}

	if err = loggingInstance.GetInstanceCrashLog(cloudprotocol.RequestServiceCrashLog{
		InstanceFilter: instanceFilter,
		LogID:          "log0", Till: &faultTime,
	}); err != nil {
		t.Fatalf("Can't get instance log: %s", err)
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

func (provider *testInstanceIDProvider) addFilter(filter cloudprotocol.InstanceFilter) (instanceID string) {
	instanceID = instanceFormFilter(filter)

	provider.instances[instanceID] = filter

	return instanceID
}

func (provider *testInstanceIDProvider) Close() {
}

func (journal *testSystemdJournal) Close() error { return nil }

func (journal *testSystemdJournal) AddMatch(match string) error {
	journal.systemdMatches = append(journal.systemdMatches, match)

	return nil
}

func (journal *testSystemdJournal) AddDisjunction() error { return nil }

func (journal *testSystemdJournal) SeekTail() error {
	journal.currentMessage = len(journal.messages)

	return nil
}

func (journal *testSystemdJournal) SeekHead() error {
	journal.currentMessage = -1

	return nil
}

func (journal *testSystemdJournal) SeekRealtimeUsec(usec uint64) error {
	if usec == uint64(time.Time{}.UnixNano()/1000) {
		return aoserrors.New("incorrect time")
	}

	journal.currentMessage = -1

	return nil
}

func (journal *testSystemdJournal) Previous() (uint64, error) {
	if len(journal.messages) == 0 {
		return uint64(sdjournal.SD_JOURNAL_NOP), nil
	}

	if journal.currentMessage == 0 {
		return uint64(sdjournal.SD_JOURNAL_NOP), nil
	}

	if journal.currentMessage == -1 {
		journal.currentMessage = len(journal.messages)
	}

	journal.currentMessage--

	return uint64(sdjournal.SD_JOURNAL_APPEND), nil
}

func (journal *testSystemdJournal) Next() (uint64, error) {
	if len(journal.messages) == 0 {
		return uint64(sdjournal.SD_JOURNAL_NOP), nil
	}

	if journal.currentMessage >= len(journal.messages)-1 {
		return uint64(sdjournal.SD_JOURNAL_NOP), nil
	}

	journal.currentMessage++

	return uint64(sdjournal.SD_JOURNAL_APPEND), nil
}

func (journal *testSystemdJournal) GetEntry() (entry *sdjournal.JournalEntry, err error) {
	if journal.simulateError {
		return entry, aoserrors.New("simulated error")
	}

	entry = journal.messages[journal.currentMessage]

	return entry, nil
}

func (journal *testSystemdJournal) addMessage(message, systemdUnit, cgroupUnit, priority string) {
	journalEntry := sdjournal.JournalEntry{Fields: make(map[string]string)}

	currentTime := time.Now()

	journalEntry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE] = fmt.Sprintf(
		"[%s] %s", currentTime.Format("2006-01-02 15:04:05.999999999Z07:00"), message+"@@@@")
	journalEntry.Fields[sdjournal.SD_JOURNAL_FIELD_SYSTEMD_UNIT] = systemdUnit
	journalEntry.Fields[sdjournal.SD_JOURNAL_FIELD_SYSTEMD_CGROUP] = cgroupUnit
	journalEntry.Fields[sdjournal.SD_JOURNAL_FIELD_PRIORITY] = priority

	journalEntry.RealtimeTimestamp = uint64(currentTime.UnixNano() / 1000)
	journalEntry.MonotonicTimestamp = uint64(currentTime.UnixNano() / 1000)

	journal.messages = append(journal.messages, &journalEntry)
}

func (journal *testSystemdJournal) isMatchesEqual(etalonMatches []string) error {
matchLoop:
	for _, etalonMatch := range etalonMatches {
		for _, journalMatch := range journal.systemdMatches {
			if etalonMatch == journalMatch {
				continue matchLoop
			}
		}

		return aoserrors.Errorf("Journal filter doesn't contains: %s", etalonMatch)
	}

	return nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func instanceFormFilter(filter cloudprotocol.InstanceFilter) string {
	return fmt.Sprintf("%s.%s.%s", filter.ServiceID, *filter.SubjectID, strconv.FormatUint(*filter.Instance, 10))
}

func getTimeRange(logData string) (from, till time.Time, err error) {
	list := strings.Split(logData, "\n")

	if len(list) < 2 || len(list[0]) < 37 || len(list[len(list)-2]) < 37 {
		return from, till, aoserrors.New("bad log data")
	}

	fromLog := list[0][strings.IndexByte(list[0], '['):]
	lastindex := strings.LastIndex(fromLog, "]")

	if from, err = time.Parse("2006-01-02 15:04:05.999999999Z07:00", fromLog[1:lastindex]); err != nil {
		return from, till, aoserrors.Wrap(err)
	}

	tillLog := list[len(list)-2][strings.IndexByte(list[len(list)-2], '['):]
	lastindex = strings.LastIndex(tillLog, "]")

	if till, err = time.Parse("2006-01-02 15:04:05.999999999Z07:00", tillLog[1:lastindex]); err != nil {
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

func checkEmptyLog(t *testing.T, logChannel <-chan cloudprotocol.PushLog) {
	t.Helper()

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

func checkErrorLog(t *testing.T, logChannel <-chan cloudprotocol.PushLog) {
	t.Helper()

	for {
		select {
		case result := <-logChannel:
			if result.Error == "" {
				t.Errorf("Should be error %s", result.Error)
			}

			return

		case <-time.After(5 * time.Second):
			t.Error("Receive log timeout")
			return
		}
	}
}

func createInstanceFilter(serviceID, subjectID string, instance int64) (filter cloudprotocol.InstanceFilter) {
	filter.ServiceID = serviceID

	if subjectID != "" {
		filter.SubjectID = &subjectID
	}

	if instance != -1 {
		localInstance := (uint64)(instance)

		filter.Instance = &localInstance
	}

	return filter
}
