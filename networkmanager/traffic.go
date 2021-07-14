// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2021 Renesas Inc.
// Copyright 2021 EPAM Systems Inc.
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

package networkmanager

import (
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-iptables/iptables"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

// Describes reset traffic period
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

type trafficChains struct {
	inChain  string
	outChain string
}

type trafficData struct {
	disabled     bool
	addresses    string
	currentValue uint64
	initialValue uint64
	subValue     uint64
	limit        uint64
	lastUpdate   time.Time
}

type trafficMonitoring struct {
	iptables         *iptables.IPTables
	trafficPeriod    int
	skipAddresses    string
	inChain          string
	outChain         string
	trafficMap       map[string]*trafficData
	serviceChainsMap map[string]*trafficChains
	trafficStorage   TrafficStorage
}

func newTrafficMonitor(trafficStorage TrafficStorage) (monitor *trafficMonitoring, err error) {
	monitor = &trafficMonitoring{
		trafficPeriod:  DayPeriod,
		trafficStorage: trafficStorage,
	}

	monitor.trafficMap = make(map[string]*trafficData)
	monitor.serviceChainsMap = make(map[string]*trafficChains)

	monitor.iptables, err = iptables.New()
	if err != nil {
		return nil, err
	}

	monitor.inChain = "AOS_SYSTEM_IN"
	monitor.outChain = "AOS_SYSTEM_OUT"

	if err = monitor.deleteAllTrafficChains(); err != nil {
		return nil, err
	}

	// We have to count only interned traffic.  Skip local sub networks and netns
	// bridge network from traffic count.
	skipNetworks := []string{
		"127.0.0.0/8", "10.0.0.0/8", "192.168.0.0/16", "172.16.0.0/12",
	}

	for _, bridgeSubnet := range predefinedPrivateNetworks {
		skipNetworks = append(skipNetworks, bridgeSubnet.ipSubNet)
	}

	monitor.skipAddresses = strings.Join(skipNetworks, ",")

	if err = monitor.createTrafficChain(monitor.inChain, "INPUT", "0/0"); err != nil {
		return nil, err
	}

	if err = monitor.createTrafficChain(monitor.outChain, "OUTPUT", "0/0"); err != nil {
		return nil, err
	}

	if err = monitor.processTrafficMonitor(); err != nil {
		return nil, err
	}

	return monitor, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (monitor *trafficMonitoring) getTrafficChainBytes(chain string) (value uint64, err error) {
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

	return 0, aoserrors.New("statistic for chain not found")
}

func (monitor *trafficMonitoring) isSamePeriod(t1, t2 time.Time) (result bool) {
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

func (monitor *trafficMonitoring) setChainState(chain, addresses string, enable bool) (err error) {
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

func (monitor *trafficMonitoring) deleteAllRules(chain string, rulespec ...string) (err error) {
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

func (monitor *trafficMonitoring) createTrafficChain(chain, rootChain, addresses string) (err error) {
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

	if err = monitor.iptables.Insert("filter", rootChain, 1, "-j", chain); err != nil {
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

	traffic := trafficData{addresses: addresses}

	if traffic.lastUpdate, traffic.initialValue, err =
		monitor.trafficStorage.GetTrafficMonitorData(chain); err != nil && !strings.Contains(err.Error(), "not exist") {
		return err
	}

	monitor.trafficMap[chain] = &traffic

	return nil
}

func (monitor *trafficMonitoring) deleteTrafficChain(chain, rootChain string) (err error) {
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

func (monitor *trafficMonitoring) processTrafficMonitor() error {
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

	monitor.saveTraffic()

	return nil
}

func (monitor *trafficMonitoring) saveTraffic() {
	for chain, traffic := range monitor.trafficMap {
		if err := monitor.trafficStorage.SetTrafficMonitorData(chain, traffic.lastUpdate, traffic.currentValue); err != nil {
			log.WithField("chain", chain).Errorf("Can't set traffic data: %s", err)
		}
	}
}

func (monitor *trafficMonitoring) deleteAllTrafficChains() (err error) {
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

func (monitor *trafficMonitoring) startTrafficMonitor(serviceID, IPAddress string, downloadLimit, uploadLimit uint64) (err error) {
	if IPAddress == "" {
		return nil
	}

	hash := fnv.New64a()
	hash.Write([]byte(serviceID))
	chainBase := strconv.FormatUint(hash.Sum64(), 16)
	serviceChains := trafficChains{inChain: "AOS_" + chainBase + "_IN", outChain: "AOS_" + chainBase + "_OUT"}

	if err = monitor.createTrafficChain(serviceChains.inChain, "FORWARD", IPAddress); err != nil {
		return err
	}

	monitor.trafficMap[serviceChains.inChain].limit = downloadLimit

	if err = monitor.createTrafficChain(serviceChains.outChain, "FORWARD", IPAddress); err != nil {
		return err
	}

	monitor.trafficMap[serviceChains.outChain].limit = uploadLimit
	monitor.serviceChainsMap[serviceID] = &serviceChains

	if err = monitor.processTrafficMonitor(); err != nil {
		return err
	}

	return nil
}

func (monitor *trafficMonitoring) stopMonitorServiceTraffic(serviceID string) (err error) {
	serviceChains, ok := monitor.serviceChainsMap[serviceID]
	if !ok {
		return nil
	}

	if serviceChains.inChain != "" {
		if err = monitor.deleteTrafficChain(serviceChains.inChain, "FORWARD"); err != nil {
			log.WithField("id", serviceID).Errorf("Can't delete chain: %s", err)
		}
	}

	if serviceChains.outChain != "" {
		if err = monitor.deleteTrafficChain(serviceChains.outChain, "FORWARD"); err != nil {
			log.WithField("id", serviceID).Errorf("Can't delete chain: %s", err)
		}
	}

	delete(monitor.serviceChainsMap, serviceID)

	return nil
}
