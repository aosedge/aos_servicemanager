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
	"context"
	"errors"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"path/filepath"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/image"
	"github.com/aoscloud/aos_common/utils/action"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/utils/imageutils"
	"github.com/aoscloud/aos_servicemanager/utils/uidgidpool"
)

/**********************************************************************************************************************
 * Consts
 *********************************************************************************************************************/

const maxConcurrentActions = 10

/**********************************************************************************************************************
 * Types
 *********************************************************************************************************************/

// ServiceStorage provides API to create, remove or access services DB.
type ServiceStorage interface {
	GetService(serviceID string) (ServiceInfo, error)
	GetAllServices() ([]ServiceInfo, error)
	AddService(ServiceInfo) error
	GetAllServiceVersions(serviceID string) ([]ServiceInfo, error)
	RemoveService(ServiceInfo) error
	ActivateService(ServiceInfo) error
}

// ServiceManager instance.
type ServiceManager struct {
	servicesDir         string
	downloadDir         string
	actionHandler       *action.Handler
	gidPool             *uidgidpool.IdentifierPool
	serviceInfoProvider ServiceStorage
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
	IsActive        bool
}

/**********************************************************************************************************************
 * Vars
 *********************************************************************************************************************/

// ErrNotExist not exist service error.
var (
	ErrNotExist        = errors.New("service not exist")
	ErrVersionMismatch = errors.New("version mismatch")
)

/**********************************************************************************************************************
 * Public
 *********************************************************************************************************************/

// New creates new launcher object.
func New(config *config.Config, serviceInfoProvider ServiceStorage) (sm *ServiceManager, err error) {
	sm = &ServiceManager{
		servicesDir:         config.ServicesDir,
		downloadDir:         config.DownloadDir,
		actionHandler:       action.New(maxConcurrentActions),
		gidPool:             uidgidpool.NewGroupIDPool(),
		serviceInfoProvider: serviceInfoProvider,
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

	return sm, nil
}

// InstallService install service to the system.
func (sm *ServiceManager) InstallService(
	newService ServiceInfo, imageURL string, fileInfo image.FileInfo) (err error) {
	return <-sm.actionHandler.Execute(newService.ServiceID,
		func(id string) error {
			return sm.doInstallService(newService, imageURL, fileInfo)
		},
	)
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

// ApplyServise applies already installed service.
func (sm *ServiceManager) ApplyServise(service ServiceInfo) (err error) {
	return <-sm.actionHandler.Execute(service.ServiceID,
		func(id string) error {
			return sm.doApplyServise(service)
		},
	)
}

// RevertServise reverts already installed service.
func (sm *ServiceManager) RevertServise(service ServiceInfo) (retErr error) {
	return <-sm.actionHandler.Execute(service.ServiceID,
		func(id string) error {
			return sm.doRevertServise(service)
		},
	)
}

/**********************************************************************************************************************
 * Private
 *********************************************************************************************************************/

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

	// download and unpack
	urlVal, err := url.Parse(imageURL)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	var serviceImage string

	if urlVal.Scheme != "file" {
		if serviceImage, err = image.Download(context.Background(), sm.downloadDir, imageURL); err != nil {
			return aoserrors.Wrap(err)
		}

		defer os.RemoveAll(serviceImage)
	} else {
		serviceImage = urlVal.Path
	}

	if err = image.CheckFileInfo(context.Background(), serviceImage, fileInfo); err != nil {
		return aoserrors.Wrap(err)
	}

	// create install dir
	log.WithFields(log.Fields{"serviceID": newService.ServiceID}).Debug("Install service")

	newService.ImagePath, err = ioutil.TempDir(sm.servicesDir, "")
	if err != nil {
		return aoserrors.Wrap(err)
	}

	defer func() {
		if err != nil {
			_ = os.RemoveAll(newService.ImagePath)
		}
	}()

	if err = imageutils.UnpackTarImage(serviceImage, newService.ImagePath); err != nil {
		return aoserrors.Wrap(err)
	}

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

	if err = sm.prepareServiceFS(newService.ImagePath, newService.GID); err != nil {
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

func (sm *ServiceManager) doApplyServise(service ServiceInfo) (err error) {
	oldServices, err := sm.serviceInfoProvider.GetAllServiceVersions(service.ServiceID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, oldService := range oldServices {
		if oldService.AosVersion == service.AosVersion {
			continue
		}

		if err := os.RemoveAll(oldService.ImagePath); err != nil {
			log.Errorf("Can't remove old service: %s", err)
		}

		if err := sm.serviceInfoProvider.RemoveService(oldService); err != nil {
			log.Errorf("Can't remove old service from storage: %s", err)
		}
	}

	if err = sm.serviceInfoProvider.ActivateService(service); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (sm *ServiceManager) doRevertServise(service ServiceInfo) (retErr error) {
	if err := os.RemoveAll(service.ImagePath); err != nil {
		retErr = err
	}

	if err := sm.serviceInfoProvider.RemoveService(service); err != nil {
		if retErr == nil {
			retErr = err
		}
	}

	return retErr
}

func (sm *ServiceManager) prepareServiceFS(imagePath string, gid int) (err error) {
	imageParts, err := getImageParts(imagePath)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	tmpTar := path.Join(imagePath, "tmpTar")
	if err = os.Rename(imageParts.ServiceFSPath, tmpTar); err != nil {
		return aoserrors.Wrap(err)
	}

	// unpack rootfs layer
	if err = imageutils.UnpackTarImage(tmpTar, imageParts.ServiceFSPath); err != nil {
		return aoserrors.Wrap(err)
	}

	os.RemoveAll(tmpTar)

	if err = filepath.Walk(imageParts.ServiceFSPath, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return aoserrors.Wrap(err)
		}

		if err = os.Chown(name, 0, gid); err != nil {
			return aoserrors.Wrap(err)
		}

		return nil
	}); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}
