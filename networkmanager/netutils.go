// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2020 Renesas Inc.
// Copyright 2020 EPAM Systems Inc.
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
	"os"
	"path"
	"runtime"
	"syscall"

	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
)

func getNetworkRoutes() (routeIPList []netlink.Route, err error) {
	initNl, err := netlink.NewHandle(syscall.NETLINK_ROUTE, syscall.NETLINK_NETFILTER)

	if err != nil {
		return nil, aoserrors.Errorf("could not create netlink handle on initial namespace: %v", err)
	}

	defer initNl.Delete()

	routeIPList, err = initNl.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return routeIPList, nil
}

func checkRouteOverlaps(toCheck *net.IPNet, networks []netlink.Route) (overlapsIPs bool) {
	for _, network := range networks {
		if network.Dst != nil && (toCheck.Contains(network.Dst.IP) || network.Dst.Contains(toCheck.IP)) {
			return true
		}
	}

	return false
}

func networkOverlaps(netX *net.IPNet, netY *net.IPNet) (sameIPNet bool) {
	return netX.Contains(netY.IP) || netY.Contains(netX.IP)
}

func removeBridgeInterface(spID string) (err error) {
	br, err := netlink.LinkByName(bridgePrefix + spID)
	if err != nil {
		return nil
	}

	if err = netlink.LinkSetDown(br); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = netlink.LinkDel(br); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func createNetNS(name string) (err error) {
	if _, err = os.Stat(path.Join(pathToNetNs, name)); os.IsNotExist(err) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		origin, err := netns.Get()
		if err != nil {
			return aoserrors.Wrap(err)
		}
		defer origin.Close()
		defer netns.Set(origin)

		newns, err := netns.NewNamed(name)
		if err != nil {
			return aoserrors.Wrap(err)
		}
		defer newns.Close()

		lo, err := netlink.LinkByName("lo")
		if err != nil {
			return aoserrors.Wrap(err)
		}

		if err = netlink.LinkSetUp(lo); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func getIPAddressRange(subnetwork *net.IPNet) (ipLowNetRange net.IP, ipHighNetRange net.IP) {
	minIPRange, maxIPRange := cidr.AddressRange(subnetwork)

	return cidr.Inc(minIPRange), cidr.Dec(maxIPRange)
}

func checkExistNetInterface(name string) (ipNet *net.IPNet, err error) {
	netInterface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, aoserrors.Errorf("unable to find interface %s", err)
	}

	addrs, err := netInterface.Addrs()
	if err != nil {
		return nil, aoserrors.Errorf("interface has no address %s", err)
	}

	for _, addr := range addrs {
		switch v := addr.(type) {
		case *net.IPNet:
			if ipv4 := v.IP.To4(); ipv4 != nil {
				_, ipSubnet, _ := net.ParseCIDR(v.String())
				return ipSubnet, nil
			}
		}
	}

	return nil, aoserrors.Errorf("interface has not IPv4 address")
}
