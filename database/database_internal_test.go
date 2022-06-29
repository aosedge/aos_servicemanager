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

package database

import (
	"database/sql"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/launcher"
	"github.com/aoscloud/aos_servicemanager/layermanager"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
	"github.com/aoscloud/aos_servicemanager/storagestate"
)

/***********************************************************************************************************************
 * Variables
 **********************************************************************************************************************/

var (
	tmpDir string
	db     *Database
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
 * Main
 **********************************************************************************************************************/

func TestMain(m *testing.M) {
	var err error

	tmpDir, err = ioutil.TempDir("", "sm_")
	if err != nil {
		log.Fatalf("Error create temporary dir: %s", err)
	}

	dbPath := path.Join(tmpDir, "test.db")

	db, err = New(dbPath, tmpDir, tmpDir)
	if err != nil {
		log.Fatalf("Can't create database: %s", err)
	}

	ret := m.Run()

	if err = os.RemoveAll(tmpDir); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	db.Close()

	os.Exit(ret)
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestAddGetService(t *testing.T) {
	// AddService
	service := servicemanager.ServiceInfo{
		ServiceID: "service1", AosVersion: 1, ServiceProvider: "sp1", Description: "", ImagePath: "to/service1",
		GID: 5001, IsActive: false, Size: 30,
	}

	if _, err := db.GetService(service.ServiceID); err == nil || !errors.Is(err, servicemanager.ErrNotExist) {
		t.Error("Should be error get service")
	}

	if _, err := db.GetAllServiceVersions(service.ServiceID); err == nil || !errors.Is(err, servicemanager.ErrNotExist) {
		t.Error("Should be error get service")
	}

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	// GetService
	serviceFromDB, err := db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}

	if !reflect.DeepEqual(serviceFromDB, service) {
		t.Error("service1 doesn't match stored one")
	}

	service2 := servicemanager.ServiceInfo{
		ServiceID: "service2", AosVersion: 1, ServiceProvider: "sp1", Description: "", ImagePath: "to/service1",
		GID: 5001, IsActive: false, Size: 80,
	}

	if err := db.AddService(service2); err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	if err := db.AddService(service2); err == nil {
		t.Error("Should be error can't add service")
	}

	serviceFromDB, err = db.GetService("service2")
	if err != nil {
		t.Errorf("Can't get service: %v", err)
	}

	if !reflect.DeepEqual(serviceFromDB, service2) {
		t.Error("service1 doesn't match stored one")
	}

	service.AosVersion = 2

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	services, err := db.GetAllServiceVersions("service1")
	if err != nil {
		t.Errorf("Can't get all service versions: %s", err)
	}

	if len(services) != 2 {
		t.Errorf("incorrect count of services %d", len(services))
	}

	services, err = db.GetAllServices()
	if err != nil {
		t.Errorf("Can't get services: %v", err)
	}

	if len(services) != 2 {
		t.Errorf("Incorrect count of services: %v", len(services))
	}

	if !reflect.DeepEqual([]servicemanager.ServiceInfo{service, service2}, services) {
		t.Error("service1 doesn't match stored one")
	}

	service.AosVersion = 3

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	if len(services) != 2 {
		t.Errorf("Unexpected service count: %v", len(services))
	}

	services, err = db.GetAllServices()
	if err != nil {
		t.Errorf("Can't get services: %v", err)
	}

	if len(services) != 2 {
		t.Errorf("Incorrect count of services: %v", len(services))
	}

	if !reflect.DeepEqual([]servicemanager.ServiceInfo{service, service2}, services) {
		t.Error("service1 doesn't match stored one")
	}

	service2.AosVersion = 5

	if err := db.AddService(service2); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	services, err = db.GetAllServices()
	if err != nil {
		t.Errorf("Can't get services: %v", err)
	}

	if len(services) != 2 {
		t.Errorf("Incorrect count of services: %d", len(services))
	}

	if !reflect.DeepEqual([]servicemanager.ServiceInfo{service, service2}, services) {
		t.Error("service1 doesn't match stored one")
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
	} else if !errors.Is(err, servicemanager.ErrNotExist) {
		t.Errorf("Can't get service: %s", err)
	}
}

func TestRemoveService(t *testing.T) {
	// AddService
	service := servicemanager.ServiceInfo{
		ServiceID: "service1", AosVersion: 1, ServiceProvider: "sp1", Description: "", ImagePath: "to/service1",
		GID: 5001, IsActive: false,
	}

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	// GetService
	serviceFromDB, err := db.GetService("service1")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}

	if !reflect.DeepEqual(serviceFromDB, service) {
		t.Error("service1 doesn't match stored one")
	}

	if err := db.RemoveService(service); err != nil {
		t.Errorf("Can't delete service: %s", err)
	}

	if _, err := db.GetService("service1"); err == nil {
		t.Errorf("Should be error: service does not exist ")
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %s", err)
	}
}

func TestActivateService(t *testing.T) {
	// AddService
	service := servicemanager.ServiceInfo{
		ServiceID: "serviceActivate", AosVersion: 1, ServiceProvider: "sp1", Description: "", ImagePath: "to/service1",
		GID: 5001, IsActive: false,
	}

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %s", err)
	}

	if err := db.ActivateService(service.ServiceID, service.AosVersion); err != nil {
		t.Errorf("Can't activate service: %s", err)
	}

	// GetService
	serviceFromDB, err := db.GetService("serviceActivate")
	if err != nil {
		t.Errorf("Can't get service: %s", err)
	}

	if serviceFromDB.IsActive != true {
		t.Error("Wrong active value")
	}
}

func TestCachedService(t *testing.T) {
	service := servicemanager.ServiceInfo{
		ServiceID: "serviceCached", AosVersion: 1, ServiceProvider: "sp1", Description: "", ImagePath: "to/service1",
		GID: 5001, IsActive: true, Cached: false,
	}

	expectedServices := []servicemanager.ServiceInfo{service}

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	services, err := db.GetAllServiceVersions("serviceCached")
	if err != nil {
		t.Errorf("Can't get all service versions: %v", err)
	}

	if !reflect.DeepEqual(expectedServices, services) {
		t.Error("Unexpected services")
	}

	if err := db.SetServiceCached(service.ServiceID, true); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	services, err = db.GetAllServiceVersions("serviceCached")
	if err != nil {
		t.Errorf("Can't get all service versions: %v", err)
	}

	expectedServices[0].Cached = true

	if !reflect.DeepEqual(expectedServices, services) {
		t.Error("Unexpected services")
	}

	service.AosVersion = 2

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	services, err = db.GetAllServiceVersions("serviceCached")
	if err != nil {
		t.Errorf("Can't get all service versions: %v", err)
	}

	expectedServices = append(expectedServices, service)

	if !reflect.DeepEqual(expectedServices, services) {
		t.Error("Unexpected services")
	}

	if err := db.SetServiceCached(service.ServiceID, true); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	services, err = db.GetAllServiceVersions("serviceCached")
	if err != nil {
		t.Errorf("Can't get all service versions: %v", err)
	}

	expectedServices[1].Cached = true

	if !reflect.DeepEqual(expectedServices, services) {
		t.Error("Unexpected services")
	}

	if err := db.SetServiceCached(service.ServiceID, false); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	services, err = db.GetAllServiceVersions("serviceCached")
	if err != nil {
		t.Errorf("Can't get all service versions: %v", err)
	}

	expectedServices[0].Cached = false
	expectedServices[1].Cached = false

	if !reflect.DeepEqual(expectedServices, services) {
		t.Error("Unexpected services")
	}
}

func TestSetTimestampService(t *testing.T) {
	service := servicemanager.ServiceInfo{
		ServiceID: "serviceTimestamp", AosVersion: 1, ServiceProvider: "sp1", Description: "", ImagePath: "to/service1",
		GID: 5001, IsActive: false,
	}

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	serviceFromDB, err := db.GetService("serviceTimestamp")
	if err != nil {
		t.Fatalf("Can't get service: %v", err)
	}

	if serviceFromDB.ServiceID != "serviceTimestamp" {
		t.Errorf("Incorrect service ID: %v", serviceFromDB.ServiceID)
	}

	service.Timestamp = time.Now().UTC()

	if err = db.SetServiceTimestamp(service.ServiceID, service.AosVersion, service.Timestamp); err != nil {
		t.Errorf("Can't set timestamp service: %v", err)
	}

	serviceFromDB, err = db.GetService("serviceTimestamp")
	if err != nil {
		t.Errorf("Can't get service: %v", err)
	}

	if serviceFromDB.Timestamp != service.Timestamp {
		t.Error("Wrong service timestamp")
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

func TestMultiThread(t *testing.T) {
	const numIterations = 1000

	var wg sync.WaitGroup

	wg.Add(4)

	go func() {
		defer wg.Done()

		for i := 0; i < numIterations; i++ {
			if err := db.SetOperationVersion(uint64(i)); err != nil {
				t.Errorf("Can't set operation version: %s", err)

				return
			}
		}
	}()

	go func() {
		defer wg.Done()

		_, err := db.GetOperationVersion()
		if err != nil {
			t.Errorf("Can't get Operation Version : %s", err)

			return
		}
	}()

	go func() {
		defer wg.Done()

		for i := 0; i < numIterations; i++ {
			if err := db.SetJournalCursor(strconv.Itoa(i)); err != nil {
				t.Errorf("Can't set journal cursor: %s", err)

				return
			}
		}
	}()

	go func() {
		defer wg.Done()

		for i := 0; i < numIterations; i++ {
			if _, err := db.GetJournalCursor(); err != nil {
				t.Errorf("Can't get journal cursor: %s", err)

				return
			}
		}
	}()

	wg.Wait()
}

func TestLayers(t *testing.T) {
	layer1 := layermanager.LayerInfo{
		Digest: "sha256:1", LayerID: "id1", Path: "path1", OSVersion: "1", VendorVersion: "1.0",
		Description: "some layer 1", AosVersion: 1, Size: 40,
	}

	if err := db.AddLayer(layer1); err != nil {
		t.Errorf("Can't add layer %s", err)
	}

	savedLayer, err := db.GetLayerInfoByDigest("sha256:1")
	if err != nil {
		t.Errorf("Can't get layer path: %v", err)
	}

	if !reflect.DeepEqual(savedLayer, layer1) {
		t.Error("service1 doesn't match stored one")
	}

	layer2 := layermanager.LayerInfo{
		Digest: "sha256:2", LayerID: "id2", Path: "path2", OSVersion: "1", VendorVersion: "2.0",
		Description: "some layer 2", AosVersion: 2, Size: 50,
	}

	if err := db.AddLayer(layer2); err != nil {
		t.Errorf("Can't add layer %s", err)
	}

	savedLayer, err = db.GetLayerInfoByDigest("sha256:2")
	if err != nil {
		t.Errorf("Can't get layer path: %v", err)
	}

	if !reflect.DeepEqual(savedLayer, layer2) {
		t.Error("service1 doesn't match stored one")
	}

	layer3 := layermanager.LayerInfo{
		Digest: "sha256:3", LayerID: "id3", Path: "path3", OSVersion: "1", VendorVersion: "1.0",
		Description: "some layer 3", AosVersion: 3, Size: 60,
	}

	if err := db.AddLayer(layer3); err != nil {
		t.Errorf("Can't add layer %s", err)
	}

	savedLayer, err = db.GetLayerInfoByDigest("sha256:3")
	if err != nil {
		t.Errorf("Can't get layer path %s", err)
	}

	if !reflect.DeepEqual(savedLayer, layer3) {
		t.Error("service1 doesn't match stored one")
	}

	if _, err := db.GetLayerInfoByDigest("sha256:12345"); err == nil {
		t.Errorf("Should be error: entry does not exist")
	}

	savedLayer, err = db.GetLayerInfoByDigest("sha256:2")
	if err != nil {
		t.Errorf("Can't get layer path %s", err)
	}

	if savedLayer.Cached {
		t.Error("Should not be cached")
	}

	if err = db.SetLayerCached("sha256:2", true); err != nil {
		t.Errorf("Can't set cached layer: %v", err)
	}

	savedLayer, err = db.GetLayerInfoByDigest("sha256:2")
	if err != nil {
		t.Errorf("Can't get layer path: %v", err)
	}

	if !savedLayer.Cached {
		t.Error("Should be cached")
	}

	if err = db.SetLayerCached("sha256:2", false); err != nil {
		t.Errorf("Can't set cached layer: %v", err)
	}

	savedLayer, err = db.GetLayerInfoByDigest("sha256:2")
	if err != nil {
		t.Errorf("Can't get layer path: %v", err)
	}

	if savedLayer.Cached {
		t.Error("Should not be cached")
	}

	layerTimestamp := time.Now().UTC()

	if err := db.SetLayerTimestamp("sha256:2", layerTimestamp); err != nil {
		t.Errorf("Can't set layer timestamp: %v", err)
	}

	savedLayer, err = db.GetLayerInfoByDigest("sha256:2")
	if err != nil {
		t.Errorf("Can't get layer info: %v", err)
	}

	if savedLayer.Timestamp != layerTimestamp {
		t.Error("Unexpected layer timestamp")
	}

	if err := db.DeleteLayerByDigest(layer2.Digest); err != nil {
		t.Errorf("Can't delete layer %s", err)
	}

	layers, err := db.GetLayersInfo()
	if err != nil {
		t.Errorf("Can't get layers info %s", err)
	}

	if len(layers) != 2 {
		t.Errorf("Count of layers in DB %d != 2", len(layers))
	}

	if layers[0].AosVersion != 1 {
		t.Errorf("Layer AosVersion should be 1")
	}
}

func TestInstances(t *testing.T) {
	const (
		testServiceID = "testService"
		testSubjectID = "testSubject"
	)

	var subjectInstances, serviceInstances, running, allInstances []launcher.InstanceInfo

	for i := 0; i < 5; i++ {
		subjectInstance := launcher.InstanceInfo{InstanceIdent: cloudprotocol.InstanceIdent{
			ServiceID: "someServiceID" + strconv.Itoa(i),
			SubjectID: testSubjectID, Instance: uint64(i),
		}, AosVersion: uint64(i), UnitSubject: true, UID: i + 100, InstanceID: uuid.New().String()}

		if i%2 == 0 {
			subjectInstance.Running = true
			running = append(running, subjectInstance)
		}

		if err := db.AddInstance(subjectInstance); err != nil {
			t.Fatalf("Can't add instance to DB %v", err)
		}

		subjectInstances = append(subjectInstances, subjectInstance)
		allInstances = append(allInstances, subjectInstance)

		serviceInstance := launcher.InstanceInfo{InstanceIdent: cloudprotocol.InstanceIdent{
			ServiceID: testServiceID,
			SubjectID: "someSubject" + strconv.Itoa(i), Instance: uint64(i),
		}, AosVersion: uint64(i + 20), UnitSubject: true, UID: i + 200, InstanceID: uuid.New().String()}

		if i%2 == 0 {
			serviceInstance.Running = true
			running = append(running, serviceInstance)
		}

		if err := db.AddInstance(serviceInstance); err != nil {
			t.Fatalf("Can't add instance to DB %v", err)
		}

		serviceInstances = append(serviceInstances, serviceInstance)
		allInstances = append(allInstances, serviceInstance)
	}

	// Test get all instances
	allResults, err := db.GetAllInstances()
	if err != nil {
		t.Fatalf("Can't get all instances from DB %v", err)
	}

	if !reflect.DeepEqual(allResults, allInstances) {
		t.Error("Incorrect get all instances result")
	}

	// Test get running instances
	runningResult, err := db.GetRunningInstances()
	if err != nil {
		t.Fatalf("Can't get running instances from DB %v", err)
	}

	if !reflect.DeepEqual(runningResult, running) {
		t.Error("Incorrect running instances result")
	}

	// Test get all instances by service ID
	serviceInstancesResult, err := db.GetServiceInstances(testServiceID)
	if err != nil {
		t.Fatalf("Can't get instances by serviceID %v", err)
	}

	if !reflect.DeepEqual(serviceInstancesResult, serviceInstances) {
		t.Error("Incorrect instances by serviceID result")
	}

	// Test get unavailable instances
	serviceInstancesResult, err = db.GetServiceInstances("notAvailableServiceID")
	if err != nil {
		t.Fatalf("Can't get instances by serviceID %v", err)
	}

	if len(serviceInstancesResult) != 0 {
		t.Error("incorrect count of instances")
	}

	// Test get all instances by subject ID
	subjectInstancesResult, err := db.GetSubjectInstances(testSubjectID)
	if err != nil {
		t.Fatalf("Can't get instances by subjectID %v", err)
	}

	if !reflect.DeepEqual(subjectInstancesResult, subjectInstances) {
		t.Error("Incorrect instances by subjectID result")
	}

	// Negative test: add the same instance should be failed
	testInstanceInfo := launcher.InstanceInfo{InstanceIdent: cloudprotocol.InstanceIdent{
		ServiceID: testServiceID,
		SubjectID: testSubjectID, Instance: 42,
	}, AosVersion: 42, UnitSubject: true, UID: 42, InstanceID: uuid.New().String()}

	if err := db.AddInstance(testInstanceInfo); err != nil {
		t.Fatalf("Can't add instance to DB %v", err)
	}

	if err := db.AddInstance(testInstanceInfo); err == nil {
		t.Error("Should be error can't add instace")
	}

	// Test update instance
	instanceResult, err := db.GetInstanceByID(testInstanceInfo.InstanceID)
	if err != nil {
		t.Fatalf("Can't get instance by ID %v", err)
	}

	if !reflect.DeepEqual(instanceResult, testInstanceInfo) {
		t.Error("Incorrect instance by ID result")
	}

	ident, version, err := db.GetInstanceInfoByID(testInstanceInfo.InstanceID)
	if err != nil {
		t.Fatalf("Can't get instance info: %v", err)
	}

	if ident != testInstanceInfo.InstanceIdent {
		t.Error("Incorrect instance ident")
	}

	if version != testInstanceInfo.AosVersion {
		t.Error("Incorrect aos version")
	}

	if _, err := db.GetInstanceByID("testInstanceID"); !errors.Is(err, launcher.ErrNotExist) {
		t.Errorf("Should be error: %v", launcher.ErrNotExist)
	}

	testInstanceInfo.Running = true

	if err := db.UpdateInstance(testInstanceInfo); err != nil {
		t.Fatalf("Can't update instance %v", err)
	}

	// Negative test: update unavailable instance should be failed
	if err := db.UpdateInstance(
		launcher.InstanceInfo{InstanceID: "unavailbale"}); !errors.Is(err, launcher.ErrNotExist) {
		t.Error("Should be error: instance not exist")
	}

	// Test get all instances identifier
	testInstanceIdent := cloudprotocol.InstanceIdent{ServiceID: testServiceID, SubjectID: testSubjectID, Instance: 42}

	if instanceResult, err = db.GetInstanceByIdent(testInstanceIdent); err != nil {
		t.Fatalf("Can't get instance by identifier %v", err)
	}

	if !reflect.DeepEqual(instanceResult, testInstanceInfo) {
		t.Error("Incorrect instance by identifier result")
	}

	// Negative test get all instances identifier by unavailable identifier
	testInstanceIdent.Instance = 10500

	if _, err = db.GetInstanceByIdent(testInstanceIdent); !errors.Is(err, launcher.ErrNotExist) {
		t.Errorf("Should be error: %v", launcher.ErrNotExist)
	}

	// Test remove instance
	if err = db.RemoveInstance(testInstanceInfo.InstanceID); err != nil {
		t.Errorf("Can't remove instance: %v", err)
	}

	if _, err := db.GetInstanceByID(testInstanceInfo.InstanceID); !errors.Is(err, launcher.ErrNotExist) {
		t.Errorf("Should be error: %v", launcher.ErrNotExist)
	}

	if err = db.RemoveInstance(testInstanceInfo.InstanceID); err != nil {
		t.Errorf("Can't remove instance: %v", err)
	}
}

func TestInstancesID(t *testing.T) {
	addedInstance := []launcher.InstanceInfo{
		{
			InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "TestSevrID", SubjectID: "TestSubID", Instance: 0},
			InstanceID:    "TestSevrID_TestSubID_0",
		},
		{
			InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "TestSevrID", SubjectID: "TestSubID", Instance: 1},
			InstanceID:    "TestSevrID_TestSubID_1",
		},
		{
			InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "TestSevrID", SubjectID: "TestSubID1", Instance: 0},
			InstanceID:    "TestSevrID_TestSubID1_0",
		},
		{
			InstanceIdent: cloudprotocol.InstanceIdent{ServiceID: "TestSevrID2", SubjectID: "TestSubID", Instance: 0},
			InstanceID:    "TestSevrID2_TestSubID_0",
		},
	}

	for _, instance := range addedInstance {
		if err := db.AddInstance(instance); err != nil {
			t.Fatalf("Can't add instance to DB %v", err)
		}
	}

	type testData struct {
		filter      cloudprotocol.InstanceFilter
		expectedIds []string
	}

	data := []testData{
		{
			filter:      cloudprotocol.NewInstanceFilter("TestSevrID", "TestSubID", 0),
			expectedIds: []string{"TestSevrID_TestSubID_0"},
		},
		{
			filter:      cloudprotocol.NewInstanceFilter("TestSevrID", "TestSubID", -1),
			expectedIds: []string{"TestSevrID_TestSubID_0", "TestSevrID_TestSubID_1"},
		},
		{
			filter:      cloudprotocol.NewInstanceFilter("TestSevrID", "", 0),
			expectedIds: []string{"TestSevrID_TestSubID_0", "TestSevrID_TestSubID1_0"},
		},
		{
			filter:      cloudprotocol.NewInstanceFilter("TestSevrID", "", -1),
			expectedIds: []string{"TestSevrID_TestSubID_0", "TestSevrID_TestSubID_1", "TestSevrID_TestSubID1_0"},
		},
	}

	for _, value := range data {
		ids, err := db.GetInstanceIDs(value.filter)
		if err != nil {
			t.Fatalf("Can't get instance ids from DB: %v", err)
		}

		if !reflect.DeepEqual(ids, value.expectedIds) {
			t.Error("Incorrect instance ids")
		}
	}
}

func TestEnvVars(t *testing.T) {
	vars, err := db.GetOverrideEnvVars()
	if err != nil {
		t.Fatalf("Can't get empty env vars: %v", err)
	}

	if len(vars) != 0 {
		t.Error("Returned env vars should be empty")
	}

	curentTime := time.Now().UTC()

	testEnvVars := []cloudprotocol.EnvVarsInstanceInfo{
		{
			InstanceFilter: cloudprotocol.NewInstanceFilter("id1", "s1", int64(1)),
			EnvVars: []cloudprotocol.EnvVarInfo{
				{ID: "varId1", Variable: "variable 1"},
				{ID: "varId2", Variable: "variable 2", TTL: &curentTime},
			},
		},
		{
			InstanceFilter: cloudprotocol.NewInstanceFilter("id2", "", -1),
			EnvVars: []cloudprotocol.EnvVarInfo{
				{ID: "varId1", Variable: "variable 1"},
				{ID: "varId2", Variable: "variable 2", TTL: &curentTime},
			},
		},
	}

	if err = db.SetOverrideEnvVars(testEnvVars); err != nil {
		t.Fatalf("Can't set env vars: %v", err)
	}

	if vars, err = db.GetOverrideEnvVars(); err != nil {
		t.Fatalf("Can't get env vars: %v", err)
	}

	if !reflect.DeepEqual(testEnvVars, vars) {
		t.Errorf("Incorrect env vars from database")
	}
}

func TestStorageState(t *testing.T) {
	var (
		testID          = "testInstanceID"
		newCheckSum     = []byte("newCheckSum")
		newStorageQuota = uint64(88888)
		newStateQuota   = uint64(99999)
	)

	if _, err := db.GetStorageStateInfoByID(testID); !errors.Is(err, storagestate.ErrNotExist) {
		t.Errorf("Should be entry does not exist")
	}

	testStateStorageInfo := storagestate.StorageStateInstanceInfo{
		StorageQuota: 12345, StateQuota: 54321,
		StateChecksum: []byte("checksum1"),
	}

	if err := db.AddStorageStateInfo(testID, testStateStorageInfo); err != nil {
		t.Fatalf("Can't add state storage info: %v", err)
	}

	info, err := db.GetStorageStateInfoByID(testID)
	if err != nil {
		t.Fatalf("Can't get state storage info: %v", err)
	}

	if !reflect.DeepEqual(info, testStateStorageInfo) {
		t.Error("State storage info from database doesn't match expected one")
	}

	if err := db.SetStateChecksumByID("noID", newCheckSum); !errors.Is(err, storagestate.ErrNotExist) {
		t.Errorf("Should be entry does not exist")
	}

	if err := db.SetStateChecksumByID(testID, newCheckSum); err != nil {
		t.Fatalf("Can't update checksum: %v", err)
	}

	testStateStorageInfo.StateChecksum = newCheckSum

	info, err = db.GetStorageStateInfoByID(testID)
	if err != nil {
		t.Fatalf("Can't get state storage info: %v", err)
	}

	if !reflect.DeepEqual(info, testStateStorageInfo) {
		t.Error("Update state storage info from database doesn't match expected one")
	}

	if err := db.SetStorageStateQuotasByID(
		"noID", newStorageQuota, newStateQuota); !errors.Is(err, storagestate.ErrNotExist) {
		t.Errorf("Should be: entry does not exist")
	}

	if err := db.SetStorageStateQuotasByID(testID, newStorageQuota, newStateQuota); err != nil {
		t.Fatalf("Can't update state and storage quotas: %v", err)
	}

	testStateStorageInfo.StateQuota = newStateQuota
	testStateStorageInfo.StorageQuota = newStorageQuota

	info, err = db.GetStorageStateInfoByID(testID)
	if err != nil {
		t.Fatalf("Can't get state storage info: %v", err)
	}

	if !reflect.DeepEqual(info, testStateStorageInfo) {
		t.Error("Update state storage info from database doesn't match expected one")
	}

	if err := db.RemoveStorageStateInfoByID(testID); err != nil {
		t.Fatalf("Can't remove state storage info: %v", err)
	}

	if _, err := db.GetStorageStateInfoByID(testID); !errors.Is(err, storagestate.ErrNotExist) {
		t.Errorf("Should be: entry does not exist")
	}
}

func TestMigrationToV1(t *testing.T) {
	migrationDB := path.Join(tmpDir, "test_migration.db")
	mergedMigrationDir := path.Join(tmpDir, "mergedMigration")

	if err := os.MkdirAll(mergedMigrationDir, 0o755); err != nil {
		t.Fatalf("Error creating merged migration dir: %s", err)
	}

	defer func() {
		if err := os.RemoveAll(mergedMigrationDir); err != nil {
			t.Fatalf("Error removing merged migration dir: %s", err)
		}
	}()

	if err := createDatabaseV0(migrationDB); err != nil {
		t.Fatalf("Can't create initial database %s", err)
	}

	// Migration upward
	db, err := newDatabase(migrationDB, "migration", mergedMigrationDir, 1)
	if err != nil {
		t.Fatalf("Can't create database: %s", err)
	}

	if err = isDatabaseVer1(db.sql); err != nil {
		t.Fatalf("Error checking db version: %s", err)
	}

	db.Close()

	// Migration downward
	db, err = newDatabase(migrationDB, "migration", mergedMigrationDir, 0)
	if err != nil {
		t.Fatalf("Can't create database: %s", err)
	}

	if err = isDatabaseVer0(db.sql); err != nil {
		t.Fatalf("Error checking db version: %s", err)
	}

	db.Close()
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func createDatabaseV0(name string) (err error) {
	sqlite, err := sql.Open("sqlite3", fmt.Sprintf("%s?_busy_timeout=%d&_journal_mode=%s&_sync=%s",
		name, busyTimeout, journalMode, syncMode))
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer sqlite.Close()

	if _, err = sqlite.Exec(
		`CREATE TABLE config (
			operationVersion INTEGER,
			cursor TEXT)`); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = sqlite.Exec(
		`INSERT INTO config (
			operationVersion,
			cursor) values(?, ?)`, launcher.OperationVersion, ""); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = sqlite.Exec(`CREATE TABLE IF NOT EXISTS services (id TEXT NOT NULL PRIMARY KEY,
															   aosVersion INTEGER,
															   serviceProvider TEXT,
															   path TEXT,
															   unit TEXT,
															   uid INTEGER,
															   gid INTEGER,
															   hostName TEXT,
															   permissions TEXT,
															   state INTEGER,
															   status INTEGER,
															   startat TIMESTAMP,
															   ttl INTEGER,
															   alertRules TEXT,
															   ulLimit INTEGER,
															   dlLimit INTEGER,
															   ulSpeed INTEGER,
															   dlSpeed INTEGER,
															   storageLimit INTEGER,
															   stateLimit INTEGER,
															   layerList TEXT,
															   deviceResources TEXT,
															   boardResources TEXT,
															   vendorVersion TEXT,
															   description TEXT)`); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = sqlite.Exec(`CREATE TABLE IF NOT EXISTS users (users TEXT NOT NULL,
															serviceid TEXT NOT NULL,
															storageFolder TEXT,
															stateCheckSum BLOB,
															PRIMARY KEY(users, serviceid))`); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = sqlite.Exec(`CREATE TABLE IF NOT EXISTS trafficmonitor (chain TEXT NOT NULL PRIMARY KEY,
																	 time TIMESTAMP,
																	 value INTEGER)`); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = sqlite.Exec(`CREATE TABLE IF NOT EXISTS layers (digest TEXT NOT NULL PRIMARY KEY,
															 layerId TEXT,
															 path TEXT,
															 osVersion TEXT,
															 vendorVersion TEXT,
															 description TEXT,
															 aosVersion INTEGER)`); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func isDatabaseVer1(sqlite *sql.DB) (err error) {
	rows, err := sqlite.Query(
		"SELECT COUNT(*) AS CNTREC FROM pragma_table_info('config') WHERE name='componentsUpdateInfo'")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer rows.Close()

	if rows.Err() != nil {
		return aoserrors.Wrap(rows.Err())
	}

	var count int
	if rows.Next() {
		if err = rows.Scan(&count); err != nil {
			return aoserrors.Wrap(err)
		}

		if count == 0 {
			return errNotExist
		}
	}

	servicesRows, err := sqlite.Query(
		"SELECT COUNT(*) AS CNTREC FROM pragma_table_info('services') WHERE name='exposedPorts'")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer servicesRows.Close()

	if servicesRows.Err() != nil {
		return aoserrors.Wrap(servicesRows.Err())
	}

	if !servicesRows.Next() {
		return errNotExist
	}

	count = 0
	if err = servicesRows.Scan(&count); err != nil {
		return aoserrors.Wrap(err)
	}

	if count == 0 {
		return errNotExist
	}

	return nil
}

func isDatabaseVer0(sqlite *sql.DB) (err error) {
	rows, err := sqlite.Query(
		"SELECT COUNT(*) AS CNTREC FROM pragma_table_info('config') WHERE name='componentsUpdateInfo'")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer rows.Close()

	if rows.Err() != nil {
		return aoserrors.Wrap(rows.Err())
	}

	var count int
	if rows.Next() {
		if err = rows.Scan(&count); err != nil {
			return aoserrors.Wrap(err)
		}

		if count != 0 {
			return errNotExist
		}
	}

	servicesRows, err := sqlite.Query(
		"SELECT COUNT(*) AS CNTREC FROM pragma_table_info('config') WHERE name='exposedPorts'")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer servicesRows.Close()

	if servicesRows.Err() != nil {
		return aoserrors.Wrap(servicesRows.Err())
	}

	if !servicesRows.Next() {
		return errNotExist
	}

	count = 0
	if err = servicesRows.Scan(&count); err != nil {
		return aoserrors.Wrap(err)
	}

	if count != 0 {
		return errNotExist
	}

	return nil
}
