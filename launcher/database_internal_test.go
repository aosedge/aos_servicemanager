package launcher

import (
	"strings"
	"testing"
)

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestDatabase(t *testing.T) {
	db, err := newDatabase("test.db")
	if err != nil {
		t.Fatalf("Can't create databse: %s", err)
	}
	defer db.close()

	// removeAllServices

	err = db.removeAllServices()
	if err != nil {
		t.Errorf("Can't delete service table: %s", err)
	}

	// addService

	service1 := serviceEntry{"service1", 1, "to/service1", stateInit, statusOk}
	err = db.addService(service1)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	service2 := serviceEntry{"service2", 2, "to/service2", stateInit, statusOk}
	err = db.addService(service2)
	if err != nil {
		t.Errorf("Can't add entry: %s", err)
	}

	// getService

	service, err := db.getService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service != service1 {
		t.Errorf("service1 doesn't match stored one")
	}

	service, err = db.getService("service2")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service != service2 {
		t.Errorf("service2 doesn't match stored one")
	}

	service, err = db.getService("service3")
	if err == nil {
		t.Errorf("Error in non existed service")
	} else if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Can't get service: %s", err)
	}

	// getServices

	services, err := db.getServices()
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

	// setServiceStatus

	err = db.setServiceStatus("service1", statusError)
	if err != nil {
		t.Errorf("Can't set service status: %s", err)
	}
	service, err = db.getService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service.status != statusError {
		t.Errorf("Service status mismatch: %s", err)
	}

	// setServiceState

	err = db.setServiceState("service1", stateRunning)
	if err != nil {
		t.Errorf("Can't set service state: %s", err)
	}
	service, err = db.getService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}
	if service.state != stateRunning {
		t.Errorf("Service status mismatch: %s", err)
	}

	// removeService

	err = db.removeService("service1")
	if err != nil {
		t.Errorf("Can't remove entry: %s", err)
	}
	service, err = db.getService("service1")
	if err == nil {
		t.Errorf("Error deleteing service")
	}

	err = db.removeService("service2")
	if err != nil {
		t.Errorf("Can't remove entry: %s", err)
	}
	service, err = db.getService("service2")
	if err == nil {
		t.Errorf("Error deleteing service")
	}
	services, err = db.getServices()
	if err != nil {
		t.Errorf("Can't get services: %s", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service count")
	}
}
