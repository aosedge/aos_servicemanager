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
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/api/cloudprotocol"
)

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

//nolint:gochecknoglobals
var defaultContent = []cloudprotocol.HostInfo{
	{IP: "127.0.0.1", Hostname: "localhost"},
	{IP: "::1", Hostname: "localhost ip6-localhost ip6-loopback"},
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func writeHostToHostsFile(hostsFilePath, ip, serviceID, hostname string, hosts []cloudprotocol.HostInfo) (err error) {
	content := bytes.NewBuffer(nil)

	if err = writeHosts(content, defaultContent); err != nil {
		return aoserrors.Wrap(err)
	}

	ownHosts := serviceID

	if hostname != "" {
		ownHosts = ownHosts + " " + hostname
	}

	if err = writeHosts(content, append([]cloudprotocol.HostInfo{{IP: ip, Hostname: ownHosts}}, hosts...)); err != nil {
		return aoserrors.Wrap(err)
	}

	//nolint:gosec // To fix application to access the host file, the file permissions must be 644
	if err = os.WriteFile(hostsFilePath, content.Bytes(), 0o644); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func writeResolveConfFile(resolvCongFilePath string, mainServers []string, extraServers []string) (err error) {
	content := bytes.NewBuffer(nil)

	if err = writeNameServers(content, mainServers); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = writeNameServers(content, extraServers); err != nil {
		return aoserrors.Wrap(err)
	}

	//nolint:gosec // To fix application to access the resolve file, the file permissions must be 644
	if err = os.WriteFile(resolvCongFilePath, content.Bytes(), 0o644); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func writeHosts(w io.Writer, hosts []cloudprotocol.HostInfo) (err error) {
	for _, host := range hosts {
		if _, err = fmt.Fprintf(w, "%s\t%s\n", host.IP, host.Hostname); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func writeNameServers(w io.Writer, nameServers []string) (err error) {
	for _, server := range nameServers {
		if _, err = fmt.Fprintf(w, "nameserver\t%s\n", server); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}
