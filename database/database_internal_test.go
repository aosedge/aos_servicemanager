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
	"os"
	"path"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/aoscloud/aos_common/migration"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/launcher"
	"github.com/aoscloud/aos_servicemanager/layermanager"
	"github.com/aoscloud/aos_servicemanager/networkmanager"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
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

	tmpDir, err = os.MkdirTemp("", "sm_")
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
	service := servicemanager.ServiceInfo{
		ServiceID: "service1",
		VersionInfo: aostypes.VersionInfo{
			AosVersion: 1,
		},
		ServiceProvider: "sp1",
		ImagePath:       "to/service1",
		Size:            30,
	}

	if _, err := db.GetAllServiceVersions(service.ServiceID); err == nil || !errors.Is(err, servicemanager.ErrNotExist) {
		t.Error("Should be error get service")
	}

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	serviceFromDB, err := db.GetAllServiceVersions("service1")
	if err != nil {
		t.Errorf("Can't get service: %v", err)
	}

	if !reflect.DeepEqual(serviceFromDB, []servicemanager.ServiceInfo{service}) {
		t.Error("service1 doesn't match stored one")
	}

	service2 := servicemanager.ServiceInfo{
		ServiceID: "service2",
		VersionInfo: aostypes.VersionInfo{
			AosVersion: 1,
		},
		ServiceProvider: "sp1",
		ImagePath:       "to/service1",
		Size:            80,
	}

	if err := db.AddService(service2); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	if err := db.AddService(service2); err == nil {
		t.Error("Should be error can't add service")
	}

	serviceFromDB, err = db.GetAllServiceVersions("service2")
	if err != nil {
		t.Errorf("Can't get service: %v", err)
	}

	if !reflect.DeepEqual(serviceFromDB, []servicemanager.ServiceInfo{service2}) {
		t.Error("service doesn't match stored one")
	}

	service3 := service2
	service3.AosVersion = 2

	if err := db.AddService(service3); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	services, err := db.GetAllServiceVersions("service2")
	if err != nil {
		t.Errorf("Can't get all service versions: %v", err)
	}

	if len(services) != 2 {
		t.Errorf("incorrect count of services %d", len(services))
	}

	if !reflect.DeepEqual([]servicemanager.ServiceInfo{service2, service3}, services) {
		t.Error("service doesn't match stored one")
	}

	services, err = db.GetServices()
	if err != nil {
		t.Errorf("Can't get services: %v", err)
	}

	if len(services) != 3 {
		t.Errorf("Incorrect count of services: %d", len(services))
	}

	if !reflect.DeepEqual([]servicemanager.ServiceInfo{service, service2, service3}, services) {
		t.Error("service doesn't match stored one")
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %v", err)
	}
}

func TestNotExistService(t *testing.T) {
	// GetService
	_, err := db.GetAllServiceVersions("service3")
	if err == nil {
		t.Error("Error in non existed service")
	} else if !errors.Is(err, servicemanager.ErrNotExist) {
		t.Errorf("Can't get service: %v", err)
	}
}

func TestRemoveService(t *testing.T) {
	service := servicemanager.ServiceInfo{
		ServiceID: "service1",
		VersionInfo: aostypes.VersionInfo{
			AosVersion: 1,
		},
		ServiceProvider: "sp1",
		ImagePath:       "to/service1",
	}

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	serviceFromDB, err := db.GetAllServiceVersions("service1")
	if err != nil {
		t.Errorf("Can't get service: %v", err)
	}

	if !reflect.DeepEqual(serviceFromDB, []servicemanager.ServiceInfo{service}) {
		t.Error("service doesn't match stored one")
	}

	if err := db.RemoveService(service.ServiceID, service.AosVersion); err != nil {
		t.Errorf("Can't remove service: %v", err)
	}

	if _, err := db.GetAllServiceVersions("service1"); err == nil {
		t.Errorf("Should be error: service does not exist ")
	}

	// Clear DB
	if err = db.removeAllServices(); err != nil {
		t.Errorf("Can't remove all services: %v", err)
	}
}

func TestCachedService(t *testing.T) {
	service := servicemanager.ServiceInfo{
		ServiceID: "serviceCached",
		VersionInfo: aostypes.VersionInfo{
			AosVersion: 1,
		},
		ServiceProvider: "sp1",
		ImagePath:       "to/service1",
		Cached:          false,
	}

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	services, err := db.GetAllServiceVersions("serviceCached")
	if err != nil {
		t.Errorf("Can't get service: %v", err)
	}

	if !reflect.DeepEqual([]servicemanager.ServiceInfo{service}, services) {
		t.Error("Unexpected services")
	}

	if err := db.SetServiceCached(service.ServiceID, service.AosVersion, true); err != nil {
		t.Errorf("Can't set service cached: %v", err)
	}

	services, err = db.GetAllServiceVersions("serviceCached")
	if err != nil {
		t.Errorf("Can't get service: %v", err)
	}

	service.Cached = true

	if !reflect.DeepEqual([]servicemanager.ServiceInfo{service}, services) {
		t.Error("Unexpected services")
	}

	service2 := service
	service2.AosVersion = 2

	if err := db.AddService(service2); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	services, err = db.GetAllServiceVersions("serviceCached")
	if err != nil {
		t.Errorf("Can't get service: %v", err)
	}

	if !reflect.DeepEqual([]servicemanager.ServiceInfo{service, service2}, services) {
		t.Error("Unexpected services")
	}

	if err := db.SetServiceCached(service2.ServiceID, service2.AosVersion, true); err != nil {
		t.Errorf("Can't set service cached: %v", err)
	}

	services, err = db.GetAllServiceVersions("serviceCached")
	if err != nil {
		t.Errorf("Can't get service: %v", err)
	}

	service2.Cached = true

	if !reflect.DeepEqual([]servicemanager.ServiceInfo{service, service2}, services) {
		t.Error("Unexpected services")
	}
}

func TestSetTimestampService(t *testing.T) {
	service := servicemanager.ServiceInfo{
		ServiceID: "serviceTimestamp",
		VersionInfo: aostypes.VersionInfo{
			AosVersion: 1,
		},
		ServiceProvider: "sp1",
		ImagePath:       "to/service1",
		Timestamp:       time.Now().UTC(),
	}

	if err := db.AddService(service); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	serviceFromDB, err := db.GetAllServiceVersions("serviceTimestamp")
	if err != nil {
		t.Errorf("Can't get service: %v", err)
	}

	if serviceFromDB[0].Timestamp != service.Timestamp {
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
		Digest: "sha256:1", LayerID: "id1", Path: "path1", OSVersion: "1",
		VersionInfo: aostypes.VersionInfo{
			VendorVersion: "1.0",
			Description:   "some layer 1",
			AosVersion:    1,
		},
		Size: 40,
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
		Digest: "sha256:2", LayerID: "id2", Path: "path2", OSVersion: "1",
		VersionInfo: aostypes.VersionInfo{
			VendorVersion: "2.0",
			Description:   "some layer 2",
			AosVersion:    2,
		}, Size: 50,
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
		Digest: "sha256:3", LayerID: "id3", Path: "path3", OSVersion: "1",
		VersionInfo: aostypes.VersionInfo{
			VendorVersion: "1.0",
			Description:   "some layer 3",
			AosVersion:    3,
		},
		Size: 60,
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

	var allInstances []launcher.InstanceInfo

	for i := 0; i < 5; i++ {
		subjectInstance := launcher.InstanceInfo{
			InstanceInfo: aostypes.InstanceInfo{
				InstanceIdent: aostypes.InstanceIdent{
					ServiceID: "someServiceID" + strconv.Itoa(i),
					SubjectID: testSubjectID, Instance: uint64(i),
				},
				NetworkParameters: aostypes.NetworkParameters{
					NetworkID: "someNetworkID" + strconv.Itoa(i),
					Subnet:    "someSubnet" + strconv.Itoa(i),
					IP:        "someIP" + strconv.Itoa(i),
				},
				StoragePath: fmt.Sprintf("Storage_%d", i),
				StatePath:   fmt.Sprintf("State_%d", i),
				UID:         uint32(i + 100),
			},
			InstanceID: uuid.New().String(),
		}

		if err := db.AddInstance(subjectInstance); err != nil {
			t.Fatalf("Can't add instance to DB %v", err)
		}

		allInstances = append(allInstances, subjectInstance)

		serviceInstance := launcher.InstanceInfo{
			InstanceInfo: aostypes.InstanceInfo{
				InstanceIdent: aostypes.InstanceIdent{
					ServiceID: testServiceID,
					SubjectID: "someSubject" + strconv.Itoa(i), Instance: uint64(i),
				},
				NetworkParameters: aostypes.NetworkParameters{
					NetworkID: "serviceNetworkID" + strconv.Itoa(i),
					Subnet:    "serviceSubnet" + strconv.Itoa(i),
					IP:        "serviceIP" + strconv.Itoa(i),
				},
				StoragePath: fmt.Sprintf("Storage_%d", i),
				StatePath:   fmt.Sprintf("State_%d", i),
				UID:         uint32(i + 200),
			},
			InstanceID: uuid.New().String(),
		}

		if err := db.AddInstance(serviceInstance); err != nil {
			t.Fatalf("Can't add instance to DB %v", err)
		}

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

	testInstanceInfo := launcher.InstanceInfo{
		InstanceInfo: aostypes.InstanceInfo{
			InstanceIdent: aostypes.InstanceIdent{
				ServiceID: testServiceID,
				SubjectID: testSubjectID, Instance: 42,
			},
			StoragePath: "storagePath",
			StatePath:   "statePath",
			UID:         42,
		},
		InstanceID: uuid.New().String(),
	}

	if err := db.AddInstance(testInstanceInfo); err != nil {
		t.Fatalf("Can't add instance to DB %v", err)
	}

	if err := db.AddInstance(testInstanceInfo); err == nil {
		t.Error("Should be error can't add instance")
	}

	if err := db.AddService(servicemanager.ServiceInfo{
		VersionInfo: aostypes.VersionInfo{
			AosVersion: 1,
		},
		ServiceID: testServiceID,
	}); err != nil {
		t.Errorf("Can't add service: %v", err)
	}

	ident, aosVersion, err := db.GetInstanceInfoByID(testInstanceInfo.InstanceID)
	if err != nil {
		t.Errorf("Can't get instance info: %v", err)
	}

	if ident != testInstanceInfo.InstanceIdent {
		t.Error("Unexpected instance ident")
	}

	if aosVersion != 1 {
		t.Error("Unexpected aos version")
	}

	// Negative test: update unavailable instance should be failed
	if err := db.UpdateInstance(
		launcher.InstanceInfo{InstanceID: "unavailbale"}); !errors.Is(err, launcher.ErrNotExist) {
		t.Error("Should be error: instance not exist")
	}

	// Test remove instance
	if err = db.RemoveInstance(testInstanceInfo.InstanceID); err != nil {
		t.Errorf("Can't remove instance: %v", err)
	}

	if err = db.RemoveInstance(testInstanceInfo.InstanceID); err != nil {
		t.Errorf("Can't remove instance: %v", err)
	}
}

func TestNetworks(t *testing.T) {
	networkParameters := []networkmanager.NetworkParameters{
		{
			NetworkID:  "testNetworkID",
			Subnet:     "testSubnet",
			IP:         "testIP",
			VlanID:     1,
			VlanIfName: "testVlanIfName",
		},
		{
			NetworkID:  "testNetworkID2",
			Subnet:     "testSubnet2",
			IP:         "testIP2",
			VlanID:     2,
			VlanIfName: "testVlanIfName2",
		},
	}

	for _, data := range networkParameters {
		if err := db.AddNetworkInfo(data); err != nil {
			t.Fatalf("Can't add network to DB: %v", err)
		}
	}

	// Test get all networks
	allResults, err := db.GetNetworksInfo()
	if err != nil {
		t.Fatalf("Can't get all networks from DB: %v", err)
	}

	if !reflect.DeepEqual(allResults, networkParameters) {
		t.Error("Incorrect get all networks result")
	}

	if err := db.RemoveNetworkInfo("testNetworkID"); err != nil {
		t.Errorf("Can't remove network: %v", err)
	}

	networkParameters = networkParameters[1:]

	// Test get all networks
	allResults, err = db.GetNetworksInfo()
	if err != nil {
		t.Fatalf("Can't get all networks from DB: %v", err)
	}

	if !reflect.DeepEqual(allResults, networkParameters) {
		t.Error("Incorrect get all networks result")
	}
}

func TestInstancesID(t *testing.T) {
	addedInstance := []launcher.InstanceInfo{
		{
			InstanceInfo: aostypes.InstanceInfo{
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "TestSevrID", SubjectID: "TestSubID", Instance: 0},
			},
			InstanceID: "TestSevrID_TestSubID_0",
		},
		{
			InstanceInfo: aostypes.InstanceInfo{
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "TestSevrID", SubjectID: "TestSubID", Instance: 1},
			},
			InstanceID: "TestSevrID_TestSubID_1",
		},
		{
			InstanceInfo: aostypes.InstanceInfo{
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "TestSevrID", SubjectID: "TestSubID1", Instance: 0},
			},
			InstanceID: "TestSevrID_TestSubID1_0",
		},
		{
			InstanceInfo: aostypes.InstanceInfo{
				InstanceIdent: aostypes.InstanceIdent{ServiceID: "TestSevrID2", SubjectID: "TestSubID", Instance: 0},
			},
			InstanceID: "TestSevrID2_TestSubID_0",
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
		{
			filter:      cloudprotocol.NewInstanceFilter("", "TestSubID", -1),
			expectedIds: []string{"TestSevrID_TestSubID_0", "TestSevrID_TestSubID_1", "TestSevrID2_TestSubID_0"},
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

func TestOnlineTime(t *testing.T) {
	onlineTime, err := db.GetOnlineTime()
	if err != nil {
		t.Fatalf("Can't get default online time: %v", err)
	}

	if onlineTime.IsZero() {
		t.Errorf("Wrong default online time: %v", onlineTime)
	}

	setOnlineTime := time.Now().Add(24 * time.Hour)

	if err = db.SetOnlineTime(setOnlineTime); err != nil {
		t.Fatalf("Can't set online time: %v", err)
	}

	getOnlineTime, err := db.GetOnlineTime()
	if err != nil {
		t.Errorf("Can't get online time: %v", err)
	}

	if !setOnlineTime.Equal(getOnlineTime) {
		t.Errorf("Wrong got online time: %v", getOnlineTime)
	}
}

func TestMigrationToV1(t *testing.T) {
	migrationDB := path.Join(tmpDir, "test_migration.db")
	mergedMigrationDir := path.Join(tmpDir, "mergedMigration")

	if err := os.MkdirAll(mergedMigrationDir, 0o755); err != nil {
		t.Fatalf("Error creating merged migration dir: %v", err)
	}

	defer func() {
		if err := os.RemoveAll(mergedMigrationDir); err != nil {
			t.Fatalf("Error removing merged migration dir: %v", err)
		}

		if err := os.RemoveAll(migrationDB); err != nil {
			t.Fatalf("Error removing migration db: %v", err)
		}
	}()

	if err := createDatabaseV0(migrationDB); err != nil {
		t.Fatalf("Can't create initial database %v", err)
	}

	// Migration upward
	db, err := newDatabase(migrationDB, "migration", mergedMigrationDir, 1)
	if err != nil {
		t.Fatalf("Can't create database: %v", err)
	}

	if err = isDatabaseVer1(db.sql); err != nil {
		t.Fatalf("Error checking db version: %v", err)
	}

	db.Close()

	// Migration downward
	db, err = newDatabase(migrationDB, "migration", mergedMigrationDir, 0)
	if err != nil {
		t.Fatalf("Can't create database: %v", err)
	}

	if err = isDatabaseVer0(db.sql); err != nil {
		t.Fatalf("Error checking db version: %v", err)
	}

	db.Close()
}

func TestMigrationToV7(t *testing.T) {
	migrationDB := path.Join(tmpDir, "test_migration.db")
	mergedMigrationDir := path.Join(tmpDir, "mergedMigration")

	if err := os.MkdirAll(mergedMigrationDir, 0o755); err != nil {
		t.Fatalf("Error creating merged migration dir: %v", err)
	}

	defer func() {
		if err := os.RemoveAll(mergedMigrationDir); err != nil {
			t.Fatalf("Error removing merged migration dir: %v", err)
		}

		if err := os.RemoveAll(migrationDB); err != nil {
			t.Fatalf("Error removing migration db: %v", err)
		}
	}()

	if err := createDatabaseV6(migrationDB, mergedMigrationDir); err != nil {
		t.Fatalf("Can't create initial database %v", err)
	}

	// Migration upward
	db, err := newDatabase(migrationDB, "migration", mergedMigrationDir, 7)
	if err != nil {
		t.Fatalf("Can't create database: %v", err)
	}

	if err = isDatabaseVer7(db.sql); err != nil {
		t.Fatalf("Error checking db version: %v", err)
	}

	db.Close()

	// Migration downward
	db, err = newDatabase(migrationDB, "migration", mergedMigrationDir, 6)
	if err != nil {
		t.Fatalf("Can't create database: %v", err)
	}

	if err = isDatabaseVer6(db.sql); err != nil {
		t.Fatalf("Error checking db version: %v", err)
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

func createDatabaseV6(name string, mergedMigrationPath string) (err error) {
	sqlite, err := sql.Open("sqlite3", fmt.Sprintf("%s?_busy_timeout=%d&_journal_mode=%s&_sync=%s",
		name, busyTimeout, journalMode, syncMode))
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer sqlite.Close()

	if _, err = sqlite.Exec(
		`CREATE TABLE config (
			operationVersion INTEGER,
			cursor TEXT,
			envvars TEXT,
			onlineTime TIMESTAMP)`); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = sqlite.Exec(
		`INSERT INTO config (
			operationVersion,
			cursor,
			envvars,
			onlineTime) values(?, ?, ?, ?)`, launcher.OperationVersion, "", "", time.Now()); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = sqlite.Exec(`CREATE TABLE IF NOT EXISTS services (id TEXT NOT NULL ,
                                                                  aosVersion INTEGER,
                                                                  providerID TEXT,
                                                                  description TEXT,
                                                                  imagePath TEXT,
                                                                  manifestDigest BLOB,
                                                                  cached INTEGER,
                                                                  timestamp TIMESTAMP,
                                                                  size INTEGER,
                                                                  GID INTEGER,
                                                                  PRIMARY KEY(id, aosVersion))`); err != nil {
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
                                                                aosVersion INTEGER,
                                                                timestamp TIMESTAMP,
                                                                cached INTEGER,
                                                                size INTEGER)`); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = sqlite.Exec(`CREATE TABLE IF NOT EXISTS instances (instanceID TEXT NOT NULL PRIMARY KEY,
                                                                   serviceID TEXT,
                                                                   subjectID TEXT,
                                                                   instance INTEGER,
                                                                   uid INTEGER,
                                                                   priority INTEGER,
                                                                   storagePath TEXT,
                                                                   statePath TEXT,
                                                                   network BLOB)`); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = sqlite.Exec(`CREATE TABLE IF NOT EXISTS network (networkID TEXT NOT NULL PRIMARY KEY,
                                                                 ip TEXT,
                                                                 subnet TEXT,
                                                                 vlanID INTEGER)`); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = migration.MergeMigrationFiles("migration", mergedMigrationPath); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = migration.SetDatabaseVersion(sqlite, "migration", 6); err != nil {
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

func isDatabaseVer6(sqlite *sql.DB) (err error) {
	rows, err := sqlite.Query(
		"SELECT COUNT(*) AS CNTREC FROM pragma_table_info('network') WHERE name='vlanIfName'")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer rows.Close()

	if rows.Err() != nil {
		return aoserrors.Wrap(rows.Err())
	}

	if !rows.Next() {
		return errNotExist
	}

	count := 0

	if err = rows.Scan(&count); err != nil {
		return aoserrors.Wrap(err)
	}

	if count != 0 {
		return aoserrors.Errorf("vlanIfName column should not exist")
	}

	return nil
}

func isDatabaseVer7(sqlite *sql.DB) (err error) {
	rows, err := sqlite.Query(
		"SELECT COUNT(*) AS CNTREC FROM pragma_table_info('network') WHERE name='vlanIfName'")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer rows.Close()

	if rows.Err() != nil {
		return aoserrors.Wrap(rows.Err())
	}

	if !rows.Next() {
		return errNotExist
	}

	count := 0

	if err = rows.Scan(&count); err != nil {
		return aoserrors.Wrap(err)
	}

	if count == 0 {
		return errNotExist
	}

	return nil
}
