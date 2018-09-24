// Package monitoring AOS Core Monitoring Component
package monitoring

import (
	"container/list"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/mem"
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	dataChannelSize = 32
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// ServiceMonitoringItf provides API to start/stop service monitoring
type ServiceMonitoringItf interface {
	StartMonitorService(serviceID string, monitoringConfig ServiceMonitoringConfig) (err error)
	StopMonitorService(serviceID string) (err error)
}

// Monitor instance
type Monitor struct {
	DataChannel chan amqp.MonitoringData

	config     config.Monitoring
	workingDir string

	sendTimer *time.Ticker
	pollTimer *time.Ticker

	mutex      sync.Mutex
	dataToSend amqp.MonitoringData

	alertProcessors *list.List
}

// ServiceMonitoringConfig contains info about service and rules for monitoring alerts
type ServiceMonitoringConfig struct {
	Pid          int32
	WorkingDir   string
	ServiceRules *amqp.ServiceAlertRules
}

/*******************************************************************************
 * Variable
 ******************************************************************************/

// ErrDisabled indicates that monitoring is disable in the config
var ErrDisabled = errors.New("Monitoring is disabled")

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new monitor instance
func New(config *config.Config) (monitor *Monitor, err error) {
	log.Debug("Create monitor")

	if config.Monitoring.Disabled {
		return nil, ErrDisabled
	}

	monitor = &Monitor{}

	monitor.DataChannel = make(chan amqp.MonitoringData, dataChannelSize)

	monitor.config = config.Monitoring
	monitor.workingDir = config.WorkingDir

	monitor.alertProcessors = list.New()

	monitor.dataToSend.Global.Alerts.CPU = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	monitor.dataToSend.Global.Alerts.RAM = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	monitor.dataToSend.Global.Alerts.UsedDisk = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	monitor.dataToSend.Global.Alerts.InTraffic = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	monitor.dataToSend.Global.Alerts.OutTraffic = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)

	if config.Monitoring.CPU != nil {
		monitor.alertProcessors.PushBack(createAlertProcessor(
			"System CPU",
			&monitor.dataToSend.Global.CPU,
			&monitor.dataToSend.Global.Alerts.CPU,
			*config.Monitoring.CPU))
	}

	if config.Monitoring.RAM != nil {
		monitor.alertProcessors.PushBack(createAlertProcessor(
			"System RAM",
			&monitor.dataToSend.Global.RAM,
			&monitor.dataToSend.Global.Alerts.RAM,
			*config.Monitoring.RAM))
	}

	if config.Monitoring.UsedDisk != nil {
		monitor.alertProcessors.PushBack(createAlertProcessor(
			"System Disk",
			&monitor.dataToSend.Global.UsedDisk,
			&monitor.dataToSend.Global.Alerts.UsedDisk,
			*config.Monitoring.UsedDisk))
	}

	monitor.pollTimer = time.NewTicker(monitor.config.PollPeriod.Duration)
	monitor.sendTimer = time.NewTicker(monitor.config.SendPeriod.Duration)

	go monitor.run()

	return monitor, nil
}

// Close closes monitor instance
func (monitor *Monitor) Close() {
	log.Debug("Close monitor")

	monitor.sendTimer.Stop()
	monitor.pollTimer.Stop()
}

// StartMonitorService starts monitoring service
func (monitor *Monitor) StartMonitorService(serviceID string, monitoringConfig ServiceMonitoringConfig) (err error) {
	log.WithField("id", serviceID).Debug("Start service monitoring")

	return nil
}

// StopMonitorService stops monitoring service
func (monitor *Monitor) StopMonitorService(serviceID string) (err error) {
	log.WithField("id", serviceID).Debug("Stop service monitoring")

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (monitor *Monitor) run() error {
	for {
		select {
		case <-monitor.sendTimer.C:
			monitor.mutex.Lock()
			monitor.sendMonitoringData()
			monitor.mutex.Unlock()

		case <-monitor.pollTimer.C:
			monitor.mutex.Lock()
			monitor.getCurrentSystemData()
			monitor.processAlerts()
			monitor.mutex.Unlock()
		}
	}
}

func (monitor *Monitor) sendMonitoringData() {
	monitor.DataChannel <- monitor.dataToSend

	// Clear arrays
	monitor.dataToSend.Global.Alerts.CPU = monitor.dataToSend.Global.Alerts.CPU[:0]
	monitor.dataToSend.Global.Alerts.RAM = monitor.dataToSend.Global.Alerts.RAM[:0]
	monitor.dataToSend.Global.Alerts.UsedDisk = monitor.dataToSend.Global.Alerts.UsedDisk[:0]
	monitor.dataToSend.Global.Alerts.InTraffic = monitor.dataToSend.Global.Alerts.InTraffic[:0]
	monitor.dataToSend.Global.Alerts.OutTraffic = monitor.dataToSend.Global.Alerts.OutTraffic[:0]
}

func (monitor *Monitor) getCurrentSystemData() {
	var err error

	monitor.dataToSend.Global.CPU, err = getSystemCPUUsage()
	if err != nil {
		log.Errorf("Can't get system CPU: %s", err)
	}

	monitor.dataToSend.Global.RAM, err = getSystemRAMUsage()
	if err != nil {
		log.Errorf("Can't get system RAM: %s", err)
	}

	monitor.dataToSend.Global.UsedDisk, err = getSystemDiskUsage(monitor.workingDir)
	if err != nil {
		log.Errorf("Can't get system Disk usage: %s", err)
	}

	log.WithFields(log.Fields{
		"CPU":  monitor.dataToSend.Global.CPU,
		"RAM":  monitor.dataToSend.Global.RAM,
		"Disk": monitor.dataToSend.Global.UsedDisk}).Debug("Monitoring data")
}

func (monitor *Monitor) processAlerts() {
	currentTime := time.Now()

	for e := monitor.alertProcessors.Front(); e != nil; e = e.Next() {
		e.Value.(*alertProcessor).checkAlertDetection(currentTime)
	}
}

// getSystemCPUUsage returns CPU usage in parcent
func getSystemCPUUsage() (cpuUse uint64, err error) {
	v, err := cpu.Percent(0, false)
	if err != nil {
		return cpuUse, err
	}

	return uint64(math.RoundToEven(v[0])), nil
}

// getSystemRAMUsage returns RAM usage in bytes
func getSystemRAMUsage() (ram uint64, err error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return ram, err
	}

	return v.Used, nil
}

// getSystemDiskUsage returns disc usage in bytes
func getSystemDiskUsage(path string) (discUse uint64, err error) {
	v, err := disk.Usage(path)
	if err != nil {
		return discUse, err
	}

	return v.Used, nil
}
