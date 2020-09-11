// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
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
	"io/ioutil"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Vars
 ******************************************************************************/

var defaultContent = []config.Host{
	{IP: "127.0.0.1", Hostname: "localhost"},
	{IP: "::1", Hostname: "localhost ip6-localhost ip6-loopback"},
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// WriteHostToHostsFile writes a pairs of hostname and IP address to the hosts file
func (manager *NetworkManager) WriteHostToHostsFile(pathToEtc string, extraHost config.Host) (err error) {
	content := bytes.NewBuffer(nil)

	extraHosts := defaultContent
	if extraHost.Hostname != "" && extraHost.IP != "" {
		extraHosts = append(extraHosts, extraHost)
	}

	if err := writeHosts(content, extraHosts); err != nil {
		return err
	}

	if err := writeHosts(content, manager.hosts); err != nil {
		return err
	}

	return ioutil.WriteFile(pathToEtc, content.Bytes(), 0644)
}

// WriteResolveConfFile prepare resolv.conf file
func (manager *NetworkManager) WriteResolveConfFile(resolvCongPath string) (err error) {
	// TODO: the correct generation of host resolv.conf will be after dnsmasq implementation
	if err := ioutil.WriteFile(resolvCongPath, []byte("nameserver 8.8.8.8"), 0644); err != nil {
		return err
	}

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func writeHosts(w io.Writer, hosts []config.Host) (err error) {
	for _, host := range hosts {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", host.IP, host.Hostname); err != nil {
			return err
		}
	}

	return nil
}
