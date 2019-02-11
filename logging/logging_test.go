package logging_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coreos/go-systemd/dbus"
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/logging"
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
var systemd *dbus.Conn

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	if db, err = database.New("tmp/servicemanager.db"); err != nil {
		return err
	}

	if systemd, err = dbus.NewSystemConnection(); err != nil {
		return err
	}

	return nil
}

func cleanup() {
	services, err := db.GetServices()
	if err != nil {
		log.Errorf("Can't get services: %s", err)
	}

	for _, service := range services {
		if err := stopService(service.ID); err != nil {
			log.Errorf("Can't stop service: %s", err)
		}

		if _, err := systemd.DisableUnitFiles([]string{service.ServiceName}, false); err != nil {
			log.Errorf("Can't disable service: %s", err)
		}
	}

	db.Close()
	systemd.Close()

	if err := os.RemoveAll("tmp"); err != nil {
		log.Errorf("Can't remove tmp folder: %s", err)
	}
}

func createService(serviceID string) (err error) {
	serviceContent := `[Unit]
	Description=AOS Service
	After=network.target
	
	[Service]
	Type=simple
	Restart=always
	RestartSec=1
	ExecStart=/bin/bash -c 'while true; do echo "[$(date --rfc-3339=ns)] This is log"; sleep 0.1; done'
	
	[Install]
	WantedBy=multi-user.target
`

	serviceName := "aos_" + serviceID + ".service"

	if err = db.AddService(database.ServiceEntry{ID: serviceID, ServiceName: serviceName}); err != nil {
		return err
	}

	fileName, err := filepath.Abs(path.Join("tmp", serviceName))
	if err != nil {
		return err
	}

	if err = ioutil.WriteFile(fileName, []byte(serviceContent), 0644); err != nil {
		return err
	}

	if _, err = systemd.LinkUnitFiles([]string{fileName}, false, true); err != nil {
		return err
	}

	if err = systemd.Reload(); err != nil {
		return err
	}

	return nil
}

func startService(serviceID string) (err error) {
	channel := make(chan string)

	if _, err = systemd.RestartUnit("aos_"+serviceID+".service", "replace", channel); err != nil {
		return err
	}

	<-channel

	return nil
}

func stopService(serviceID string) (err error) {
	channel := make(chan string)

	if _, err = systemd.StopUnit("aos_"+serviceID+".service", "replace", channel); err != nil {
		return err
	}

	<-channel

	return nil
}

func crashService(serviceID string) {
	systemd.KillUnit("aos_"+serviceID+".service", int32(syscall.SIGSEGV))
}

func getTimeRange(logData string) (from, till time.Time, err error) {
	list := strings.Split(logData, "\n")

	if len(list) < 2 || len(list[0]) < 37 || len(list[len(list)-2]) < 37 {
		return from, till, errors.New("bad log data")
	}
	if from, err = time.Parse("2006-01-02 15:04:05.999999999Z07:00", list[0][1:36]); err != nil {
		return from, till, err
	}

	if till, err = time.Parse("2006-01-02 15:04:05.999999999Z07:00", list[len(list)-2][1:36]); err != nil {
		return from, till, err
	}

	return from, till, nil
}

func checkReceivedLog(t *testing.T, logChannel chan amqp.PushServiceLog, from, till time.Time) {
	receivedLog := ""

	for {
		select {
		case result := <-logChannel:
			if result.Error != nil {
				t.Errorf("Error log received: %s", *result.Error)
				return
			}

			if result.Data == nil {
				t.Error("No data")
				return
			}

			zr, err := gzip.NewReader(bytes.NewBuffer(*result.Data))
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

			if *result.Part == *result.PartCount {
				logFrom, logTill, err := getTimeRange(receivedLog)
				if err != nil {
					t.Errorf("Can't get log time range: %s", err)
					return
				}

				if logFrom.Before(from) {
					t.Error("Log range out of requested")
				}

				if logTill.After(till) {
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

func TestGetServiceLog(t *testing.T) {
	logging, err := logging.New(&config.Config{Logging: config.Logging{MaxPartSize: 1024}}, db)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	from := time.Now()

	if err = createService("service0"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	if err = startService("service0"); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(10 * time.Second)

	if err = stopService("service0"); err != nil {
		t.Fatalf("Can't stop service: %s", err)
	}

	till := from.Add(5 * time.Second)

	logging.GetServiceLog(amqp.RequestServiceLog{
		ServiceID: "service0",
		LogID:     "log0",
		From:      &from,
		Till:      &till})

	checkReceivedLog(t, logging.LogChannel, from, till)

	logging.GetServiceLog(amqp.RequestServiceLog{
		ServiceID: "service0",
		LogID:     "log0",
		From:      &from})

	checkReceivedLog(t, logging.LogChannel, from, time.Now())
}

func TestGetWrongServiceLog(t *testing.T) {
	logging, err := logging.New(&config.Config{Logging: config.Logging{MaxPartSize: 1024}}, db)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	till := time.Now()
	from := till.Add(-1 * time.Hour)

	logging.GetServiceLog(amqp.RequestServiceLog{
		ServiceID: "nonExisting",
		LogID:     "log1",
		From:      &from,
		Till:      &till})

	select {
	case result := <-logging.LogChannel:
		if result.Error == nil {
			log.Error("Expect log error")
		}

	case <-time.After(5 * time.Second):
		log.Errorf("Receive log timeout")
	}
}

func TestGetServiceCrashLog(t *testing.T) {
	logging, err := logging.New(&config.Config{Logging: config.Logging{MaxPartSize: 1024}}, db)
	if err != nil {
		t.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	if err = createService("service1"); err != nil {
		t.Fatalf("Can't create service: %s", err)
	}

	from := time.Now()

	if err = startService("service1"); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	time.Sleep(5 * time.Second)

	crashService("service1")

	till := time.Now()

	time.Sleep(1 * time.Second)

	logging.GetServiceCrashLog(amqp.RequestServiceCrashLog{
		ServiceID: "service1",
		LogID:     "log2"})

	checkReceivedLog(t, logging.LogChannel, from, till)
}
