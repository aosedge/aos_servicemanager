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

package wsclient_test

import (
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/wsclient"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/wsserver"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const serverURL = "wss://localhost:8088"

/*******************************************************************************
 * Types
 ******************************************************************************/

type messageProcessor struct {
	sendMessage    wsserver.SendMessage
	messageHandler func(message []byte) (response []byte)
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var server *wsserver.Server
var clientProcessor *messageProcessor

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
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	url, err := url.Parse(serverURL)
	if err != nil {
		log.Fatalf("Can't parse url: %s", err)
	}

	server, err = wsserver.New("TestServer", url.Host, "../wsserver/data/crt.pem", "../wsserver/data/key.pem", newMessageProcessor)
	if err != nil {
		log.Fatalf("Can't create ws server: %s", err)
	}
	defer server.Close()

	// Wait for server become ready
	time.Sleep(1 * time.Second)

	ret := m.Run()

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestConnectDisconnect(t *testing.T) {
	client, err := wsclient.New("Test", nil)
	if err != nil {
		t.Fatalf("Can't create ws client: %s", err)
	}
	defer client.Close()

	if err = client.Connect(serverURL); err != nil {
		t.Errorf("Can't connect to ws server: %s", err)
	}

	if err = client.Disconnect(); err != nil {
		t.Errorf("Can't connect to ws server: %s", err)
	}

	if err = client.Connect(serverURL); err != nil {
		t.Errorf("Can't connect to ws server: %s", err)
	}
}
func TestSendRequest(t *testing.T) {
	type Request struct {
		Type      string
		RequestID string
		Value     int
	}

	type Response struct {
		Type      string
		RequestID string
		Value     float32
		Error     *string `json:"Error,omitempty"`
	}

	client, err := wsclient.New("Test", nil)
	if err != nil {
		t.Fatalf("Can't create ws client: %s", err)
	}
	defer client.Close()

	if err = client.Connect(serverURL); err != nil {
		t.Fatalf("Can't connect to ws server: %s", err)
	}

	clientProcessor.messageHandler = func(messageIn []byte) (messageOut []byte) {
		var req Request
		var rsp Response

		if err := json.Unmarshal(messageIn, &req); err != nil {
			return
		}

		rsp.Type = req.Type
		rsp.RequestID = req.RequestID
		rsp.Value = float32(req.Value) / 10.0

		if messageOut, err = json.Marshal(rsp); err != nil {
			return
		}

		return messageOut
	}

	req := Request{Type: "GET", RequestID: uuid.New().String()}
	rsp := Response{}

	if err = client.SendRequest("RequestID", &req, &rsp); err != nil {
		t.Errorf("Can't send request: %s", err)
	}

	if rsp.Type != req.Type {
		t.Errorf("Wrong response type: %s", rsp.Type)
	}
}

func TestWrongIDRequest(t *testing.T) {
	type Request struct {
		Type      string
		RequestID string
		Value     int
	}

	type Response struct {
		Type      string
		RequestID string
		Value     float32
		Error     *string `json:"Error,omitempty"`
	}

	client, err := wsclient.New("Test", nil)
	if err != nil {
		t.Fatalf("Can't create ws client: %s", err)
	}
	defer client.Close()

	if err = client.Connect(serverURL); err != nil {
		t.Fatalf("Can't connect to ws server: %s", err)
	}

	clientProcessor.messageHandler = func(messageIn []byte) (messageOut []byte) {
		var req Request
		var rsp Response

		if err := json.Unmarshal(messageIn, &req); err != nil {
			return
		}

		rsp.Type = req.Type
		rsp.RequestID = uuid.New().String()
		rsp.Value = float32(req.Value) / 10.0

		if messageOut, err = json.Marshal(rsp); err != nil {
			return
		}

		return messageOut
	}

	req := Request{Type: "GET", RequestID: uuid.New().String()}
	rsp := Response{}

	if err = client.SendRequest("RequestID", &req, &rsp); err == nil {
		t.Error("Error expected")
	}
}

func TestErrorChannel(t *testing.T) {
	client, err := wsclient.New("Test", nil)
	if err != nil {
		t.Fatalf("Can't create ws client: %s", err)
	}
	defer client.Close()

	if err = client.Connect(serverURL); err != nil {
		t.Fatalf("Can't connect to ws server: %s", err)
	}

	server.Close()

	select {
	case <-client.ErrorChannel:

	case <-time.After(5 * time.Second):
		t.Error("Waiting error channel timeout")
	}

	url, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("Can't parse url: %s", err)
	}

	server, err = wsserver.New("TestServer", url.Host, "../wsserver/data/crt.pem", "../wsserver/data/key.pem", newMessageProcessor)
	if err != nil {
		t.Fatalf("Can't create ws server: %s", err)
	}

	// Wait for server become ready
	time.Sleep(1 * time.Second)
}

func TestMessageHandler(t *testing.T) {
	type Message struct {
		Type  string
		Value int
	}

	messageChannel := make(chan Message)

	client, err := wsclient.New("Test", func(data []byte) {
		var message Message

		if err := json.Unmarshal(data, &message); err != nil {
			t.Errorf("Parse message error: %s", err)
			return
		}

		messageChannel <- message
	})

	if err != nil {
		t.Fatalf("Can't create ws client: %s", err)
	}
	defer client.Close()

	if err = client.Connect(serverURL); err != nil {
		t.Fatalf("Can't connect to ws server: %s", err)
	}

	if err = clientProcessor.sendMessage(websocket.TextMessage, []byte(`{"Type":"NOTIFY", "Value": 123}`)); err != nil {
		t.Fatalf("Can't send message: %s", err)
	}

	select {
	case message := <-messageChannel:
		if message.Type != "NOTIFY" || message.Value != 123 {
			t.Error("Wrong message value")
		}

	case <-time.After(5 * time.Second):
		t.Error("Waiting message timeout")
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newMessageProcessor(sendMessage wsserver.SendMessage) (processor wsserver.MessageProcessor, err error) {
	clientProcessor = &messageProcessor{sendMessage: sendMessage}

	return clientProcessor, nil
}

func (processor *messageProcessor) ProcessMessage(messageType int, message []byte) (response []byte, err error) {
	if processor.messageHandler != nil {
		response = processor.messageHandler(message)
	}

	return response, nil
}
