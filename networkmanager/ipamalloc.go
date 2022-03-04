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

// Package networkmanager provides set of API to configure network
package networkmanager

import (
	"net"

	"github.com/aoscloud/aos_common/aoserrors"
	log "github.com/sirupsen/logrus"
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type ipSubnetwork struct {
	predefinedPrivateNetworks []*net.IPNet
	usedIPSubnetNetworks      map[string]*net.IPNet
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func newIPam() (ipam *ipSubnetwork, err error) {
	log.Debug("Create ipam allocator")

	ipam = &ipSubnetwork{}

	if ipam.predefinedPrivateNetworks, err = makeNetPools(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	ipam.usedIPSubnetNetworks = make(map[string]*net.IPNet)

	return ipam, nil
}

func (ipam *ipSubnetwork) tryToGetExistIPNetFromPool(spID string) (allocIPNet *net.IPNet, usedIPNet bool) {
	allocIPNet, usedIPNet = ipam.usedIPSubnetNetworks[spID]
	if usedIPNet {
		return allocIPNet, usedIPNet
	}

	return nil, false
}

func (ipam *ipSubnetwork) requestIPNetPool(spID string) (allocIPNet *net.IPNet, usedIPNet bool, err error) {
	allocIPNet, usedIPNet = ipam.tryToGetExistIPNetFromPool(spID)
	if usedIPNet {
		return allocIPNet, usedIPNet, nil
	}

	if len(ipam.predefinedPrivateNetworks) == 0 {
		return nil, usedIPNet, aoserrors.Errorf("IP subnet pool is empty")
	}

	allocIPNet, err = ipam.findUnusedIPSubnetwork()
	if err != nil {
		return nil, usedIPNet, aoserrors.Wrap(err)
	}

	ipam.usedIPSubnetNetworks[spID] = allocIPNet

	return allocIPNet, usedIPNet, nil
}

func (ipam *ipSubnetwork) releaseIPNetPool(spID string) {
	ip, exist := ipam.usedIPSubnetNetworks[spID]
	if !exist {
		return
	}

	delete(ipam.usedIPSubnetNetworks, spID)

	ipam.predefinedPrivateNetworks = append(ipam.predefinedPrivateNetworks, ip)
}

func (ipam *ipSubnetwork) findUnusedIPSubnetwork() (unusedIPNet *net.IPNet, err error) {
	networks, err := getNetworkRoutes()
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	for i, nw := range ipam.predefinedPrivateNetworks {
		if !checkRouteOverlaps(nw, networks) {
			ipam.predefinedPrivateNetworks = append(ipam.predefinedPrivateNetworks[:i], ipam.predefinedPrivateNetworks[i+1:]...)
			return nw, nil
		}
	}

	return nil, aoserrors.Errorf("no available network")
}
