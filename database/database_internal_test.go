package database

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
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
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	// GetService
	service, err := db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}

	if service != service1 {
		t.Error("service1 doesn't match stored one")
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %s", err)
	}
}

func TestUpdateService(t *testing.T) {
	// AddService
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	service1 = ServiceEntry{"service1", 2, "to/new_service1", "new_service1.service", "new_user1",
		`{"*":"rw", "new":"r"}`, 1, 1, time.Now().UTC().Add(time.Hour * 10), 0, "{}", 123, 456, 789, 1024}

	// UpdateService
	err = db.UpdateService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	// GetService
	service, err := db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service != service1 {
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
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
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
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
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
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
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
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	// RemoveService
	err = db.RemoveService("service1")
	if err != nil {
		t.Errorf("Can't remove entry: %s", err)
	}
	_, err = db.GetService("service1")
	if err == nil {
		t.Errorf("Error deleteing service")
	}
}

func TestGetServices(t *testing.T) {
	// Add service 1
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	// Add service 2
	service2 := ServiceEntry{"service2", 1, "to/service2", "service2.service", "user2", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err = db.AddService(service2)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
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
		if service != service1 && service != service2 {
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
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	service2 := ServiceEntry{"service2", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err = db.AddService(service2)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	// Add users services
	err = db.AddUsersService([]string{"user1"}, "service1")
	if err != nil {
		t.Errorf("Can't add users service: %s", err)
	}

	err = db.AddUsersService([]string{"user2"}, "service2")
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
	err := db.AddUsersService([]string{"user0", "user1"}, "service1")
	if err != nil {
		t.Errorf("Can't add users service: %s", err)
	}

	// Add service
	err = db.AddUsersService([]string{"user0", "user1"}, "service1")
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
	result, err := db.IsUsersService([]string{"user2", "user3"}, "service18")
	if err != nil {
		t.Fatalf("Can't check if service in users: %s", err)
	}

	if result {
		t.Errorf("Error users service: %s", err)
	}
}

func TestRemoveUsersService(t *testing.T) {
	// Add service
	err := db.AddUsersService([]string{"user0", "user1"}, "service1")
	if err != nil {
		t.Errorf("Can't add users service: %s", err)
	}

	// Remove service
	err = db.RemoveUsersService([]string{"user0", "user1"}, "service1")
	if err != nil {
		t.Errorf("Can't remove users service: %s", err)
	}

	result, err := db.IsUsersService([]string{"user0", "user1"}, "service1")
	if err != nil {
		t.Fatalf("Can't check if service in users: %s", err)
	}

	if result {
		t.Errorf("Error users service: %s", err)
	}
}

func TestAddUsersList(t *testing.T) {
	numUsers := 5
	numServices := 3

	for i := 0; i < numUsers; i++ {
		users := []string{fmt.Sprintf("user%d", i)}
		for j := 0; j < numServices; j++ {
			err := db.AddUsersService(users, fmt.Sprintf("service%d", j))
			if err != nil {
				t.Errorf("Can't add users service: %s", err)
			}
		}
	}

	// Check users list
	usersList, err := db.GetUsersList()
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

		entries, err := db.GetUsersEntriesByServiceID(serviceID)
		if err != nil {
			t.Errorf("Can't get users entries: %s", err)
		}

		for _, entry := range entries {
			if entry.ServiceID != serviceID {
				t.Errorf("Invalid serviceID: %s", entry.ServiceID)
			}

			ok := false

			for i := 0; i < numUsers; i++ {
				if entry.Users[0] == fmt.Sprintf("user%d", i) {
					ok = true
					break
				}
			}

			if !ok {
				t.Errorf("Invalid users: %s", entry.Users)
			}
		}

		err = db.DeleteUsersByServiceID(serviceID)
		if err != nil {
			t.Errorf("Can't delete users: %s", err)
		}
	}

	usersList, err = db.GetUsersList()
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
	err := db.AddUsersService([]string{"user1"}, "service1")
	if err != nil {
		t.Errorf("Can't add users service: %s", err)
	}

	// Check default values
	entry, err := db.GetUsersEntry([]string{"user1"}, "service1")
	if err != nil {
		t.Errorf("Can't get users entry: %s", err)
	}

	if entry.StorageFolder != "" || len(entry.StateChecksum) != 0 {
		t.Error("Wrong users entry value")
	}

	if err = db.SetUsersStorageFolder([]string{"user1"}, "service1", "stateFolder1"); err != nil {
		t.Errorf("Can't set users storage folder: %s", err)
	}

	if err = db.SetUsersStateChecksum([]string{"user1"}, "service1", []byte{0, 1, 2, 3, 4, 5}); err != nil {
		t.Errorf("Can't set users state checksum: %s", err)
	}

	entry, err = db.GetUsersEntry([]string{"user1"}, "service1")
	if err != nil {
		t.Errorf("Can't get users entry: %s", err)
	}

	if entry.StorageFolder != "stateFolder1" || !reflect.DeepEqual(entry.StateChecksum, []byte{0, 1, 2, 3, 4, 5}) {
		t.Error("Wrong users entry value")
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

func TestDBVersion(t *testing.T) {
	db, err := New("tmp/version.db")
	if err != nil {
		log.Fatalf("Can't create database: %s", err)
	}

	if err = db.setVersion(dbVersion - 1); err != nil {
		log.Errorf("Can't set database version: %s", err)
	}

	db.Close()

	db, err = New("tmp/version.db")
	if err == nil {
		log.Error("Expect version mismatch error")
	} else if err != ErrVersionMismatch {
		log.Errorf("Can't create database: %s", err)
	}

	db.Close()
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

func TestGetServiceByServiceName(t *testing.T) {
	// AddService
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0,
		time.Now().UTC(), 0, "", 0, 0, 0, 0}
	err := db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	// GetService
	service, err := db.GetServiceByServiceName("service1.service")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}

	if service != service1 {
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

func TestUpgradeMetadata(t *testing.T) {
	setUpgradeMetadata := amqp.UpgradeMetadata{
		Data: []amqp.UpgradeFileInfo{
			amqp.UpgradeFileInfo{
				Target: "target",
				URLs:   []string{"url1", "url2", "url3"},
				Sha256: "sha256",
				Sha512: "sha512",
				Size:   1234}}}

	if err := db.SetUpgradeMetadata(setUpgradeMetadata); err != nil {
		t.Fatalf("Can't set upgrade metadata: %s", err)
	}

	getUpgradeMetadata, err := db.GetUpgradeMetadata()
	if err != nil {
		t.Fatalf("Can't get upgrade metadata: %s", err)
	}

	if !reflect.DeepEqual(setUpgradeMetadata, getUpgradeMetadata) {
		t.Fatalf("Wrong upgrade metadata value: %v", getUpgradeMetadata)
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
