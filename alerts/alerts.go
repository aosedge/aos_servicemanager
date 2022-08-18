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

// Package alerts provides set of API to send system and services alerts
package alerts

import (
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	log "github.com/sirupsen/logrus"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const alertChannelSize = 50

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// Alerts instance.
type Alerts struct {
	alertsChannel chan cloudprotocol.AlertItem
}

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new alerts object.
func New() (instance *Alerts, err error) {
	log.Debug("New alerts")

	instance = &Alerts{
		alertsChannel: make(chan cloudprotocol.AlertItem, alertChannelSize),
	}

	return instance, nil
}

// GetAlertsChannel returns channel with alerts to be sent.
func (instance *Alerts) GetAlertsChannel() (channel <-chan cloudprotocol.AlertItem) {
	return instance.alertsChannel
}

// SendResourceAlert sends resource alert.
func (instance *Alerts) SendAlert(alert cloudprotocol.AlertItem) {
	if len(instance.alertsChannel) >= cap(instance.alertsChannel) {
		log.Warn("Skip alert, channel is full")

		return
	}

	instance.alertsChannel <- alert
}
