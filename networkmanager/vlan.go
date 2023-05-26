// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2023 Renesas Electronics Corporation.
// Copyright (C) 2023 EPAM Systems, Inc.
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
	"errors"
	"net"
	"syscall"

	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/vishvananda/netlink"
)

// Vlan represents vlan configuration.
type Vlan struct {
	vlanID int
	bridge string
	ifName string
	ip     string
	subnet string
}

func createVlan(vlanConf Vlan) error {
	br, err := setupBridge(vlanConf)
	if err != nil {
		return err
	}

	vlan, err := setupVlan(vlanConf)
	if err != nil {
		return err
	}

	if err := addVlanToBridge(br, vlan); err != nil {
		return err
	}

	return nil
}

func addVlanToBridge(br *netlink.Bridge, vlan *netlink.Vlan) error {
	// connect host vlan to the bridge
	if err := netlink.LinkSetMaster(vlan, br); err != nil {
		return aoserrors.Errorf("failed to connect %q to bridge %s: %v", vlan.Attrs().Name, br.Attrs().Name, err)
	}

	return nil
}

func setupBridge(vlanConf Vlan) (*netlink.Bridge, error) {
	// create bridge if necessary
	br, err := ensureBridge(vlanConf.bridge)
	if err != nil {
		return nil, err
	}

	if err := setupBridgeAddr(vlanConf, br); err != nil {
		return nil, err
	}

	return br, nil
}

func setupVlan(vlanConf Vlan) (*netlink.Vlan, error) {
	mIndex, err := getMasterInterfaceIndex()
	if err != nil {
		return nil, aoserrors.Errorf("failed to lookup master index %v", err)
	}

	log.Debugf("Creating vlan %s with vlanID %d", vlanConf.ifName, vlanConf.vlanID)

	vlan := &netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        vlanConf.ifName,
			ParentIndex: mIndex,
		},
		VlanId: vlanConf.vlanID,
	}

	if err := netlink.LinkAdd(vlan); err != nil && !errors.Is(err, syscall.EEXIST) {
		return nil, aoserrors.Errorf("failed to create vlan: %v", err)
	}

	if err := netlink.LinkSetUp(vlan); err != nil {
		return nil, aoserrors.Errorf("failed to create vlan: %v", err)
	}

	// Re-fetch link to read all attributes
	vlan, err = vlanByName(vlanConf.ifName)
	if err != nil {
		return nil, err
	}

	return vlan, nil
}

func ensureBridge(brName string) (br *netlink.Bridge, err error) {
	br = &netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name: brName,
			// Let kernel use default txqueuelen; leaving it unset
			// means 0, and a zero-length TX queue messes up FIFO
			// traffic shapers which use TX queue length as the
			// default packet limit
			TxQLen: -1,
		},
	}

	if err = netlink.LinkAdd(br); err != nil && !errors.Is(err, syscall.EEXIST) {
		return nil, aoserrors.Errorf("could not add %q: %v", brName, err)
	}

	// Re-fetch link to read all attributes and if it already existed,
	// ensure it's really a bridge with similar configuration
	if br, err = bridgeByName(brName); err != nil {
		return nil, err
	}

	if err = netlink.LinkSetUp(br); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return br, nil
}

func bridgeByName(name string) (*netlink.Bridge, error) {
	brLink, err := netlink.LinkByName(name)
	if err != nil {
		return nil, aoserrors.Errorf("could not lookup %q: %v", name, err)
	}

	br, ok := brLink.(*netlink.Bridge)
	if !ok {
		return nil, aoserrors.Errorf("%q already exists but is not a bridge", name)
	}

	return br, nil
}

func vlanByName(name string) (*netlink.Vlan, error) {
	vlanLink, err := netlink.LinkByName(name)
	if err != nil {
		return nil, aoserrors.Errorf("could not lookup %q: %v", name, err)
	}

	vlan, ok := vlanLink.(*netlink.Vlan)
	if !ok {
		return nil, aoserrors.Errorf("%q already exists but is not a vlan", name)
	}

	return vlan, nil
}

func setupBridgeAddr(vlanConf Vlan, br netlink.Link) error {
	addrs, err := netlink.AddrList(br, netlink.FAMILY_V4)
	if err != nil && !errors.Is(err, syscall.ENOENT) {
		return aoserrors.Errorf("could not get list of IP addresses: %v", err)
	}

	if len(addrs) != 0 {
		if len(addrs) > 1 {
			return aoserrors.Errorf("bridge %q has more than one address", br.Attrs().Name)
		}

		if addrs[0].IPNet.IP.String() == vlanConf.ip {
			return nil
		}

		if err := netlink.AddrDel(br, &netlink.Addr{IPNet: addrs[0].IPNet, Label: ""}); err != nil {
			return aoserrors.Errorf("could not remove IP address from %q: %v", br.Attrs().Name, err)
		}
	}

	_, ipnet, err := net.ParseCIDR(vlanConf.subnet)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	log.Debugf("Adding IP address %s", ipnet.String())

	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   net.ParseIP(vlanConf.ip),
			Mask: ipnet.Mask,
		}, Label: "",
	}

	if err := netlink.AddrAdd(br, addr); err != nil && !errors.Is(err, syscall.EEXIST) {
		return aoserrors.Errorf("could not add IP address to %q: %v", br.Attrs().Name, err)
	}

	return nil
}

func getMasterInterfaceIndex() (index int, err error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return index, aoserrors.Wrap(err)
	}

	for _, route := range routes {
		if route.Dst == nil {
			return route.LinkIndex, nil
		}
	}

	return index, aoserrors.Errorf("master index not found")
}
