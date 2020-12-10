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

package visidentifier_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/visprotocol"
	"gitpct.epam.com/epmd-aepr/aos_common/wsserver"

	"aos_servicemanager/identification/visidentifier"
	"aos_servicemanager/launcher"
	"aos_servicemanager/pluginprovider"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const serverURL = "wss://localhost:8088"

/*******************************************************************************
 * Types
 ******************************************************************************/

type testServiceProvider struct {
	services map[string]*launcher.Service
}

type clientHandler struct {
	subscriptionID string
	users          []string
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var vis pluginprovider.Identifier
var server *wsserver.Server

var serviceProvider = testServiceProvider{services: make(map[string]*launcher.Service)}
var testHandler = &clientHandler{}

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	url, err := url.Parse(serverURL)
	if err != nil {
		return err
	}

	if server, err = wsserver.New("TestServer", url.Host,
		"../../ci/crt.pem",
		"../../ci/key.pem", testHandler); err != nil {
		return err
	}

	time.Sleep(1 * time.Second)

	if vis, err = visidentifier.New([]byte(`{"VisServer": "wss://localhost:8088"}`)); err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	if err = vis.Close(); err != nil {
		return err
	}

	return nil
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Setup error: %s", err)
	}

	ret := m.Run()

	if err := cleanup(); err != nil {
		log.Fatalf("Cleanup error: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestGetSystemID(t *testing.T) {
	systemID, err := vis.GetSystemID()
	if err != nil {
		t.Fatalf("Error getting system ID: %s", err)
	}

	if systemID == "" {
		t.Fatalf("Wrong system ID value: %s", systemID)
	}
}

func TestGetUsers(t *testing.T) {
	testHandler.users = []string{uuid.New().String(), uuid.New().String(), uuid.New().String()}

	users, err := vis.GetUsers()
	if err != nil {
		t.Fatalf("Error getting users: %s", err)
	}

	if !reflect.DeepEqual(users, testHandler.users) {
		t.Errorf("Wrong users value: %s", users)
	}
}

func TestUsersChanged(t *testing.T) {
	newUsers := []string{uuid.New().String(), uuid.New().String(), uuid.New().String()}

	if err := vis.SetUsers(newUsers); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	select {
	case users := <-vis.UsersChangedChannel():
		if !reflect.DeepEqual(newUsers, users) {
			t.Errorf("Wrong users value: %s", users)
		}

	case <-time.After(5 * time.Second):
		t.Error("Waiting for users changed timeout")
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (handler *clientHandler) ProcessMessage(client *wsserver.Client, messageType int, message []byte) (response []byte, err error) {
	var header visprotocol.MessageHeader

	if err = json.Unmarshal(message, &header); err != nil {
		return nil, err
	}

	var rsp interface{}

	switch header.Action {
	case visprotocol.ActionSubscribe:
		handler.subscriptionID = uuid.New().String()

		rsp = &visprotocol.SubscribeResponse{
			MessageHeader:  header,
			SubscriptionID: handler.subscriptionID}

	case visprotocol.ActionUnsubscribe:
		var unsubscribeReq visprotocol.UnsubscribeRequest

		if err = json.Unmarshal(message, &unsubscribeReq); err != nil {
			return nil, err
		}

		unsubscribeRsp := visprotocol.UnsubscribeResponse{
			MessageHeader:  header,
			SubscriptionID: unsubscribeReq.SubscriptionID,
		}

		rsp = &unsubscribeRsp

		if unsubscribeReq.SubscriptionID != handler.subscriptionID {
			unsubscribeRsp.Error = &visprotocol.ErrorInfo{Message: "subscription ID not found"}
			break
		}

		handler.subscriptionID = ""

	case visprotocol.ActionUnsubscribeAll:
		handler.subscriptionID = ""

		rsp = &visprotocol.UnsubscribeAllResponse{MessageHeader: header}

	case visprotocol.ActionGet:
		var getReq visprotocol.GetRequest

		getRsp := visprotocol.GetResponse{
			MessageHeader: header}

		if err = json.Unmarshal(message, &getReq); err != nil {
			return nil, err
		}

		switch getReq.Path {
		case "Attribute.Vehicle.VehicleIdentification.VIN":
			getRsp.Value = map[string]string{getReq.Path: "VIN1234567890"}

		case "Attribute.Vehicle.UserIdentification.Users":
			getRsp.Value = map[string][]string{getReq.Path: handler.users}
		}

		rsp = &getRsp

	case visprotocol.ActionSet:
		var setReq visprotocol.SetRequest

		setRsp := visprotocol.SetResponse{
			MessageHeader: header}

		rsp = &setRsp

		if err = json.Unmarshal(message, &setReq); err != nil {
			return nil, err
		}

		switch setReq.Path {
		case "Attribute.Vehicle.VehicleIdentification.VIN":
			setRsp.Error = &visprotocol.ErrorInfo{Message: "readonly path"}

		case "Attribute.Vehicle.UserIdentification.Users":
			handler.users = nil

			for _, claim := range setReq.Value.([]interface{}) {
				handler.users = append(handler.users, claim.(string))
			}

			if handler.subscriptionID != "" {
				go func() {
					message, err := json.Marshal(&visprotocol.SubscriptionNotification{
						Action:         "subscription",
						SubscriptionID: handler.subscriptionID,
						Value:          map[string][]string{"Attribute.Vehicle.UserIdentification.Users": handler.users}})
					if err != nil {
						log.Errorf("Error marshal request: %s", err)
					}

					clients := server.GetClients()

					for _, client := range clients {
						if err := client.SendMessage(websocket.TextMessage, message); err != nil {
							log.Errorf("Error sending message: %s", err)
						}
					}
				}()
			}
		}

	default:
		return nil, errors.New("unknown action")
	}

	if response, err = json.Marshal(rsp); err != nil {
		return
	}

	return response, nil
}

func (handler *clientHandler) ClientConnected(client *wsserver.Client) {

}

func (handler *clientHandler) ClientDisconnected(client *wsserver.Client) {

}
