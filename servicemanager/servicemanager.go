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

package servicemanager

import (
	"bytes"
	"context"
	"errors"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/image"
	"github.com/aoscloud/aos_common/utils/action"
	"github.com/aoscloud/aos_common/utils/fs"
	"github.com/opencontainers/go-digest"
	log "github.com/sirupsen/logrus"
	"golang.org/x/mod/sumdb/dirhash"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/utils/uidgidpool"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const maxConcurrentActions = 10

const tmpRootFSDir = "tmprootfs"

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// ServiceStorage provides API to create, remove or access services DB.
type ServiceStorage interface {
	GetService(serviceID string) (ServiceInfo, error)
	GetAllServices() ([]ServiceInfo, error)
	AddService(ServiceInfo) error
	GetAllServiceVersions(serviceID string) ([]ServiceInfo, error)
	RemoveService(ServiceInfo) error
	ActivateService(serviceID string, aosVersion uint64) error
	SetServiceTimestamp(serviceID string, aosVersion uint64, timestamp time.Time) error
}

// LayerProvider layer provider.
type LayerProvider interface {
	UninstallLayer(digest string) (deallocatedSize int64, err error)
}

type byServiceTTL []ServiceInfo

// ServiceManager instance.
type ServiceManager struct {
	sync.Mutex
	servicesDir             string
	downloadDir             string
	serviceTTLDays          uint64
	pendingRemove           []ServiceInfo
	availableSize           int64
	numTryAllocateSpace     uint64
	actionHandler           *action.Handler
	gidPool                 *uidgidpool.IdentifierPool
	serviceInfoProvider     ServiceStorage
	layerProvider           LayerProvider
	isDownloadSamePartition bool
	isLayersSamePartition   bool
}

// ServiceInfo service information.
type ServiceInfo struct {
	ServiceID       string
	AosVersion      uint64
	ServiceProvider string
	Description     string
	ImagePath       string
	GID             int
	ManifestDigest  []byte
	Timestamp       time.Time
	IsActive        bool
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var (
	// ErrNotExist not exist service error.
	ErrNotExist = errors.New("service not exist")
	// ErrVersionMismatch new service version <= existing one.
	ErrVersionMismatch = errors.New("version mismatch")
	ErrNotEnoughMemory = errors.New("not enough memory")
)

// GetAvailableSize used to be able to mocking the functionality in tests.
var GetAvailableSize = fs.GetAvailableSize // nolint:gochecknoglobals

/***********************************************************************************************************************
 * Sort instance priority
 **********************************************************************************************************************/

func (services byServiceTTL) Len() int { return len(services) }

func (services byServiceTTL) Less(i, j int) bool {
	return services[i].Timestamp.Before(services[j].Timestamp)
}

func (services byServiceTTL) Swap(i, j int) { services[i], services[j] = services[j], services[i] }

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new service manager object.
func New(
	config *config.Config, layerProvider LayerProvider, serviceInfoProvider ServiceStorage,
) (sm *ServiceManager, err error) {
	sm = &ServiceManager{
		servicesDir:         config.ServicesDir,
		downloadDir:         config.DownloadDir,
		serviceTTLDays:      config.ServiceTTLDays,
		actionHandler:       action.New(maxConcurrentActions),
		gidPool:             uidgidpool.NewGroupIDPool(),
		serviceInfoProvider: serviceInfoProvider,
		layerProvider:       layerProvider,
	}

	if err = os.MkdirAll(sm.servicesDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	services, err := sm.serviceInfoProvider.GetAllServices()
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	for _, service := range services {
		if err = sm.gidPool.AddID(service.GID); err != nil {
			log.Errorf("Can't add service GID to pool: %s", err)
		}
	}

	if err := sm.removeDamagedServiceFolders(services); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := os.RemoveAll(sm.downloadDir); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	serviceMountPoint, err := fs.GetMountPoint(sm.servicesDir)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	layersMountPoint, err := fs.GetMountPoint(config.LayersDir)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	sm.isLayersSamePartition = serviceMountPoint == layersMountPoint

	downloadMountPoint, err := fs.GetMountPoint(config.DownloadDir)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	sm.isDownloadSamePartition = serviceMountPoint == downloadMountPoint

	return sm, nil
}

// InstallService install service to the system.
func (sm *ServiceManager) InstallService(
	newService ServiceInfo, imageURL string, fileInfo image.FileInfo,
) (err error) {
	return <-sm.actionHandler.Execute(newService.ServiceID,
		func(id string) error {
			return sm.doInstallService(newService, imageURL, fileInfo)
		},
	)
}

// RemoveService removes service from the system.
func (sm *ServiceManager) RemoveService(serviceID string) (err error) {
	return <-sm.actionHandler.Execute(serviceID,
		func(id string) error {
			return sm.doRemoveService(serviceID)
		},
	)
}

// AllocateLayersSpace allocate space by removing outdated services.
func (sm *ServiceManager) AllocateLayersSpace(extraSpace int64) (allocatedLayersSize int64, err error) {
	if _, allocatedLayersSize, err = sm.removePendingServices(0, extraSpace); err != nil {
		return 0, err
	}

	return allocatedLayersSize, nil
}

// GetAllServicesStatus gets all services status.
func (sm *ServiceManager) GetAllServicesStatus() ([]ServiceInfo, error) {
	sm.Lock()

	sm.pendingRemove = nil

	sm.Unlock()

	services, err := sm.serviceInfoProvider.GetAllServices()

	return services, aoserrors.Wrap(err)
}

// GetServiceInfo gets service information by id.
func (sm *ServiceManager) GetServiceInfo(serviceID string) (serviceInfo ServiceInfo, err error) {
	serviceInfo, err = sm.serviceInfoProvider.GetService(serviceID)
	return serviceInfo, aoserrors.Wrap(err)
}

// GetImageParts gets image parts for the service.
func (sm *ServiceManager) GetImageParts(service ServiceInfo) (parts ImageParts, err error) {
	return getImageParts(service.ImagePath)
}

// ApplyService applies already installed service.
func (sm *ServiceManager) ApplyService(service ServiceInfo) (err error) {
	return <-sm.actionHandler.Execute(service.ServiceID,
		func(id string) error {
			return sm.doApplyService(service)
		},
	)
}

// RevertService reverts already installed service.
func (sm *ServiceManager) RevertService(service ServiceInfo) (retErr error) {
	return <-sm.actionHandler.Execute(service.ServiceID,
		func(id string) error {
			return sm.doRevertService(service)
		},
	)
}

// UseService sets last use of service.
func (sm *ServiceManager) UseService(serviceID string, aosVersion uint64) error {
	return aoserrors.Wrap(sm.serviceInfoProvider.SetServiceTimestamp(serviceID, aosVersion, time.Now().UTC()))
}

func (sm *ServiceManager) ValidateService(service ServiceInfo) error {
	manifestCheckSum, err := getManifestChecksum(service.ImagePath)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if !bytes.Equal(service.ManifestDigest, manifestCheckSum) {
		return aoserrors.New("manifest checksum mismatch")
	}

	return validateUnpackedImage(service.ImagePath)
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (sm *ServiceManager) doInstallService(newService ServiceInfo, imageURL string, fileInfo image.FileInfo) error {
	serviceFromStorage, err := sm.serviceInfoProvider.GetService(newService.ServiceID)
	if err != nil && !errors.Is(err, ErrNotExist) {
		return aoserrors.Wrap(err)
	}

	var isServiceExist bool

	if err == nil {
		isServiceExist = true

		if newService.AosVersion <= serviceFromStorage.AosVersion {
			return ErrVersionMismatch
		}
	}

	log.WithFields(log.Fields{"serviceID": newService.ServiceID}).Debug("Install service")

	newService.ImagePath, err = ioutil.TempDir(sm.servicesDir, "")
	if err != nil {
		return aoserrors.Wrap(err)
	}

	serviceSize, err := sm.extractPackageByURL(newService.ImagePath, sm.downloadDir, imageURL, fileInfo)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	defer func() {
		var size int64

		if err != nil {
			size = serviceSize
			_ = os.RemoveAll(newService.ImagePath)
		}

		sm.deallocateSpace(size)
	}()

	if err = validateUnpackedImage(newService.ImagePath); err != nil {
		return aoserrors.Wrap(err)
	}

	if isServiceExist {
		newService.GID = serviceFromStorage.GID
	} else {
		newService.GID, err = sm.gidPool.GetFreeID()
		if err != nil {
			return aoserrors.Wrap(err)
		}
	}

	rootFSDigest, err := sm.prepareServiceFS(newService.ImagePath, newService.GID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = updateRootFSDigestInManifest(newService.ImagePath, rootFSDigest); err != nil {
		return aoserrors.Wrap(err)
	}

	newService.ManifestDigest, err = getManifestChecksum(newService.ImagePath)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = sm.serviceInfoProvider.AddService(newService); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (sm *ServiceManager) doApplyService(service ServiceInfo) (err error) {
	oldServices, err := sm.serviceInfoProvider.GetAllServiceVersions(service.ServiceID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, oldService := range oldServices {
		if oldService.AosVersion == service.AosVersion {
			continue
		}

		_, _, err := sm.removeService(oldService)
		if err != nil {
			log.Errorf("Can't remove old service: %s", err)
			continue
		}
	}

	if err = sm.serviceInfoProvider.ActivateService(service.ServiceID, service.AosVersion); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (sm *ServiceManager) doRevertService(service ServiceInfo) error {
	if _, _, err := sm.removeService(service); err != nil {
		return err
	}

	return nil
}

func (sm *ServiceManager) removePendingServices(
	extraServiceSpace int64, extraLayerSpace int64,
) (deallocatedServicesSize, deallocatedLayersSize int64, err error) {
	for deallocatedServicesSize < extraServiceSpace || deallocatedLayersSize < extraLayerSpace {
		if len(sm.pendingRemove) == 0 {
			return 0, 0, ErrNotEnoughMemory
		}

		freedServiceSize, freedLayersSize, err := sm.removeService(sm.pendingRemove[0])
		if err != nil {
			return 0, 0, err
		}

		deallocatedServicesSize += freedServiceSize
		deallocatedLayersSize += freedLayersSize

		if sm.isLayersSamePartition {
			deallocatedServicesSize += freedLayersSize
			deallocatedLayersSize += freedServiceSize
		}

		sm.pendingRemove = sm.pendingRemove[1:]
	}

	return deallocatedServicesSize, deallocatedLayersSize, nil
}

func (sm *ServiceManager) doRemoveService(serviceID string) error {
	service, err := sm.serviceInfoProvider.GetService(serviceID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if service.Timestamp.Add(time.Hour * 24 * time.Duration(sm.serviceTTLDays)).Before(time.Now()) {
		if _, _, err := sm.removeService(service); err != nil {
			return err
		}
	} else {
		sm.Lock()

		sm.pendingRemove = append(sm.pendingRemove, service)
		sort.Sort(byServiceTTL(sm.pendingRemove))

		sm.Unlock()
	}

	return nil
}

func (sm *ServiceManager) removeService(service ServiceInfo) (freedServiceSize, freedLayersSize int64, err error) {
	imageParts, err := getImageParts(service.ImagePath)
	if err != nil {
		return 0, 0, aoserrors.Wrap(err)
	}

	servicesInfo, err := sm.serviceInfoProvider.GetAllServices()
	if err != nil {
		return 0, 0, aoserrors.Wrap(err)
	}

layersLoop:
	for _, digest := range imageParts.LayersDigest {
		for _, serviceInfo := range servicesInfo {
			if serviceInfo.ServiceID == service.ServiceID && serviceInfo.AosVersion == service.AosVersion {
				continue
			}

			serviceImageParts, err := getImageParts(serviceInfo.ImagePath)
			if err != nil {
				return 0, 0, aoserrors.Wrap(err)
			}

			for _, compareDigest := range serviceImageParts.LayersDigest {
				if digest == compareDigest {
					continue layersLoop
				}
			}
		}

		size, err := sm.layerProvider.UninstallLayer(digest)
		if err != nil {
			return 0, 0, aoserrors.Wrap(err)
		}

		freedLayersSize += size
	}

	freedServiceSize = imageParts.ServiceSize

	if err := os.RemoveAll(service.ImagePath); err != nil {
		return 0, 0, aoserrors.Wrap(err)
	}

	if err := sm.serviceInfoProvider.RemoveService(service); err != nil {
		return 0, 0, aoserrors.Wrap(err)
	}

	return freedServiceSize, freedLayersSize, nil
}

func (sm *ServiceManager) prepareServiceFS(imagePath string, gid int) (rootFSDigest digest.Digest, err error) {
	imageParts, err := getImageParts(imagePath)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	originRootFSPath := imageParts.ServiceFSPath

	tmpRootFS := path.Join(imagePath, tmpRootFSDir)

	// unpack rootfs layer
	if err = image.UnpackTarImage(imageParts.ServiceFSPath, tmpRootFS); err != nil {
		return "", aoserrors.Wrap(err)
	}

	if err = os.RemoveAll(imageParts.ServiceFSPath); err != nil {
		log.Errorf("Can't remove temp file: %s", err)
	}

	if err = filepath.Walk(tmpRootFS, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return aoserrors.Wrap(err)
		}

		if err = os.Chown(name, 0, gid); err != nil {
			return aoserrors.Wrap(err)
		}

		return nil
	}); err != nil {
		return "", aoserrors.Wrap(err)
	}

	rootFSHash, err := dirhash.HashDir(tmpRootFS, tmpRootFS, dirDigest)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	if rootFSDigest, err = digest.Parse(rootFSHash); err != nil {
		return "", aoserrors.Wrap(err)
	}

	if err = os.Rename(tmpRootFS, path.Join(path.Dir(originRootFSPath), rootFSDigest.Hex())); err != nil {
		return "", aoserrors.Wrap(err)
	}

	return rootFSDigest, nil
}

func (sm *ServiceManager) removeDamagedServiceFolders(services []ServiceInfo) error {
	for _, service := range services {
		fi, err := os.Stat(service.ImagePath)
		if err != nil || !fi.Mode().IsDir() {
			log.Warnf("Service missing: %v", service.ImagePath)

			if err = sm.serviceInfoProvider.RemoveService(service); err != nil {
				return aoserrors.Wrap(err)
			}
		}
	}

	files, err := ioutil.ReadDir(sm.servicesDir)
	if err != nil {
		return aoserrors.Wrap(err)
	}

filesLoop:
	for _, file := range files {
		fullPath := path.Join(sm.servicesDir, file.Name())

		for _, service := range services {
			if fullPath == service.ImagePath {
				continue filesLoop
			}
		}

		log.Warnf("Service missing in storage: %v", fullPath)

		if err = os.RemoveAll(fullPath); err != nil {
			log.Errorf("Can't remove service directory: %v", err)
		}
	}

	return nil
}

func (sm *ServiceManager) tryAllocateSpace(requiredSize int64) (err error) {
	sm.Lock()
	defer sm.Unlock()

	if sm.numTryAllocateSpace == 0 {
		if sm.availableSize, err = GetAvailableSize(sm.servicesDir); err != nil {
			sm.Unlock()
			return aoserrors.Wrap(err)
		}
	}

	if sm.availableSize < requiredSize {
		freedServicesSize, _, err := sm.removePendingServices(requiredSize-sm.availableSize, 0)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		sm.availableSize += freedServicesSize
	}

	sm.availableSize -= requiredSize
	sm.numTryAllocateSpace++

	return nil
}

func (sm *ServiceManager) extractPackageByURL(
	extractDir, downloadDir, packageURL string, fileInfo image.FileInfo,
) (serviceSize int64, err error) {
	urlVal, err := url.Parse(packageURL)
	if err != nil {
		return 0, aoserrors.Wrap(err)
	}

	var sourceFile string

	if urlVal.Scheme != "file" {
		if sm.isDownloadSamePartition {
			if err = sm.tryAllocateSpace(int64(fileInfo.Size)); err != nil {
				return 0, aoserrors.Wrap(err)
			}

			defer sm.deallocateSpace(int64(fileInfo.Size))
		}

		if sourceFile, err = image.Download(context.Background(), downloadDir, packageURL); err != nil {
			return 0, aoserrors.Wrap(err)
		}

		defer os.RemoveAll(sourceFile)
	} else {
		sourceFile = urlVal.Path
	}

	if err = image.CheckFileInfo(context.Background(), sourceFile, fileInfo); err != nil {
		return 0, aoserrors.Wrap(err)
	}

	size, err := image.GetUncompressedTarContentSize(sourceFile)
	if err != nil {
		return 0, aoserrors.Wrap(err)
	}

	if err = sm.tryAllocateSpace(size); err != nil {
		return 0, aoserrors.Wrap(err)
	}

	if err = image.UnpackTarImage(sourceFile, extractDir); err != nil {
		return size, aoserrors.Wrap(err)
	}

	return size, nil
}

func (sm *ServiceManager) deallocateSpace(size int64) {
	sm.Lock()
	defer sm.Unlock()

	sm.availableSize += size
	sm.numTryAllocateSpace--
}
