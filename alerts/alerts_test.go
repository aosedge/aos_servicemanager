package alerts_test

import (
	"errors"
	"fmt"
	"log/syslog"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/alerts"
	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
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
 * Vars
 ******************************************************************************/

var db *database.Database

var errTimeout = errors.New("timeout")

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	db, err = database.New("tmp/servicemanager.db")
	if err != nil {
		return err
	}

	return nil
}

func cleanup() {
	db.Close()

	if err := os.RemoveAll("tmp"); err != nil {
		log.Errorf("Can't remove tmp folder: %s", err)
	}
}

func waitResult(alertsChannel <-chan amqp.Alerts, timeout time.Duration, checkAlert func(alert amqp.AlertItem) (success bool, err error)) (err error) {
	for {
		select {
		case alerts := <-alertsChannel:
			for _, alert := range alerts.Data {
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

func waitSystemAlerts(alertsChannel <-chan amqp.Alerts, timeout time.Duration, source string, version *uint64, data []string) (err error) {
	return waitResult(alertsChannel, timeout, func(alert amqp.AlertItem) (success bool, err error) {
		if alert.Tag != amqp.AlertSystemError {
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

	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{PollPeriod: config.Duration{Duration: 1 * time.Second}}}, db)
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

	if err = waitSystemAlerts(alertsHandler.AlertsChannel, 5*time.Second, "system", nil, messages); err != nil {
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
			if alert.Tag == amqp.AlertSystemError {
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
	alertsHandler, err := alerts.New(&config.Config{Alerts: config.Alerts{PollPeriod: config.Duration{Duration: 1 * time.Second}}}, db)
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
	alertsHandler, err = alerts.New(&config.Config{Alerts: config.Alerts{PollPeriod: config.Duration{Duration: 1 * time.Second}}}, db)
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}
	defer alertsHandler.Close()

	// Check all offline messages are handled
	if err = waitSystemAlerts(alertsHandler.AlertsChannel, 5*time.Second, "system", nil, messages); err != nil {
		t.Errorf("Result failed: %s", err)
	}
}
