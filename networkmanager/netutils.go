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
	"os"
	"path"
	"runtime"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

func removeInterface(ifName string) error {
	br, err := netlink.LinkByName(ifName)
	if err != nil {
		return nil // nolint:nilerr
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
		defer func() {
			if setErr := netns.Set(origin); setErr != nil {
				if err == nil {
					err = aoserrors.Wrap(setErr)
				}
			}
		}()

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
