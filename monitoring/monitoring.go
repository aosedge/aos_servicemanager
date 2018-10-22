// Package monitoring AOS Core Monitoring Component
package monitoring

import (
	"container/list"
	"errors"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-iptables/iptables"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/process"
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
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

// ServiceMonitoringItf provides API to start/stop service monitoring
type ServiceMonitoringItf interface {
	StartMonitorService(serviceID string, monitoringConfig ServiceMonitoringConfig) (err error)
	StopMonitorService(serviceID string) (err error)
}

// Monitor instance
type Monitor struct {
	DataChannel chan amqp.MonitoringData

	db database.MonitoringItf

	iptables      *iptables.IPTables
	trafficPeriod int
	skipAddresses string
	inChain       string
	outChain      string
	trafficMap    map[string]*trafficMonitoring

	config     config.Monitoring
	workingDir string

	sendTimer *time.Ticker
	pollTimer *time.Ticker

	mutex      sync.Mutex
	dataToSend amqp.MonitoringData

	alertProcessors *list.List

	serviceMap map[string]*serviceMonitoring
}

// ServiceMonitoringConfig contains info about service and rules for monitoring alerts
type ServiceMonitoringConfig struct {
	Pid           int32
	IPAddress     string
	WorkingDir    string
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
	inChain                string
	outChain               string
	workingDir             string
	process                *process.Process
	monitoringData         amqp.ServiceMonitoringData
	alertProcessorElements []*list.Element
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
func New(config *config.Config, db database.MonitoringItf) (monitor *Monitor, err error) {
	log.Debug("Create monitor")

	if config.Monitoring.Disabled {
		return nil, ErrDisabled
	}

	monitor = &Monitor{db: db}

	monitor.DataChannel = make(chan amqp.MonitoringData, config.Monitoring.MaxOfflineMessages)

	monitor.config = config.Monitoring
	monitor.workingDir = config.WorkingDir

	monitor.alertProcessors = list.New()

	monitor.dataToSend.Global.Alerts.CPU = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	monitor.dataToSend.Global.Alerts.RAM = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	monitor.dataToSend.Global.Alerts.UsedDisk = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	monitor.dataToSend.Global.Alerts.InTraffic = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	monitor.dataToSend.Global.Alerts.OutTraffic = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	monitor.dataToSend.ServicesData = make([]amqp.ServiceMonitoringData, 0)

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

	if config.Monitoring.InTraffic != nil {
		monitor.alertProcessors.PushBack(createAlertProcessor(
			"IN Traffic",
			&monitor.dataToSend.Global.InTraffic,
			&monitor.dataToSend.Global.Alerts.InTraffic,
			*config.Monitoring.InTraffic))
	}

	if config.Monitoring.OutTraffic != nil {
		monitor.alertProcessors.PushBack(createAlertProcessor(
			"OUT Traffic",
			&monitor.dataToSend.Global.OutTraffic,
			&monitor.dataToSend.Global.Alerts.OutTraffic,
			*config.Monitoring.OutTraffic))
	}

	monitor.serviceMap = make(map[string]*serviceMonitoring)

	monitor.pollTimer = time.NewTicker(monitor.config.PollPeriod.Duration)
	monitor.sendTimer = time.NewTicker(monitor.config.SendPeriod.Duration)

	if err = monitor.setupTrafficMonitor(); err != nil {
		return nil, err
	}

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
	monitor.mutex.Lock()
	defer monitor.mutex.Unlock()

	log.WithFields(log.Fields{
		"id":  serviceID,
		"pid": monitoringConfig.Pid,
		"ip":  monitoringConfig.IPAddress}).Debug("Start service monitoring")

	if _, ok := monitor.serviceMap[serviceID]; ok {
		return errors.New("Service already under monitoring")
	}

	// convert id to hashed u64 value
	hash := fnv.New64a()
	hash.Write([]byte(serviceID))

	// create base chain name
	chainBase := strconv.FormatUint(hash.Sum64(), 16)

	serviceMonitoring := serviceMonitoring{
		inChain:    "AOS_" + chainBase + "_IN",
		outChain:   "AOS_" + chainBase + "_OUT",
		workingDir: monitoringConfig.WorkingDir,
		monitoringData: amqp.ServiceMonitoringData{
			ServiceID: serviceID}}

	serviceMonitoring.monitoringData.Alerts.CPU = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	serviceMonitoring.monitoringData.Alerts.RAM = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	serviceMonitoring.monitoringData.Alerts.UsedDisk = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	serviceMonitoring.monitoringData.Alerts.InTraffic = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)
	serviceMonitoring.monitoringData.Alerts.OutTraffic = make([]amqp.AlertData, 0, monitor.config.MaxAlertsPerMessage)

	serviceMonitoring.process, err = process.NewProcess(monitoringConfig.Pid)
	if err != nil {
		return err
	}

	if err = monitor.createTrafficChain(serviceMonitoring.inChain, "FORWARD", monitoringConfig.IPAddress); err != nil {
		return err
	}

	monitor.trafficMap[serviceMonitoring.inChain].limit = monitoringConfig.DownloadLimit

	if err = monitor.createTrafficChain(serviceMonitoring.outChain, "FORWARD", monitoringConfig.IPAddress); err != nil {
		return err
	}

	monitor.trafficMap[serviceMonitoring.outChain].limit = monitoringConfig.UploadLimit

	rules := monitoringConfig.ServiceRules

	// For optimization capacity should be equals numbers of measurement values
	// 5 - RAM, CPU, UsedDisk, InTraffic, OutTraffic
	serviceMonitoring.alertProcessorElements = make([]*list.Element, 0, 5)

	if rules != nil && rules.CPU != nil {
		e := monitor.alertProcessors.PushBack(createAlertProcessor(
			serviceID+" CPU",
			&serviceMonitoring.monitoringData.CPU,
			&serviceMonitoring.monitoringData.Alerts.CPU,
			*rules.CPU))

		serviceMonitoring.alertProcessorElements = append(serviceMonitoring.alertProcessorElements, e)
	}

	if rules != nil && rules.RAM != nil {
		e := monitor.alertProcessors.PushBack(createAlertProcessor(
			serviceID+" RAM",
			&serviceMonitoring.monitoringData.RAM,
			&serviceMonitoring.monitoringData.Alerts.RAM,
			*rules.RAM))

		serviceMonitoring.alertProcessorElements = append(serviceMonitoring.alertProcessorElements, e)
	}

	if rules != nil && rules.UsedDisk != nil {
		e := monitor.alertProcessors.PushBack(createAlertProcessor(
			serviceID+" Disk",
			&serviceMonitoring.monitoringData.UsedDisk,
			&serviceMonitoring.monitoringData.Alerts.UsedDisk,
			*rules.UsedDisk))

		serviceMonitoring.alertProcessorElements = append(serviceMonitoring.alertProcessorElements, e)
	}

	if rules != nil && rules.InTraffic != nil {
		e := monitor.alertProcessors.PushBack(createAlertProcessor(
			serviceID+" Traffic IN",
			&serviceMonitoring.monitoringData.InTraffic,
			&serviceMonitoring.monitoringData.Alerts.InTraffic,
			*rules.InTraffic))

		serviceMonitoring.alertProcessorElements = append(serviceMonitoring.alertProcessorElements, e)
	}

	if rules != nil && rules.OutTraffic != nil {
		e := monitor.alertProcessors.PushBack(createAlertProcessor(
			serviceID+" Traffic OUT",
			&serviceMonitoring.monitoringData.OutTraffic,
			&serviceMonitoring.monitoringData.Alerts.OutTraffic,
			*rules.OutTraffic))

		serviceMonitoring.alertProcessorElements = append(serviceMonitoring.alertProcessorElements, e)
	}

	monitor.serviceMap[serviceID] = &serviceMonitoring

	monitor.processTraffic()

	return nil
}

// StopMonitorService stops monitoring service
func (monitor *Monitor) StopMonitorService(serviceID string) (err error) {
	monitor.mutex.Lock()
	defer monitor.mutex.Unlock()

	log.WithField("id", serviceID).Debug("Stop service monitoring")

	if _, ok := monitor.serviceMap[serviceID]; !ok {
		return errors.New("Service is not under monitoring")
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

/*******************************************************************************
 * Private
 ******************************************************************************/

func (monitor *Monitor) run() error {
	for {
		select {
		case <-monitor.sendTimer.C:
			monitor.mutex.Lock()
			monitor.sendMonitoringData()
			monitor.saveTraffic()
			monitor.mutex.Unlock()

		case <-monitor.pollTimer.C:
			monitor.mutex.Lock()
			monitor.processTraffic()
			monitor.getCurrentSystemData()
			monitor.getCurrentServicesData()
			monitor.processAlerts()
			monitor.mutex.Unlock()
		}
	}
}

func (monitor *Monitor) sendMonitoringData() {
	// Update services
	monitor.dataToSend.ServicesData = make([]amqp.ServiceMonitoringData, 0, len(monitor.serviceMap))

	for _, service := range monitor.serviceMap {
		monitor.dataToSend.ServicesData = append(monitor.dataToSend.ServicesData, service.monitoringData)

		// Clear arrayes
		service.monitoringData.Alerts.CPU = service.monitoringData.Alerts.CPU[:0]
		service.monitoringData.Alerts.RAM = service.monitoringData.Alerts.RAM[:0]
		service.monitoringData.Alerts.UsedDisk = service.monitoringData.Alerts.UsedDisk[:0]
		service.monitoringData.Alerts.InTraffic = service.monitoringData.Alerts.InTraffic[:0]
		service.monitoringData.Alerts.OutTraffic = service.monitoringData.Alerts.OutTraffic[:0]
	}

	if len(monitor.DataChannel) < cap(monitor.DataChannel) {
		monitor.DataChannel <- monitor.dataToSend
	} else {
		log.Warn("Skip sending monitoring data. Channel full.")
	}

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
	var err error

	for serviceID, value := range monitor.serviceMap {
		value.monitoringData.CPU, err = getServiceCPUUsage(value.process)
		if err != nil {
			log.Errorf("Can't get service CPU: %s", err)
		}

		value.monitoringData.RAM, err = getServiceRAMUsage(value.process)
		if err != nil {
			log.Errorf("Can't get service RAM: %s", err)
		}

		// TODO: Add proper service disk usage when persistant storage is implemented.
		// Put 0 for now.
		// value.monitoringData.UsedDisk, err = getSystemDiskUsage(value.workingDir)
		// if err != nil {
		//	log.Errorf("Can't get service Disc usage: %s", err)
		// }

		value.monitoringData.UsedDisk = 0

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

		// intialValue is used to keep traffic between resets
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
		if err := monitor.db.SetTrafficMonitorData(chain, traffic.lastUpdate, traffic.currentValue); err != nil {
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

// getServiceCPUUsage returns service CPU usage in percent
func getServiceCPUUsage(p *process.Process) (cpuUse uint64, err error) {
	v, err := p.CPUPercent()
	if err != nil {
		return cpuUse, err
	}

	return uint64(math.RoundToEven(v)), nil
}

// getServiceRAMUsage returns service RAM usage in bytes
func getServiceRAMUsage(p *process.Process) (ram uint64, err error) {
	v, err := p.MemoryInfo()
	if err != nil {
		return ram, err
	}

	return v.VMS, nil
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

	return 0, errors.New("Statistic for chain not found")
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
		monitor.db.GetTrafficMonitorData(chain); err != nil && err != database.ErrNotExist {
		return err
	}

	monitor.trafficMap[chain] = &traffic

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
		monitor.db.SetTrafficMonitorData(chain, traffic.lastUpdate, traffic.currentValue)
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
