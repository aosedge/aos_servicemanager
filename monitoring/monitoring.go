// Package monitoring AOS Core Monitoring Component
package monitoring

import (
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
			monitor.mutex.Unlock()
		}
	}
}

func (monitor *Monitor) sendMonitoringData() {
	monitor.DataChannel <- monitor.dataToSend
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
