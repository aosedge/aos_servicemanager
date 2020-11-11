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

package umcontroller

import (
	"sort"

	log "github.com/sirupsen/logrus"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// UmController update managers controller
type UmController struct {
	server            *umCtrlServer
	eventChannel      chan umCtrlInternalMsg
	stopChannel       chan bool
	connections       []umConnection
	currentComponents []amqp.ComponentInfo
}

type umConnection struct {
	umID           string
	isLocalClient  bool
	handler        *umHandler
	updatePriority uint32
	components     []string
}

type umCtrlInternalMsg struct {
	umID        string
	handler     *umHandler
	requestType int
	status      umStatus
}

type umStatus struct {
	umState       string
	componsStatus []systemComponentStatus
}

type systemComponentStatus struct {
	id            string
	vendorVersion string
	aosVersion    uint64
	status        string
	err           string
}

type systemComponent struct {
	id            string
	vendorVersion string
	aosVersion    uint64
	annotations   string
	url           string
	sha256        []byte
	sha512        []byte
	size          uint64
}

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	openConnection = iota
	closeConnection
	umStatusUpdate
)

// Component status
const (
	StatusPending     = "pending"
	StatusDownloading = "downloading"
	StatusDownloaded  = "downloaded"
	StatusInstalling  = "installing"
	StatusInstalled   = "installed"
	StatusError       = "error"
)

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new update managers controller
func New(config *config.Config, insecure bool) (umCtrl *UmController, err error) {
	umCtrl = &UmController{eventChannel: make(chan umCtrlInternalMsg), stopChannel: make(chan bool)}

	umCtrl.server, err = newServer(config.UmController, umCtrl.eventChannel, insecure)
	if err != nil {
		return nil, err
	}

	for _, client := range config.UmController.UmClients {
		umCtrl.connections = append(umCtrl.connections, umConnection{umID: client.UmID,
			isLocalClient: client.IsLocal, updatePriority: client.Priority, handler: nil})
	}

	sort.Slice(umCtrl.connections, func(i, j int) bool {
		return umCtrl.connections[i].updatePriority < umCtrl.connections[j].updatePriority
	})

	go umCtrl.processInternallMessages()
	go umCtrl.server.Start()

	return umCtrl, nil
}

// Close close server
func (umCtrl *UmController) Close() {
	umCtrl.stopChannel <- true
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (umCtrl *UmController) processInternallMessages() {
	for {
		select {
		case internalMsg := <-umCtrl.eventChannel:
			log.Debug("Internal Event ", internalMsg.requestType)

			switch internalMsg.requestType {
			case openConnection:
				umCtrl.handleNewConnection(internalMsg.umID, internalMsg.handler, internalMsg.status)

			case closeConnection:
				umCtrl.handleCloseConnection(internalMsg.umID)

			case umStatusUpdate:
				umCtrl.umHandlerStatusUpdate(internalMsg.umID, internalMsg.status)

			default:
				log.Error("Unsupported internal message ", internalMsg.requestType)
			}

		case <-umCtrl.stopChannel:
			log.Debug("Close all connections")
			umCtrl.server.Stop()

			return
		}
	}
}

func (umCtrl *UmController) handleNewConnection(umID string, handler *umHandler, status umStatus) {
	if handler == nil {
		log.Error("Handler is nil")
		return
	}

	for i, value := range umCtrl.connections {
		if value.umID != umID {
			continue
		}
		
		umCtrl.updateCurrentComponetsStatus(status.componsStatus)

		if value.handler != nil {
			log.Warn("Connection already availabe umID = ", umID)
			value.handler.Close()
		}

		umCtrl.connections[i].handler = handler
		umCtrl.connections[i].components = []string{}

		for _, newComponent := range status.componsStatus {
			idExist := false
			for _, value := range umCtrl.connections[i].components {
				if value == newComponent.id {
					idExist = true
					break
				}
			}

			if idExist == true {
				continue
			}

			umCtrl.connections[i].components = append(umCtrl.connections[i].components, newComponent.id)

		}

		return
	}

	log.Error("Unexpected new UM connection with ID = ", umID)
	handler.Close()

	return
}

func (umCtrl *UmController) handleCloseConnection(umID string) {
	log.Debug("Close UM connection umid = ", umID)
	for i, value := range umCtrl.connections {
		if value.umID == umID {
			umCtrl.connections[i].handler = nil
		}
	}
}

func (umCtrl *UmController) umHandlerStatusUpdate(umID string, status umStatus) {
	log.Debugf("Status um = %s changed to %s", umID, status.umState)
	umCtrl.updateCurrentComponetsStatus(status.componsStatus)
}

func (umCtrl *UmController) updateCurrentComponetsStatus(componsStatus []systemComponentStatus) {
	for _, value := range componsStatus {
		if value.status == StatusInstalled {
			toRemove := []int{}

			for i, curStatus := range umCtrl.currentComponents {
				if value.id == curStatus.ID {
					if curStatus.Status != StatusInstalled {
						continue
					}

					if value.vendorVersion != curStatus.VendorVersion {
						toRemove = append(toRemove, i)
						continue
					}
				}
			}

			sort.Ints(toRemove)

			for i, value := range toRemove {
				umCtrl.currentComponents = append(umCtrl.currentComponents[:value-i],
					umCtrl.currentComponents[value-i+1:]...)
			}
		}

		umCtrl.updateComponentElement(value)
	}
}

func (umCtrl *UmController) updateComponentElement(component systemComponentStatus) {
	for i, curElement := range umCtrl.currentComponents {
		if curElement.ID == component.id && curElement.VendorVersion == component.vendorVersion {
			umCtrl.currentComponents[i].Status = component.status
			umCtrl.currentComponents[i].Error = component.err
			return
		}
	}

	umCtrl.currentComponents = append(umCtrl.currentComponents, amqp.ComponentInfo{
		ID:            component.id,
		VendorVersion: component.vendorVersion,
		AosVersion:    component.aosVersion,
		Status:        component.status,
		Error:         component.err,
	})

	return
}
