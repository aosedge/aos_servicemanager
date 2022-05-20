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

package monitorcontroller

import (
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	log "github.com/sirupsen/logrus"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const monitoringChannelSize = 64

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// MonitorController instance.
type MonitorController struct {
	monitoringChannel chan cloudprotocol.MonitoringData
}

// New creates new monitoringcontroller instance.
func New() (monitor *MonitorController, err error) {
	monitor = &MonitorController{
		monitoringChannel: make(chan cloudprotocol.MonitoringData, monitoringChannelSize),
	}

	return monitor, nil
}

// SendMonitoringData sends monitoring data.
func (monitor *MonitorController) SendMonitoringData(monitoringData cloudprotocol.MonitoringData) {
	if len(monitor.monitoringChannel) < cap(monitor.monitoringChannel) {
		monitor.monitoringChannel <- monitoringData
	} else {
		log.Warn("Skip sending monitoring data. Channel full.")
	}
}

// GetMonitoringDataChannel get monitoring channel.
func (monitor *MonitorController) GetMonitoringDataChannel() (monitoringChannel <-chan cloudprotocol.MonitoringData) {
	return monitor.monitoringChannel
}
