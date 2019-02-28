// Package logging provides set of API to retrieve system and services log
package logging

import (
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-systemd/sdjournal"
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
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

// ServiceProvider provides service entry
type ServiceProvider interface {
	GetService(id string) (entry database.ServiceEntry, err error)
}

// Logging instance
type Logging struct {
	LogChannel chan amqp.PushServiceLog

	serviceProvider ServiceProvider
	config          config.Logging
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

	go instance.getServiceLog(request)
}

// GetServiceCrashLog returns service log
func (instance *Logging) GetServiceCrashLog(request amqp.RequestServiceCrashLog) {
	log.WithFields(log.Fields{
		"serviceID": request.ServiceID,
		"logID":     request.LogID}).Debug("Get service crash log")

	go instance.getServiceCrashLog(request)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (instance *Logging) getServiceLog(request amqp.RequestServiceLog) {
	var err error

	// error handling
	defer func() {
		if r := recover(); r != nil {
			errorStr := fmt.Sprintf("%s: %s", r, err)

			log.Error(errorStr)

			response := amqp.PushServiceLog{
				LogID: request.LogID,
				Error: &errorStr}
			instance.LogChannel <- response
		}
	}()

	var service database.ServiceEntry

	service, err = instance.serviceProvider.GetService(request.ServiceID)
	if err != nil {
		panic("Can't get service")
	}

	var journal *sdjournal.Journal

	journal, err = sdjournal.NewJournal()
	if err != nil {
		panic("Can't open sd journal")
	}
	defer journal.Close()

	if err = journal.AddMatch(serviceField + "=" + service.ServiceName); err != nil {
		panic("Can't add filter")
	}

	if request.From != nil {
		if err = journal.SeekRealtimeUsec(uint64(request.From.UnixNano() / 1000)); err != nil {
			panic("Can't seek log")
		}
	} else {
		if err = journal.SeekHead(); err != nil {
			panic("Can't seek log")
		}
	}

	var tillRealtime uint64

	if request.Till != nil {
		tillRealtime = uint64(request.Till.UnixNano() / 1000)
	}

	var archInstance *archivator

	if archInstance, err = newArchivator(instance.LogChannel, instance.config.MaxPartSize); err != nil {
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
			panic("Can't archive log")
		}
	}

	if err = archInstance.sendLog(request.LogID); err != nil {
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

			response := amqp.PushServiceLog{
				LogID: request.LogID,
				Error: &errorStr}
			instance.LogChannel <- response
		}
	}()

	var service database.ServiceEntry

	service, err = instance.serviceProvider.GetService(request.ServiceID)
	if err != nil {
		panic("Can't get service")
	}

	var journal *sdjournal.Journal

	journal, err = sdjournal.NewJournal()
	if err != nil {
		panic("Can't open sd journal")
	}
	defer journal.Close()

	if err = journal.AddMatch(unitField + "=" + service.ServiceName); err != nil {
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

	if archInstance, err = newArchivator(instance.LogChannel, instance.config.MaxPartSize); err != nil {
		panic("Can't create archivator")
	}

	if crashTime > 0 {
		if err = journal.AddDisjunction(); err != nil {
			panic("Can't add filter")
		}

		if err = journal.AddMatch(serviceField + "=" + service.ServiceName); err != nil {
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

			if serviceName, ok := logEntry.Fields[serviceField]; ok && service.ServiceName == serviceName {
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
