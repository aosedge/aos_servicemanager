// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2020 Renesas Inc.
// Copyright 2020 EPAM Systems Inc.
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

package dbushandler_test

import (
	"aos_servicemanager/dbushandler"
	"aos_servicemanager/launcher"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/godbus/dbus"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type testServiceProvider struct {
	services map[string]*launcher.Service
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var serviceProvider = &testServiceProvider{services: make(map[string]*launcher.Service)}

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	dbus, err := dbushandler.New(serviceProvider)
	if err != nil {
		log.Fatalf("Can't create D-Bus handler: %s", err)
	}

	ret := m.Run()

	dbus.Close()

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestGetPermission(t *testing.T) {
	serviceProvider.services["Service1"] = &launcher.Service{ID: "Service1",
		Permissions: `{"*": "rw", "123": "rw"}`}

	conn, err := dbus.SessionBus()
	if err != nil {
		t.Fatalf("Can't connect to session bus: %s", err)
	}

	var (
		permissionJson string
		status         string
		permissions    map[string]string
	)

	obj := conn.Object(dbushandler.InterfaceName, dbushandler.ObjectPath)

	err = obj.Call(dbushandler.InterfaceName+".GetPermission", 0, "Service1").Store(&permissionJson, &status)
	if err != nil {
		t.Fatalf("Can't make D-Bus call: %s", err)
	}

	if strings.ToUpper(status) != "OK" {
		t.Fatalf("Can't get permissions: %s", status)
	}

	err = json.Unmarshal([]byte(permissionJson), &permissions)
	if err != nil {
		t.Fatalf("Can't decode permissions: %s", err)
	}

	if len(permissions) != 2 {
		t.Fatal("Permission list length !=2")
	}

	if permissions["*"] != "rw" {
		t.Fatal("Incorrect permission")
	}
}

func TestIntrospect(t *testing.T) {
	conn, err := dbus.SessionBus()
	if err != nil {
		t.Fatalf("Can't connect to session bus: %s", err)
	}

	var intro string

	obj := conn.Object(dbushandler.InterfaceName, dbushandler.ObjectPath)

	if err = obj.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&intro); err != nil {
		t.Errorf("Can't make D-Bus call: %s", err)
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (serviceProvider *testServiceProvider) GetService(serviceID string) (service launcher.Service, err error) {
	s, ok := serviceProvider.services[serviceID]
	if !ok {
		return service, fmt.Errorf("service %s does not exist", serviceID)
	}

	return *s, nil
}
