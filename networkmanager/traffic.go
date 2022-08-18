// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2021 Renesas Electronics Corporation.
// Copyright (C) 2021 EPAM Systems, Inc.
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
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/coreos/go-iptables/iptables"
	log "github.com/sirupsen/logrus"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

// Describes reset traffic period.
const (
	MinutePeriod = iota
	HourPeriod
	DayPeriod
	MonthPeriod
	YearPeriod
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// TrafficStorage provides API to create, remove or access monitoring data.
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
	iptables          IPTablesInterface
	trafficPeriod     int
	skipAddresses     string
	inChain           string
	outChain          string
	trafficMap        map[string]*trafficData
	instanceChainsMap map[string]*trafficChains
	trafficStorage    TrafficStorage
}

type IPTablesInterface interface {
	ListWithCounters(table, chain string) ([]string, error)
	Append(table, chain string, rulespec ...string) error
	Delete(table, chain string, rulespec ...string) error
	NewChain(table, chain string) error
	Insert(table, chain string, pos int, rulespec ...string) error
	ClearChain(table, chain string) error
	DeleteChain(table, chain string) error
	ListChains(table string) ([]string, error)
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var (
	ErrEntryNotExist = errors.New("entry does not exist")
	ErrRuleNotExist  = errors.New("chain rule not exist")
)

// These global variables are used to be able to mocking the functionality in tests.
// nolint:gochecknoglobals
var (
	IsSamePeriod = isSamePeriod
	IPTables     IPTablesInterface
)

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func newTrafficMonitor(trafficStorage TrafficStorage) (monitor *trafficMonitoring, err error) {
	monitor = &trafficMonitoring{
		trafficPeriod:  DayPeriod,
		trafficStorage: trafficStorage,
	}

	monitor.trafficMap = make(map[string]*trafficData)
	monitor.instanceChainsMap = make(map[string]*trafficChains)

	if monitor.iptables = IPTables; monitor.iptables == nil {
		if monitor.iptables, err = iptables.New(); err != nil {
			return nil, aoserrors.Wrap(err)
		}
	}

	monitor.inChain = "AOS_SYSTEM_IN"
	monitor.outChain = "AOS_SYSTEM_OUT"

	if err = monitor.deleteAllTrafficChains(); err != nil {
		return nil, aoserrors.Wrap(err)
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
		return nil, aoserrors.Wrap(err)
	}

	if err = monitor.createTrafficChain(monitor.outChain, "OUTPUT", "0/0"); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err = monitor.processTrafficMonitor(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return monitor, nil
}

func (monitor *trafficMonitoring) getTrafficChainBytes(chain string) (value uint64, err error) {
	stats, err := monitor.iptables.ListWithCounters("filter", chain)
	if err != nil {
		return 0, aoserrors.Wrap(err)
	}

	if len(stats) > 0 {
		items := strings.Fields(stats[len(stats)-1])
		for i, item := range items {
			if item == "-c" && len(items) >= i+3 {
				if value, err = strconv.ParseUint(items[i+2], 10, 64); err != nil {
					return 0, aoserrors.Wrap(err)
				}

				return value, nil
			}
		}
	}

	return 0, aoserrors.New("statistic for chain not found")
}

func isSamePeriod(trafficPeriod int, t1, t2 time.Time) (result bool) {
	y1, m1, d1 := t1.Date()
	h1 := t1.Hour()
	min1 := t1.Minute()

	y2, m2, d2 := t2.Date()
	h2 := t2.Hour()
	min2 := t2.Minute()

	switch trafficPeriod {
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
			return aoserrors.Wrap(err)
		}

		if err = monitor.iptables.Append("filter", chain, addrType, addresses); err != nil {
			return aoserrors.Wrap(err)
		}
	} else {
		if err = monitor.deleteAllRules(chain, addrType, addresses); err != nil {
			return aoserrors.Wrap(err)
		}

		if err = monitor.iptables.Append("filter", chain, addrType, addresses, "-j", "DROP"); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (monitor *trafficMonitoring) deleteAllRules(chain string, rulespec ...string) (err error) {
	for {
		if err = monitor.iptables.Delete("filter", chain, rulespec...); err != nil {
			var errIPTables *iptables.Error

			if errors.As(err, &errIPTables) {
				if errIPTables.IsNotExist() {
					return nil
				}
			}

			if errors.Is(err, ErrRuleNotExist) {
				return nil
			}

			return aoserrors.Wrap(err)
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
		return aoserrors.Wrap(err)
	}

	if err = monitor.iptables.Insert("filter", rootChain, 1, "-j", chain); err != nil {
		return aoserrors.Wrap(err)
	}

	// This addresses will be not count but returned back to the root chain
	if monitor.skipAddresses != "" {
		if err = monitor.iptables.Append("filter", chain, skipAddrType, monitor.skipAddresses, "-j", "RETURN"); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if err = monitor.iptables.Append("filter", chain, addrType, addresses); err != nil {
		return aoserrors.Wrap(err)
	}

	traffic := trafficData{addresses: addresses}

	traffic.lastUpdate, traffic.initialValue, err = monitor.trafficStorage.GetTrafficMonitorData(chain)
	if err != nil && !errors.Is(err, ErrEntryNotExist) {
		return aoserrors.Wrap(err)
	}

	monitor.trafficMap[chain] = &traffic

	return nil
}

func (monitor *trafficMonitoring) deleteTrafficChain(chain, rootChain string) (err error) {
	log.WithField("chain", chain).Debug("Delete iptables chain")

	// Store traffic data to DB
	if traffic, ok := monitor.trafficMap[chain]; ok {
		if err := monitor.trafficStorage.SetTrafficMonitorData(chain,
			traffic.lastUpdate, traffic.currentValue); err != nil {
			log.Errorf("Can't set traffic monitoring: %s", err)
		}
	}

	delete(monitor.trafficMap, chain)

	if err = monitor.deleteAllRules(rootChain, "-j", chain); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = monitor.iptables.ClearChain("filter", chain); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = monitor.iptables.DeleteChain("filter", chain); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (monitor *trafficMonitoring) processTrafficMonitor() (err error) {
	timestamp := time.Now().UTC()

	for chain, traffic := range monitor.trafficMap {
		var (
			value    uint64
			chainErr error
		)

		if !traffic.disabled {
			if value, chainErr = monitor.getTrafficChainBytes(chain); chainErr != nil && err == nil {
				err = aoserrors.Errorf("Can't get chain byte count: %s", chainErr)
				continue
			}
		}

		if !IsSamePeriod(monitor.trafficPeriod, timestamp, traffic.lastUpdate) {
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

		if chainErr = monitor.checkTrafficLimit(traffic, chain); chainErr != nil && err == nil {
			err = chainErr
		}
	}

	monitor.saveTraffic()

	return err
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
		return aoserrors.Wrap(err)
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

func (monitor *trafficMonitoring) startInstanceTrafficMonitor(
	instanceID, ipAddress string, downloadLimit, uploadLimit uint64,
) (err error) {
	if ipAddress == "" {
		return nil
	}

	hash := fnv.New64a()
	hash.Write([]byte(instanceID))
	chainBase := strconv.FormatUint(hash.Sum64(), 16)
	serviceChains := trafficChains{inChain: "AOS_" + chainBase + "_IN", outChain: "AOS_" + chainBase + "_OUT"}

	if err = monitor.createTrafficChain(serviceChains.inChain, "FORWARD", ipAddress); err != nil {
		return aoserrors.Wrap(err)
	}

	monitor.trafficMap[serviceChains.inChain].limit = downloadLimit

	if err = monitor.createTrafficChain(serviceChains.outChain, "FORWARD", ipAddress); err != nil {
		return aoserrors.Wrap(err)
	}

	monitor.trafficMap[serviceChains.outChain].limit = uploadLimit
	monitor.instanceChainsMap[instanceID] = &serviceChains

	if err = monitor.processTrafficMonitor(); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (monitor *trafficMonitoring) stopInstanceTrafficMonitor(instanceID string) (err error) {
	serviceChains, ok := monitor.instanceChainsMap[instanceID]
	if !ok {
		return nil
	}

	if serviceChains.inChain != "" {
		if err = monitor.deleteTrafficChain(serviceChains.inChain, "FORWARD"); err != nil {
			log.WithField("id", instanceID).Errorf("Can't delete chain: %s", err)
		}
	}

	if serviceChains.outChain != "" {
		if err = monitor.deleteTrafficChain(serviceChains.outChain, "FORWARD"); err != nil {
			log.WithField("id", instanceID).Errorf("Can't delete chain: %s", err)
		}
	}

	delete(monitor.instanceChainsMap, instanceID)

	return nil
}

func (monitor *trafficMonitoring) checkTrafficLimit(traffic *trafficData, chain string) (err error) {
	if traffic.limit != 0 {
		if traffic.currentValue > traffic.limit && !traffic.disabled {
			// disable chain
			if chainErr := monitor.setChainState(chain, traffic.addresses, false); chainErr != nil && err == nil {
				err = aoserrors.Errorf("can't disable chain: %s", err)
			} else {
				resetTrafficData(traffic, true)
			}
		}

		if traffic.currentValue < traffic.limit && traffic.disabled {
			// enable chain
			if chainErr := monitor.setChainState(chain, traffic.addresses, true); chainErr != nil && err == nil {
				err = aoserrors.Errorf("can't enable chain: %s", err)
			} else {
				resetTrafficData(traffic, false)
			}
		}
	}

	return err
}

func resetTrafficData(traffic *trafficData, disable bool) {
	traffic.disabled = disable
	traffic.initialValue = traffic.currentValue
	traffic.subValue = 0
}
