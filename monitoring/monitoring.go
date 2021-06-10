// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
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

// Package monitoring AOS Core Monitoring Component
package monitoring

import (
	"container/list"
	"errors"
	"hash/fnv"
	"io/ioutil"
	"math"
	"path"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/load"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/process"
	log "github.com/sirupsen/logrus"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/platform"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

// Service status
const (
	MinutePeriod = iota
	HourPeriod
	DayPeriod
	MonthPeriod
	YearPeriod
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// ResourceAlertSender interface to send resource alerts
type ResourceAlertSender interface {
	SendResourceAlert(source, resource string, time time.Time, value uint64)
}

type TrafficMonitoring interface {
	GetSystemTraffic() (inputTraffic, outputTraffic uint64, err error)
	GetServiceTraffic(serviceID string) (inputTraffic, outputTraffic uint64, err error)
}

// Monitor instance
type Monitor struct {
	DataChannel chan amqp.MonitoringData

	resourceAlerts ResourceAlertSender

	config     config.Monitoring
	workingDir string
	storageDir string

	sendTimer *time.Ticker
	pollTimer *time.Ticker

	sync.Mutex

	dataToSend amqp.MonitoringData

	alertProcessors *list.List

	serviceMap        map[string]*serviceMonitoring
	trafficMonitoring TrafficMonitoring
}

// ServiceMonitoringConfig contains info about service and rules for monitoring alerts
type ServiceMonitoringConfig struct {
	ServiceDir   string
	IPAddress    string
	UID          uint32
	GID          uint32
	ServiceRules *amqp.ServiceAlertRules
}

type trafficMonitoring struct {
	disabled     bool
	addresses    string
	currentValue uint64
	initialValue uint64
	subValue     uint64
	limit        uint64
	lastUpdate   time.Time
}

type serviceMonitoring struct {
	serviceDir             string
	uid                    uint32
	gid                    uint32
	monitoringData         amqp.ServiceMonitoringData
	alertProcessorElements []*list.Element
}

/*******************************************************************************
 * Variable
 ******************************************************************************/

// ErrDisabled indicates that monitoring is disable in the config
var ErrDisabled = errors.New("monitoring is disabled")

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new monitor instance
func New(config *config.Config, resourceAlerts ResourceAlertSender, trafficMonitoring TrafficMonitoring) (monitor *Monitor, err error) {
	log.Debug("Create monitor")

	if config.Monitoring.Disabled {
		return nil, ErrDisabled
	}

	monitor = &Monitor{resourceAlerts: resourceAlerts, trafficMonitoring: trafficMonitoring}

	monitor.DataChannel = make(chan amqp.MonitoringData, config.Monitoring.MaxOfflineMessages)

	monitor.config = config.Monitoring
	monitor.workingDir = config.WorkingDir
	monitor.storageDir = config.StorageDir

	monitor.alertProcessors = list.New()

	monitor.dataToSend.ServicesData = make([]amqp.ServiceMonitoringData, 0)

	if monitor.resourceAlerts != nil {
		if config.Monitoring.CPU != nil {
			monitor.alertProcessors.PushBack(createAlertProcessor(
				"System CPU",
				&monitor.dataToSend.Global.CPU,
				func(time time.Time, value uint64) {
					monitor.resourceAlerts.SendResourceAlert("system", "cpu", time, value)
				},
				*config.Monitoring.CPU))
		}

		if config.Monitoring.RAM != nil {
			monitor.alertProcessors.PushBack(createAlertProcessor(
				"System RAM",
				&monitor.dataToSend.Global.RAM,
				func(time time.Time, value uint64) {
					monitor.resourceAlerts.SendResourceAlert("system", "ram", time, value)
				},
				*config.Monitoring.RAM))
		}

		if config.Monitoring.UsedDisk != nil {
			monitor.alertProcessors.PushBack(createAlertProcessor(
				"System Disk",
				&monitor.dataToSend.Global.UsedDisk,
				func(time time.Time, value uint64) {
					monitor.resourceAlerts.SendResourceAlert("system", "disk", time, value)
				},
				*config.Monitoring.UsedDisk))
		}

		if config.Monitoring.InTraffic != nil {
			monitor.alertProcessors.PushBack(createAlertProcessor(
				"IN Traffic",
				&monitor.dataToSend.Global.InTraffic,
				func(time time.Time, value uint64) {
					monitor.resourceAlerts.SendResourceAlert("system", "inTraffic", time, value)
				},
				*config.Monitoring.InTraffic))
		}

		if config.Monitoring.OutTraffic != nil {
			monitor.alertProcessors.PushBack(createAlertProcessor(
				"OUT Traffic",
				&monitor.dataToSend.Global.OutTraffic,
				func(time time.Time, value uint64) {
					monitor.resourceAlerts.SendResourceAlert("system", "outTraffic", time, value)
				},
				*config.Monitoring.OutTraffic))
		}
	}

	monitor.serviceMap = make(map[string]*serviceMonitoring)

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
	monitor.Lock()
	defer monitor.Unlock()

	load.Misc()

	if _, ok := monitor.serviceMap[serviceID]; ok {
		log.WithField("id", serviceID).Warning("Service already under monitoring")
		return nil
	}

	log.WithFields(log.Fields{
		"id": serviceID,
		"ip": monitoringConfig.IPAddress}).Debug("Start service monitoring")

	// convert id to hashed u64 value
	hash := fnv.New64a()
	hash.Write([]byte(serviceID))

	serviceMonitoring := serviceMonitoring{
		serviceDir: monitoringConfig.ServiceDir,
		uid:        monitoringConfig.UID,
		gid:        monitoringConfig.GID,
		monitoringData: amqp.ServiceMonitoringData{
			ServiceID: serviceID}}

	rules := monitoringConfig.ServiceRules

	if monitor.resourceAlerts != nil {
		// For optimization capacity should be equals numbers of measurement values
		// 5 - RAM, CPU, UsedDisk, InTraffic, OutTraffic
		serviceMonitoring.alertProcessorElements = make([]*list.Element, 0, 5)

		if rules != nil && rules.CPU != nil {
			e := monitor.alertProcessors.PushBack(createAlertProcessor(
				serviceID+" CPU",
				&serviceMonitoring.monitoringData.CPU,
				func(time time.Time, value uint64) {
					monitor.resourceAlerts.SendResourceAlert(serviceID, "cpu", time, value)
				},
				*rules.CPU))

			serviceMonitoring.alertProcessorElements = append(serviceMonitoring.alertProcessorElements, e)
		}

		if rules != nil && rules.RAM != nil {
			e := monitor.alertProcessors.PushBack(createAlertProcessor(
				serviceID+" RAM",
				&serviceMonitoring.monitoringData.RAM,
				func(time time.Time, value uint64) {
					monitor.resourceAlerts.SendResourceAlert(serviceID, "ram", time, value)
				},
				*rules.RAM))

			serviceMonitoring.alertProcessorElements = append(serviceMonitoring.alertProcessorElements, e)
		}

		if rules != nil && rules.UsedDisk != nil {
			e := monitor.alertProcessors.PushBack(createAlertProcessor(
				serviceID+" Disk",
				&serviceMonitoring.monitoringData.UsedDisk,
				func(time time.Time, value uint64) {
					monitor.resourceAlerts.SendResourceAlert(serviceID, "disk", time, value)
				},
				*rules.UsedDisk))

			serviceMonitoring.alertProcessorElements = append(serviceMonitoring.alertProcessorElements, e)
		}

		if rules != nil && rules.InTraffic != nil {
			e := monitor.alertProcessors.PushBack(createAlertProcessor(
				serviceID+" Traffic IN",
				&serviceMonitoring.monitoringData.InTraffic,
				func(time time.Time, value uint64) {
					monitor.resourceAlerts.SendResourceAlert(serviceID, "inTraffic", time, value)
				},
				*rules.InTraffic))

			serviceMonitoring.alertProcessorElements = append(serviceMonitoring.alertProcessorElements, e)
		}

		if rules != nil && rules.OutTraffic != nil {
			e := monitor.alertProcessors.PushBack(createAlertProcessor(
				serviceID+" Traffic OUT",
				&serviceMonitoring.monitoringData.OutTraffic,
				func(time time.Time, value uint64) {
					monitor.resourceAlerts.SendResourceAlert(serviceID, "outTraffic", time, value)
				},
				*rules.OutTraffic))

			serviceMonitoring.alertProcessorElements = append(serviceMonitoring.alertProcessorElements, e)
		}
	}

	monitor.serviceMap[serviceID] = &serviceMonitoring

	return nil
}

// StopMonitorService stops monitoring service
func (monitor *Monitor) StopMonitorService(serviceID string) (err error) {
	monitor.Lock()
	defer monitor.Unlock()

	log.WithField("id", serviceID).Debug("Stop service monitoring")

	if _, ok := monitor.serviceMap[serviceID]; !ok {
		return nil
	}

	for _, e := range monitor.serviceMap[serviceID].alertProcessorElements {
		monitor.alertProcessors.Remove(e)
	}

	delete(monitor.serviceMap, serviceID)

	return nil
}

// GetServicePid returns service PID
func GetServicePid(servicePath string) (pid int32, err error) {
	pidStr, err := ioutil.ReadFile(path.Join(servicePath, ".pid"))
	if err != nil {
		return pid, err
	}

	pid64, err := strconv.ParseInt(string(pidStr), 10, 0)
	if err != nil {
		return pid, err
	}

	return int32(pid64), nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (monitor *Monitor) run() error {
	for {
		select {
		case <-monitor.sendTimer.C:
			monitor.Lock()
			monitor.sendMonitoringData()
			monitor.Unlock()

		case <-monitor.pollTimer.C:
			monitor.Lock()
			monitor.getCurrentSystemData()
			monitor.getCurrentServicesData()
			monitor.processAlerts()
			monitor.Unlock()
		}
	}
}

func (monitor *Monitor) sendMonitoringData() {
	// Update services
	monitor.dataToSend.ServicesData = make([]amqp.ServiceMonitoringData, 0, len(monitor.serviceMap))

	for _, service := range monitor.serviceMap {
		monitor.dataToSend.ServicesData = append(monitor.dataToSend.ServicesData, service.monitoringData)
	}

	if len(monitor.DataChannel) < cap(monitor.DataChannel) {
		monitor.dataToSend.Timestamp = time.Now()
		monitor.DataChannel <- monitor.dataToSend
	} else {
		log.Warn("Skip sending monitoring data. Channel full.")
	}
}

func (monitor *Monitor) getCurrentSystemData() {
	cpu, err := getSystemCPUUsage()
	if err != nil {
		log.Errorf("Can't get system CPU: %s", err)
	}

	monitor.dataToSend.Global.CPU = uint64(math.Round(cpu))

	monitor.dataToSend.Global.RAM, err = getSystemRAMUsage()
	if err != nil {
		log.Errorf("Can't get system RAM: %s", err)
	}

	monitor.dataToSend.Global.UsedDisk, err = getSystemDiskUsage(monitor.workingDir)
	if err != nil {
		log.Errorf("Can't get system Disk usage: %s", err)
	}

	monitor.dataToSend.Global.InTraffic, monitor.dataToSend.Global.OutTraffic, err = monitor.trafficMonitoring.GetSystemTraffic()
	if err != nil {
		log.Errorf("Can't get system traffic value: %s", err)
	}

	log.WithFields(log.Fields{
		"CPU":  monitor.dataToSend.Global.CPU,
		"RAM":  monitor.dataToSend.Global.RAM,
		"Disk": monitor.dataToSend.Global.UsedDisk,
		"IN":   monitor.dataToSend.Global.InTraffic,
		"OUT":  monitor.dataToSend.Global.OutTraffic}).Debug("Monitoring data")
}

func (monitor *Monitor) getCurrentServicesData() {
	for serviceID, value := range monitor.serviceMap {
		cpuUsage, err := getServiceCPUUsage(int32(value.uid))
		if err != nil {
			log.Errorf("Can't get service CPU: %s", err)
		}

		value.monitoringData.CPU = uint64(math.Round(cpuUsage / float64(runtime.NumCPU())))

		value.monitoringData.RAM, err = getServiceRAMUsage(int32(value.uid))
		if err != nil {
			log.Errorf("Can't get service RAM: %s", err)
		}

		value.monitoringData.UsedDisk, err = getServiceDiskUsage(monitor.storageDir, value.uid, value.gid)
		if err != nil {
			log.Errorf("Can't get service Disc usage: %s", err)
		}

		value.monitoringData.InTraffic, value.monitoringData.OutTraffic, err = monitor.trafficMonitoring.GetServiceTraffic(serviceID)
		if err != nil {
			log.Errorf("Can't get service traffic: %s", err)
		}

		log.WithFields(log.Fields{
			"id":   serviceID,
			"CPU":  value.monitoringData.CPU,
			"RAM":  value.monitoringData.RAM,
			"Disk": value.monitoringData.UsedDisk,
			"IN":   value.monitoringData.InTraffic,
			"OUT":  value.monitoringData.OutTraffic}).Debug("Service monitoring data")
	}
}

func (monitor *Monitor) processAlerts() {
	currentTime := time.Now()

	for e := monitor.alertProcessors.Front(); e != nil; e = e.Next() {
		e.Value.(*alertProcessor).checkAlertDetection(currentTime)
	}
}

// getSystemCPUUsage returns CPU usage in parcent
func getSystemCPUUsage() (cpuUse float64, err error) {
	v, err := cpu.Percent(0, false)
	if err != nil {
		return 0, err
	}

	cpuUse = v[0]

	return cpuUse, nil
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

// getServiceCPUUsage returns service CPU usage in percent
func getServiceCPUUsage(uid int32) (cpuUse float64, err error) {
	processes, err := process.Processes()
	if err != nil {
		return 0, err
	}

	for _, process := range processes {
		uids, err := process.Uids()
		if err != nil {
			continue
		}

		for _, id := range uids {
			if id == uid {
				cpu, err := process.CPUPercent()
				if err != nil {
					return 0, err
				}

				cpuUse += cpu
				break
			}
		}
	}

	return cpuUse, nil
}

// getServiceRAMUsage returns service RAM usage in bytes
func getServiceRAMUsage(uid int32) (ram uint64, err error) {
	processes, err := process.Processes()
	if err != nil {
		return 0, err
	}

	for _, process := range processes {
		uids, err := process.Uids()
		if err != nil {
			continue
		}

		for _, id := range uids {
			if id == uid {
				memInfo, err := process.MemoryInfo()
				if err != nil {
					return 0, err
				}

				ram += memInfo.RSS
				break
			}
		}
	}

	return ram, nil
}

// getServiceDiskUsage returns service disk usage in bytes
func getServiceDiskUsage(path string, uid, gid uint32) (diskUse uint64, err error) {
	if diskUse, err = platform.GetUserFSQuotaUsage(path, uid, gid); err != nil {
		return diskUse, err
	}

	return diskUse, nil
}
