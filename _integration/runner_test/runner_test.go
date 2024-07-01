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

package runner_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"testing"
	"time"

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/api/cloudprotocol"
	"github.com/coreos/go-systemd/v22/dbus"
	log "github.com/sirupsen/logrus"

	"github.com/aosedge/aos_servicemanager/runner"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const serviceTemplate = `[Unit]
Description=AOS Service

[Service]
Type=simple
Restart=always
ExecStart=/bin/sh %s/%%i/service.sh %%i

[Install]
WantedBy=multi-user.target
`

const serviceContent = `#!/bin/bash
echo "Hello from: $1"
sleep 10
exit 1
`
const serviceFileName = "service.sh"

const waitStatusTimeout = 20 * time.Second

const aosServiceTemplate = "aos-service@%s.service"

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var tmpDir string

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

var systemd *dbus.Conn

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
***********************************************************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %v", err)
	}

	ret := m.Run()

	if err := cleanup(); err != nil {
		log.Errorf("Can't cleaning up: %v", err)
	}

	os.Exit(ret)
}

/***********************************************************************************************************************
* Tests
***********************************************************************************************************************/

func TestStartStopService(t *testing.T) {
	runnerInstance, err := runner.New()
	if err != nil {
		t.Fatalf("Can't create runner: %v", err)
	}
	defer runnerInstance.Close()

	serviceDir, err := createService("id1")
	if err != nil {
		t.Fatalf("Can't create service: %v", err)
	}

	status := runnerInstance.StartInstance("id1", serviceDir,
		runner.RunParameters{StartInterval: 2 * time.Second, StartBurst: 1, RestartInterval: 3 * time.Second})
	if status.Err != nil {
		t.Errorf("Can't start service: %v", status.Err)
	}

	if status.State != cloudprotocol.InstanceStateActive {
		t.Error("Service is not active")
	}

	// test no service binary
	status = runnerInstance.StartInstance("someID", serviceDir,
		runner.RunParameters{StartInterval: 2 * time.Second, StartBurst: 1, RestartInterval: 3 * time.Second})
	if status.Err == nil {
		t.Error("Should be error can't start service instance")
	}

	if status.State != cloudprotocol.InstanceStateFailed {
		t.Error("State should be failed")
	}

	// wait for service failed

	failStatus, err := waitForStatus(runnerInstance.InstanceStatusChannel())
	if err != nil {
		t.Fatalf("Wait for status error: %v", err)
	}

	if len(failStatus) != 1 {
		t.Error("Count of updated statuses should be 1")
	}

	if failStatus[0].InstanceID != "id1" {
		t.Error("Incorrect instance id in status")
	}

	if failStatus[0].State != cloudprotocol.InstanceStateFailed {
		t.Errorf("Incorrect service state: %s", failStatus[0].State)
	}

	// wait for service active

	activeStatus, err := waitForStatus(runnerInstance.InstanceStatusChannel())
	if err != nil {
		t.Fatalf("Wait for status error: %v", err)
	}

	if activeStatus[0].InstanceID != "id1" {
		t.Error("Incorrect instance id in status")
	}

	if activeStatus[0].State != cloudprotocol.InstanceStateActive {
		t.Errorf("Incorrect service state: %s", activeStatus[0].State)
	}

	// stop instance
	if err := runnerInstance.StopInstance("id1"); err != nil {
		t.Errorf("Can't stop service: %v", err)
	}

	// test service not loaded
	if err := runnerInstance.StopInstance("someID"); err != nil {
		t.Errorf("Can't stop service: %v", err)
	}
}

func TestRunParameters(t *testing.T) {
	runnerInstance, err := runner.New()
	if err != nil {
		t.Fatalf("Can't create runner: %v", err)
	}
	defer runnerInstance.Close()

	serviceDir, err := createService("id1")
	if err != nil {
		t.Fatalf("Can't create service: %v", err)
	}

	// Check default run parameters

	status := runnerInstance.StartInstance("id1", serviceDir, runner.RunParameters{})
	if status.Err != nil {
		t.Errorf("Can't start service: %v", status.Err)
	}

	defaultParameters := runner.RunParameters{
		StartInterval:   5 * time.Second,
		StartBurst:      3,
		RestartInterval: 1 * time.Second,
	}

	runParameters, err := getRunParameters("id1")
	if err != nil {
		t.Fatalf("Can't get run parameters: %v", err)
	}

	if runParameters != defaultParameters {
		t.Errorf("Wrong run parameters: %v", runParameters)
	}

	if err := runnerInstance.StopInstance("id1"); err != nil {
		t.Errorf("Can't stop service: %v", err)
	}

	// // Check other run parameters

	testData := []runner.RunParameters{
		{StartInterval: 5 * time.Second, StartBurst: 5, RestartInterval: 1 * time.Second},
		{StartInterval: 2 * time.Second, StartBurst: 1, RestartInterval: 2 * time.Hour},
		{StartInterval: 3 * time.Second, StartBurst: 2, RestartInterval: 50 * time.Second},
	}

	for _, testParams := range testData {
		status := runnerInstance.StartInstance("id1", serviceDir, testParams)
		if status.Err != nil {
			t.Errorf("Can't start service: %v", status.Err)
		}

		if runParameters, err = getRunParameters("id1"); err != nil {
			t.Fatalf("Can't get run parameters: %v", err)
		}

		if runParameters != testParams {
			t.Errorf("Wrong run parameters: %v", runParameters)
		}

		if err := runnerInstance.StopInstance("id1"); err != nil {
			t.Errorf("Can't stop service: %v", err)
		}
	}

	// Check invalid parameters

	status = runnerInstance.StartInstance("id1", serviceDir, runner.RunParameters{
		StartInterval:   1 * time.Nanosecond,
		RestartInterval: 1 * time.Nanosecond,
	})

	if status.Err == nil {
		t.Error("Error expected")
	}

	if status.State != cloudprotocol.InstanceStateFailed {
		t.Error("Failed status expected")
	}
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func setup() (err error) {
	tmpDir, err = ioutil.TempDir("", "aos_")
	if err != nil {
		return aoserrors.Wrap(err)
	}

	serviceFile := path.Join(tmpDir, fmt.Sprintf(aosServiceTemplate, ""))

	if err = ioutil.WriteFile(serviceFile, []byte(fmt.Sprintf(serviceTemplate, tmpDir)), 0o600); err != nil {
		return aoserrors.Wrap(err)
	}

	systemd, err = dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = systemd.LinkUnitFilesContext(context.Background(), []string{serviceFile}, true, true); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = systemd.ReloadContext(context.Background()); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func cleanup() (err error) {
	if _, disableErr := systemd.DisableUnitFilesContext(
		context.Background(), []string{fmt.Sprintf(aosServiceTemplate, "")}, true); disableErr != nil && err == nil {
		err = aoserrors.Wrap(disableErr)
	}

	systemd.Close()

	if removeErr := os.RemoveAll(tmpDir); removeErr != nil && err == nil {
		err = aoserrors.Wrap(removeErr)
	}

	return err
}

func waitForStatus(channel <-chan []runner.InstanceStatus) ([]runner.InstanceStatus, error) {
	select {
	case status := <-channel:
		return status, nil

	case <-time.After(waitStatusTimeout):
		return nil, aoserrors.New("wait timeout")
	}
}

func createService(id string) (serviceDir string, err error) {
	serviceDir = path.Join(tmpDir, id)

	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		return "", aoserrors.Wrap(err)
	}

	if err := ioutil.WriteFile(path.Join(serviceDir, serviceFileName), []byte(serviceContent), 0o600); err != nil {
		return "", aoserrors.Wrap(err)
	}

	return serviceDir, nil
}

func getRunParameters(id string) (runner.RunParameters, error) {
	properties, err := systemd.GetAllPropertiesContext(context.Background(), fmt.Sprintf(aosServiceTemplate, id))
	if err != nil {
		return runner.RunParameters{}, aoserrors.Wrap(err)
	}

	startInterval, ok := properties["StartLimitIntervalUSec"].(uint64)
	if !ok {
		return runner.RunParameters{}, aoserrors.New("invalid start interval type")
	}

	startBurst, ok := properties["StartLimitBurst"].(uint32)
	if !ok {
		return runner.RunParameters{}, aoserrors.New("invalid start burst type")
	}

	restartInterval, ok := properties["RestartUSec"].(uint64)
	if !ok {
		return runner.RunParameters{}, aoserrors.New("invalid restart interval type")
	}

	return runner.RunParameters{
		StartInterval:   time.Duration(startInterval) * time.Microsecond,
		StartBurst:      uint(startBurst),
		RestartInterval: time.Duration(restartInterval) * time.Microsecond,
	}, nil
}
