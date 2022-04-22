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

package monitoring

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/process"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
)

/***********************************************************************************************************************
 * Init
 **********************************************************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testSender struct {
	alertCallback func(alert cloudprotocol.AlertItem)
}

type testTrafficMonitoring struct {
	inputTraffic, outputTraffic uint64
}

type testQuotaData struct {
	cpu  float64
	ram  uint64
	disk uint64
}

type testAlertData struct {
	alerts            []cloudprotocol.AlertItem
	monitoringData    cloudprotocol.MonitoringData
	trafficMonitoring testTrafficMonitoring
	quotaData         testQuotaData
	monitoringConfig  MonitorParams
}

type testProcessData struct {
	uid       int32
	quotaData testQuotaData
}

/***********************************************************************************************************************
 * Variable
 **********************************************************************************************************************/

var (
	systemQuotaData               testQuotaData
	instanceTrafficMonitoringData map[string]testTrafficMonitoring
	processesData                 []*testProcessData
)

/***********************************************************************************************************************
 * Main
 **********************************************************************************************************************/

func TestMain(m *testing.M) {
	ret := m.Run()

	os.Exit(ret)
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestAlertProcessor(t *testing.T) {
	var sourceValue uint64

	destination := make([]uint64, 0, 2)

	alert := createAlertProcessor(
		"Test",
		&sourceValue,
		func(time time.Time, value uint64) {
			log.Debugf("T: %s, %d", time, value)
			destination = append(destination, value)
		},
		aostypes.AlertRule{
			MinTimeout:   aostypes.Duration{Duration: 3 * time.Second},
			MinThreshold: 80,
			MaxThreshold: 90,
		})

	values := []uint64{50, 91, 79, 92, 93, 94, 95, 94, 79, 91, 92, 93, 94, 32, 91, 92, 93, 94, 95, 96}
	alertsCount := []int{0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 3, 3, 3}

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
	duration := 100 * time.Millisecond

	sender := &testSender{}
	trafficMonitoring := &testTrafficMonitoring{}

	monitor, err := New(&config.Config{
		WorkingDir: ".",
		Monitoring: config.Monitoring{
			SendPeriod: aostypes.Duration{Duration: duration},
			PollPeriod: aostypes.Duration{Duration: duration},
		},
	},
		sender, trafficMonitoring)
	if err != nil {
		t.Fatalf("Can't create monitoring instance: %s", err)
	}
	defer monitor.Close()

	timer := time.NewTimer(duration * 2)
	numSends := 3
	sendTime := time.Now()

	for {
		select {
		case <-monitor.GetMonitoringDataChannel():
			currentTime := time.Now()

			period := currentTime.Sub(sendTime)
			// check is period in +-10% range
			if period > duration*110/100 || period < duration*90/100 {
				t.Errorf("Period mismatch: %s", period)
			}

			sendTime = currentTime

			timer.Reset(duration * 2)

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
	duration := 100 * time.Millisecond

	var alerts []cloudprotocol.AlertItem

	sender := &testSender{
		alertCallback: func(alert cloudprotocol.AlertItem) {
			alerts = append(alerts, alert)
		},
	}

	var trafficMonitoring testTrafficMonitoring

	systemCPUPersent = getSystemCPUPersent
	systemVirtualMemory = getSystemRAM
	systemDiskUsage = getSystemDisk

	monitoring := config.Monitoring{
		ServiceAlertRules: aostypes.ServiceAlertRules{
			CPU: &aostypes.AlertRule{
				MinTimeout:   aostypes.Duration{},
				MinThreshold: 30,
				MaxThreshold: 40,
			},
			RAM: &aostypes.AlertRule{
				MinTimeout:   aostypes.Duration{},
				MinThreshold: 1000,
				MaxThreshold: 2000,
			},
			UsedDisk: &aostypes.AlertRule{
				MinTimeout:   aostypes.Duration{},
				MinThreshold: 2000,
				MaxThreshold: 4000,
			},
			InTraffic: &aostypes.AlertRule{
				MinTimeout:   aostypes.Duration{},
				MinThreshold: 100,
				MaxThreshold: 200,
			},
			OutTraffic: &aostypes.AlertRule{
				MinTimeout:   aostypes.Duration{},
				MinThreshold: 100,
				MaxThreshold: 200,
			},
		},
		SendPeriod: aostypes.Duration{Duration: duration},
		PollPeriod: aostypes.Duration{Duration: duration},
	}

	testData := []testAlertData{
		{
			trafficMonitoring: testTrafficMonitoring{inputTraffic: 150, outputTraffic: 150},
			monitoringData: cloudprotocol.MonitoringData{
				Global: cloudprotocol.GlobalMonitoringData{
					RAM:        1100,
					CPU:        35,
					UsedDisk:   2300,
					InTraffic:  150,
					OutTraffic: 150,
				},
			},
			quotaData: testQuotaData{
				cpu:  35,
				ram:  1100,
				disk: 2300,
			},
		},
		{
			trafficMonitoring: testTrafficMonitoring{inputTraffic: 150, outputTraffic: 250},
			monitoringData: cloudprotocol.MonitoringData{
				Global: cloudprotocol.GlobalMonitoringData{
					RAM:        1100,
					CPU:        45,
					UsedDisk:   2300,
					InTraffic:  150,
					OutTraffic: 250,
				},
			},
			quotaData: testQuotaData{
				cpu:  45,
				ram:  1100,
				disk: 2300,
			},
			alerts: []cloudprotocol.AlertItem{
				prepareSystemAlertItem("cpu", time.Now(), 45),
				prepareSystemAlertItem("outTraffic", time.Now(), 250),
			},
		},
		{
			trafficMonitoring: testTrafficMonitoring{inputTraffic: 350, outputTraffic: 250},
			monitoringData: cloudprotocol.MonitoringData{
				Global: cloudprotocol.GlobalMonitoringData{
					RAM:        2100,
					CPU:        45,
					UsedDisk:   4300,
					InTraffic:  350,
					OutTraffic: 250,
				},
			},
			quotaData: testQuotaData{
				cpu:  45,
				ram:  2100,
				disk: 4300,
			},
			alerts: []cloudprotocol.AlertItem{
				prepareSystemAlertItem("cpu", time.Now(), 45),
				prepareSystemAlertItem("ram", time.Now(), 2100),
				prepareSystemAlertItem("disk", time.Now(), 4300),
				prepareSystemAlertItem("inTraffic", time.Now(), 350),
				prepareSystemAlertItem("outTraffic", time.Now(), 250),
			},
		},
	}

	for _, item := range testData {
		trafficMonitoring = item.trafficMonitoring
		systemQuotaData = item.quotaData

		monitor, err := New(
			&config.Config{
				WorkingDir: ".",
				Monitoring: monitoring,
			}, sender, &trafficMonitoring)
		if err != nil {
			t.Fatalf("Can't create monitoring instance: %s", err)
		}

		select {
		case monitoringData := <-monitor.GetMonitoringDataChannel():
			if monitoringData.Global != item.monitoringData.Global {
				t.Errorf("Incorrect system monitoring data: %v", monitoringData.Global)
			}

			if len(alerts) != len(item.alerts) {
				t.Fatalf("Incorrect alerts number: %d", len(alerts))
			}

			for i, currentAlert := range alerts {
				if item.alerts[i].Payload != currentAlert.Payload {
					t.Errorf("Incorrect system alert payload: %v", currentAlert.Payload)
				}
			}

		case <-time.After(duration * 2):
			t.Fatal("Monitoring data timeout")
		}

		alerts = nil

		monitor.Close()
	}
}

func TestInstances(t *testing.T) {
	duration := 100 * time.Millisecond
	instanceTrafficMonitoringData = make(map[string]testTrafficMonitoring)

	var alerts []cloudprotocol.AlertItem

	sender := &testSender{
		alertCallback: func(alert cloudprotocol.AlertItem) {
			alerts = append(alerts, alert)
		},
	}

	trafficMonitoring := &testTrafficMonitoring{}

	monitor, err := New(
		&config.Config{
			WorkingDir: ".",
			Monitoring: config.Monitoring{
				SendPeriod: aostypes.Duration{Duration: duration},
				PollPeriod: aostypes.Duration{Duration: duration},
			},
		},
		sender,
		trafficMonitoring)
	if err != nil {
		t.Fatalf("Can't create monitoring instance: %s", err)
	}
	defer monitor.Close()

	alertRules := &aostypes.ServiceAlertRules{
		CPU: &aostypes.AlertRule{
			MinTimeout:   aostypes.Duration{},
			MinThreshold: 30,
			MaxThreshold: 40,
		},
		RAM: &aostypes.AlertRule{
			MinTimeout:   aostypes.Duration{},
			MinThreshold: 1000,
			MaxThreshold: 2000,
		},
		UsedDisk: &aostypes.AlertRule{
			MinTimeout:   aostypes.Duration{},
			MinThreshold: 2000,
			MaxThreshold: 3000,
		},
		InTraffic: &aostypes.AlertRule{
			MinTimeout:   aostypes.Duration{},
			MinThreshold: 100,
			MaxThreshold: 200,
		},
		OutTraffic: &aostypes.AlertRule{
			MinTimeout:   aostypes.Duration{},
			MinThreshold: 100,
			MaxThreshold: 200,
		},
	}

	getUserFSQuotaUsage = testUserFSQuotaUsage
	getProcesses = getTestProcessesList

	defer func() {
		getProcesses = getProcessesList
	}()

	testData := []testAlertData{
		{
			trafficMonitoring: testTrafficMonitoring{inputTraffic: 150, outputTraffic: 150},
			quotaData: testQuotaData{
				cpu:  35,
				ram:  1100,
				disk: 2300,
			},
			monitoringConfig: MonitorParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:        5000,
				GID:        5000,
				AlertRules: alertRules,
			},
		},
		{
			trafficMonitoring: testTrafficMonitoring{inputTraffic: 250, outputTraffic: 150},
			quotaData: testQuotaData{
				cpu:  45,
				ram:  2100,
				disk: 2300,
			},
			monitoringConfig: MonitorParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service2",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:        3000,
				GID:        5000,
				AlertRules: alertRules,
			},
			alerts: []cloudprotocol.AlertItem{
				prepareInstanceAlertItem(cloudprotocol.InstanceIdent{
					ServiceID: "service2",
					SubjectID: "subject1",
					Instance:  1,
				}, "ram", time.Now(), 2100),
				prepareInstanceAlertItem(cloudprotocol.InstanceIdent{
					ServiceID: "service2",
					SubjectID: "subject1",
					Instance:  1,
				}, "inTraffic", time.Now(), 250),
			},
		},
		{
			trafficMonitoring: testTrafficMonitoring{inputTraffic: 150, outputTraffic: 250},
			quotaData: testQuotaData{
				cpu:  45,
				ram:  1100,
				disk: 2300,
			},
			monitoringConfig: MonitorParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject2",
					Instance:  2,
				},
				UID:        2000,
				GID:        5000,
				AlertRules: alertRules,
			},
			alerts: []cloudprotocol.AlertItem{
				prepareInstanceAlertItem(cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject2",
					Instance:  2,
				}, "ram", time.Now(), 2200),
				prepareInstanceAlertItem(cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject2",
					Instance:  2,
				}, "outTraffic", time.Now(), 250),
			},
		},
		{
			trafficMonitoring: testTrafficMonitoring{inputTraffic: 150, outputTraffic: 250},
			quotaData: testQuotaData{
				cpu:  45,
				ram:  1100,
				disk: 2300,
			},
			monitoringConfig: MonitorParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject2",
					Instance:  2,
				},
				UID:        2000,
				GID:        5000,
				AlertRules: alertRules,
			},
			alerts: []cloudprotocol.AlertItem{
				prepareInstanceAlertItem(cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject2",
					Instance:  2,
				}, "ram", time.Now(), 2200),
				prepareInstanceAlertItem(cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject2",
					Instance:  2,
				}, "outTraffic", time.Now(), 250),
			},
		},
	}

	var expectedInstanceAlertCount int

	monitoringInstances := []cloudprotocol.InstanceMonitoringData{
		{
			InstanceIdent: cloudprotocol.InstanceIdent{
				ServiceID: "service1",
				SubjectID: "subject1",
				Instance:  1,
			},
			RAM:        1100,
			CPU:        uint64(math.Round(35 / float64(runtime.NumCPU()))),
			UsedDisk:   2300,
			InTraffic:  150,
			OutTraffic: 150,
		},
		{
			InstanceIdent: cloudprotocol.InstanceIdent{
				ServiceID: "service2",
				SubjectID: "subject1",
				Instance:  1,
			},
			RAM:        2100,
			CPU:        uint64(math.Round(45 / float64(runtime.NumCPU()))),
			UsedDisk:   2300,
			InTraffic:  250,
			OutTraffic: 150,
		},
		{
			InstanceIdent: cloudprotocol.InstanceIdent{
				ServiceID: "service1",
				SubjectID: "subject2",
				Instance:  2,
			},
			RAM:        2200,
			CPU:        uint64(math.Round((45 + 45) / float64(runtime.NumCPU()))),
			UsedDisk:   4600,
			InTraffic:  150,
			OutTraffic: 250,
		},
		{
			InstanceIdent: cloudprotocol.InstanceIdent{
				ServiceID: "service1",
				SubjectID: "subject2",
				Instance:  2,
			},
			RAM:        2200,
			CPU:        uint64(math.Round((45 + 45) / float64(runtime.NumCPU()))),
			UsedDisk:   2300,
			InTraffic:  150,
			OutTraffic: 250,
		},
	}

	for i, item := range testData {
		processesData = append(processesData, &testProcessData{
			uid:       int32(item.monitoringConfig.UID),
			quotaData: item.quotaData,
		})

		instanceID := fmt.Sprintf("instance%d", i)
		if err := monitor.StartInstanceMonitor(instanceID, item.monitoringConfig); err != nil {
			t.Fatalf("Can't start monitoring instance: %s", err)
		}

		instanceTrafficMonitoringData[instanceID] = item.trafficMonitoring
		expectedInstanceAlertCount += len(item.alerts)
	}

	defer func() {
		processesData = nil
	}()

	select {
	case monitoringData := <-monitor.GetMonitoringDataChannel():
		if len(monitoringData.ServiceInstances) != len(monitoringInstances) {
			t.Fatalf("Incorrect instance monitoring count: %d", len(monitoringData.ServiceInstances))
		}

	monitoringLoop:
		for _, receivedMonitoring := range monitoringData.ServiceInstances {
			for _, expectedMonitoring := range monitoringInstances {
				if expectedMonitoring == receivedMonitoring {
					continue monitoringLoop
				}
			}

			t.Error("Unexpected monitoring data")
		}

	case <-time.After(duration * 2):
		t.Fatal("Monitoring data timeout")
	}

	if len(alerts) != expectedInstanceAlertCount {
		t.Fatalf("Incorrect alerts number: %d", len(alerts))
	}

	for i, item := range testData {
	alertLoop:
		for _, expectedAlert := range item.alerts {
			for _, receivedAlert := range alerts {
				if expectedAlert.Payload == receivedAlert.Payload {
					continue alertLoop
				}
			}

			t.Error("Incorrect system alert payload")
		}

		if err := monitor.StopInstanceMonitor(fmt.Sprintf("instance%d", i)); err != nil {
			t.Fatalf("Can't stop monitoring instance: %s", err)
		}
	}

	// this select is used to make sure that the monitoring of the instances has been stopped
	// and monitoring data is not received on them
	select {
	case monitoringData := <-monitor.GetMonitoringDataChannel():
		if len(monitoringData.ServiceInstances) != 0 {
			t.Fatalf("Incorrect instance monitoring count: %d", len(monitoringData.ServiceInstances))
		}

	case <-time.After(duration * 2):
		t.Fatal("Monitoring data timeout")
	}
}

// /********************************************************************************************************************
//  * Interfaces
//  *******************************************************************************************************************/

func (sender *testSender) SendAlert(alert cloudprotocol.AlertItem) {
	sender.alertCallback(alert)
}

// /********************************************************************************************************************
//  * Private
//  *******************************************************************************************************************/

func (trafficMonitoring *testTrafficMonitoring) GetSystemTraffic() (inputTraffic, outputTraffic uint64, err error) {
	return trafficMonitoring.inputTraffic, trafficMonitoring.outputTraffic, nil
}

func (trafficMonitoring *testTrafficMonitoring) GetInstanceTraffic(instanceID string) (
	inputTraffic, outputTraffic uint64, err error,
) {
	trafficMonitoringData, ok := instanceTrafficMonitoringData[instanceID]
	if !ok {
		return 0, 0, aoserrors.New("incorrect instance ID")
	}

	return trafficMonitoringData.inputTraffic, trafficMonitoringData.outputTraffic, nil
}

func getSystemCPUPersent(interval time.Duration, percpu bool) (persent []float64, err error) {
	return []float64{systemQuotaData.cpu}, nil
}

func getSystemRAM() (virtualMemory *mem.VirtualMemoryStat, err error) {
	return &mem.VirtualMemoryStat{Used: systemQuotaData.ram}, nil
}

func getSystemDisk(path string) (diskUsage *disk.UsageStat, err error) {
	return &disk.UsageStat{Used: systemQuotaData.disk}, nil
}

func (p *testProcessData) Uids() ([]int32, error) {
	return []int32{p.uid}, nil
}

func (p *testProcessData) CPUPercent() (float64, error) {
	return p.quotaData.cpu, nil
}

func (p *testProcessData) MemoryInfo() (*process.MemoryInfoStat, error) {
	return &process.MemoryInfoStat{
		RSS: p.quotaData.ram,
	}, nil
}

func testUserFSQuotaUsage(path string, uid, gid uint32) (byteUsed uint64, err error) {
	for _, quota := range processesData {
		if quota.uid == int32(uid) {
			return quota.quotaData.disk, nil
		}
	}

	return 0, aoserrors.New("incorrect uid")
}

func getTestProcessesList() (processes []processInterface, err error) {
	processesInterface := make([]processInterface, len(processesData))

	for i, process := range processesData {
		processesInterface[i] = process
	}

	return processesInterface, nil
}
