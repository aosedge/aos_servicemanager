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

package vismodule

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/nunc-ota/aos_common/visprotocol"
	"gitpct.epam.com/nunc-ota/aos_common/wsclient"

	"aos_servicemanager/config"
	"aos_servicemanager/database"
	"aos_servicemanager/identification/vismodule/dbushandler"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const Name = "vis"

const (
	usersChangedChannelSize = 1
	errorChannelSize        = 1
)

const defaultReconnectTime = 10 * time.Second

/*******************************************************************************
 * Types
 ******************************************************************************/

// VisModule vis module instance
type VisModule struct {
	config moduleConfig

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

// ServiceProvider provides service entry
type ServiceProvider interface {
	GetService(id string) (entry database.ServiceEntry, err error)
}

type moduleConfig struct {
	VISServer     string
	ReconnectTime config.Duration
}

/*******************************************************************************
 * init
 ******************************************************************************/

/*******************************************************************************
 * Public
 ******************************************************************************/

func New(configJSON []byte, serviceProvider ServiceProvider) (module *VisModule, err error) {
	log.Info("Create VIS identification module")

	module = &VisModule{}

	// default reconnect time
	module.config.ReconnectTime.Duration = defaultReconnectTime

	if err = json.Unmarshal(configJSON, &module.config); err != nil {
		return nil, err
	}

	if module.dbusHandler, err = dbushandler.New(serviceProvider); err != nil {
		return nil, err
	}

	if module.wsClient, err = wsclient.New("VIS", module.messageHandler); err != nil {
		return nil, err
	}

	module.usersChangedChannel = make(chan []string, usersChangedChannelSize)
	module.errorChannel = make(chan error, errorChannelSize)
	module.connectionCond = sync.NewCond(module)

	go module.handleConnection(module.config.VISServer, module.config.ReconnectTime.Duration)

	return module, nil
}

func (module *VisModule) Close() (err error) {
	log.Info("Close VIS identification module")

	var retErr error

	if err = module.dbusHandler.Close(); err != nil {
		retErr = err
	}

	if err = module.wsClient.Close(); err != nil && retErr == nil {
		retErr = err
	}

	return retErr
}

func (module *VisModule) GetSystemID() (vin string, err error) {
	module.waitConnection()

	var rsp visprotocol.GetResponse

	req := visprotocol.GetRequest{
		MessageHeader: visprotocol.MessageHeader{
			Action:    visprotocol.ActionGet,
			RequestID: wsclient.GenerateRequestID()},
		Path: "Attribute.Vehicle.VehicleIdentification.VIN"}

	if err = module.wsClient.SendRequest("RequestID", req.RequestID, &req, &rsp); err != nil {
		return "", err
	}

	value, err := getValueByPath("Attribute.Vehicle.VehicleIdentification.VIN", rsp.Value)
	if err != nil {
		return "", err
	}

	ok := false
	if module.vin, ok = value.(string); !ok {
		return "", errors.New("wrong VIN type")
	}

	log.WithField("VIN", module.vin).Debug("Get VIN")

	return module.vin, err
}

func (module *VisModule) GetUsers() (users []string, err error) {
	module.waitConnection()

	if module.users == nil {
		var rsp visprotocol.GetResponse

		req := visprotocol.GetRequest{
			MessageHeader: visprotocol.MessageHeader{
				Action:    visprotocol.ActionGet,
				RequestID: wsclient.GenerateRequestID()},
			Path: "Attribute.Vehicle.UserIdentification.Users"}

		if err = module.wsClient.SendRequest("RequestID", req.MessageHeader.RequestID, &req, &rsp); err != nil {
			return nil, err
		}

		module.Lock()
		defer module.Unlock()

		if err = module.setUsers(rsp.Value); err != nil {
			return nil, err
		}
	}

	log.WithField("users", module.users).Debug("Get users")

	return module.users, err
}

func (module *VisModule) UsersChangedChannel() (channel <-chan []string) {
	return module.usersChangedChannel
}

func (module *VisModule) ErrorChannel() (channel <-chan error) {
	return module.errorChannel
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (module *VisModule) waitConnection() {
	module.Lock()
	defer module.Unlock()

	if module.isConnected {
		return
	}

	for !module.isConnected {
		module.connectionCond.Wait()
	}
}

func (module *VisModule) handleConnection(url string, reconnectTime time.Duration) {
	for {
		if err := module.wsClient.Connect(url); err != nil {
			log.Errorf("Can't connect to VIS: %s", err)
			goto reconnect
		}

		module.subscribeMap = sync.Map{}

		if err := module.subscribe("Attribute.Vehicle.UserIdentification.Users", module.handleUsersChanged); err != nil {
			log.Errorf("Can't subscribe to VIS: %s", err)
			goto reconnect
		}

		module.users = nil
		module.vin = ""

		module.Lock()
		module.isConnected = true
		module.Unlock()

		module.connectionCond.Broadcast()

		select {
		case err := <-module.wsClient.ErrorChannel:
			module.Lock()
			module.isConnected = false
			module.Unlock()

			module.errorChannel <- err
		}

	reconnect:
		time.Sleep(reconnectTime)
	}
}

func (module *VisModule) messageHandler(message []byte) {
	var header visprotocol.MessageHeader

	if err := json.Unmarshal(message, &header); err != nil {
		log.Errorf("Error parsing VIS response: %s", err)
		return
	}

	switch header.Action {
	case visprotocol.ActionSubscription:
		module.processSubscriptions(message)

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

func (module *VisModule) processSubscriptions(message []byte) (err error) {
	var notification visprotocol.SubscriptionNotification

	if err = json.Unmarshal(message, &notification); err != nil {
		return err
	}

	// serve subscriptions
	subscriptionFound := false
	module.subscribeMap.Range(func(key, value interface{}) bool {
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

func (module *VisModule) setUsers(value interface{}) (err error) {
	value, err = getValueByPath("Attribute.Vehicle.UserIdentification.Users", value)
	if err != nil {
		return err
	}

	itfs, ok := value.([]interface{})
	if !ok {
		return errors.New("wrong users type")
	}

	module.users = make([]string, len(itfs))

	for i, itf := range itfs {
		item, ok := itf.(string)
		if !ok {
			return errors.New("wrong users type")
		}
		module.users[i] = item
	}

	return nil
}

func (module *VisModule) handleUsersChanged(value interface{}) {
	module.Lock()
	defer module.Unlock()

	if err := module.setUsers(value); err != nil {
		log.Errorf("Can't set users: %s", err)
		return
	}

	module.usersChangedChannel <- module.users

	log.WithField("users", module.users).Debug("Users changed")
}

func (module *VisModule) subscribe(path string, callback func(value interface{})) (err error) {
	var rsp visprotocol.SubscribeResponse

	req := visprotocol.SubscribeRequest{
		MessageHeader: visprotocol.MessageHeader{
			Action:    visprotocol.ActionSubscribe,
			RequestID: wsclient.GenerateRequestID()},
		Path: path}

	if err = module.wsClient.SendRequest("RequestID", req.MessageHeader.RequestID, &req, &rsp); err != nil {
		return err
	}

	if rsp.SubscriptionID == "" {
		return errors.New("no subscriptionID in response")
	}

	module.subscribeMap.Store(rsp.SubscriptionID, callback)

	return nil
}
