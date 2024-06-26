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
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/aostypes"
	"github.com/aosedge/aos_common/image"
	"github.com/aosedge/aos_common/spaceallocator"
	"github.com/opencontainers/go-digest"
	log "github.com/sirupsen/logrus"
	"golang.org/x/mod/sumdb/dirhash"

	"github.com/aosedge/aos_servicemanager/config"
	"github.com/aosedge/aos_servicemanager/utils/whiteouts"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const tmpRootFSDir = "tmprootfs"

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// ServiceStorage provides API to create, remove or access services DB.
type ServiceStorage interface {
	GetAllServiceVersions(serviceID string) ([]ServiceInfo, error)
	GetServices() ([]ServiceInfo, error)
	AddService(info ServiceInfo) error
	RemoveService(serviceID string, version string) error
	SetServiceCached(serviceID string, version string, cached bool) error
}

// ServiceManager instance.
type ServiceManager struct {
	sync.Mutex
	servicesDir            string
	downloadDir            string
	serviceTTLDays         uint64
	serviceInfoProvider    ServiceStorage
	serviceAllocator       spaceallocator.Allocator
	downloadAllocator      spaceallocator.Allocator
	validateTTLStopChannel chan struct{}
}

// ServiceInfo service information.
type ServiceInfo struct {
	ServiceID       string
	ServiceProvider string
	Version         string
	ImagePath       string
	ManifestDigest  []byte
	Timestamp       time.Time
	Cached          bool
	Size            uint64
	GID             uint32
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var (
	// ErrNotExist not exist service error.
	ErrNotExist = errors.New("service not exist")
	// ErrVersionMismatch new service version <= existing one.
	ErrVersionMismatch = errors.New("version mismatch")
)

// NewSpaceAllocator space allocator constructor.
//
//nolint:gochecknoglobals // used for unit test mock
var NewSpaceAllocator = spaceallocator.New

// RemoveCachedServicesPeriod global variable is used to be able to mocking the services TTL functionality in test.
//
//nolint:gochecknoglobals // used for unit test mock
var RemoveCachedServicesPeriod = 24 * time.Hour

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new service manager object.
func New(
	config *config.Config, serviceInfoProvider ServiceStorage,
) (sm *ServiceManager, err error) {
	sm = &ServiceManager{
		servicesDir:            config.ServicesDir,
		downloadDir:            config.DownloadDir,
		serviceTTLDays:         config.ServiceTTLDays,
		serviceInfoProvider:    serviceInfoProvider,
		validateTTLStopChannel: make(chan struct{}),
	}

	if err = os.MkdirAll(sm.servicesDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := os.RemoveAll(sm.downloadDir); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err := os.MkdirAll(sm.downloadDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if sm.serviceAllocator, err = NewSpaceAllocator(
		sm.servicesDir, config.ServicesPartLimit, sm.removeOutdatedService); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if sm.downloadAllocator, err = NewSpaceAllocator(sm.downloadDir, 0, nil); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	services, err := sm.serviceInfoProvider.GetServices()
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	for _, service := range services {
		if !service.Cached {
			continue
		}

		if err := sm.serviceAllocator.AddOutdatedItem(
			fmt.Sprintf("%s_%s", service.ServiceID, service.Version), service.Size, service.Timestamp); err != nil {
			return nil, aoserrors.Wrap(err)
		}
	}

	if err := sm.removeDamagedServiceFolders(services); err != nil {
		log.Errorf("Can't remove damaged service folders: %v", err)
	}

	if err := sm.removeOutdatedServices(services); err != nil {
		log.Errorf("Can't remove outdated services: %v", err)
	}

	go sm.validateTTLs()

	return sm, nil
}

// Close closes service manager instance.
func (sm *ServiceManager) Close() {
	if err := sm.serviceAllocator.Close(); err != nil {
		log.Errorf("Can't close service allocator: %v", err)
	}

	if err := sm.downloadAllocator.Close(); err != nil {
		log.Errorf("Can't close download allocator: %v", err)
	}

	close(sm.validateTTLStopChannel)
}

// GetServiceInfo gets service information by id.
func (sm *ServiceManager) GetServiceInfo(serviceID string) (serviceInfo ServiceInfo, err error) {
	services, err := sm.serviceInfoProvider.GetAllServiceVersions(serviceID)
	if err != nil {
		return serviceInfo, aoserrors.Wrap(err)
	}

	for _, service := range services {
		if service.ServiceID == serviceID && !service.Cached {
			return service, nil
		}
	}

	return serviceInfo, ErrNotExist
}

// GetImageParts gets image parts for the service.
func (sm *ServiceManager) GetImageParts(service ServiceInfo) (parts ImageParts, err error) {
	return getImageParts(service.ImagePath)
}

// ProcessDesiredServices installs, removes, restores desired services on the system.
func (sm *ServiceManager) ProcessDesiredServices(desiredServices []aostypes.ServiceInfo) error {
	services, err := sm.serviceInfoProvider.GetServices()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if desiredServices, err = sm.updateCachedServices(desiredServices, services); err != nil {
		return err
	}

	if err = sm.installServices(desiredServices); err != nil {
		return err
	}

	return nil
}

// ValidateService validate service.
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

func (sm *ServiceManager) updateCachedServices(
	desiredServices []aostypes.ServiceInfo, storeServices []ServiceInfo,
) (installServices []aostypes.ServiceInfo, err error) {
nextService:
	for _, storeService := range storeServices {
		for i, desiredService := range desiredServices {
			if desiredService.ServiceID == storeService.ServiceID && desiredService.Version == storeService.Version {
				if storeService.Cached {
					if err := sm.setServiceCached(storeService, false); err != nil {
						return desiredServices, err
					}
				}

				desiredServices = append(desiredServices[:i], desiredServices[i+1:]...)

				continue nextService
			}
		}

		if !storeService.Cached {
			if err := sm.setServiceCached(storeService, true); err != nil {
				return desiredServices, err
			}
		}
	}

	return desiredServices, nil
}

func (sm *ServiceManager) installServices(desiredServices []aostypes.ServiceInfo) error {
	for _, desiredService := range desiredServices {
		if err := sm.installService(desiredService); err != nil {
			return err
		}
	}

	return nil
}

func (sm *ServiceManager) installService(serviceInfo aostypes.ServiceInfo) error {
	log.WithFields(log.Fields{
		"id":      serviceInfo.ServiceID,
		"version": serviceInfo.Version,
	}).Debug("Install service")

	var spacePackage, spaceService spaceallocator.Space

	imagePath, size, spacePackage, err := sm.extractPackageByURL(&serviceInfo)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	defer func() {
		if err != nil {
			releaseAllocatedSpace(imagePath, spaceService, spacePackage)

			log.WithFields(log.Fields{
				"id":        serviceInfo.ServiceID,
				"version":   serviceInfo.Version,
				"imagePath": imagePath,
			}).Errorf("Can't install service: %v", err)

			return
		}

		acceptAllocatedSpace(spaceService, spacePackage)
	}()

	if err = validateUnpackedImage(imagePath); err != nil {
		return aoserrors.Wrap(err)
	}

	serviceSize, space, rootFSDigest, err := sm.prepareServiceFS(imagePath, int(serviceInfo.GID))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	spaceService = space
	size += uint64(serviceSize)

	if err = updateRootFSDigestInManifest(imagePath, rootFSDigest); err != nil {
		return aoserrors.Wrap(err)
	}

	manifestDigest, err := getManifestChecksum(imagePath)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = sm.serviceInfoProvider.AddService(ServiceInfo{
		ServiceID:       serviceInfo.ServiceID,
		ServiceProvider: serviceInfo.ProviderID,
		Version:         serviceInfo.Version,
		ImagePath:       imagePath,
		Size:            size,
		ManifestDigest:  manifestDigest,
		Timestamp:       time.Now().UTC(),
		GID:             serviceInfo.GID,
	}); err != nil {
		return aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{
		"id":        serviceInfo.ServiceID,
		"version":   serviceInfo.Version,
		"imagePath": imagePath,
	}).Info("Service successfully installed")

	return nil
}

func (sm *ServiceManager) validateTTLs() {
	removeTicker := time.NewTicker(RemoveCachedServicesPeriod)
	defer removeTicker.Stop()

	for {
		select {
		case <-removeTicker.C:
			services, err := sm.serviceInfoProvider.GetServices()
			if err != nil {
				log.Errorf("Can't get services: %v", err)

				continue
			}

			if err := sm.removeOutdatedServices(services); err != nil {
				log.Errorf("Can't remove outdated services: %v", err)
			}

		case <-sm.validateTTLStopChannel:
			return
		}
	}
}

func (sm *ServiceManager) removeOutdatedServices(services []ServiceInfo) error {
	for _, service := range services {
		if service.Cached {
			if service.Timestamp.Add(time.Hour * 24 * time.Duration(sm.serviceTTLDays)).Before(time.Now()) {
				if err := sm.removeService(service); err != nil {
					return err
				}

				sm.serviceAllocator.RestoreOutdatedItem(fmt.Sprintf("%s_%s", service.ServiceID, service.Version))
			}
		}
	}

	return nil
}

func (sm *ServiceManager) removeOutdatedService(id string) error {
	serviceInfo := strings.Split(id, "_")
	if len(serviceInfo) < 2 { //nolint:gomnd
		return aoserrors.New("Unexpected service id format")
	}

	version := serviceInfo[len(serviceInfo)-1]
	serviceInfo = serviceInfo[:len(serviceInfo)-1]
	serviceID := strings.Join(serviceInfo, "_")

	services, err := sm.serviceInfoProvider.GetAllServiceVersions(serviceID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, service := range services {
		if service.Version == version {
			if err := os.RemoveAll(service.ImagePath); err != nil {
				return aoserrors.Wrap(err)
			}

			if err := sm.serviceInfoProvider.RemoveService(service.ServiceID, service.Version); err != nil {
				return aoserrors.Wrap(err)
			}
		}
	}

	return nil
}

func (sm *ServiceManager) setServiceCached(service ServiceInfo, cached bool) error {
	if err := sm.serviceInfoProvider.SetServiceCached(service.ServiceID, service.Version, cached); err != nil {
		return aoserrors.Wrap(err)
	}

	id := fmt.Sprintf("%s_%s", service.ServiceID, service.Version)

	if cached {
		if err := sm.serviceAllocator.AddOutdatedItem(
			id, service.Size, service.Timestamp); err != nil {
			return aoserrors.Wrap(err)
		}

		return nil
	}

	sm.serviceAllocator.RestoreOutdatedItem(id)

	return nil
}

func (sm *ServiceManager) removeService(service ServiceInfo) error {
	if err := os.RemoveAll(service.ImagePath); err != nil {
		return aoserrors.Wrap(err)
	}

	if service.Cached {
		sm.serviceAllocator.RestoreOutdatedItem(fmt.Sprintf("%s_%s", service.ServiceID, service.Version))
	}

	sm.serviceAllocator.FreeSpace(service.Size)

	if err := sm.serviceInfoProvider.RemoveService(service.ServiceID, service.Version); err != nil {
		return aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{
		"serviceID": service.ServiceID,
		"version":   service.Version,
	}).Info("Service successfully removed")

	return nil
}

func (sm *ServiceManager) prepareServiceFS(
	imagePath string, gid int,
) (serviceSize int64, space spaceallocator.Space, rootFSDigest digest.Digest, err error) {
	imageParts, err := getImageParts(imagePath)
	if err != nil {
		return 0, nil, "", aoserrors.Wrap(err)
	}

	if serviceSize, err = image.GetUncompressedTarContentSize(imageParts.ServiceFSPath); err != nil {
		return 0, nil, "", aoserrors.Wrap(err)
	}

	if space, err = sm.serviceAllocator.AllocateSpace(uint64(serviceSize)); err != nil {
		return 0, nil, "", aoserrors.Wrap(err)
	}

	originRootFSPath := imageParts.ServiceFSPath

	tmpRootFS := filepath.Join(imagePath, tmpRootFSDir)

	// unpack rootfs layer
	if err = image.UnpackTarImage(imageParts.ServiceFSPath, tmpRootFS); err != nil {
		return 0, nil, "", aoserrors.Wrap(err)
	}

	serviceFSArchiveSize, err := getFileSize(imageParts.ServiceFSPath)
	if err != nil {
		return 0, nil, "", aoserrors.Wrap(err)
	}

	sm.serviceAllocator.FreeSpace(serviceFSArchiveSize)

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
		return 0, nil, "", aoserrors.Wrap(err)
	}

	if err := whiteouts.OCIWhiteoutsToOverlay(tmpRootFS, 0, gid); err != nil {
		return 0, nil, "", aoserrors.Wrap(err)
	}

	rootFSHash, err := dirhash.HashDir(tmpRootFS, tmpRootFS, dirDigest)
	if err != nil {
		return 0, nil, "", aoserrors.Wrap(err)
	}

	if rootFSDigest, err = digest.Parse(rootFSHash); err != nil {
		return 0, nil, "", aoserrors.Wrap(err)
	}

	if err = os.Rename(tmpRootFS, filepath.Join(path.Dir(originRootFSPath), rootFSDigest.Hex())); err != nil {
		return 0, nil, "", aoserrors.Wrap(err)
	}

	return serviceSize, space, rootFSDigest, nil
}

func (sm *ServiceManager) removeDamagedServiceFolders(services []ServiceInfo) error {
	for _, service := range services {
		fi, err := os.Stat(service.ImagePath)
		if err != nil || !fi.Mode().IsDir() {
			log.Warnf("Service missing: %v", service.ImagePath)

			if err = sm.serviceInfoProvider.RemoveService(service.ServiceID, service.Version); err != nil {
				return aoserrors.Wrap(err)
			}
		}
	}

	files, err := os.ReadDir(sm.servicesDir)
	if err != nil {
		return aoserrors.Wrap(err)
	}

filesLoop:
	for _, file := range files {
		fullPath := filepath.Join(sm.servicesDir, file.Name())

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

func (sm *ServiceManager) extractPackageByURL(
	serviceInfo *aostypes.ServiceInfo,
) (imagePath string, serviceSize uint64, space spaceallocator.Space, err error) {
	urlVal, err := url.Parse(serviceInfo.URL)
	if err != nil {
		return "", 0, nil, aoserrors.Wrap(err)
	}

	var sourceFile string

	if urlVal.Scheme != "file" {
		space, err := sm.downloadAllocator.AllocateSpace(serviceInfo.Size)
		if err != nil {
			return "", 0, nil, aoserrors.Wrap(err)
		}

		defer func() {
			if err := space.Release(); err != nil {
				log.Errorf("Can't release memory: %v", err)
			}
		}()

		if sourceFile, err = image.Download(context.Background(), sm.downloadDir, serviceInfo.URL); err != nil {
			return "", 0, nil, aoserrors.Wrap(err)
		}

		defer os.RemoveAll(sourceFile)
	} else {
		sourceFile = urlVal.Path
	}

	if err = image.CheckFileInfo(context.Background(), sourceFile, image.FileInfo{
		Sha256: serviceInfo.Sha256,
		Size:   serviceInfo.Size,
	}); err != nil {
		return "", 0, nil, aoserrors.Wrap(err)
	}

	size, err := image.GetUncompressedTarContentSize(sourceFile)
	if err != nil {
		return "", 0, nil, aoserrors.Wrap(err)
	}

	if space, err = sm.serviceAllocator.AllocateSpace(uint64(size)); err != nil {
		return "", 0, nil, aoserrors.Wrap(err)
	}

	imagePath, err = os.MkdirTemp(sm.servicesDir, "")
	if err != nil {
		return "", 0, nil, aoserrors.Wrap(err)
	}

	if err = image.UnpackTarImage(sourceFile, imagePath); err != nil {
		return "", 0, nil, aoserrors.Wrap(err)
	}

	return imagePath, uint64(size), space, nil
}

func acceptAllocatedSpace(spaceService spaceallocator.Space, spacePackage spaceallocator.Space) {
	if err := spacePackage.Accept(); err != nil {
		log.Errorf("Can't accept memory: %v", err)
	}

	if err := spaceService.Accept(); err != nil {
		log.Errorf("Can't accept memory: %v", err)
	}
}

func releaseAllocatedSpace(
	imagePath string, spaceService spaceallocator.Space, spacePackage spaceallocator.Space,
) {
	if err := os.RemoveAll(imagePath); err != nil {
		log.Errorf("Can't remove service image: %v", err)
	}

	if err := spacePackage.Release(); err != nil {
		log.Errorf("Can't release memory: %v", err)
	}

	if spaceService != nil {
		if err := spaceService.Release(); err != nil {
			log.Errorf("Can't release memory: %v", err)
		}
	}
}

func getFileSize(fileName string) (size uint64, err error) {
	var stat syscall.Stat_t

	if err = syscall.Stat(fileName, &stat); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}

		return 0, aoserrors.Wrap(err)
	}

	return uint64(stat.Size), nil
}
