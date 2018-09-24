package monitoring

import (
	"os"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
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
 * Private
 ******************************************************************************/

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestAlertProcessor(t *testing.T) {
	var sourceValue uint64
	destination := make([]amqp.AlertData, 0, 2)

	alert := createAlertProcessor(
		"Test",
		&sourceValue,
		&destination,
		config.AlertRule{
			MinTimeout:   config.Duration{Duration: 3 * time.Second},
			MinThreshold: 80,
			MaxThreshold: 90})

	values := []uint64{50, 91, 79, 92, 93, 94, 95, 94, 79, 91, 92, 93, 94, 32, 91, 92, 93, 94, 95, 96}
	alertsCount := []int{0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2}

	currentTime := time.Now()

	for i, value := range values {
		sourceValue = value
		alert.checkAlertDetection(currentTime)
		if alertsCount[i] != len(destination) {
			t.Errorf("Wrong alert count %d at %d", len(destination), i)
		}
		currentTime = currentTime.Add(time.Second)
	}
}

func TestPeriodicReport(t *testing.T) {
	sendDuration := 1 * time.Second

	monitor, err := New(&config.Config{
		WorkingDir: ".",
		Monitoring: config.Monitoring{
			SendPeriod: config.Duration{Duration: sendDuration},
			PollPeriod: config.Duration{Duration: 1 * time.Second}}})
	if err != nil {
		t.Fatalf("Can't create monitoring instance: %s", err)
	}
	defer monitor.Close()

	timer := time.NewTimer(sendDuration * 2)
	numSends := 3
	sendTime := time.Now()

	for {
		select {
		case <-monitor.DataChannel:
			currentTime := time.Now()

			period := currentTime.Sub(sendTime)
			// check is period in +-10% range
			if period > sendDuration*110/100 || period < sendDuration*90/100 {
				t.Errorf("Period mismatch: %s", period)
			}

			sendTime = currentTime
			timer.Reset(sendDuration * 2)
			numSends--
			if numSends == 0 {
				return
			}

		case <-timer.C:
			t.Fatal("Monitoring data timeout")
		}
	}
}

func TestSystemAlerts(t *testing.T) {
	sendDuration := 1 * time.Second

	monitor, err := New(
		&config.Config{
			WorkingDir: ".",
			Monitoring: config.Monitoring{
				SendPeriod:          config.Duration{Duration: sendDuration},
				PollPeriod:          config.Duration{Duration: 1 * time.Second},
				MaxAlertsPerMessage: 10,
				CPU: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				RAM: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				UsedDisk: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0}}})
	if err != nil {
		t.Fatalf("Can't create monitoring instance: %s", err)
	}
	defer monitor.Close()

	for {
		select {
		case data := <-monitor.DataChannel:
			if len(data.Global.Alerts.CPU) != 1 {
				t.Errorf("Wrong number of CPU alerts: %d", len(data.Global.Alerts.CPU))
			}
			if len(data.Global.Alerts.RAM) != 1 {
				t.Errorf("Wrong number of RAM alerts: %d", len(data.Global.Alerts.RAM))
			}
			if len(data.Global.Alerts.UsedDisk) != 1 {
				t.Errorf("Wrong number of Disk alerts: %d", len(data.Global.Alerts.UsedDisk))
			}
			return

		case <-time.After(sendDuration * 2):
			t.Fatal("Monitoring data timeout")
		}
	}
}
