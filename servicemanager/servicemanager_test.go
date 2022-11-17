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

package servicemanager_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/image"
	"github.com/aoscloud/aos_common/spaceallocator"
	"github.com/aoscloud/aos_common/utils/fs"
	"github.com/opencontainers/go-digest"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
)

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
 * Consts
 **********************************************************************************************************************/

const (
	kilobyte           = uint64(1 << 10)
	megabyte           = uint64(1 << 20)
	errorAddServiceID  = "errorAddServiceID"
	errorGetServicID   = "errorGetServicID"
	blobsFolder        = "blobs"
	defaultServiceSize = int64(2 * kilobyte)
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testServiceStorage struct {
	getAllError bool
	Services    []servicemanager.ServiceInfo
}

type testAllocator struct {
	sync.Mutex

	totalSize     uint64
	allocatedSize uint64
	partLimit     uint
	remover       spaceallocator.ItemRemover
	outdatedItems []testOutdatedItem
}

type expectedService struct {
	serviceID string
	version   uint64
}

type testSpace struct {
	allocator *testAllocator
	size      uint64
}

type testOutdatedItem struct {
	id   string
	size uint64
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var (
	tmpDir           string
	serviceAllocator = &testAllocator{}
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
***********************************************************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	ret := m.Run()

	cleanup()

	os.Exit(ret)
}

/***********************************************************************************************************************
* Tests
***********************************************************************************************************************/

func TestInstallService(t *testing.T) {
	serviceStorage := &testServiceStorage{}

	config := &config.Config{
		ServicesDir: filepath.Join(tmpDir, "servicemanager", "services"),
		LayersDir:   filepath.Join(tmpDir, "layers"),
		DownloadDir: filepath.Join(tmpDir, "downloads"),
	}

	serviceAllocator = &testAllocator{}

	sm, err := servicemanager.New(config, serviceStorage)
	if err != nil {
		t.Fatalf("Can't create SM: %s", err)
	}
	defer sm.Close()

	services := make(map[string][]aostypes.ServiceInfo)

	generateService := []struct {
		serviceID      string
		serviceContent string
		serviceSize    uint64
		version        int
	}{
		{serviceID: "service1", serviceContent: "service1", serviceSize: 2 * kilobyte, version: 1},
		{serviceID: "service2", serviceContent: "service2", serviceSize: 1 * kilobyte, version: 1},
		{serviceID: "service2", serviceContent: "service2.2", serviceSize: 2 * kilobyte, version: 2},
		{serviceID: "service3", serviceContent: "service2", serviceSize: 3 * kilobyte, version: 1},
	}

	for _, service := range generateService {
		serviceInfo, err := prepareService(
			service.serviceContent, service.serviceID, uint64(service.version), int64(service.serviceSize))
		if err != nil {
			t.Fatalf("Can't prepare service: %v", err)
		}

		services[service.serviceID] = append(services[service.serviceID], serviceInfo)
	}

	cases := []struct {
		desiredServices   []aostypes.ServiceInfo
		removedServices   []expectedService
		restoreServices   []expectedService
		installedServices []expectedService
	}{
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service2", version: 1},
				{serviceID: "service3", version: 1},
			}),
			installedServices: []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service2", version: 1},
				{serviceID: "service3", version: 1},
			},
		},
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service2", version: 1},
				{serviceID: "service2", version: 2},
				{serviceID: "service3", version: 1},
			}),
			installedServices: []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service2", version: 1},
				{serviceID: "service2", version: 2},
				{serviceID: "service3", version: 1},
			},
		},
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service3", version: 1},
			}),
			installedServices: []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service3", version: 1},
			},
			removedServices: []expectedService{
				{serviceID: "service2", version: 1},
				{serviceID: "service2", version: 2},
			},
		},
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service2", version: 2},
			}),
			installedServices: []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service2", version: 1},
				{serviceID: "service2", version: 2},
			},
			removedServices: []expectedService{
				{serviceID: "service3", version: 1},
			},
			restoreServices: []expectedService{
				{serviceID: "service2", version: 1},
				{serviceID: "service2", version: 2},
			},
		},
	}

	for _, tCase := range cases {
		if err := sm.ProcessDesiredServices(tCase.desiredServices); err != nil {
			t.Errorf("Can't process desired services: %v", err)
		}

	nextInstallService:
		for _, installService := range tCase.installedServices {
			for _, storeService := range serviceStorage.Services {
				if installService.serviceID == storeService.ServiceID &&
					installService.version == storeService.AosVersion {
					continue nextInstallService
				}
			}

			t.Errorf("Service %s should be installed", installService.serviceID)
		}

	nextRemoveService:
		for _, removeService := range tCase.removedServices {
			for _, storeService := range serviceStorage.Services {
				if removeService.serviceID == storeService.ServiceID &&
					removeService.version == storeService.AosVersion && storeService.Cached {
					continue nextRemoveService
				}
			}

			t.Errorf("Service %s should be cached", removeService.serviceID)
		}

	nextRestoreService:
		for _, restoreService := range tCase.restoreServices {
			for _, storeService := range serviceStorage.Services {
				if restoreService.serviceID == storeService.ServiceID &&
					restoreService.version == storeService.AosVersion && !storeService.Cached {
					continue nextRestoreService
				}
			}

			t.Errorf("Service %s should not be cached", restoreService.serviceID)
		}
	}
}

func TestRemoteDownloadLayer(t *testing.T) {
	serviceStorage := &testServiceStorage{}

	config := &config.Config{
		ServicesDir: filepath.Join(tmpDir, "servicemanager", "services"),
		LayersDir:   filepath.Join(tmpDir, "layers"),
		DownloadDir: filepath.Join(tmpDir, "downloads"),
	}

	serviceAllocator = &testAllocator{}

	sm, err := servicemanager.New(config, serviceStorage)
	if err != nil {
		t.Fatalf("Can't create SM: %s", err)
	}
	defer sm.Close()

	fileServerDir, err := ioutil.TempDir("", "sm_fileserver")
	if err != nil {
		t.Fatalf("Error create temporary dir: %s", err)
	}

	serviceInfo, err := prepareService(
		"Service content", "service1", 1, defaultServiceSize)
	if err != nil {
		t.Fatalf("Can't prepare service: %v", err)
	}

	urlVal, err := url.Parse(serviceInfo.URL)
	if err != nil {
		t.Fatalf("Can't parse url: %s", err)
	}

	_ = os.Rename(urlVal.Path, filepath.Join(fileServerDir, "downloadImage"))

	server := &http.Server{
		Addr:              ":9000",
		Handler:           http.FileServer(http.Dir(fileServerDir)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("Can't serve http server: %s", err)
		}
	}()

	time.Sleep(1 * time.Second)

	defer server.Close()

	serviceInfo.URL = "http://:9000/downloadImage"

	if err := sm.ProcessDesiredServices([]aostypes.ServiceInfo{serviceInfo}); err != nil {
		t.Errorf("Can't process desired services: %v", err)
	}

	if _, err := sm.GetServiceInfo("service1"); err != nil {
		t.Errorf("Can't get service info: %v", err)
	}
}

func TestImageParts(t *testing.T) {
	serviceStorage := &testServiceStorage{}

	config := &config.Config{
		ServicesDir: filepath.Join(tmpDir, "servicemanager", "services"),
		LayersDir:   filepath.Join(tmpDir, "layers"),
		DownloadDir: filepath.Join(tmpDir, "downloads"),
	}

	serviceAllocator = &testAllocator{}

	sm, err := servicemanager.New(config, serviceStorage)
	if err != nil {
		t.Fatalf("Can't create SM: %s", err)
	}
	defer sm.Close()

	serviceID := "testService0"

	serviceInfo, err := prepareService("Service content", serviceID, 1, defaultServiceSize)
	if err != nil {
		t.Fatalf("Can't prepare test service: %s", err)
	}

	if err = sm.ProcessDesiredServices([]aostypes.ServiceInfo{serviceInfo}); err != nil {
		t.Errorf("Can't install service: %s", err)
	}

	storeServiceInfo, err := sm.GetServiceInfo(serviceID)
	if err != nil {
		t.Errorf("Can't get service info: %v", err)
	}

	imageParts, err := sm.GetImageParts(storeServiceInfo)
	if err != nil {
		t.Errorf("Can't get image parts: %s", err)
	}

	if imageParts.ImageConfigPath == "" {
		t.Error("Image config path should not be empty")
	}

	if imageParts.ServiceConfigPath == "" {
		t.Error("Service config path should not be empty")
	}

	if imageParts.ServiceFSPath == "" {
		t.Error("Service fs path should not be empty")
	}

	if len(imageParts.LayersDigest) != 1 {
		t.Error("Count of layers should be 1")
	}
}

func TestValidateService(t *testing.T) {
	serviceStorage := &testServiceStorage{}

	config := &config.Config{
		ServicesDir: filepath.Join(tmpDir, "servicemanager", "services"),
		LayersDir:   filepath.Join(tmpDir, "layers"),
		DownloadDir: filepath.Join(tmpDir, "downloads"),
	}

	serviceAllocator = &testAllocator{}

	sm, err := servicemanager.New(config, serviceStorage)
	if err != nil {
		t.Fatalf("Can't create SM: %s", err)
	}
	defer sm.Close()

	serviceID := "testServiceValidate"

	service, err := prepareService("Service content", serviceID, 1, defaultServiceSize)
	if err != nil {
		t.Errorf("Can't prepare test service: %s", err)
	}

	if err := sm.ProcessDesiredServices([]aostypes.ServiceInfo{service}); err != nil {
		t.Errorf("Can't process desired services: %v", err)
	}

	serviceInfo, err := sm.GetServiceInfo(serviceID)
	if err != nil {
		t.Errorf("Can't get service info: %s", err)
	}

	if err = sm.ValidateService(serviceInfo); err != nil {
		t.Errorf("Error service validation: %s", err)
	}
}

func TestAllocateMemoryInstallService(t *testing.T) {
	serviceStorage := &testServiceStorage{}

	config := &config.Config{
		ServicesDir:    filepath.Join(tmpDir, "servicemanager", "services"),
		DownloadDir:    filepath.Join(tmpDir, "downloads"),
		ServiceTTLDays: 2,
	}

	serviceAllocator = &testAllocator{
		totalSize: 1 * megabyte,
	}

	sm, err := servicemanager.New(config, serviceStorage)
	if err != nil {
		t.Fatalf("Can't create SM: %v", err)
	}
	defer sm.Close()

	services := make(map[string][]aostypes.ServiceInfo)

	generateService := []struct {
		serviceID      string
		serviceContent string
		serviceSize    uint64
		version        int
	}{
		{serviceID: "service1", serviceContent: "service1", serviceSize: 512 * kilobyte, version: 1},
		{serviceID: "service2", serviceContent: "service2", serviceSize: 520 * kilobyte, version: 1},
		{serviceID: "service3", serviceContent: "service2", serviceSize: 600 * kilobyte, version: 1},
	}

	for _, service := range generateService {
		serviceInfo, err := prepareService(
			service.serviceContent, service.serviceID, uint64(service.version), int64(service.serviceSize))
		if err != nil {
			t.Fatalf("Can't prepare service: %v", err)
		}

		services[service.serviceID] = append(services[service.serviceID], serviceInfo)
	}

	cases := []struct {
		desiredServices     []aostypes.ServiceInfo
		processDesiredError error
	}{
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service1", version: 1},
			}),
		},
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service2", version: 1},
			}),
		},
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service2", version: 1},
				{serviceID: "service3", version: 1},
			}),
			processDesiredError: spaceallocator.ErrNoSpace,
		},
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service3", version: 1},
			}),
		},
	}

	for _, tCase := range cases {
		if err := sm.ProcessDesiredServices(tCase.desiredServices); !errors.Is(err, tCase.processDesiredError) {
			t.Errorf("Can't process desired service: %v", err)
		}
	}
}

func TestCachedServiceOnStart(t *testing.T) {
	serviceStorage := &testServiceStorage{}

	config := &config.Config{
		ServicesDir:    filepath.Join(tmpDir, "servicemanager", "services"),
		DownloadDir:    filepath.Join(tmpDir, "downloads"),
		ServiceTTLDays: 2,
	}

	serviceAllocator = &testAllocator{
		totalSize: 1 * megabyte,
	}

	sm, err := servicemanager.New(config, serviceStorage)
	if err != nil {
		t.Fatalf("Can't create SM: %v", err)
	}

	services := make(map[string][]aostypes.ServiceInfo)

	generateService := []struct {
		serviceID      string
		serviceContent string
		serviceSize    uint64
		version        int
	}{
		{serviceID: "service1", serviceContent: "service1", serviceSize: 512 * kilobyte, version: 1},
		{serviceID: "service2", serviceContent: "service2", serviceSize: 256 * kilobyte, version: 1},
		{serviceID: "service3", serviceContent: "service3", serviceSize: 300 * kilobyte, version: 1},
	}

	for _, service := range generateService {
		serviceInfo, err := prepareService(
			service.serviceContent, service.serviceID, uint64(service.version), int64(service.serviceSize))
		if err != nil {
			t.Fatalf("Can't prepare service: %v", err)
		}

		services[service.serviceID] = append(services[service.serviceID], serviceInfo)
	}

	cases := []struct {
		desiredServices     []aostypes.ServiceInfo
		processDesiredError error
		useServiceID        expectedService
	}{
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service2", version: 1},
			}),
			useServiceID: expectedService{serviceID: "service1", version: 1},
		},
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service2", version: 1},
				{serviceID: "service3", version: 1},
			}),
			processDesiredError: spaceallocator.ErrNoSpace,
		},
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service2", version: 1},
			}),
		},
	}

	for _, tCase := range cases {
		if err := sm.ProcessDesiredServices(tCase.desiredServices); !errors.Is(err, tCase.processDesiredError) {
			t.Errorf("Can't process desired service: %v", err)
		}
	}

	sm.Close()

	if sm, err = servicemanager.New(config, serviceStorage); err != nil {
		t.Fatalf("Can't create SM: %v", err)
	}
	defer sm.Close()

	if _, err := sm.GetServiceInfo("service1"); !errors.Is(err, servicemanager.ErrNotExist) {
		t.Fatalf("Should be error not exist: %v", err)
	}

	if err := sm.ProcessDesiredServices(getDesiredServices(services, []expectedService{
		{serviceID: "service2", version: 1},
		{serviceID: "service3", version: 1},
	})); err != nil {
		t.Errorf("Can't process desired service: %v", err)
	}
}

func TestFailCreateAllocator(t *testing.T) {
	serviceAllocator = &testAllocator{
		partLimit: 100,
	}

	serviceStorage := &testServiceStorage{}

	config := &config.Config{
		ServicesDir:       filepath.Join(tmpDir, "servicemanager", "services"),
		DownloadDir:       filepath.Join(tmpDir, "downloads"),
		ServicesPartLimit: 110,
	}

	if _, err := servicemanager.New(config, serviceStorage); err == nil {
		t.Fatal("Should be error creating allocator")
	}
}

func TestRemoveServiceVersionOnInstall(t *testing.T) {
	serviceStorage := &testServiceStorage{}

	config := &config.Config{
		ServicesDir:    filepath.Join(tmpDir, "servicemanager", "services"),
		DownloadDir:    filepath.Join(tmpDir, "downloads"),
		ServiceTTLDays: 2,
	}

	serviceAllocator = &testAllocator{}

	sm, err := servicemanager.New(config, serviceStorage)
	if err != nil {
		t.Fatalf("Can't create SM: %v", err)
	}
	defer sm.Close()

	services := make(map[string][]aostypes.ServiceInfo)

	generateService := []struct {
		serviceID      string
		serviceContent string
		serviceSize    uint64
		version        int
	}{
		{serviceID: "service1", serviceContent: "service1", serviceSize: 512 * kilobyte, version: 1},
		{serviceID: "service1", serviceContent: "service2", serviceSize: 256 * kilobyte, version: 2},
		{serviceID: "service1", serviceContent: "service3", serviceSize: 300 * kilobyte, version: 2},
	}

	for _, service := range generateService {
		serviceInfo, err := prepareService(
			service.serviceContent, service.serviceID, uint64(service.version), int64(service.serviceSize))
		if err != nil {
			t.Fatalf("Can't prepare service: %v", err)
		}

		services[service.serviceID] = append(services[service.serviceID], serviceInfo)
	}

	cases := []struct {
		desiredServices      []aostypes.ServiceInfo
		expectedServiceCount int
	}{
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service1", version: 1},
			}),
			expectedServiceCount: 1,
		},
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service1", version: 2},
			}),
			expectedServiceCount: 2,
		},
		{
			desiredServices: getDesiredServices(services, []expectedService{
				{serviceID: "service1", version: 1},
				{serviceID: "service1", version: 2},
				{serviceID: "service1", version: 3},
			}),
			expectedServiceCount: 2,
		},
	}

	for _, tCase := range cases {
		if err := sm.ProcessDesiredServices(tCase.desiredServices); err != nil {
			t.Errorf("Can't process desired service: %v", err)
		}

		serviceVersions, err := serviceStorage.GetAllServiceVersions(tCase.desiredServices[0].ID)
		if err != nil {
			t.Fatalf("Can't get services version: %v", err)
		}

		if len(serviceVersions) != tCase.expectedServiceCount {
			t.Error("Unexpected services size")
		}
	}
}

/***********************************************************************************************************************
* Interfaces
***********************************************************************************************************************/

func newSpaceAllocator(
	path string, partLimit uint, remover spaceallocator.ItemRemover,
) (spaceallocator.Allocator, error) {
	if serviceAllocator.partLimit < partLimit {
		return nil, aoserrors.New("can't create space allocator")
	}

	if remover != nil {
		serviceAllocator.remover = remover
	}

	return serviceAllocator, nil
}

func (allocator *testAllocator) AllocateSpace(size uint64) (spaceallocator.Space, error) {
	allocator.Lock()
	defer allocator.Unlock()

	if allocator.totalSize != 0 && allocator.allocatedSize+size > allocator.totalSize {
		for allocator.allocatedSize+size > allocator.totalSize {
			if len(allocator.outdatedItems) == 0 {
				return nil, spaceallocator.ErrNoSpace
			}

			if err := allocator.remover(allocator.outdatedItems[0].id); err != nil {
				return nil, err
			}

			if allocator.outdatedItems[0].size < allocator.allocatedSize {
				allocator.allocatedSize -= allocator.outdatedItems[0].size
			} else {
				allocator.allocatedSize = 0
			}

			allocator.outdatedItems = allocator.outdatedItems[1:]
		}
	}

	allocator.allocatedSize += size

	return &testSpace{allocator: allocator, size: size}, nil
}

func (allocator *testAllocator) FreeSpace(size uint64) {
	allocator.Lock()
	defer allocator.Unlock()

	if size > allocator.allocatedSize {
		allocator.allocatedSize = 0
	} else {
		allocator.allocatedSize -= size
	}
}

func (allocator *testAllocator) AddOutdatedItem(id string, size uint64, timestamp time.Time) error {
	allocator.outdatedItems = append(allocator.outdatedItems, testOutdatedItem{id: id, size: size})

	return nil
}

func (allocator *testAllocator) RestoreOutdatedItem(id string) {
	for i, item := range allocator.outdatedItems {
		if item.id == id {
			allocator.outdatedItems = append(allocator.outdatedItems[:i], allocator.outdatedItems[i+1:]...)

			break
		}
	}
}

func (allocator *testAllocator) Close() error {
	return nil
}

func (space *testSpace) Accept() error {
	return nil
}

func (space *testSpace) Release() error {
	space.allocator.FreeSpace(space.size)

	return nil
}

func (storage *testServiceStorage) GetAllServiceVersions(serviceID string) (service []servicemanager.ServiceInfo, err error) {
	if serviceID == errorGetServicID {
		return service, aoserrors.New("can't get service")
	}

	for _, storeService := range storage.Services {
		if storeService.ServiceID == serviceID {
			service = append(service, storeService)
		}
	}

	if len(service) == 0 {
		return service, servicemanager.ErrNotExist
	}

	return service, nil
}

func (storage *testServiceStorage) GetServices() (services []servicemanager.ServiceInfo, err error) {
	if storage.getAllError {
		return nil, aoserrors.New("can't get services")
	}

	return storage.Services, nil
}

func (storage *testServiceStorage) AddService(service servicemanager.ServiceInfo) (err error) {
	if service.ServiceID == errorAddServiceID {
		return aoserrors.New("can't add service")
	}

	storage.Services = append(storage.Services, service)

	return err
}

func (storage *testServiceStorage) RemoveService(serviceID string, aosVersion uint64) error {
	for i, outService := range storage.Services {
		if outService.ServiceID == serviceID && outService.AosVersion == aosVersion {
			storage.Services = append(storage.Services[:i], storage.Services[i+1:]...)

			return nil
		}
	}

	return servicemanager.ErrNotExist
}

func (storage *testServiceStorage) SetServiceCached(serviceID string, aosVersion uint64, cached bool) (err error) {
	var found bool

	for i, serviceInfo := range storage.Services {
		if serviceInfo.ServiceID == serviceID {
			storage.Services[i].Cached = cached

			found = true
		}
	}

	if !found {
		err = servicemanager.ErrNotExist
	}

	return err
}

/***********************************************************************************************************************
* Private
***********************************************************************************************************************/

func setup() (err error) {
	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return aoserrors.Wrap(err)
	}

	servicemanager.NewSpaceAllocator = newSpaceAllocator

	return nil
}

func cleanup() {
	os.RemoveAll(tmpDir)
}

func prepareService(
	testContent, serviceID string, aosVersion uint64, servicelayerSize int64,
) (serviceInfo aostypes.ServiceInfo, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	defer os.RemoveAll(imageDir)

	// create dir
	if err := os.MkdirAll(filepath.Join(imageDir, "rootfs", "home"), 0o755); err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	file, err := os.Create(filepath.Join(imageDir, "rootfs", "home", "service.py"))
	if err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	if err := file.Truncate(servicelayerSize); err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	rootFsPath := filepath.Join(imageDir, "rootfs")

	serviceSize, err := fs.GetDirSize(rootFsPath)
	if err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	fsDigest, err := generateFsLayer(imageDir, rootFsPath)
	if err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	aosSrvConfigDigest, err := generateAndSaveDigest(filepath.Join(imageDir, blobsFolder), []byte("{}"))
	if err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	imgSpecDigestDigest, err := generateAndSaveDigest(filepath.Join(imageDir, blobsFolder), []byte("{}"))
	if err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	imgAosLayerDigest, err := generateAndSaveDigest(imageDir, []byte("{}"))
	if err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	if err := genarateImageManfest(
		imageDir, &imgSpecDigestDigest, &aosSrvConfigDigest, &fsDigest,
		serviceSize, []digest.Digest{imgAosLayerDigest}); err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	imageFile, err := ioutil.TempFile("", "aos_")
	if err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	outputURL := imageFile.Name()
	imageFile.Close()

	if err = packImage(imageDir, outputURL); err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	fileInfo, err := image.CreateFileInfo(context.Background(), outputURL)
	if err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	return aostypes.ServiceInfo{
		VersionInfo: aostypes.VersionInfo{
			AosVersion: aosVersion,
		},
		ID:     serviceID,
		URL:    "file://" + outputURL,
		Sha256: fileInfo.Sha256,
		Sha512: fileInfo.Sha512,
		Size:   fileInfo.Size,
	}, nil
}

func generateFsLayer(imgFolder, rootfs string) (digest digest.Digest, err error) {
	blobsDir := filepath.Join(imgFolder, blobsFolder)
	if err := os.MkdirAll(blobsDir, 0o755); err != nil {
		return digest, aoserrors.Wrap(err)
	}

	tarFile := filepath.Join(blobsDir, "_temp.tar.gz")

	if output, err := exec.Command("tar", "-C", rootfs, "-czf", tarFile, "./").CombinedOutput(); err != nil {
		return digest, aoserrors.Errorf("error: %s, code: %s", string(output), err)
	}
	defer os.Remove(tarFile)

	file, err := os.Open(tarFile)
	if err != nil {
		return digest, aoserrors.Wrap(err)
	}
	defer file.Close()

	byteValue, err := ioutil.ReadAll(file)
	if err != nil {
		return digest, aoserrors.Wrap(err)
	}

	digest, err = generateAndSaveDigest(blobsDir, byteValue)
	if err != nil {
		return digest, aoserrors.Wrap(err)
	}

	os.RemoveAll(rootfs)

	return digest, nil
}

func generateAndSaveDigest(folder string, data []byte) (retDigest digest.Digest, err error) {
	fullPath := filepath.Join(folder, "sha256")
	if err := os.MkdirAll(fullPath, 0o755); err != nil {
		return retDigest, aoserrors.Wrap(err)
	}

	h := sha256.New()
	h.Write(data)
	retDigest = digest.NewDigest("sha256", h)

	file, err := os.Create(filepath.Join(fullPath, retDigest.Hex()))
	if err != nil {
		return retDigest, aoserrors.Wrap(err)
	}
	defer file.Close()

	_, err = file.Write(data)
	if err != nil {
		return retDigest, aoserrors.Wrap(err)
	}

	return retDigest, nil
}

func genarateImageManfest(folderPath string, imgConfig, aosSrvConfig, rootfsLayer *digest.Digest,
	rootfsLayerSize int64, srvLayers []digest.Digest,
) (err error) {
	type serviceManifest struct {
		imagespec.Manifest
		AosService *imagespec.Descriptor `json:"aosService,omitempty"`
	}

	var manifest serviceManifest
	manifest.SchemaVersion = 2

	manifest.Config = imagespec.Descriptor{
		MediaType: "application/vnd.oci.image.config.v1+json",
		Digest:    *imgConfig,
	}

	if aosSrvConfig != nil {
		manifest.AosService = &imagespec.Descriptor{
			MediaType: "application/vnd.aos.service.config.v1+json",
			Digest:    *aosSrvConfig,
		}
	}

	layerDescriptor := imagespec.Descriptor{
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		Digest:    *rootfsLayer,
		Size:      rootfsLayerSize,
	}

	manifest.Layers = append(manifest.Layers, layerDescriptor)

	for _, layerDigest := range srvLayers {
		layerDescriptor := imagespec.Descriptor{
			MediaType: "application/vnd.aos.image.layer.v1.tar",
			Digest:    layerDigest,
		}

		manifest.Layers = append(manifest.Layers, layerDescriptor)
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	jsonFile, err := os.Create(filepath.Join(folderPath, "manifest.json"))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err := jsonFile.Write(data); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func packImage(source, name string) (err error) {
	log.WithFields(log.Fields{"source": source, "name": name}).Debug("Pack image")

	if output, err := exec.Command("tar", "-C", source, "-cf", name, "./").CombinedOutput(); err != nil {
		return aoserrors.Errorf("tar error: %s, code: %s", string(output), err)
	}

	return nil
}

func getDesiredServices(
	services map[string][]aostypes.ServiceInfo, expectedServices []expectedService,
) (desiredServices []aostypes.ServiceInfo) {
	for _, expectedService := range expectedServices {
		serviceInfos, ok := services[expectedService.serviceID]
		if !ok {
			continue
		}

		for _, serviceInfo := range serviceInfos {
			if serviceInfo.AosVersion == expectedService.version {
				desiredServices = append(desiredServices, serviceInfo)
				break
			}
		}
	}

	return
}
