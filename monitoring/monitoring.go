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
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-iptables/iptables"
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

// TrafficStorage provides API to create, remove or access monitoring data
type TrafficStorage interface {
	SetTrafficMonitorData(chain string, timestamp time.Time, value uint64) (err error)
	GetTrafficMonitorData(chain string) (timestamp time.Time, value uint64, err error)
	RemoveTrafficMonitorData(chain string) (err error)
}

// ResourceAlertSender interface to send resource alerts
type ResourceAlertSender interface {
	SendResourceAlert(source, resource string, time time.Time, value uint64)
}

// Monitor instance
type Monitor struct {
	DataChannel chan amqp.MonitoringData

	trafficStorage TrafficStorage
	resourceAlerts ResourceAlertSender

	iptables      *iptables.IPTables
	trafficPeriod int
	skipAddresses string
	inChain       string
	outChain      string
	trafficMap    map[string]*trafficMonitoring

	config     config.Monitoring
	workingDir string
	storageDir string

	sendTimer *time.Ticker
	pollTimer *time.Ticker

	sync.Mutex

	dataToSend amqp.MonitoringData

	alertProcessors *list.List

	serviceMap map[string]*serviceMonitoring
}

// ServiceMonitoringConfig contains info about service and rules for monitoring alerts
type ServiceMonitoringConfig struct {
	ServiceDir    string
	User          string
	UploadLimit   uint64
	DownloadLimit uint64
	ServiceRules  *amqp.ServiceAlertRules
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
	user                   string
	inChain                string
	outChain               string
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
func New(config *config.Config, trafficStorage TrafficStorage,
	resourceAlerts ResourceAlertSender) (monitor *Monitor, err error) {
	log.Debug("Create monitor")

	if config.Monitoring.Disabled {
		return nil, ErrDisabled
	}

	monitor = &Monitor{trafficStorage: trafficStorage, resourceAlerts: resourceAlerts}

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

	if err = monitor.setupTrafficMonitor(); err != nil {
		return nil, err
	}

	monitor.processTraffic()

	go monitor.run()

	return monitor, nil
}

// Close closes monitor instance
func (monitor *Monitor) Close() {
	log.Debug("Close monitor")

	monitor.sendTimer.Stop()
	monitor.pollTimer.Stop()

	monitor.deleteAllTrafficChains()
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

	ipAddress, err := GetServiceIPAddress(monitoringConfig.ServiceDir)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"id": serviceID,
		"ip": ipAddress}).Debug("Start service monitoring")

	// convert id to hashed u64 value
	hash := fnv.New64a()
	hash.Write([]byte(serviceID))

	// create base chain name
	chainBase := strconv.FormatUint(hash.Sum64(), 16)

	serviceMonitoring := serviceMonitoring{
		serviceDir: monitoringConfig.ServiceDir,
		user:       monitoringConfig.User,
		inChain:    "AOS_" + chainBase + "_IN",
		outChain:   "AOS_" + chainBase + "_OUT",
		monitoringData: amqp.ServiceMonitoringData{
			ServiceID: serviceID}}

	if err = monitor.createTrafficChain(serviceMonitoring.inChain, "FORWARD", ipAddress); err != nil {
		return err
	}

	monitor.trafficMap[serviceMonitoring.inChain].limit = monitoringConfig.DownloadLimit

	if err = monitor.createTrafficChain(serviceMonitoring.outChain, "FORWARD", ipAddress); err != nil {
		return err
	}

	monitor.trafficMap[serviceMonitoring.outChain].limit = monitoringConfig.UploadLimit

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

	monitor.processTraffic()

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

	if err = monitor.deleteTrafficChain(monitor.serviceMap[serviceID].inChain, "FORWARD"); err != nil {
		log.WithField("id", serviceID).Errorf("Can't delete chain: %s", err)
	}

	if err = monitor.deleteTrafficChain(monitor.serviceMap[serviceID].outChain, "FORWARD"); err != nil {
		log.WithField("id", serviceID).Errorf("Can't delete chain: %s", err)
	}

	for _, e := range monitor.serviceMap[serviceID].alertProcessorElements {
		monitor.alertProcessors.Remove(e)
	}

	delete(monitor.serviceMap, serviceID)

	return nil
}

// GetServiceIPAddress returns ip address assigned to service
func GetServiceIPAddress(servicePath string) (address string, err error) {
	data, err := ioutil.ReadFile(path.Join(servicePath, ".ip"))
	if err != nil {
		return address, err
	}

	address = string(data)

	return address, nil
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
			monitor.saveTraffic()
			monitor.Unlock()

		case <-monitor.pollTimer.C:
			monitor.Lock()
			monitor.processTraffic()
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

	if traffic, ok := monitor.trafficMap[monitor.inChain]; ok {
		monitor.dataToSend.Global.InTraffic = traffic.currentValue
	} else {
		log.WithField("chain", monitor.inChain).Error("Can't get service traffic value")
	}

	if traffic, ok := monitor.trafficMap[monitor.outChain]; ok {
		monitor.dataToSend.Global.OutTraffic = traffic.currentValue
	} else {
		log.WithField("chain", monitor.outChain).Error("Can't get service traffic value")
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
		err := monitor.updateServiceParams(serviceID, value)
		if err != nil {
			log.Errorf("Can't update service params: %s", err)
		}

		cpuUsage, err := getServiceCPUUsage(value.user)
		if err != nil {
			log.Errorf("Can't get service CPU: %s", err)
		}

		value.monitoringData.CPU = uint64(math.Round(cpuUsage / float64(runtime.NumCPU())))

		value.monitoringData.RAM, err = getServiceRAMUsage(value.user)
		if err != nil {
			log.Errorf("Can't get service RAM: %s", err)
		}

		value.monitoringData.UsedDisk, err = getServiceDiskUsage(monitor.storageDir, value.user)
		if err != nil {
			log.Errorf("Can't get service Disc usage: %s", err)
		}

		if traffic, ok := monitor.trafficMap[value.inChain]; ok {
			value.monitoringData.InTraffic = traffic.currentValue
		} else {
			log.WithField("chain", value.inChain).Error("Can't get service traffic value")
		}

		if traffic, ok := monitor.trafficMap[value.outChain]; ok {
			value.monitoringData.OutTraffic = traffic.currentValue
		} else {
			log.WithField("chain", value.outChain).Error("Can't get service traffic value")
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

func (monitor *Monitor) isSamePeriod(t1, t2 time.Time) (result bool) {
	y1, m1, d1 := t1.Date()
	h1 := t1.Hour()
	min1 := t1.Minute()

	y2, m2, d2 := t2.Date()
	h2 := t2.Hour()
	min2 := t2.Minute()

	switch monitor.trafficPeriod {
	case MinutePeriod:
		return y1 == y2 && m1 == m2 && d1 == d2 && h1 == h2 && min1 == min2

	case HourPeriod:
		return y1 == y2 && m1 == m2 && d1 == d2 && h1 == h2

	case DayPeriod:
		return y1 == y2 && m1 == m2 && d1 == d2

	case MonthPeriod:
		return y1 == y2 && m1 == m2

	case YearPeriod:
		return y1 == y2

	default:
		return false
	}
}

func (monitor *Monitor) processTraffic() {
	timestamp := time.Now().UTC()

	for chain, traffic := range monitor.trafficMap {
		var value uint64
		var err error

		if !traffic.disabled {
			value, err = monitor.getTrafficChainBytes(chain)
			if err != nil {
				log.WithField("chain", chain).Errorf("Can't get chain byte count: %s", err)
				continue
			}
		}

		if !monitor.isSamePeriod(timestamp, traffic.lastUpdate) {
			log.WithField("chain", chain).Debug("Reset stats")
			// we count statistics per day, if date is different then reset stats
			traffic.initialValue = 0
			traffic.subValue = value
		}

		// initialValue is used to keep traffic between resets
		// Unfortunately, github.com/coreos/go-iptables/iptables doesn't provide API to reset chain statistics.
		// We use subValue to reset statistics.
		traffic.currentValue = traffic.initialValue + value - traffic.subValue
		traffic.lastUpdate = timestamp

		if traffic.limit != 0 {
			if traffic.currentValue > traffic.limit && !traffic.disabled {
				// disable chain
				if err := monitor.setChainState(chain, traffic.addresses, false); err != nil {
					log.WithField("chain", chain).Errorf("Can't disable chain: %s", err)
				} else {
					traffic.disabled = true
					traffic.initialValue = traffic.currentValue
					traffic.subValue = 0
				}
			}

			if traffic.currentValue < traffic.limit && traffic.disabled {
				// enable chain
				if err = monitor.setChainState(chain, traffic.addresses, true); err != nil {
					log.WithField("chain", chain).Errorf("Can't enable chain: %s", err)
				} else {
					traffic.disabled = false
					traffic.initialValue = traffic.currentValue
					traffic.subValue = 0
				}
			}
		}
	}
}

func (monitor *Monitor) saveTraffic() {
	for chain, traffic := range monitor.trafficMap {
		if err := monitor.trafficStorage.SetTrafficMonitorData(chain, traffic.lastUpdate, traffic.currentValue); err != nil {
			log.WithField("chain", chain).Errorf("Can't set traffic data: %s", err)
		}
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
func getServiceCPUUsage(user string) (cpuUse float64, err error) {
	processes, err := process.Processes()
	if err != nil {
		return 0, err
	}

	for _, process := range processes {
		processUser, err := process.Username()
		if err != nil {
			continue
		}

		if processUser == user {
			cpu, err := process.CPUPercent()
			if err != nil {
				return 0, err
			}

			cpuUse += cpu
		}
	}

	return cpuUse, nil
}

// getServiceRAMUsage returns service RAM usage in bytes
func getServiceRAMUsage(user string) (ram uint64, err error) {
	processes, err := process.Processes()
	if err != nil {
		return 0, err
	}

	for _, process := range processes {
		processUser, err := process.Username()
		if err != nil {
			continue
		}

		if processUser == user {
			memInfo, err := process.MemoryInfo()
			if err != nil {
				return 0, err
			}

			ram += memInfo.RSS
		}
	}

	return ram, nil
}

// getServiceDiskUsage returns service disk usage in bytes
func getServiceDiskUsage(path string, user string) (diskUse uint64, err error) {
	if diskUse, err = platform.GetUserFSQuotaUsage(path, user); err != nil {
		return diskUse, err
	}

	return diskUse, nil
}

func (monitor *Monitor) setupTrafficMonitor() (err error) {
	monitor.trafficPeriod = DayPeriod

	monitor.trafficMap = make(map[string]*trafficMonitoring)

	monitor.iptables, err = iptables.New()
	if err != nil {
		return err
	}

	monitor.inChain = "AOS_SYSTEM_IN"
	monitor.outChain = "AOS_SYSTEM_OUT"

	if err = monitor.deleteAllTrafficChains(); err != nil {
		return err
	}

	// We have to count only interned traffic.  Skip local sub networks and netns
	// bridge network from traffic count.
	monitor.skipAddresses = "127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
	if monitor.config.NetnsBridgeIP != "" {
		monitor.skipAddresses += "," + monitor.config.NetnsBridgeIP
	}

	if err = monitor.createTrafficChain(monitor.inChain, "INPUT", "0/0"); err != nil {
		return err
	}

	if err = monitor.createTrafficChain(monitor.outChain, "OUTPUT", "0/0"); err != nil {
		return err
	}

	return nil
}

func (monitor *Monitor) getTrafficChainBytes(chain string) (value uint64, err error) {
	stats, err := monitor.iptables.ListWithCounters("filter", chain)
	if err != nil {
		return 0, err
	}

	if len(stats) > 0 {
		items := strings.Fields(stats[len(stats)-1])
		for i, item := range items {
			if item == "-c" && len(items) >= i+3 {
				return strconv.ParseUint(items[i+2], 10, 64)
			}
		}
	}

	return 0, errors.New("statistic for chain not found")
}

func (monitor *Monitor) setChainState(chain, addresses string, enable bool) (err error) {
	log.WithFields(log.Fields{"chain": chain, "state": enable}).Debug("Set chain state")

	var addrType string

	if strings.HasSuffix(chain, "_IN") {
		addrType = "-d"
	}

	if strings.HasSuffix(chain, "_OUT") {
		addrType = "-s"
	}

	if enable {
		if err = monitor.deleteAllRules(chain, addrType, addresses, "-j", "DROP"); err != nil {
			return err
		}

		if err = monitor.iptables.Append("filter", chain, addrType, addresses); err != nil {
			return err
		}
	} else {
		if err = monitor.deleteAllRules(chain, addrType, addresses); err != nil {
			return err
		}

		if err = monitor.iptables.Append("filter", chain, addrType, addresses, "-j", "DROP"); err != nil {
			return err
		}
	}

	return nil
}

func (monitor *Monitor) createTrafficChain(chain, rootChain, addresses string) (err error) {
	var skipAddrType, addrType string

	log.WithField("chain", chain).Debug("Create iptables chain")

	if strings.HasSuffix(chain, "_IN") {
		skipAddrType = "-s"
		addrType = "-d"
	}

	if strings.HasSuffix(chain, "_OUT") {
		skipAddrType = "-d"
		addrType = "-s"
	}

	if err = monitor.iptables.NewChain("filter", chain); err != nil {
		return err
	}

	if err = monitor.iptables.Append("filter", rootChain, "-j", chain); err != nil {
		return err
	}

	// This addresses will be not count but returned back to the root chain
	if monitor.skipAddresses != "" {
		if err = monitor.iptables.Append("filter", chain, skipAddrType, monitor.skipAddresses, "-j", "RETURN"); err != nil {
			return err
		}
	}

	if err = monitor.iptables.Append("filter", chain, addrType, addresses); err != nil {
		return err
	}

	traffic := trafficMonitoring{addresses: addresses}

	if traffic.lastUpdate, traffic.initialValue, err =
		monitor.trafficStorage.GetTrafficMonitorData(chain); err != nil && !strings.Contains(err.Error(), "not exist") {
		return err
	}

	monitor.trafficMap[chain] = &traffic

	return nil
}

func (monitor *Monitor) editTrafficChain(chain, addresses string) (err error) {
	var addrType string

	log.WithField("chain", chain).Debug("Edit iptables chain")

	traffic, ok := monitor.trafficMap[chain]
	if !ok {
		return errors.New("chain doesn't exist")
	}

	if strings.HasSuffix(chain, "_IN") {
		addrType = "-d"
	}

	if strings.HasSuffix(chain, "_OUT") {
		addrType = "-s"
	}

	traffic.addresses = addresses

	if traffic.disabled {
		if err = monitor.deleteAllRules(chain, addrType, traffic.addresses, "-j", "DROP"); err != nil {
			return err
		}

		if err = monitor.iptables.Append("filter", chain, addrType, addresses, "-j", "DROP"); err != nil {
			return err
		}
	} else {
		if err = monitor.deleteAllRules(chain, addrType, traffic.addresses); err != nil {
			return err
		}

		if err = monitor.iptables.Append("filter", chain, addrType, addresses); err != nil {
			return err
		}
	}

	return nil
}
func (monitor *Monitor) deleteAllRules(chain string, rulespec ...string) (err error) {
	for {
		if err = monitor.iptables.Delete("filter", chain, rulespec...); err != nil {
			errIPTables, ok := err.(*iptables.Error)
			if ok && errIPTables.IsNotExist() {
				return nil
			}

			return err
		}
	}
}

func (monitor *Monitor) deleteTrafficChain(chain, rootChain string) (err error) {
	log.WithField("chain", chain).Debug("Delete iptables chain")

	// Store traffic data to DB
	if traffic, ok := monitor.trafficMap[chain]; ok {
		monitor.trafficStorage.SetTrafficMonitorData(chain, traffic.lastUpdate, traffic.currentValue)
	}

	delete(monitor.trafficMap, chain)

	if err = monitor.deleteAllRules(rootChain, "-j", chain); err != nil {
		return err
	}

	if err = monitor.iptables.ClearChain("filter", chain); err != nil {
		return err
	}

	if err = monitor.iptables.DeleteChain("filter", chain); err != nil {
		return err
	}

	return nil
}

func (monitor *Monitor) deleteAllTrafficChains() (err error) {
	// Delete all aos related chains
	chainList, err := monitor.iptables.ListChains("filter")
	if err != nil {
		return err
	}

	for _, chain := range chainList {
		switch {
		case !strings.HasPrefix(chain, "AOS_"):
			continue

		case chain == monitor.inChain:
			err = monitor.deleteTrafficChain(chain, "INPUT")

		case chain == monitor.outChain:
			err = monitor.deleteTrafficChain(chain, "OUTPUT")

		case strings.HasSuffix(chain, "_IN"):
			err = monitor.deleteTrafficChain(chain, "FORWARD")

		case strings.HasSuffix(chain, "_OUT"):
			err = monitor.deleteTrafficChain(chain, "FORWARD")
		}

		if err != nil {
			log.WithField("chain", chain).Errorf("Can't delete chain: %s", err)
		}
	}

	return nil
}

func (monitor *Monitor) updateServiceParams(serviceID string, monitoring *serviceMonitoring) (err error) {
	ipAddress, err := GetServiceIPAddress(monitoring.serviceDir)
	if err != nil {
		return err
	}

	if monitor.trafficMap[monitoring.inChain].addresses != ipAddress {
		log.WithFields(log.Fields{
			"chain": monitoring.inChain,
			"oldIp": monitor.trafficMap[monitoring.inChain].addresses,
			"newIp": ipAddress}).Warn("Chain IP address changed")

		if err = monitor.editTrafficChain(monitoring.inChain, ipAddress); err != nil {
			return err
		}
	}

	if monitor.trafficMap[monitoring.outChain].addresses != ipAddress {
		log.WithFields(log.Fields{
			"chain": monitoring.outChain,
			"oldIp": monitor.trafficMap[monitoring.outChain].addresses,
			"newIp": ipAddress}).Warn("Chain IP address changed")

		if err = monitor.editTrafficChain(monitoring.outChain, ipAddress); err != nil {
			return err
		}
	}

	return nil
}
