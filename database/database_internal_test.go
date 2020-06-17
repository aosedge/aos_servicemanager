// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
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

package database

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/launcher"
)

/*******************************************************************************
 * Variables
 ******************************************************************************/

var db *Database

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	var err error

	if err = os.MkdirAll("tmp", 0755); err != nil {
		log.Fatalf("Error creating service images: %s", err)
	}

	db, err = New("tmp/test.db")
	if err != nil {
		log.Fatalf("Can't create database: %s", err)
	}

	ret := m.Run()

	if err = os.RemoveAll("tmp"); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	db.Close()

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestAddService(t *testing.T) {
	// AddService
	service1 := launcher.Service{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	// GetService
	service, err := db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}

	if !reflect.DeepEqual(service, service1) {
		t.Error("service1 doesn't match stored one")
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %s", err)
	}
}

func TestUpdateService(t *testing.T) {
	// AddService
	service1 := launcher.Service{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	service1 = launcher.Service{"service1", 2, "to/new_service1", "new_service1.service", "new_user1",
		`{"*":"rw", "new":"r"}`, 1, 1, time.Now().UTC().Add(time.Hour * 10), 0, "{}",
		123, 456, 789, 1024, []string{"path1", "path2"}}

	// UpdateService
	err = db.UpdateService(service1)
	if err != nil {
		t.Errorf("Can't update service: %s", err)
	}

	// GetService
	service, err := db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if !reflect.DeepEqual(service, service1) {
		t.Errorf("service1 doesn't match updated one: %v", service)
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %s", err)
	}
}

func TestNotExistService(t *testing.T) {
	// GetService
	_, err := db.GetService("service3")
	if err == nil {
		t.Error("Error in non existed service")
	} else if err != ErrNotExist {
		t.Errorf("Can't get service: %s", err)
	}
}

func TestSetServiceStatus(t *testing.T) {
	// AddService
	service1 := launcher.Service{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	// SetServiceStatus
	err = db.SetServiceStatus("service1", 1)
	if err != nil {
		t.Errorf("Can't set service status: %s", err)
	}
	service, err := db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service.Status != 1 {
		t.Errorf("Service status mismatch")
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %s", err)
	}
}

func TestSetServiceState(t *testing.T) {
	service1 := launcher.Service{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	// SetServiceState
	err = db.SetServiceState("service1", 1)
	if err != nil {
		t.Errorf("Can't set service state: %s", err)
	}
	service, err := db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service.State != 1 {
		t.Errorf("Service state mismatch")
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %s", err)
	}
}

func TestSetServiceStartTime(t *testing.T) {
	service1 := launcher.Service{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	time := time.Date(2018, 1, 1, 15, 35, 49, 0, time.UTC)
	// SetServiceState
	err = db.SetServiceStartTime("service1", time)
	if err != nil {
		t.Errorf("Can't set service state: %s", err)
	}
	service, err := db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service.StartAt != time {
		t.Errorf("Service start time mismatch")
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %s", err)
	}
}

func TestRemoveService(t *testing.T) {
	// AddService
	service1 := launcher.Service{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	// RemoveService
	err = db.RemoveService("service1")
	if err != nil {
		t.Errorf("Can't remove service: %s", err)
	}
	_, err = db.GetService("service1")
	if err == nil {
		t.Errorf("Error deleteing service")
	}
}

func TestGetServices(t *testing.T) {
	// Add service 1
	service1 := launcher.Service{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	// Add service 2
	service2 := launcher.Service{"service2", 1, "to/service2", "service2.service", "user2", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err = db.AddService(service2)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	// GetServices
	services, err := db.GetServices()
	if err != nil {
		t.Errorf("Can't get services: %s", err)
	}
	if len(services) != 2 {
		t.Error("Wrong service count")
	}
	for _, service := range services {
		if !reflect.DeepEqual(service, service1) && !reflect.DeepEqual(service, service2) {
			t.Error("Error getting services")
		}
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %s", err)
	}
}

func TestAddUsersService(t *testing.T) {
	// Add services
	service1 := launcher.Service{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	service2 := launcher.Service{"service2", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err = db.AddService(service2)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	// Add service to users
	err = db.AddServiceToUsers([]string{"user1"}, "service1")
	if err != nil {
		t.Errorf("Can't add users service: %s", err)
	}

	err = db.AddServiceToUsers([]string{"user2"}, "service2")
	if err != nil {
		t.Errorf("Can't add users service: %s", err)
	}

	// Check user1
	services, err := db.GetUsersServices([]string{"user1"})
	if err != nil {
		t.Errorf("Can't get users services: %s", err)
	}

	if len(services) != 1 {
		t.Error("Wrong service count")
	}

	if services[0].ID != "service1" {
		t.Errorf("Wrong service id: %s", services[0].ID)
	}

	// Check user2
	services, err = db.GetUsersServices([]string{"user2"})
	if err != nil {
		t.Errorf("Can't get users services: %s", err)
	}

	if len(services) != 1 {
		t.Error("Wrong service count")
	}

	if services[0].ID != "service2" {
		t.Errorf("Wrong service id: %s", services[0].ID)
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %s", err)
	}

	if err = db.removeAllUsers(); err != nil {
		t.Errorf("Can't remove all users: %s", err)
	}
}

func TestAddSameUsersService(t *testing.T) {
	// Add service
	err := db.AddServiceToUsers([]string{"user0", "user1"}, "service1")
	if err != nil {
		t.Errorf("Can't add users service: %s", err)
	}

	// Add service
	err = db.AddServiceToUsers([]string{"user0", "user1"}, "service1")
	if err == nil {
		t.Error("Error adding same users service")
	}

	// Clear DB
	if err = db.removeAllUsers(); err != nil {
		t.Errorf("Can't remove all users: %s", err)
	}
}

func TestNotExistUsersServices(t *testing.T) {
	// GetService
	_, err := db.GetUsersService([]string{"user2", "user3"}, "service18")
	if err != nil && err != ErrNotExist {
		t.Fatalf("Can't check if service in users: %s", err)
	}

	if err == nil {
		t.Errorf("Error users service: %s", err)
	}
}

func TestRemoveUsersService(t *testing.T) {
	// Add service
	err := db.AddServiceToUsers([]string{"user0", "user1"}, "service1")
	if err != nil {
		t.Errorf("Can't add users service: %s", err)
	}

	// Remove service
	err = db.RemoveServiceFromUsers([]string{"user0", "user1"}, "service1")
	if err != nil {
		t.Errorf("Can't remove users service: %s", err)
	}

	_, err = db.GetUsersService([]string{"user0", "user1"}, "service1")
	if err != nil && err != ErrNotExist {
		t.Fatalf("Can't check if service in users: %s", err)
	}

	if err == nil {
		t.Errorf("Error users service: %s", err)
	}
}

func TestAddUsersList(t *testing.T) {
	numUsers := 5
	numServices := 3

	for i := 0; i < numUsers; i++ {
		users := []string{fmt.Sprintf("user%d", i)}
		for j := 0; j < numServices; j++ {
			err := db.AddServiceToUsers(users, fmt.Sprintf("service%d", j))
			if err != nil {
				t.Errorf("Can't add users service: %s", err)
			}
		}
	}

	// Check users list
	usersList, err := db.getUsersList()
	if err != nil {
		t.Fatalf("Can't get users list: %s", err)
	}

	if len(usersList) != numUsers {
		t.Fatal("Wrong users count")
	}

	for _, users := range usersList {
		ok := false

		for i := 0; i < numUsers; i++ {
			if users[0] == fmt.Sprintf("user%d", i) {
				ok = true
				break
			}
		}

		if !ok {
			t.Errorf("Invalid users: %s", users)
		}
	}

	for j := 0; j < numServices; j++ {
		serviceID := fmt.Sprintf("service%d", j)

		usersServices, err := db.GetUsersServicesByServiceID(serviceID)
		if err != nil {
			t.Errorf("Can't get users services: %s", err)
		}

		for _, userService := range usersServices {
			if userService.ServiceID != serviceID {
				t.Errorf("Invalid serviceID: %s", userService.ServiceID)
			}

			ok := false

			for i := 0; i < numUsers; i++ {
				if userService.Users[0] == fmt.Sprintf("user%d", i) {
					ok = true
					break
				}
			}

			if !ok {
				t.Errorf("Invalid users: %s", userService.Users)
			}
		}

		err = db.RemoveServiceFromAllUsers(serviceID)
		if err != nil {
			t.Errorf("Can't delete users: %s", err)
		}
	}

	usersList, err = db.getUsersList()
	if err != nil {
		t.Fatalf("Can't get users list: %s", err)
	}

	if len(usersList) != 0 {
		t.Fatal("Wrong users count")
	}

	// Clear DB
	if err = db.removeAllUsers(); err != nil {
		t.Errorf("Can't remove all users: %s", err)
	}
}

func TestUsersStorage(t *testing.T) {
	// Add users service
	err := db.AddServiceToUsers([]string{"user1"}, "service1")
	if err != nil {
		t.Errorf("Can't add users service: %s", err)
	}

	// Check default values
	usersService, err := db.GetUsersService([]string{"user1"}, "service1")
	if err != nil {
		t.Errorf("Can't get users service: %s", err)
	}

	if usersService.StorageFolder != "" || len(usersService.StateChecksum) != 0 {
		t.Error("Wrong users service value")
	}

	if err = db.SetUsersStorageFolder([]string{"user1"}, "service1", "stateFolder1"); err != nil {
		t.Errorf("Can't set users storage folder: %s", err)
	}

	if err = db.SetUsersStateChecksum([]string{"user1"}, "service1", []byte{0, 1, 2, 3, 4, 5}); err != nil {
		t.Errorf("Can't set users state checksum: %s", err)
	}

	usersService, err = db.GetUsersService([]string{"user1"}, "service1")
	if err != nil {
		t.Errorf("Can't get users service: %s", err)
	}

	if usersService.StorageFolder != "stateFolder1" || !reflect.DeepEqual(usersService.StateChecksum, []byte{0, 1, 2, 3, 4, 5}) {
		t.Error("Wrong users service value")
	}

	// Clear DB
	if err = db.removeAllUsers(); err != nil {
		t.Errorf("Can't remove all users: %s", err)
	}
}
func TestTrafficMonitor(t *testing.T) {
	setTime := time.Now()
	setValue := uint64(100)

	if err := db.SetTrafficMonitorData("chain1", setTime, setValue); err != nil {
		t.Fatalf("Can't set traffic monitor: %s", err)
	}

	getTime, getValue, err := db.GetTrafficMonitorData("chain1")
	if err != nil {
		t.Fatalf("Can't get traffic monitor: %s", err)
	}

	if !getTime.Equal(setTime) || getValue != setValue {
		t.Fatalf("Wrong value time: %s, value %d", getTime, getValue)
	}

	if err := db.RemoveTrafficMonitorData("chain1"); err != nil {
		t.Fatalf("Can't remove traffic monitor: %s", err)
	}

	if _, _, err := db.GetTrafficMonitorData("chain1"); err == nil {
		t.Fatal("Entry should be removed")
	}

	// Clear DB
	if err := db.removeAllTrafficMonitor(); err != nil {
		t.Errorf("Can't remove all traffic monitor: %s", err)
	}
}

func TestOperationVersion(t *testing.T) {
	var setOperationVersion uint64 = 123

	if err := db.SetOperationVersion(setOperationVersion); err != nil {
		t.Fatalf("Can't set operation version: %s", err)
	}

	getOperationVersion, err := db.GetOperationVersion()
	if err != nil {
		t.Fatalf("Can't get operation version: %s", err)
	}

	if setOperationVersion != getOperationVersion {
		t.Errorf("Wrong operation version: %d", getOperationVersion)
	}
}

func TestCursor(t *testing.T) {
	setCursor := "cursor123"

	if err := db.SetJournalCursor(setCursor); err != nil {
		t.Fatalf("Can't set logging cursor: %s", err)
	}

	getCursor, err := db.GetJournalCursor()
	if err != nil {
		t.Fatalf("Can't get logger cursor: %s", err)
	}

	if getCursor != setCursor {
		t.Fatalf("Wrong cursor value: %s", getCursor)
	}
}

func TestGetServiceByUnitName(t *testing.T) {
	// AddService
	service1 := launcher.Service{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0, []string{"path1", "path2"}}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	// GetService
	service, err := db.GetServiceByUnitName("service1.service")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}

	if !reflect.DeepEqual(service, service1) {
		t.Error("service1 doesn't match stored one")
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %s", err)
	}
}

func TestUpgradeState(t *testing.T) {
	setUpgradeState := 4

	if err := db.SetUpgradeState(setUpgradeState); err != nil {
		t.Fatalf("Can't set upgrade state: %s", err)
	}

	getUpgradeState, err := db.GetUpgradeState()
	if err != nil {
		t.Fatalf("Can't get upgrade state: %s", err)
	}

	if setUpgradeState != getUpgradeState {
		t.Fatalf("Wrong upgrade state value: %v", getUpgradeState)
	}
}

func TestUpgradeData(t *testing.T) {
	setUpgradeData := amqp.SystemUpgrade{
		DecryptDataStruct: amqp.DecryptDataStruct{URLs: []string{"url1", "url2", "url3"},
			Sha256: []byte("sha256"),
			Sha512: []byte("sha512"),
			Size:   1234},
	}

	if err := db.SetUpgradeData(setUpgradeData); err != nil {
		t.Fatalf("Can't set upgrade data: %s", err)
	}

	getUpgradeData, err := db.GetUpgradeData()
	if err != nil {
		t.Fatalf("Can't get upgrade data: %s", err)
	}

	if !reflect.DeepEqual(setUpgradeData, getUpgradeData) {
		t.Fatalf("Wrong upgrade data value: %v", getUpgradeData)
	}
}

func TestUpgradeVersion(t *testing.T) {
	setUpgradeVersion := uint64(5)

	if err := db.SetUpgradeVersion(setUpgradeVersion); err != nil {
		t.Fatalf("Can't set upgrade version: %s", err)
	}

	getUpgradeVersion, err := db.GetUpgradeVersion()
	if err != nil {
		t.Fatalf("Can't get upgrade version: %s", err)
	}

	if setUpgradeVersion != getUpgradeVersion {
		t.Fatalf("Wrong upgrade version value: %v", getUpgradeVersion)
	}
}

func TestMultiThread(t *testing.T) {
	const numIterations = 1000

	var wg sync.WaitGroup

	wg.Add(4)

	go func() {
		defer wg.Done()

		for i := 0; i < numIterations; i++ {
			if err := db.SetUpgradeState(i); err != nil {
				t.Fatalf("Can't set upgrade state: %s", err)
			}
		}
	}()

	go func() {
		defer wg.Done()

		_, err := db.GetUpgradeState()
		if err != nil {
			t.Fatalf("Can't get upgrade state: %s", err)
		}
	}()

	go func() {
		defer wg.Done()

		for i := 0; i < numIterations; i++ {
			if err := db.SetUpgradeVersion(uint64(i)); err != nil {
				t.Fatalf("Can't set upgrade version: %s", err)
			}
		}
	}()

	go func() {
		defer wg.Done()

		for i := 0; i < numIterations; i++ {
			if _, err := db.GetUpgradeVersion(); err != nil {
				t.Fatalf("Can't get upgrade version: %s", err)
			}
		}
	}()

	wg.Wait()
}

func TestLayers(t *testing.T) {
	if err := db.AddLayer("sha256:1", "id1", "path1", "1"); err != nil {
		t.Errorf("Can't add layer %s", err)
	}

	if err := db.AddLayer("sha256:2", "id2", "path2", "1"); err != nil {
		t.Errorf("Can't add layer %s", err)
	}

	if err := db.AddLayer("sha256:3", "id3", "path3", "1"); err != nil {
		t.Errorf("Can't add layer %s", err)
	}

	path, err := db.GetLayerPathByDigest("sha256:2")
	if err != nil {
		t.Errorf("Can't get layer path %s", err)
	}

	if path != "path2" {
		t.Errorf("Path form db %s != path2", path)
	}

	if _, err := db.GetLayerPathByDigest("sha256:12345"); err == nil {
		t.Errorf("Should be error: entry does not exist")
	}

	if err := db.DeleteLayerByDigest("sha256:2"); err != nil {
		t.Errorf("Can't delete layer %s", err)
	}

	layers, err := db.GetLayersInfo()
	if err != nil {
		t.Errorf("Can't get layers info %s", err)
	}

	if len(layers) != 2 {
		t.Errorf("Count of layers in DB %d != 2", len(layers))
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (db *Database) getUsersList() (usersList [][]string, err error) {
	rows, err := db.sql.Query("SELECT DISTINCT users FROM users")
	if err != nil {
		return usersList, err
	}
	defer rows.Close()

	usersList = make([][]string, 0)

	for rows.Next() {
		var usersJSON []byte
		err = rows.Scan(&usersJSON)
		if err != nil {
			return usersList, err
		}

		var users []string

		if err = json.Unmarshal(usersJSON, &users); err != nil {
			return usersList, err
		}

		usersList = append(usersList, users)
	}

	return usersList, rows.Err()
}
