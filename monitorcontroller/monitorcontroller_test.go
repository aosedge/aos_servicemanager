// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2022 Renesas Electronics Corporation.
// Copyright (C) 2022 EPAM Systems, Inc.
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

package monitorcontroller_test

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/aoscloud/aos_servicemanager/monitorcontroller"
	log "github.com/sirupsen/logrus"
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
 * Main
 **********************************************************************************************************************/

func TestMain(m *testing.M) {
	ret := m.Run()

	os.Exit(ret)
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestSendMonitorData(t *testing.T) {
	duration := 100 * time.Millisecond

	controller, err := monitorcontroller.New()
	if err != nil {
		t.Fatalf("Can't create monitoring controller: %v", err)
	}

	nodeMonitoringData := cloudprotocol.NodeMonitoringData{
		NodeID:    "nodeID",
		Timestamp: time.Now(),
		MonitoringData: cloudprotocol.MonitoringData{
			RAM:        1100,
			CPU:        35,
			InTraffic:  150,
			OutTraffic: 150,
			Disk: []cloudprotocol.PartitionUsage{
				{
					Name:     "partition_1",
					UsedSize: 2300,
				},
			},
		},
		ServiceInstances: []cloudprotocol.InstanceMonitoringData{
			{
				InstanceIdent: aostypes.InstanceIdent{
					ServiceID: "serviceID",
					SubjectID: "subjectID",
					Instance:  64,
				},
				MonitoringData: cloudprotocol.MonitoringData{
					RAM:        500,
					CPU:        50,
					InTraffic:  200,
					OutTraffic: 200,
					Disk: []cloudprotocol.PartitionUsage{
						{
							Name:     "partition_2",
							UsedSize: 1000,
						},
					},
				},
			},
		},
	}

	controller.SendMonitoringData(nodeMonitoringData)

	select {
	case receivedNodeMonitoringData := <-controller.GetMonitoringDataChannel():
		if !reflect.DeepEqual(receivedNodeMonitoringData, nodeMonitoringData) {
			t.Errorf("Unexpected node monitoring data")
		}

	case <-time.After(duration * 2):
		t.Fatal("Monitoring data timeout")
	}
}
