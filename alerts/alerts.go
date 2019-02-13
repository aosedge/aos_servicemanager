// Package alerts provides set of API to send system and services alerts
package alerts

import (
	"sync"
	"time"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"

	"github.com/coreos/go-systemd/sdjournal"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	alertsChannelSize   = 32
	alertsDataAllocSize = 10
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// ResourceAlertsItf interface to send resource alerts
type ResourceAlertsItf interface {
	SendResourceAlert(serviceID, resource string, time time.Time, value uint64)
}

// Alerts instance
type Alerts struct {
	AlertsChannel chan amqp.Alerts

	config config.Alerts
	db     database.JournalItf

	alerts amqp.Alerts

	journal      *sdjournal.Journal
	cursor       string
	mutex        sync.Mutex
	ticker       *time.Ticker
	closeChannel chan bool
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new alerts object
func New(config *config.Config, db database.JournalItf) (instance *Alerts, err error) {
	log.Debug("New alerts")

	instance = &Alerts{config: config.Alerts, db: db}

	instance.AlertsChannel = make(chan amqp.Alerts, alertsChannelSize)
	instance.closeChannel = make(chan bool)

	instance.ticker = time.NewTicker(instance.config.PollPeriod.Duration)

	instance.alerts.Data = make([]amqp.AlertItem, 0, alertsDataAllocSize)

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

/*******************************************************************************
 * Private
 ******************************************************************************/

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

	cursor, err := instance.db.GetJournalCursor()
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

		instance.cursor = cursor
	} else {
		if err = instance.journal.SeekTail(); err != nil {
			return err
		}

		if _, err = instance.journal.Previous(); err != nil {
			return err
		}
	}

	go func() {
		for {
			select {
			case <-instance.ticker.C:
				if err = instance.processJournal(); err != nil {
					log.Errorf("Journal process error: %s", err)
				}

				instance.mutex.Lock()
				if len(instance.alerts.Data) != 0 {
					instance.AlertsChannel <- instance.alerts
					instance.alerts.Data = make([]amqp.AlertItem, 0, alertsDataAllocSize)
				}
				instance.mutex.Unlock()

			case <-instance.closeChannel:
				return
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
			cursor, err := instance.journal.GetCursor()
			if err != nil {
				return err
			}

			if cursor != instance.cursor {
				if err = instance.db.SetJournalCursor(cursor); err != nil {
					return err
				}

				instance.cursor = cursor
			}

			return nil
		}

		entry, err := instance.journal.GetEntry()
		if err != nil {
			return err
		}

		t := time.Unix(int64(entry.RealtimeTimestamp/1000000),
			int64((entry.RealtimeTimestamp%1000000)*1000))

		log.WithFields(log.Fields{"timestamp": t, "message": entry.Fields["MESSAGE"]}).Debug("Alert")

		item := amqp.AlertItem{
			Timestamp: t,
			Tag:       amqp.AlertSystemError,
			Source:    "system",
			Payload:   amqp.SystemAlert{Message: entry.Fields["MESSAGE"]}}

		instance.mutex.Lock()
		instance.alerts.Data = append(instance.alerts.Data, item)
		instance.mutex.Unlock()
	}
}
