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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

const aosServicePrefix = "aos-service@"

const (
	waitJournalTimeout = 1 * time.Second
	journalSavePeriod  = 10 * time.Second
)

const alertChannelSize = 50

const microSecondsInSecond = 1000000

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// InstanceProvider provides instance info.
type InstanceProvider interface {
	GetInstanceInfoByID(instanceID string) (instance cloudprotocol.InstanceIdent, aosVersion uint64, err error)
}

// CursorStorage provides API to set and get journal cursor.
type CursorStorage interface {
	SetJournalCursor(cursor string) (err error)
	GetJournalCursor() (cursor string, err error)
}

// JournalInterface systemd journal interface.
type JournalInterface interface {
	Close() error
	AddMatch(match string) error
	AddDisjunction() error
	SeekTail() error
	Previous() (uint64, error)
	SeekCursor(cursor string) error
	Next() (uint64, error)
	GetEntry() (*sdjournal.JournalEntry, error)
	Wait(timeout time.Duration) int
	GetCursor() (string, error)
}

// Alerts instance.
type Alerts struct {
	sync.Mutex
	alertsChannel    chan cloudprotocol.AlertItem
	config           config.Alerts
	cursorStorage    CursorStorage
	instanceProvider InstanceProvider
	filterRegexp     []*regexp.Regexp
	journal          JournalInterface
	ticker           *time.Ticker
	closeChannel     chan bool
}

/***********************************************************************************************************************
 * Variable
 **********************************************************************************************************************/

// ErrDisabled indicates that alerts is disable in the config.
var ErrDisabled = errors.New("alerts is disabled")

var coreComponents = []string{ // nolint:gochecknoglobals
	"aos-servicemanager",
	"aos-updatemanager",
	"aos-iamanager",
	"aos-communicationmanager",
}

// SDJournal is using to mock systemd journal in unit tests.
var SDJournal JournalInterface // nolint:gochecknoglobals

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new alerts object.
func New(config *config.Config, instanceProvider InstanceProvider,
	cursorStorage CursorStorage,
) (instance *Alerts, err error) {
	log.Debug("New alerts")

	if config.Alerts.Disabled {
		return nil, ErrDisabled
	}

	instance = &Alerts{
		config: config.Alerts, cursorStorage: cursorStorage,
		instanceProvider: instanceProvider,
		alertsChannel:    make(chan cloudprotocol.AlertItem, alertChannelSize),
		closeChannel:     make(chan bool),
		ticker:           time.NewTicker(journalSavePeriod),
	}

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

// Close closes logging.
func (instance *Alerts) Close() {
	log.Debug("Close alerts")

	instance.closeChannel <- true

	instance.ticker.Stop()

	if err := instance.storeCurrentCursor(); err != nil {
		log.Errorf("Can't store cursor: %s", err)
	}

	instance.journal.Close()
}

// GetAlertsChannel returns channel with alerts to be sent.
func (instance *Alerts) GetAlertsChannel() (channel <-chan cloudprotocol.AlertItem) {
	return instance.alertsChannel
}

// SendResourceAlert sends resource alert.
func (instance *Alerts) SendAlert(alert cloudprotocol.AlertItem) {
	instance.pushAlert(alert)
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (instance *Alerts) setupJournal() (err error) {
	if instance.journal = SDJournal; instance.journal == nil {
		if instance.journal, err = sdjournal.NewJournal(); err != nil {
			return aoserrors.Wrap(err)
		}
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

	go instance.handleChannels()

	return nil
}

func (instance *Alerts) handleChannels() {
	result := sdjournal.SD_JOURNAL_APPEND

	for {
		select {
		case <-instance.ticker.C:
			if err := instance.storeCurrentCursor(); err != nil {
				log.Error("Can't store journal cursor: ", err)
			}

		case <-instance.closeChannel:
			return

		default:
			if result != sdjournal.SD_JOURNAL_NOP {
				if err := instance.processJournal(); err != nil {
					log.Errorf("Journal process error: %s", err)
				}
			}

			if result = instance.journal.Wait(waitJournalTimeout); result < 0 {
				log.Errorf("Wait journal error: %s", syscall.Errno(-result))
			}
		}
	}
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

		if entry == nil {
			return nil
		}

		unit := entry.Fields[sdjournal.SD_JOURNAL_FIELD_SYSTEMD_UNIT]

		if unit == "init.scope" {
			if priority, err := strconv.Atoi(entry.Fields[sdjournal.SD_JOURNAL_FIELD_PRIORITY]); err != nil ||
				priority > instance.config.ServiceAlertPriority {
				continue
			}

			unit = entry.Fields["UNIT"]
		}

		// with cgroup v2 logs from container do not contains _SYSTEMD_UNIT due to restrictions
		// that's why id should be extracted from _SYSTEMD_CGROUP
		// format: /system.slice/system-aos@service.slice/AOS_INSTANCE_ID
		if len(unit) == 0 {
			systemdCgroup := entry.Fields[sdjournal.SD_JOURNAL_FIELD_SYSTEMD_CGROUP]

			if len(systemdCgroup) > 0 {
				// add prefix 'aos-service@' and postfix '.service'
				// to service uuid and get proper seervice object from DB
				unit = systemdCgroup
			} else {
				continue
			}
		}

		alert := cloudprotocol.AlertItem{
			Timestamp: time.Unix(int64(entry.RealtimeTimestamp/microSecondsInSecond),
				int64((entry.RealtimeTimestamp%microSecondsInSecond)*1000)),
		}

		instance.fillServiceInstanceAlert(&alert, entry, unit)

		if alert.Payload == nil {
			instance.fillCoreComponentAlert(&alert, entry, unit)
		}

		if alert.Payload == nil {
			instance.fillSystemAlert(&alert, entry)
		}

		if alert.Payload != nil {
			instance.pushAlert(alert)
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

func (instance *Alerts) pushAlert(alert cloudprotocol.AlertItem) {
	if len(instance.alertsChannel) >= cap(instance.alertsChannel) {
		log.Warn("Skip alert, channel is full")

		return
	}

	instance.alertsChannel <- alert
}

func (instance *Alerts) fillServiceInstanceAlert(
	alert *cloudprotocol.AlertItem, entry *sdjournal.JournalEntry, unitName string,
) {
	if strings.Contains(unitName, aosServicePrefix) {
		instanceID := filepath.Base(unitName)
		instanceID = strings.TrimPrefix(instanceID, aosServicePrefix)
		instanceID = strings.TrimSuffix(instanceID, ".service")

		var (
			instanceAlert cloudprotocol.ServiceInstanceAlert
			err           error
		)

		instanceAlert.InstanceIdent, instanceAlert.AosVersion, err = instance.instanceProvider.GetInstanceInfoByID(
			instanceID)
		if err != nil {
			log.Errorf("Can't get instance info: %s", err)

			return
		}

		instanceAlert.Message = entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE]

		alert.Tag = cloudprotocol.AlertTagServiceInstance
		alert.Payload = instanceAlert
	}
}

func (instance *Alerts) fillCoreComponentAlert(
	alert *cloudprotocol.AlertItem, entry *sdjournal.JournalEntry, unitName string,
) {
	for _, component := range coreComponents {
		if strings.Contains(unitName, component) {
			coreAlert := cloudprotocol.CoreAlert{
				CoreComponent: component,
				Message:       entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE],
			}

			alert.Tag = cloudprotocol.AlertTagAosCore
			alert.Payload = coreAlert

			return
		}
	}
}

func (instance *Alerts) fillSystemAlert(alert *cloudprotocol.AlertItem, entry *sdjournal.JournalEntry) {
	skipped := false

	for _, substr := range instance.filterRegexp {
		skipped = substr.MatchString(entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE])

		if skipped {
			break
		}
	}

	if !skipped {
		systemAlert := cloudprotocol.SystemAlert{Message: entry.Fields[sdjournal.SD_JOURNAL_FIELD_MESSAGE]}

		alert.Payload = systemAlert
		alert.Tag = cloudprotocol.AlertTagSystemError
	}
}
