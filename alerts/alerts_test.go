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

package alerts_test

import (
	"os"
	"testing"

	"github.com/aosedge/aos_common/api/cloudprotocol"
	log "github.com/sirupsen/logrus"

	"github.com/aosedge/aos_servicemanager/alerts"
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
 * Tests
 **********************************************************************************************************************/

func TestAlerts(t *testing.T) {
	alertsHandler, err := alerts.New()
	if err != nil {
		t.Fatalf("Can't create alerts: %s", err)
	}

	testAlert := cloudprotocol.AlertItem{
		Tag:     cloudprotocol.AlertTagSystemError,
		Payload: cloudprotocol.SystemAlert{Message: "some error"},
	}

	for i := 1; i < 55; i++ {
		alertsHandler.SendAlert(testAlert)
	}

	if len(alertsHandler.GetAlertsChannel()) != 50 {
		t.Error("Incorrect channel size ", len(alertsHandler.GetAlertsChannel()))
	}
}
