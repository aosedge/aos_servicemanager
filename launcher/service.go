// SPX-License-Identifier: Apache-2.0
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

package launcher

import (
	"errors"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/aoscloud/aos_servicemanager/servicemanager"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type serviceInfo struct {
	servicemanager.ServiceInfo
	serviceConfig *aostypes.ServiceConfig
	imageConfig   *imagespec.Image
	err           error
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var errOfflineTimeout = errors.New("offline timeout")

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (launcher *Launcher) cacheCurrentServices(instances []InstanceInfo) {
	launcher.runMutex.Lock()
	defer launcher.runMutex.Unlock()

	now := time.Now()

	launcher.currentServices = make(map[string]*serviceInfo)

	for _, instance := range instances {
		if _, ok := launcher.currentServices[instance.ServiceID]; ok {
			continue
		}

		var service serviceInfo

		if service.ServiceInfo, service.err = launcher.serviceProvider.GetServiceInfo(
			instance.ServiceID); errors.Is(service.err, servicemanager.ErrNotExist) {
			service.ServiceID = instance.ServiceID
			service.AosVersion = 0
		}

		if service.err == nil {
			service.serviceConfig, service.err = launcher.getServiceConfig(service.ServiceInfo)
		}

		if service.err == nil {
			if service.serviceConfig.OfflineTTL.Duration != 0 &&
				launcher.onlineTime.Add(service.serviceConfig.OfflineTTL.Duration).Before(now) {
				service.err = errOfflineTimeout
			}
		}

		if service.err == nil {
			service.imageConfig, service.err = launcher.getImageConfig(service.ServiceInfo)
		}

		if service.err == nil {
			service.err = launcher.serviceProvider.ValidateService(service.ServiceInfo)
		}

		launcher.currentServices[instance.ServiceID] = &service
	}
}

func (launcher *Launcher) getCurrentServiceInfo(serviceID string) (*serviceInfo, error) {
	service, ok := launcher.currentServices[serviceID]
	if !ok {
		return nil, aoserrors.Errorf("service info is not available: %s", serviceID)
	}

	return service, service.err
}
