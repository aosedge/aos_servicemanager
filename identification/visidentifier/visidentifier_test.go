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
	"math/rand"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/nunc-ota/aos_common/visprotocol"
	"gitpct.epam.com/nunc-ota/aos_common/wsserver"

	"aos_servicemanager/database"
	"aos_servicemanager/identification/visidentifier"
	"aos_servicemanager/identification/visidentifier/dbushandler"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const serverURL = "wss://localhost:8088"

/*******************************************************************************
 * Types
 ******************************************************************************/

type messageProcessor struct {
	sendMessage wsserver.SendMessage
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var vis *visidentifier.Instance
var server *wsserver.Server
var clientProcessor *messageProcessor
var db *database.Database

var subscriptionID = "test_subscription"

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
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	if db, err = database.New("tmp/servicemanager.db"); err != nil {
		return err
	}

	rand.Seed(time.Now().UnixNano())

	url, err := url.Parse(serverURL)
	if err != nil {
		return err
	}

	if server, err = wsserver.New("TestServer", url.Host,
		"../../vendor/gitpct.epam.com/nunc-ota/aos_common/wsserver/data/crt.pem",
		"../../vendor/gitpct.epam.com/nunc-ota/aos_common/wsserver/data/key.pem", newMessageProcessor); err != nil {
		return err
	}

	if vis, err = visidentifier.New([]byte(`{"VisServer": "wss://localhost:8088"}`), db); err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	db.Close()

	if err := os.RemoveAll("tmp"); err != nil {
		return err
	}

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
	users, err := vis.GetUsers()
	if err != nil {
		t.Fatalf("Error getting users: %s", err)
	}

	if users == nil {
		t.Fatalf("Wrong users value: %s", users)
	}
}

func TestUsersChanged(t *testing.T) {
	newUsers := []string{generateRandomString(10), generateRandomString(10)}

	message, err := json.Marshal(&visprotocol.SubscriptionNotification{
		Action:         "subscription",
		SubscriptionID: subscriptionID,
		Value:          map[string][]string{"Attribute.Vehicle.UserIdentification.Users": newUsers}})
	if err != nil {
		t.Fatalf("Error marshal request: %s", err)
	}

	if err := clientProcessor.sendMessage(websocket.TextMessage, message); err != nil {
		t.Fatalf("Error send message: %s", err)
	}

	select {
	case users := <-vis.UsersChangedChannel():
		if len(users) != len(newUsers) {
			t.Errorf("Wrong users len: %d", len(users))
		}

	case <-time.After(100 * time.Millisecond):
		t.Error("Waiting for users changed timeout")
	}
}

func TestGetPermission(t *testing.T) {
	if err := db.AddService(database.ServiceEntry{ID: "Service1",
		Permissions: `{"*": "rw", "123": "rw"}`}); err != nil {
		t.Fatalf("Can't add service: %s", err)
	}

	conn, err := dbus.SessionBus()
	if err != nil {
		t.Fatalf("Can't connect to session bus: %s", err)
	}

	var (
		permissionJson string
		status         string
		permissions    map[string]string
	)

	obj := conn.Object(dbushandler.InterfaceName, dbushandler.ObjectPath)

	err = obj.Call(dbushandler.InterfaceName+".GetPermission", 0, "Service1").Store(&permissionJson, &status)
	if err != nil {
		t.Fatalf("Can't make D-Bus call: %s", err)
	}

	if strings.ToUpper(status) != "OK" {
		t.Fatalf("Can't get permissions: %s", status)
	}

	err = json.Unmarshal([]byte(permissionJson), &permissions)
	if err != nil {
		t.Fatalf("Can't decode permissions: %s", err)
	}

	if len(permissions) != 2 {
		t.Fatal("Permission list length !=2")
	}

	if permissions["*"] != "rw" {
		t.Fatal("Incorrect permission")
	}
}

func TestIntrospect(t *testing.T) {
	conn, err := dbus.SessionBus()
	if err != nil {
		t.Fatalf("Can't connect to session bus: %s", err)
	}

	var intro string

	obj := conn.Object(dbushandler.InterfaceName, dbushandler.ObjectPath)

	if err = obj.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&intro); err != nil {
		t.Errorf("Can't make D-Bus call: %s", err)
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func generateRandomString(size uint) (result string) {
	letterRunes := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

	tmp := make([]rune, size)
	for i := range tmp {
		tmp[i] = letterRunes[rand.Intn(len(letterRunes))]
	}

	return string(tmp)
}

func newMessageProcessor(sendMessage wsserver.SendMessage) (processor wsserver.MessageProcessor, err error) {
	clientProcessor = &messageProcessor{sendMessage: sendMessage}

	return clientProcessor, nil
}

func (processor *messageProcessor) ProcessMessage(messageType int, messageIn []byte) (messageOut []byte, err error) {
	var header visprotocol.MessageHeader

	if err = json.Unmarshal(messageIn, &header); err != nil {
		return nil, err
	}

	var rsp interface{}

	switch header.Action {
	case visprotocol.ActionSubscribe:
		rsp = &visprotocol.SubscribeResponse{
			MessageHeader:  header,
			SubscriptionID: subscriptionID}

	case visprotocol.ActionGet:
		var getReq visprotocol.GetRequest

		getRsp := visprotocol.GetResponse{
			MessageHeader: header}

		if err = json.Unmarshal(messageIn, &getReq); err != nil {
			return nil, err
		}

		switch getReq.Path {
		case "Attribute.Vehicle.VehicleIdentification.VIN":
			getRsp.Value = map[string]string{getReq.Path: "VIN1234567890"}

		case "Attribute.Vehicle.UserIdentification.Users":
			getRsp.Value = map[string][]string{getReq.Path: []string{"user1", "user2", "user3"}}
		}

		rsp = &getRsp

	default:
		return nil, errors.New("unknown action")
	}

	if messageOut, err = json.Marshal(rsp); err != nil {
		return
	}

	return messageOut, nil
}
