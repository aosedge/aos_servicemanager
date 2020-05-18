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

package visidentifier

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/visprotocol"
	"gitpct.epam.com/epmd-aepr/aos_common/wsclient"

	"aos_servicemanager/config"
	"aos_servicemanager/identification/visidentifier/dbushandler"
	"aos_servicemanager/launcher"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	usersChangedChannelSize = 1
	errorChannelSize        = 1
)

const defaultReconnectTime = 10 * time.Second

/*******************************************************************************
 * Types
 ******************************************************************************/

// Instance vis identifier instance
type Instance struct {
	config instanceConfig

	usersChangedChannel chan []string
	errorChannel        chan error

	dbusHandler *dbushandler.DBusHandler

	wsClient *wsclient.Client

	vin   string
	users []string

	subscribeMap sync.Map

	isConnected    bool
	connectionCond *sync.Cond

	sync.Mutex
}

// ServiceProvider provides service info
type ServiceProvider interface {
	GetService(serviceID string) (service launcher.Service, err error)
}

type instanceConfig struct {
	VISServer     string
	ReconnectTime config.Duration
}

/*******************************************************************************
 * init
 ******************************************************************************/

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new vis identifier instance
func New(configJSON []byte, serviceProvider ServiceProvider) (instance *Instance, err error) {
	log.Info("Create VIS identification instance")

	instance = &Instance{}

	// default reconnect time
	instance.config.ReconnectTime.Duration = defaultReconnectTime

	if err = json.Unmarshal(configJSON, &instance.config); err != nil {
		return nil, err
	}

	if instance.dbusHandler, err = dbushandler.New(serviceProvider); err != nil {
		return nil, err
	}

	if instance.wsClient, err = wsclient.New("VIS", instance.messageHandler); err != nil {
		return nil, err
	}

	instance.usersChangedChannel = make(chan []string, usersChangedChannelSize)
	instance.errorChannel = make(chan error, errorChannelSize)
	instance.connectionCond = sync.NewCond(instance)

	go instance.handleConnection(instance.config.VISServer, instance.config.ReconnectTime.Duration)

	return instance, nil
}

// Close closes vis identifier instance
func (instance *Instance) Close() (err error) {
	log.Info("Close VIS identification instance")

	var retErr error

	if err = instance.dbusHandler.Close(); err != nil {
		retErr = err
	}

	if err = instance.wsClient.Close(); err != nil && retErr == nil {
		retErr = err
	}

	return retErr
}

// GetSystemID returns the system ID
func (instance *Instance) GetSystemID() (systemID string, err error) {
	instance.waitConnection()

	var rsp visprotocol.GetResponse

	req := visprotocol.GetRequest{
		MessageHeader: visprotocol.MessageHeader{
			Action:    visprotocol.ActionGet,
			RequestID: wsclient.GenerateRequestID()},
		Path: "Attribute.Vehicle.VehicleIdentification.VIN"}

	if err = instance.wsClient.SendRequest("RequestID", req.MessageHeader.RequestID, &req, &rsp); err != nil {
		return "", err
	}

	value, err := getValueByPath("Attribute.Vehicle.VehicleIdentification.VIN", rsp.Value)
	if err != nil {
		return "", err
	}

	ok := false
	if instance.vin, ok = value.(string); !ok {
		return "", errors.New("wrong VIN type")
	}

	log.WithField("VIN", instance.vin).Debug("Get VIN")

	return instance.vin, err
}

// GetUsers returns the user claims
func (instance *Instance) GetUsers() (users []string, err error) {
	instance.waitConnection()

	if instance.users == nil {
		var rsp visprotocol.GetResponse

		req := visprotocol.GetRequest{
			MessageHeader: visprotocol.MessageHeader{
				Action:    visprotocol.ActionGet,
				RequestID: wsclient.GenerateRequestID()},
			Path: "Attribute.Vehicle.UserIdentification.Users"}

		if err = instance.wsClient.SendRequest("RequestID", req.MessageHeader.RequestID, &req, &rsp); err != nil {
			return nil, err
		}

		instance.Lock()
		defer instance.Unlock()

		if err = instance.setUsers(rsp.Value); err != nil {
			return nil, err
		}
	}

	log.WithField("users", instance.users).Debug("Get users")

	return instance.users, err
}

// UsersChangedChannel returns users changed channel
func (instance *Instance) UsersChangedChannel() (channel <-chan []string) {
	return instance.usersChangedChannel
}

// ErrorChannel returns error channel
func (instance *Instance) ErrorChannel() (channel <-chan error) {
	return instance.errorChannel
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (instance *Instance) waitConnection() {
	instance.Lock()
	defer instance.Unlock()

	if instance.isConnected {
		return
	}

	for !instance.isConnected {
		instance.connectionCond.Wait()
	}
}

func (instance *Instance) handleConnection(url string, reconnectTime time.Duration) {
	for {
		if err := instance.wsClient.Connect(url); err != nil {
			log.Errorf("Can't connect to VIS: %s", err)
			goto reconnect
		}

		instance.subscribeMap = sync.Map{}

		if err := instance.subscribe("Attribute.Vehicle.UserIdentification.Users", instance.handleUsersChanged); err != nil {
			log.Errorf("Can't subscribe to VIS: %s", err)
			goto reconnect
		}

		instance.users = nil
		instance.vin = ""

		instance.Lock()
		instance.isConnected = true
		instance.Unlock()

		instance.connectionCond.Broadcast()

		select {
		case err := <-instance.wsClient.ErrorChannel:
			instance.Lock()
			instance.isConnected = false
			instance.Unlock()

			instance.errorChannel <- err
		}

	reconnect:
		time.Sleep(reconnectTime)
	}
}

func (instance *Instance) messageHandler(message []byte) {
	var header visprotocol.MessageHeader

	if err := json.Unmarshal(message, &header); err != nil {
		log.Errorf("Error parsing VIS response: %s", err)
		return
	}

	switch header.Action {
	case visprotocol.ActionSubscription:
		instance.processSubscriptions(message)

	default:
		log.WithField("action", header.Action).Warning("Unexpected message received")
	}
}

func getValueByPath(path string, value interface{}) (result interface{}, err error) {
	if valueMap, ok := value.(map[string]interface{}); ok {
		if value, ok = valueMap[path]; !ok {
			return nil, errors.New("path not found")
		}
		return value, nil
	}

	if value == nil {
		return result, errors.New("no value found")
	}

	return value, nil
}

func (instance *Instance) processSubscriptions(message []byte) (err error) {
	var notification visprotocol.SubscriptionNotification

	if err = json.Unmarshal(message, &notification); err != nil {
		return err
	}

	// serve subscriptions
	subscriptionFound := false
	instance.subscribeMap.Range(func(key, value interface{}) bool {
		if key.(string) == notification.SubscriptionID {
			subscriptionFound = true
			value.(func(interface{}))(notification.Value)
			return false
		}
		return true
	})

	if !subscriptionFound {
		log.Warningf("Unexpected subscription id: %s", notification.SubscriptionID)
	}

	return nil
}

func (instance *Instance) setUsers(value interface{}) (err error) {
	value, err = getValueByPath("Attribute.Vehicle.UserIdentification.Users", value)
	if err != nil {
		return err
	}

	itfs, ok := value.([]interface{})
	if !ok {
		return errors.New("wrong users type")
	}

	instance.users = make([]string, len(itfs))

	for i, itf := range itfs {
		item, ok := itf.(string)
		if !ok {
			return errors.New("wrong users type")
		}
		instance.users[i] = item
	}

	return nil
}

func (instance *Instance) handleUsersChanged(value interface{}) {
	instance.Lock()
	defer instance.Unlock()

	if err := instance.setUsers(value); err != nil {
		log.Errorf("Can't set users: %s", err)
		return
	}

	instance.usersChangedChannel <- instance.users

	log.WithField("users", instance.users).Debug("Users changed")
}

func (instance *Instance) subscribe(path string, callback func(value interface{})) (err error) {
	var rsp visprotocol.SubscribeResponse

	req := visprotocol.SubscribeRequest{
		MessageHeader: visprotocol.MessageHeader{
			Action:    visprotocol.ActionSubscribe,
			RequestID: wsclient.GenerateRequestID()},
		Path: path}

	if err = instance.wsClient.SendRequest("RequestID", req.MessageHeader.RequestID, &req, &rsp); err != nil {
		return err
	}

	if rsp.SubscriptionID == "" {
		return errors.New("no subscriptionID in response")
	}

	instance.subscribeMap.Store(rsp.SubscriptionID, callback)

	return nil
}
