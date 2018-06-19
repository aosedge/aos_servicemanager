package database

import (
	"log"
	"os"
	"strings"
	"testing"
)

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		log.Fatalf("Error creating service images: %s", err)
	}

	ret := m.Run()

	if err := os.RemoveAll("tmp"); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestAddService(t *testing.T) {
	db, err := New("tmp/test.db")
	if err != nil {
		t.Fatalf("Can't create database: %s", err)
	}
	defer db.Close()

	// AddService
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0}
	err = db.AddService(service1)
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

func TestNotExistService(t *testing.T) {
	db, err := New("tmp/test.db")
	if err != nil {
		t.Fatalf("Can't create database: %s", err)
	}
	defer db.Close()

	// GetService
	_, err = db.GetService("service3")
	if err == nil {
		t.Error("Error in non existed service")
	} else if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Can't get service: %s", err)
	}
}

func TestSetServiceStatus(t *testing.T) {
	db, err := New("tmp/test.db")
	if err != nil {
		t.Fatalf("Can't create database: %s", err)
	}
	defer db.Close()

	// AddService
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0}
	err = db.AddService(service1)
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
	db, err := New("tmp/test.db")
	if err != nil {
		t.Fatalf("Can't create database: %s", err)
	}
	defer db.Close()

	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0}
	err = db.AddService(service1)
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

func TestRemoveService(t *testing.T) {
	db, err := New("tmp/test.db")
	if err != nil {
		t.Fatalf("Can't create database: %s", err)
	}
	defer db.Close()

	// AddService
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0}
	err = db.AddService(service1)
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
	db, err := New("tmp/test.db")
	if err != nil {
		t.Fatalf("Can't create database: %s", err)
	}
	defer db.Close()

	// Add service 1
	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", `{"*":"rw"}`, 0, 0}
	err = db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	// Add service 2
	service2 := ServiceEntry{"service2", 1, "to/service2", "service2.service", "user2", `{"*":"rw"}`, 0, 0}
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
