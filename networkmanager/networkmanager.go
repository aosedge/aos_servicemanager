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

// Package launcher provides set of API to controls services lifecycle

package networkmanager

import (
	"fmt"
	"path"

	"github.com/docker/libnetwork"
	netconfig "github.com/docker/libnetwork/config"
	"github.com/docker/libnetwork/netlabel"
	"github.com/docker/libnetwork/options"
	"github.com/docker/libnetwork/types"
	log "github.com/sirupsen/logrus"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	typeBridge = "bridge"
)

const (
	serviceHostsPath       = "/etc/hosts"
	serviceResolveConfPath = "/etc/resolv.conf"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// NetworkManager network manager instance
type NetworkManager struct {
	controller libnetwork.NetworkController
	hosts      []config.Host
}

// NetworkParams network parameters set for service
type NetworkParams struct {
	Hostname string
	Aliases  []string
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates network manager instance
func New(cfg *config.Config) (manager *NetworkManager, err error) {
	log.Debug("Create network manager")

	manager = &NetworkManager{hosts: cfg.Hosts}

	manager.controller, err = libnetwork.New(
		netconfig.OptionDriverConfig(
			typeBridge, map[string]interface{}{
				netlabel.GenericData: options.Generic{
					"EnableIPForwarding": true,
					"EnableIPTables":     true},
			}),
		netconfig.OptionDataDir(cfg.WorkingDir),
		netconfig.OptionExecRoot("/run/aos"),
	)
	if err != nil {
		return nil, err
	}

	return manager, nil
}

// Close closes network manager instance
func (manager *NetworkManager) Close() (err error) {
	log.Debug("Close network manager")

	return nil
}

// GetID returns libnetwork controller id
func (manager *NetworkManager) GetID() (id string) {
	return manager.controller.ID()
}

// CreateNetwork creates SP network
func (manager *NetworkManager) CreateNetwork(spID string) (err error) {
	log.WithFields(log.Fields{"spID": spID}).Debug("Create network")

	if err = manager.NetworkExists(spID); err == nil {
		return fmt.Errorf("network %s exists", spID)
	}

	if _, err = manager.controller.NewNetwork(typeBridge, spID, ""); err != nil {
		return err
	}

	return nil
}

// NetworkExists returns true if network exists
func (manager *NetworkManager) NetworkExists(spID string) (err error) {
	// Check if network exists
	if _, err = manager.controller.NetworkByName(spID); err != nil {
		return err
	}

	return nil
}

// DeleteNetwork deletes SP network
func (manager *NetworkManager) DeleteNetwork(spID string) (err error) {
	log.WithFields(log.Fields{"spID": spID}).Debug("Delete network")

	network, err := manager.controller.NetworkByName(spID)
	if err != nil {
		return err
	}

	for _, ep := range network.Endpoints() {
		sandbox := ep.Info().Sandbox()

		if sandbox != nil {
			log.WithFields(log.Fields{"serviceID": sandbox.ContainerID(), "spID": network.Name()}).Debug("Service removed from network")

			if leaveErr := ep.Leave(sandbox); leaveErr != nil {
				if err == nil {
					err = leaveErr
				}
			}
		}

		if len(sandbox.Endpoints()) == 0 {
			if sandboxErr := sandbox.Delete(); sandboxErr != nil {
				if err == nil {
					err = sandboxErr
				}
			}
		}

		if epErr := ep.Delete(true); epErr != nil {
			if err == nil {
				err = epErr
			}
		}
	}

	if netErr := network.Delete(); netErr != nil {
		if err == nil {
			err = netErr
		}
	}

	return nil
}

// AddServiceToNetwork adds service to SP network
func (manager *NetworkManager) AddServiceToNetwork(serviceID, spID, servicePath string, params NetworkParams) (err error) {
	log.WithFields(log.Fields{"serviceID": serviceID, "spID": spID}).Debug("Add service to network")
	log.WithFields(log.Fields{"hostname": params.Hostname, "aliases": params.Aliases}).Debug("Network params")

	network, err := manager.controller.NetworkByName(spID)
	if err != nil {
		return err
	}

	endpoint, err := network.EndpointByName(serviceID)
	if err == nil {
		return fmt.Errorf("service %s already in SP network %s", serviceID, spID)
	}

	if _, ok := err.(types.NotFoundError); !ok {
		return err
	}

	var endpointOptions []libnetwork.EndpointOption

	for _, alias := range params.Aliases {
		endpointOptions = append(endpointOptions, libnetwork.CreateOptionMyAlias(alias))
	}

	if endpoint, err = network.CreateEndpoint(serviceID, endpointOptions...); err != nil {
		return err
	}

	sandbox, err := manager.controller.SandboxByID(serviceID)
	if err != nil {
		if _, ok := err.(types.NotFoundError); !ok {
			return err
		}

		options := []libnetwork.SandboxOption{
			libnetwork.OptionHostsPath(path.Join(servicePath, serviceHostsPath)),
			libnetwork.OptionResolvConfPath(path.Join(servicePath, serviceResolveConfPath)),
		}

		for _, host := range manager.hosts {
			options = append(options, libnetwork.OptionExtraHost(host.Hostname, host.IP))
		}

		if params.Hostname != "" {
			options = append(options, libnetwork.OptionHostname(params.Hostname))
		}

		if sandbox, err = manager.controller.NewSandbox(serviceID, options...); err != nil {
			return err
		}
	}

	if err = endpoint.Join(sandbox); err != nil {
		return err
	}

	return nil
}

// RemoveServiceFromNetwork removes service from network
func (manager *NetworkManager) RemoveServiceFromNetwork(serviceID, spID string) (err error) {
	log.WithFields(log.Fields{"serviceID": serviceID, "spID": spID}).Debug("Remove service from network")

	network, err := manager.controller.NetworkByName(spID)
	if err != nil {
		return err
	}

	endpoint, err := network.EndpointByName(serviceID)
	if err != nil {
		return err
	}

	sandbox := endpoint.Info().Sandbox()

	if sandbox != nil {
		if err = endpoint.Leave(sandbox); err != nil {
			return err
		}
	}

	if len(sandbox.Endpoints()) == 0 {
		if err = sandbox.Delete(); err != nil {
			return err
		}
	}

	if err = endpoint.Delete(true); err != nil {
		return err
	}

	return nil
}

// IsServiceInNetwork returns true if service belongs to network
func (manager *NetworkManager) IsServiceInNetwork(serviceID, spID string) (result bool, err error) {
	network, err := manager.controller.NetworkByName(spID)
	if err != nil {
		if _, ok := err.(types.NotFoundError); !ok {
			return false, err
		}

		return false, nil
	}

	if _, err = network.EndpointByName(serviceID); err != nil {
		if _, ok := err.(types.NotFoundError); !ok {
			return false, err
		}

		return false, nil
	}

	return true, nil
}

// GetServiceIP return service IP address
func (manager *NetworkManager) GetServiceIP(serviceID, spID string) (ip string, err error) {
	network, err := manager.controller.NetworkByName(spID)
	if err != nil {
		return "", err
	}

	endpoint, err := network.EndpointByName(serviceID)
	if err != nil {
		return "", err
	}

	ip = endpoint.Info().Iface().Address().IP.String()

	log.WithFields(log.Fields{"serviceID": serviceID, "spID": spID, "ip": ip}).Debug("Get service IP")

	return ip, nil
}

// DeleteAllNetworks deletes all networks
func (manager *NetworkManager) DeleteAllNetworks() (err error) {
	log.Debug("Delete all networks")

	for _, network := range manager.controller.Networks() {
		if netErr := manager.DeleteNetwork(network.Name()); netErr != nil {
			log.Errorf("Can't delete %s network: %s", network.Name(), netErr)

			if err == nil {
				err = netErr
			}
		}
	}

	for _, sandbox := range manager.controller.Sandboxes() {
		if sandboxErr := sandbox.Delete(); sandboxErr != nil {
			log.Errorf("Can't delete %s sandbox: %s", sandbox.ContainerID(), sandboxErr)

			if err == nil {
				err = sandboxErr
			}
		}
	}

	return err
}
