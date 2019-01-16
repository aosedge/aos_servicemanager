package monitoring

import (
	"io/ioutil"
	"os"
	"os/exec"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

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

	// Make containers

	serviceConfig := `
{
	"ociVersion": "1.0.0",
	"process": {
		"user": {
			"uid": 0,
			"gid": 0
		},
		"args": [
			"ping", "8.8.8.8", "-c10", "-w10"
		],
		"env": [
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"TERM=xterm"
		],
		"cwd": "/",
		"capabilities": {
			"bounding": [
				"CAP_AUDIT_WRITE",
				"CAP_KILL",
				"CAP_NET_BIND_SERVICE",
				"CAP_NET_RAW"
			],
			"effective": [
				"CAP_AUDIT_WRITE",
				"CAP_KILL",
				"CAP_NET_BIND_SERVICE",
				"CAP_NET_RAW"
			],
			"inheritable": [
				"CAP_AUDIT_WRITE",
				"CAP_KILL",
				"CAP_NET_BIND_SERVICE",
				"CAP_NET_RAW"
			],
			"permitted": [
				"CAP_AUDIT_WRITE",
				"CAP_KILL",
				"CAP_NET_BIND_SERVICE",
				"CAP_NET_RAW"
			],
			"ambient": [
				"CAP_AUDIT_WRITE",
				"CAP_KILL",
				"CAP_NET_BIND_SERVICE",
				"CAP_NET_RAW"
			]
		},
		"rlimits": [
			{
				"type": "RLIMIT_NOFILE",
				"hard": 1024,
				"soft": 1024
			}
		],
		"noNewPrivileges": true
	},
	"root": {
		"path": "rootfs",
		"readonly": true
	},
	"hostname": "runc",
	"mounts": [
		{
			"destination": "/proc",
			"type": "proc",
			"source": "proc"
		},
		{
			"destination": "/dev",
			"type": "tmpfs",
			"source": "tmpfs",
			"options": [
				"nosuid",
				"strictatime",
				"mode=755",
				"size=65536k"
			]
		},
		{
			"destination": "/dev/pts",
			"type": "devpts",
			"source": "devpts",
			"options": [
				"nosuid",
				"noexec",
				"newinstance",
				"ptmxmode=0666",
				"mode=0620",
				"gid=5"
			]
		},
		{
			"destination": "/dev/shm",
			"type": "tmpfs",
			"source": "shm",
			"options": [
				"nosuid",
				"noexec",
				"nodev",
				"mode=1777",
				"size=65536k"
			]
		},
		{
			"destination": "/dev/mqueue",
			"type": "mqueue",
			"source": "mqueue",
			"options": [
				"nosuid",
				"noexec",
				"nodev"
			]
		},
		{
			"destination": "/sys",
			"type": "sysfs",
			"source": "sysfs",
			"options": [
				"nosuid",
				"noexec",
				"nodev",
				"ro"
			]
		},
		{
			"destination": "/sys/fs/cgroup",
			"type": "cgroup",
			"source": "cgroup",
			"options": [
				"nosuid",
				"noexec",
				"nodev",
				"relatime",
				"ro"
			]
		},
		{
			"destination": "/bin",
			"type": "bind",
			"source": "/bin",
			"options": [
				"bind",
				"ro"
			]
		},
		{
			"destination": "/sbin",
			"type": "bind",
			"source": "/sbin",
			"options": [
				"bind",
				"ro"
			]
		},
		{
			"destination": "/lib",
			"type": "bind",
			"source": "/lib",
			"options": [
				"bind",
				"ro"
			]
		},
		{
			"destination": "/usr",
			"type": "bind",
			"source": "/usr",
			"options": [
				"bind",
				"ro"
			]
		},
		{
			"destination": "/tmp",
			"type": "bind",
			"source": "/tmp",
			"options": [
				"bind",
				"rw"
			]
		},
		{
			"destination": "/lib64",
			"type": "bind",
			"source": "/lib64",
			"options": [
				"bind",
				"ro"
			]
		},
		{
			"destination": "/etc/hosts",
			"type": "bind",
			"source": "/etc/hosts",
			"options": [
				"bind",
				"ro"
			]
		},
		{
			"destination": "/etc/resolv.conf",
			"type": "bind",
			"source": "/etc/resolv.conf",
			"options": [
				"bind",
				"ro"
			]
		},
		{
			"destination": "/etc/nsswitch.conf",
			"type": "bind",
			"source": "/etc/nsswitch.conf",
			"options": [
				"bind",
				"ro"
			]
		},
		{
			"destination": "/etc/ssl",
			"type": "bind",
			"source": "/etc/ssl",
			"options": [
				"bind",
				"ro"
			]
		}
	],
	"hooks": {
		"prestart": [
			{
				"path": "/usr/local/bin/netns",
				"args": ["-d"]
			}
		]
	},
	"linux": {
		"resources": {
			"devices": [
				{
					"allow": false,
					"access": "rwm"
				}
			],
			"cpu": {
				"shares": 1024
			},
			"network": {
				"classID": 10
			}
		},
		"namespaces": [
			{
				"type": "pid"
			},
			{
				"type": "network"
			},
			{
				"type": "ipc"
			},
			{
				"type": "uts"
			},
			{
				"type": "mount"
			}
		],
		"maskedPaths": [
			"/proc/kcore",
			"/proc/latency_stats",
			"/proc/timer_list",
			"/proc/timer_stats",
			"/proc/sched_debug",
			"/sys/firmware",
			"/proc/scsi"
		],
		"readonlyPaths": [
			"/proc/asound",
			"/proc/bus",
			"/proc/fs",
			"/proc/irq",
			"/proc/sys",
			"/proc/sysrq-trigger"
		]
	}
}
`
	if err := os.MkdirAll("tmp/service1/rootfs", 0755); err != nil {
		return err
	}

	if err := ioutil.WriteFile("tmp/service1/config.json", []byte(serviceConfig), 0644); err != nil {
		return err
	}

	if err := os.MkdirAll("tmp/service2/rootfs", 0755); err != nil {
		return err
	}

	if err := ioutil.WriteFile("tmp/service2/config.json", []byte(serviceConfig), 0644); err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	db.Close()

	if err := os.RemoveAll("tmp"); err != nil {
		return err
	}

	return nil
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	ret := m.Run()

	if err := cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

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
			MaxOfflineMessages: 10,
			SendPeriod:         config.Duration{Duration: sendDuration},
			PollPeriod:         config.Duration{Duration: 1 * time.Second}}},
		db)
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
				MaxOfflineMessages:  10,
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
					MaxThreshold: 0},
				InTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				OutTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0}}},
		db)
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
			if len(data.Global.Alerts.InTraffic) != 1 {
				t.Errorf("Wrong number of IN traffic alerts: %d", len(data.Global.Alerts.InTraffic))
			}
			if len(data.Global.Alerts.OutTraffic) != 1 {
				t.Errorf("Wrong number of OUT traffic alerts: %d", len(data.Global.Alerts.OutTraffic))
			}
			return

		case <-time.After(sendDuration * 2):
			t.Fatal("Monitoring data timeout")
		}
	}
}

func TestServices(t *testing.T) {
	sendDuration := 2 * time.Second

	monitor, err := New(
		&config.Config{
			WorkingDir: ".",
			Monitoring: config.Monitoring{
				MaxOfflineMessages:  10,
				SendPeriod:          config.Duration{Duration: sendDuration},
				PollPeriod:          config.Duration{Duration: 1 * time.Second},
				MaxAlertsPerMessage: 10}},
		db)
	if err != nil {
		t.Fatalf("Can't create monitoring instance: %s", err)
	}
	defer monitor.Close()

	cmd1 := exec.Command("runc", "run", "--pid-file", "tmp/service1/.pid", "-b", "tmp/service1", "service1")
	cmd2 := exec.Command("runc", "run", "--pid-file", "tmp/service2/.pid", "-b", "tmp/service2", "service2")

	if err := cmd1.Start(); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	if err := cmd2.Start(); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	// Wait while .ip amd .pid files are created
	time.Sleep(1 * time.Second)

	err = monitor.StartMonitorService("Service1",
		ServiceMonitoringConfig{
			ServiceDir: "tmp/service1",
			WorkingDir: ".",
			ServiceRules: &amqp.ServiceAlertRules{
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
					MaxThreshold: 0},
				InTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				OutTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0}}})
	if err != nil {
		t.Fatalf("Can't start monitoring service: %s", err)
	}

	monitor.StartMonitorService("Service2",
		ServiceMonitoringConfig{
			ServiceDir: "tmp/service2",
			WorkingDir: ".",
			ServiceRules: &amqp.ServiceAlertRules{
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
					MaxThreshold: 0},
				InTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0},
				OutTraffic: &config.AlertRule{
					MinTimeout:   config.Duration{},
					MinThreshold: 0,
					MaxThreshold: 0}}})
	if err != nil {
		t.Fatalf("Can't start monitoring service: %s", err)
	}

	terminate := false

	for terminate != true {
		select {
		case data := <-monitor.DataChannel:
			if len(data.ServicesData) != 2 {
				t.Errorf("Wrong number of services: %d", len(data.ServicesData))
			}

			for _, serviceData := range data.ServicesData {
				if len(serviceData.Alerts.CPU) != 1 {
					t.Errorf("Wrong number of CPU alerts: %d", len(serviceData.Alerts.CPU))
				}

				if len(serviceData.Alerts.RAM) != 1 {
					t.Errorf("Wrong number of RAM alerts: %d", len(serviceData.Alerts.RAM))
				}

				if len(serviceData.Alerts.UsedDisk) != 1 {
					t.Errorf("Wrong number of Disk alerts: %d", len(serviceData.Alerts.UsedDisk))
				}
			}

			terminate = true

		case <-time.After(sendDuration * 2):
			t.Fatal("Monitoring data timeout")
		}
	}

	err = monitor.StopMonitorService("Service1")
	if err != nil {
		t.Fatalf("Can't stop monitoring service: %s", err)
	}

	terminate = false

	for terminate != true {
		select {
		case data := <-monitor.DataChannel:
			if len(data.ServicesData) != 1 {
				t.Errorf("Wrong number of services: %d", len(data.ServicesData))
			}

			for _, serviceData := range data.ServicesData {
				if len(serviceData.Alerts.CPU) != 0 {
					t.Errorf("Wrong number of CPU alerts: %d", len(serviceData.Alerts.CPU))
				}

				if len(serviceData.Alerts.RAM) != 0 {
					t.Errorf("Wrong number of RAM alerts: %d", len(serviceData.Alerts.RAM))
				}

				if len(serviceData.Alerts.UsedDisk) != 0 {
					t.Errorf("Wrong number of Disk alerts: %d", len(serviceData.Alerts.UsedDisk))
				}
			}

			terminate = true

		case <-time.After(sendDuration * 2):
			t.Fatal("Monitoring data timeout")
		}
	}

	err = monitor.StopMonitorService("Service2")
	if err != nil {
		t.Fatalf("Can't stop monitoring service: %s", err)
	}

	if err := cmd1.Wait(); err != nil {
		t.Errorf("Can't wait for service: %s", err)
	}

	if err := cmd2.Wait(); err != nil {
		t.Errorf("Can't wait for service: %s", err)
	}
}

func TestTrafficLimit(t *testing.T) {
	sendDuration := 2 * time.Second

	monitor, err := New(
		&config.Config{
			WorkingDir: ".",
			Monitoring: config.Monitoring{
				MaxOfflineMessages:  256,
				SendPeriod:          config.Duration{Duration: sendDuration},
				PollPeriod:          config.Duration{Duration: 1 * time.Second},
				MaxAlertsPerMessage: 10}},
		db)
	if err != nil {
		t.Fatalf("Can't create monitoring instance: %s", err)
	}
	defer monitor.Close()

	monitor.trafficPeriod = MinutePeriod

	// wait for beginning of next minute
	time.Sleep(time.Duration((60 - time.Now().Second())) * time.Second)

	cmd1 := exec.Command("runc", "run", "--pid-file", "tmp/service1/.pid", "-b", "tmp/service1", "service1")

	if err := cmd1.Start(); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	// Wait while .ip amd .pid files are created
	time.Sleep(1 * time.Second)

	err = monitor.StartMonitorService("Service1",
		ServiceMonitoringConfig{
			ServiceDir:    "tmp/service1",
			WorkingDir:    ".",
			UploadLimit:   300,
			DownloadLimit: 300})
	if err != nil {
		t.Fatalf("Can't start monitoring service: %s", err)
	}

	if err := cmd1.Wait(); err == nil {
		t.Error("Ping should fail")
	}

	err = monitor.StopMonitorService("Service1")
	if err != nil {
		t.Fatalf("Can't stop monitoring service: %s", err)
	}

	// wait for beginning of next minute
	time.Sleep(time.Duration((60 - time.Now().Second())) * time.Second)

	// Start again

	cmd1 = exec.Command("runc", "run", "--pid-file", "tmp/service1/.pid", "-b", "tmp/service1", "service1")

	if err := cmd1.Start(); err != nil {
		t.Fatalf("Can't start service: %s", err)
	}

	// Wait while .ip amd .pid files are created
	time.Sleep(1 * time.Second)

	err = monitor.StartMonitorService("Service1",
		ServiceMonitoringConfig{
			ServiceDir:    "tmp/service1",
			WorkingDir:    ".",
			UploadLimit:   2000,
			DownloadLimit: 2000})
	if err != nil {
		t.Fatalf("Can't start monitoring service: %s", err)
	}

	if err := cmd1.Wait(); err != nil {
		t.Errorf("Wait for service error: %s", err)
	}

	err = monitor.StopMonitorService("Service1")
	if err != nil {
		t.Fatalf("Can't stop monitoring service: %s", err)
	}
}
