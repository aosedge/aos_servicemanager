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

func TestDatabase(t *testing.T) {
	db, err := New("tmp/test.db")
	if err != nil {
		t.Fatalf("Can't create databse: %s", err)
	}
	defer db.Close()

	// removeAllServices

	err = db.removeAllServices()
	if err != nil {
		t.Errorf("Can't delete service table: %s", err)
	}

	// AddService

	service1 := ServiceEntry{"service1", 1, "to/service1", "service1.service", "user1", 0, 0}
	err = db.AddService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	service2 := ServiceEntry{"service2", 2, "to/service2", "service2.service", "user2", 0, 0}
	err = db.AddService(service2)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	// GetService

	service, err := db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service != service1 {
		t.Errorf("service1 doesn't match stored one")
	}

	service, err = db.GetService("service2")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service != service2 {
		t.Errorf("service2 doesn't match stored one")
	}

	service, err = db.GetService("service3")
	if err == nil {
		t.Errorf("Error in non existed service")
	} else if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Can't get service: %s", err)
	}

	// GetServices

	services, err := db.GetServices()
	if err != nil {
		t.Errorf("Can't get services: %s", err)
	}
	if len(services) != 2 {
		t.Errorf("Wrong service count")
	}
	for _, service = range services {
		if service != service1 && service != service2 {
			t.Errorf("Error getting services")
		}
	}

	// SetServiceStatus

	err = db.SetServiceStatus("service1", 1)
	if err != nil {
		t.Errorf("Can't set service status: %s", err)
	}
	service, err = db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service.Status != 1 {
		t.Errorf("Service status mismatch")
	}

	// SetServiceState

	err = db.SetServiceState("service1", 1)
	if err != nil {
		t.Errorf("Can't set service state: %s", err)
	}
	service, err = db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service.State != 1 {
		t.Errorf("Service state mismatch")
	}

	// RemoveService

	err = db.RemoveService("service1")
	if err != nil {
		t.Errorf("Can't remove entry: %s", err)
	}
	service, err = db.GetService("service1")
	if err == nil {
		t.Errorf("Error deleteing service")
	}

	err = db.RemoveService("service2")
	if err != nil {
		t.Errorf("Can't remove entry: %s", err)
	}
	service, err = db.GetService("service2")
	if err == nil {
		t.Errorf("Error deleteing service")
	}
	services, err = db.GetServices()
	if err != nil {
		t.Errorf("Can't get services: %s", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service count")
	}
}
