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

package visclient

import (
	"encoding/json"
	"errors"
	"sync"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/nunc-ota/aos_common/visprotocol"
	"gitpct.epam.com/nunc-ota/aos_common/wsclient"

	"aos_servicemanager/visclient/dbushandler"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	usersChangedChannelSize = 1
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// UserChangedNtf user changed notification
type UserChangedNtf struct {
	Users []string
}

// Client VIS client object
type Client struct {
	UsersChangedChannel chan []string
	ErrorChannel        chan error

	dbusHandler *dbushandler.DBusHandler

	wsClient *wsclient.Client

	vin   string
	users []string

	subscribeMap sync.Map

	sync.Mutex
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new visclient
func New(serviceProvider dbushandler.ServiceProvider) (vis *Client, err error) {
	vis = &Client{}

	if vis.dbusHandler, err = dbushandler.New(serviceProvider); err != nil {
		return nil, err
	}

	if vis.wsClient, err = wsclient.New("VIS", vis.messageHandler); err != nil {
		return nil, err
	}

	vis.UsersChangedChannel = make(chan []string, usersChangedChannelSize)
	vis.ErrorChannel = vis.wsClient.ErrorChannel

	return vis, nil
}

// Connect connects to the VIS
func (vis *Client) Connect(url string) (err error) {
	if err = vis.wsClient.Connect(url); err != nil {
		return err
	}

	vis.subscribeMap = sync.Map{}

	if err = vis.subscribe("Attribute.Vehicle.UserIdentification.Users", vis.handleUsersChanged); err != nil {
		if err := vis.wsClient.Disconnect(); err != nil {
			log.Errorf("Can't disconnect from VIS: %s", err)
		}

		return err
	}

	vis.users = nil
	vis.vin = ""

	return nil
}

// Disconnect disconnects from the VIS
func (vis *Client) Disconnect() (err error) {
	return vis.wsClient.Disconnect()
}

// IsConnected returns true if connected to VIS
func (vis *Client) IsConnected() (result bool) {
	return vis.wsClient.IsConnected()
}

// GetVIN returns VIN
func (vis *Client) GetVIN() (vin string, err error) {
	var rsp visprotocol.GetResponse

	req := visprotocol.GetRequest{
		MessageHeader: visprotocol.MessageHeader{
			Action:    visprotocol.ActionGet,
			RequestID: wsclient.GenerateRequestID()},
		Path: "Attribute.Vehicle.VehicleIdentification.VIN"}

	if err = vis.wsClient.SendRequest("RequestID", &req, &rsp); err != nil {
		return "", err
	}

	value, err := getValueByPath("Attribute.Vehicle.VehicleIdentification.VIN", rsp.Value)
	if err != nil {
		return "", err
	}

	ok := false
	if vis.vin, ok = value.(string); !ok {
		return "", errors.New("wrong VIN type")
	}

	log.WithField("VIN", vis.vin).Debug("Get VIN")

	return vis.vin, err
}

// GetUsers returns user list
func (vis *Client) GetUsers() (users []string, err error) {
	if vis.users == nil {
		var rsp visprotocol.GetResponse

		req := visprotocol.GetRequest{
			MessageHeader: visprotocol.MessageHeader{
				Action:    visprotocol.ActionGet,
				RequestID: wsclient.GenerateRequestID()},
			Path: "Attribute.Vehicle.UserIdentification.Users"}

		if err = vis.wsClient.SendRequest("RequestID", &req, &rsp); err != nil {
			return nil, err
		}

		vis.Lock()
		defer vis.Unlock()

		if err = vis.setUsers(rsp.Value); err != nil {
			return nil, err
		}
	}

	log.WithField("users", vis.users).Debug("Get users")

	return vis.users, err
}

// Close closes vis client
func (vis *Client) Close() (err error) {
	if err = vis.dbusHandler.Close(); err != nil {
		return err
	}

	if err = vis.wsClient.Close(); err != nil {
		return err
	}

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (vis *Client) messageHandler(message []byte) {
	var header visprotocol.MessageHeader

	if err := json.Unmarshal(message, &header); err != nil {
		log.Errorf("Error parsing VIS response: %s", err)
		return
	}

	switch header.Action {
	case visprotocol.ActionSubscription:
		vis.processSubscriptions(message)

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

func (vis *Client) processSubscriptions(message []byte) (err error) {
	var notification visprotocol.SubscriptionNotification

	if err = json.Unmarshal(message, &notification); err != nil {
		return err
	}

	// serve subscriptions
	subscriptionFound := false
	vis.subscribeMap.Range(func(key, value interface{}) bool {
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

func (vis *Client) setUsers(value interface{}) (err error) {
	value, err = getValueByPath("Attribute.Vehicle.UserIdentification.Users", value)
	if err != nil {
		return err
	}

	itfs, ok := value.([]interface{})
	if !ok {
		return errors.New("wrong users type")
	}

	vis.users = make([]string, len(itfs))

	for i, itf := range itfs {
		item, ok := itf.(string)
		if !ok {
			return errors.New("wrong users type")
		}
		vis.users[i] = item
	}

	return nil
}

func (vis *Client) handleUsersChanged(value interface{}) {
	vis.Lock()
	defer vis.Unlock()

	if err := vis.setUsers(value); err != nil {
		log.Errorf("Can't set users: %s", err)
		return
	}

	vis.UsersChangedChannel <- vis.users

	log.WithField("users", vis.users).Debug("Users changed")
}

func (vis *Client) subscribe(path string, callback func(value interface{})) (err error) {
	var rsp visprotocol.SubscribeResponse

	req := visprotocol.SubscribeRequest{
		MessageHeader: visprotocol.MessageHeader{
			Action:    visprotocol.ActionSubscribe,
			RequestID: wsclient.GenerateRequestID()},
		Path: path}

	if err = vis.wsClient.SendRequest("RequestID", &req, &rsp); err != nil {
		return err
	}

	if rsp.SubscriptionID == "" {
		return errors.New("no subscriptionID in response")
	}

	vis.subscribeMap.Store(rsp.SubscriptionID, callback)

	return nil
}
