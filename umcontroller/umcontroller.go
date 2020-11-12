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
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/looplab/fsm"
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
	fsm               *fsm.FSM
	connectionMonitor allConnectionMonitor
	operable          bool
}

type umConnection struct {
	umID           string
	isLocalClient  bool
	handler        *umHandler
	updatePriority uint32
	state          string
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

type allConnectionMonitor struct {
	connTimer     *time.Timer
	timeoutChan   chan bool
	stopTimerChan chan bool
	wg            sync.WaitGroup
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

// FSM states
const (
	stateInit       = "init"
	stateIdle       = "idle"
	stateFaultState = "fault"
)

// FSM events
const (
	evAllClientsConnected = "allClientsConnected"
	evConnectionTimeout   = "connectionTimeout"
)

// client sates
const (
	umIdle     = "IDLE"
	umPrepared = "PREPARED"
	umUpdated  = "UPDATED"
	umFailed   = "FAILED"
)

const connectionTimeoutSec = 300

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new update managers controller
func New(config *config.Config, insecure bool) (umCtrl *UmController, err error) {
	umCtrl = &UmController{eventChannel: make(chan umCtrlInternalMsg),
		stopChannel:       make(chan bool),
		connectionMonitor: allConnectionMonitor{stopTimerChan: make(chan bool, 1), timeoutChan: make(chan bool, 1)},
		operable:          true,
	}

	for _, client := range config.UmController.UmClients {
		umCtrl.connections = append(umCtrl.connections, umConnection{umID: client.UmID,
			isLocalClient: client.IsLocal, updatePriority: client.Priority, handler: nil})
	}

	sort.Slice(umCtrl.connections, func(i, j int) bool {
		return umCtrl.connections[i].updatePriority < umCtrl.connections[j].updatePriority
	})

	umCtrl.fsm = fsm.NewFSM(
		stateInit,
		fsm.Events{
			//process Idle state
			{Name: evAllClientsConnected, Src: []string{stateInit}, Dst: stateIdle},
			{Name: evConnectionTimeout, Src: []string{stateInit}, Dst: stateFaultState},
		},
		fsm.Callbacks{
			"enter_" + stateIdle: umCtrl.processIdleState,
			"before_event":       umCtrl.onEvent,
		},
	)

	umCtrl.server, err = newServer(config.UmController, umCtrl.eventChannel, insecure)
	if err != nil {
		return nil, err
	}

	go umCtrl.processInternallMessages()
	go umCtrl.connectionMonitor.startConnectionTimer(len(umCtrl.connections))
	go umCtrl.server.Start()

	return umCtrl, nil
}

// Close close server
func (umCtrl *UmController) Close() {
	umCtrl.operable = false
	umCtrl.stopChannel <- true
}

// GetSystemComponents returns list of system components information
func (umCtrl *UmController) GetSystemComponents() (components []amqp.ComponentInfo, err error) {
	currentState := umCtrl.fsm.Current()
	if currentState != stateInit {
		return umCtrl.currentComponents, nil
	}

	umCtrl.connectionMonitor.wg.Wait()

	return umCtrl.currentComponents, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (umCtrl *UmController) processInternallMessages() {
	for {
		select {
		case <-umCtrl.connectionMonitor.timeoutChan:
			if len(umCtrl.connections) == 0 {
				umCtrl.generateFSMEvent(evAllClientsConnected)
			} else {
				log.Error("Ums connection timeout")
				umCtrl.generateFSMEvent(evConnectionTimeout)
			}

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
			umCtrl.updateFinishCond.Broadcast()

			return
		}
	}
}

func (umCtrl *UmController) handleNewConnection(umID string, handler *umHandler, status umStatus) {
	if handler == nil {
		log.Error("Handler is nil")
		return
	}

	umIDfound := false
	for i, value := range umCtrl.connections {
		if value.umID != umID {
			continue
		}

		umCtrl.updateCurrentComponetsStatus(status.componsStatus)

		umIDfound = true

		if value.handler != nil {
			log.Warn("Connection already availabe umID = ", umID)
			value.handler.Close()
		}

		umCtrl.connections[i].handler = handler
		umCtrl.connections[i].state = handler.GetInitilState()
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

		break

	}

	if umIDfound == false {
	log.Error("Unexpected new UM connection with ID = ", umID)
		handler.Close()
		return
	}

	for _, value := range umCtrl.connections {
		if value.handler == nil {
			return
		}
	}

	log.Debug("All connection to Ums established")

	umCtrl.connectionMonitor.stopConnectionTimer()

	umCtrl.generateFSMEvent(evAllClientsConnected)

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

func (umCtrl *UmController) generateFSMEvent(event string, args ...interface{}) (err error) {
	if umCtrl.operable == false {
		return errors.New("update controller in shutdown state")
	}

	err = umCtrl.fsm.Event(event, args...)
	if err != nil {
		log.Error("Error transaction ", err)
	}

	return err
}

func (monitor *allConnectionMonitor) startConnectionTimer(connectionsCount int) {
	if connectionsCount == 0 {
		monitor.timeoutChan <- true
		return
	}

	if monitor.connTimer != nil {
		log.Debug("Timer already started")
		return
	}

	monitor.wg.Add(1)
	defer monitor.wg.Done()

	monitor.connTimer = time.NewTimer(connectionTimeoutSec * time.Second)

	select {
	case <-monitor.connTimer.C:
		monitor.timeoutChan <- true

	case <-monitor.stopTimerChan:
		monitor.connTimer.Stop()
	}

	monitor.connTimer = nil

	return
}

func (monitor *allConnectionMonitor) stopConnectionTimer() {
	monitor.stopTimerChan <- true
}

/*******************************************************************************
 * FSM callbacks
 ******************************************************************************/

func (umCtrl *UmController) onEvent(e *fsm.Event) {
	log.Infof("[CtrlFSM] %s -> %s : Event: %s", e.Src, e.Dst, e.Event)
}

func (umCtrl *UmController) processIdleState(e *fsm.Event) {

}

func (umCtrl *UmController) processFaultState(e *fsm.Event) {

}
