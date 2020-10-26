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

package amqphandler_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/streadway/amqp"

	"aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
)

/*******************************************************************************
 * Const
 ******************************************************************************/

const (
	inQueueName  = "in_queue"
	outQueueName = "out_queue"
	consumerName = "test_consumer"
	exchangeName = "test_exchange"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type backendClient struct {
	conn       *amqp.Connection
	channel    *amqp.Channel
	delivery   <-chan amqp.Delivery
	errChannel chan *amqp.Error
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var testClient backendClient

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

	if testClient.conn, err = amqp.Dial("amqp://guest:guest@localhost:5672/"); err != nil {
		return err
	}

	if testClient.channel, err = testClient.conn.Channel(); err != nil {
		return err
	}

	if _, err = testClient.channel.QueueDeclare(inQueueName, false, false, false, false, nil); err != nil {
		return err
	}

	if _, err = testClient.channel.QueueDeclare(outQueueName, false, false, false, false, nil); err != nil {
		return err
	}

	if err = testClient.channel.ExchangeDeclare(exchangeName, "fanout", false, false, false, false, nil); err != nil {
		return err
	}

	if err = testClient.channel.QueueBind(inQueueName, "", exchangeName, false, nil); err != nil {
		return err
	}

	if testClient.delivery, err = testClient.channel.Consume(inQueueName, "", true, false, false, false, nil); err != nil {
		return err
	}

	testClient.errChannel = testClient.conn.NotifyClose(make(chan *amqp.Error, 1))

	return nil
}

func cleanup() {
	if testClient.channel != nil {
		testClient.channel.QueueDelete(inQueueName, false, false, false)
		testClient.channel.QueueDelete(outQueueName, false, false, false)
		testClient.channel.ExchangeDelete(exchangeName, false, false)
		testClient.channel.Close()
	}

	if testClient.conn != nil {
		testClient.conn.Close()
	}

	if err := os.RemoveAll("tmp"); err != nil {
		log.Errorf("Can't remove tmp folder: %s", err)
	}
}

func sendMessage(correlationID string, message interface{}) (err error) {
	dataJSON, err := json.Marshal(message)
	if err != nil {
		return err
	}

	log.Debug(string(dataJSON))

	return testClient.channel.Publish(
		"",
		outQueueName,
		false,
		false,
		amqp.Publishing{
			CorrelationId: correlationID,
			ContentType:   "text/plain",
			Body:          dataJSON})
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {

	if err := setup(); err != nil {
		log.Fatalf("Error creating service images: %s", err)
	}

	ret := m.Run()

	cleanup()

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestSendMessages(t *testing.T) {
	amqpHandler, err := amqphandler.New(&config.Config{UnitStatusTimeout: 2})
	if err != nil {
		t.Fatalf("Can't create amqp: %s", err)
	}
	defer amqpHandler.Close()

	if err = amqpHandler.ConnectRabbit("localhost", "guest", "guest",
		exchangeName, consumerName, outQueueName); err != nil {
		t.Fatalf("Can't connect to server: %s", err)
	}

	testData := []*amqphandler.AOSMessage{
		&amqphandler.AOSMessage{
			Header: amqphandler.MessageHeader{MessageType: amqphandler.StateAcceptanceType, Version: amqphandler.ProtocolVersion},
			Data:   &amqphandler.StateAcceptance{ServiceID: "service0", Checksum: "0123456890", Result: "accepted", Reason: "just because"}},

		&amqphandler.AOSMessage{
			Header: amqphandler.MessageHeader{MessageType: amqphandler.UpdateStateType, Version: amqphandler.ProtocolVersion},
			Data:   &amqphandler.UpdateState{ServiceID: "service1", Checksum: "0993478847", State: "This is new state"}},

		&amqphandler.AOSMessage{
			Header: amqphandler.MessageHeader{MessageType: amqphandler.RequestServiceLogType, Version: amqphandler.ProtocolVersion},
			Data:   &amqphandler.RequestServiceLog{ServiceID: "service2", LogID: uuid.New().String(), From: &time.Time{}, Till: &time.Time{}}},

		&amqphandler.AOSMessage{
			Header: amqphandler.MessageHeader{MessageType: amqphandler.RequestServiceCrashLogType, Version: amqphandler.ProtocolVersion},
			Data:   &amqphandler.RequestServiceCrashLog{ServiceID: "service3", LogID: uuid.New().String()}},

		&amqphandler.AOSMessage{
			Header: amqphandler.MessageHeader{MessageType: amqphandler.SystemRevertType, Version: amqphandler.ProtocolVersion},
			Data:   &amqphandler.SystemRevert{ImageVersion: 3}},

		&amqphandler.AOSMessage{
			Header: amqphandler.MessageHeader{MessageType: amqphandler.SystemUpgradeType, Version: amqphandler.ProtocolVersion},
			Data:   &amqphandler.SystemUpgrade{ImageVersion: 4}},
	}

	for _, message := range testData {
		correlationID := uuid.New().String()

		if err = sendMessage(correlationID, message); err != nil {
			t.Errorf("Can't send message: %s", err)
			continue
		}

		select {
		case receiveMessage := <-amqpHandler.MessageChannel:
			if !reflect.DeepEqual(message.Data, receiveMessage.Data) {
				t.Errorf("Wrong data received: %v %v", message.Data, receiveMessage.Data)
				continue
			}

			if correlationID != receiveMessage.CorrelationID {
				t.Errorf("Wrong correlation ID received: %s %s", correlationID, receiveMessage.CorrelationID)
				continue
			}

		case err = <-testClient.errChannel:
			t.Fatalf("AMQP error: %s", err)
			return

		case <-time.After(5 * time.Second):
			t.Error("Waiting data timeout")
			continue
		}
	}
}

func TestReceiveMessages(t *testing.T) {
	amqpHandler, err := amqphandler.New(&config.Config{UnitStatusTimeout: 2})
	if err != nil {
		t.Fatalf("Can't create amqp: %s", err)
	}
	defer amqpHandler.Close()

	if err = amqpHandler.ConnectRabbit("localhost", "guest", "guest",
		exchangeName, consumerName, outQueueName); err != nil {
		t.Fatalf("Can't connect to server: %s", err)
	}

	type messageDesc struct {
		correlationID string
		call          func() error
		data          amqphandler.AOSMessage
		getDataType   func() interface{}
	}

	type messageHeader struct {
		Version     uint64
		MessageType string
	}

	initialSetupData := []amqphandler.ServiceInfo{
		amqphandler.ServiceInfo{ID: "service0", Version: 1, Status: "running", Error: "", StateChecksum: "1234567890"},
		amqphandler.ServiceInfo{ID: "service1", Version: 2, Status: "stopped", Error: "crash", StateChecksum: "1234567890"},
		amqphandler.ServiceInfo{ID: "service2", Version: 3, Status: "unknown", Error: "unknown", StateChecksum: "1234567890"},
	}

	initialLayersSetupData := []amqphandler.LayerInfo{
		amqphandler.LayerInfo{LayerID: "layer0", Digest: "sha256:0", Status: "installed"},
		amqphandler.LayerInfo{LayerID: "layer1", Digest: "sha256:1", Status: "installed"},
		amqphandler.LayerInfo{LayerID: "layer2", Digest: "sha256:2", Status: "installed"},
	}

	monitoringData := amqphandler.MonitoringData{Timestamp: time.Now().UTC()}
	monitoringData.Global.RAM = 1024
	monitoringData.Global.CPU = 50
	monitoringData.Global.UsedDisk = 2048
	monitoringData.Global.InTraffic = 8192
	monitoringData.Global.OutTraffic = 4096
	monitoringData.ServicesData = []amqphandler.ServiceMonitoringData{
		amqphandler.ServiceMonitoringData{ServiceID: "service0", RAM: 1024, CPU: 50, UsedDisk: 100000},
		amqphandler.ServiceMonitoringData{ServiceID: "service1", RAM: 128, CPU: 60, UsedDisk: 200000},
		amqphandler.ServiceMonitoringData{ServiceID: "service2", RAM: 256, CPU: 70, UsedDisk: 300000},
		amqphandler.ServiceMonitoringData{ServiceID: "service3", RAM: 512, CPU: 80, UsedDisk: 400000}}

	sendNewStateCorrelationID := uuid.New().String()

	pushServiceLogError := "Error"
	var pushServiceLogPartCount uint64 = 2
	var pushServiceLogPart uint64 = 1
	pushServiceLogData := amqphandler.PushServiceLog{
		LogID:     "log0",
		PartCount: &pushServiceLogPartCount,
		Part:      &pushServiceLogPart,
		Data:      &[]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		Error:     &pushServiceLogError}

	var alertVersion uint64 = 2

	alertsData := amqphandler.Alerts{
		amqphandler.AlertItem{
			Timestamp: time.Now().UTC(),
			Tag:       amqphandler.AlertTagSystemError,
			Source:    "system",
			Payload:   map[string]interface{}{"Message": "System error"},
		},
		amqphandler.AlertItem{
			Timestamp: time.Now().UTC(),
			Tag:       amqphandler.AlertTagSystemError,
			Source:    "service 1",
			Version:   &alertVersion,
			Payload:   map[string]interface{}{"Message": "Service crashed"},
		},
		amqphandler.AlertItem{
			Timestamp: time.Now().UTC(),
			Tag:       amqphandler.AlertTagResource,
			Source:    "system",
			Payload:   map[string]interface{}{"Parameter": "cpu", "Value": float64(100)},
		},
	}

	testData := []messageDesc{
		messageDesc{
			call: func() error {
				return amqpHandler.SendInitialSetup(initialSetupData, initialLayersSetupData)
			},
			data: amqphandler.AOSMessage{
				Header: amqphandler.MessageHeader{MessageType: amqphandler.UnitStatusType, Version: amqphandler.ProtocolVersion},
				Data:   &amqphandler.UnitStatus{Services: initialSetupData, Layers: initialLayersSetupData}},
			getDataType: func() interface{} {
				return &amqphandler.UnitStatus{}
			},
		},

		messageDesc{
			call: func() error {
				return amqpHandler.SendServiceStatus(initialSetupData[0])
			},
			data: amqphandler.AOSMessage{
				Header: amqphandler.MessageHeader{MessageType: amqphandler.ServiceStatusType, Version: amqphandler.ProtocolVersion},
				Data:   &amqphandler.UnitStatus{Services: []amqphandler.ServiceInfo{initialSetupData[0]}}},
			getDataType: func() interface{} {
				return &amqphandler.UnitStatus{}
			},
		},

		messageDesc{
			call: func() error {
				return amqpHandler.SendMonitoringData(monitoringData)
			},
			data: amqphandler.AOSMessage{
				Header: amqphandler.MessageHeader{MessageType: amqphandler.MonitoringDataType, Version: amqphandler.ProtocolVersion},
				Data:   &monitoringData},
			getDataType: func() interface{} {
				return &amqphandler.MonitoringData{}
			},
		},

		messageDesc{
			correlationID: sendNewStateCorrelationID,
			call: func() error {
				return amqpHandler.SendNewState("service0", "This is state", "12345679", sendNewStateCorrelationID)
			},
			data: amqphandler.AOSMessage{
				Header: amqphandler.MessageHeader{MessageType: amqphandler.NewStateType, Version: amqphandler.ProtocolVersion},
				Data:   &amqphandler.NewState{ServiceID: "service0", Checksum: "12345679", State: "This is state"}},
			getDataType: func() interface{} {
				return &amqphandler.NewState{}
			},
		},

		messageDesc{
			call: func() error {
				return amqpHandler.SendStateRequest("service1", true)
			},
			data: amqphandler.AOSMessage{
				Header: amqphandler.MessageHeader{MessageType: amqphandler.StateRequestType, Version: amqphandler.ProtocolVersion},
				Data:   &amqphandler.StateRequest{ServiceID: "service1", Default: true}},
			getDataType: func() interface{} {
				return &amqphandler.StateRequest{}
			},
		},

		messageDesc{
			call: func() error {
				return amqpHandler.SendServiceLog(pushServiceLogData)
			},
			data: amqphandler.AOSMessage{
				Header: amqphandler.MessageHeader{MessageType: amqphandler.PushServiceLogType, Version: amqphandler.ProtocolVersion},
				Data: &amqphandler.PushServiceLog{
					LogID:     pushServiceLogData.LogID,
					PartCount: pushServiceLogData.PartCount,
					Part:      pushServiceLogData.Part,
					Data:      pushServiceLogData.Data,
					Error:     pushServiceLogData.Error}},
			getDataType: func() interface{} {
				return &amqphandler.PushServiceLog{}
			},
		},

		messageDesc{
			call: func() error {
				return amqpHandler.SendAlerts(alertsData)
			},
			data: amqphandler.AOSMessage{
				Header: amqphandler.MessageHeader{MessageType: amqphandler.AlertsType, Version: amqphandler.ProtocolVersion},
				Data:   &alertsData},
			getDataType: func() interface{} {
				return &amqphandler.Alerts{}
			},
		},

		messageDesc{
			call: func() error {
				return amqpHandler.SendSystemRevertStatus("success", "", 3)
			},
			data: amqphandler.AOSMessage{
				Header: amqphandler.MessageHeader{MessageType: amqphandler.SystemRevertStatusType, Version: amqphandler.ProtocolVersion},
				Data:   &amqphandler.SystemRevertStatus{Status: "success", ImageVersion: 3}},
			getDataType: func() interface{} {
				return &amqphandler.SystemRevertStatus{}
			},
		},

		messageDesc{
			call: func() error {
				return amqpHandler.SendSystemUpgradeStatus("failed", "", 4)
			},
			data: amqphandler.AOSMessage{
				Header: amqphandler.MessageHeader{MessageType: amqphandler.SystemUpgradeStatusType, Version: amqphandler.ProtocolVersion},
				Data:   &amqphandler.SystemUpgradeStatus{Status: "failed", ImageVersion: 4}},
			getDataType: func() interface{} {
				return &amqphandler.SystemUpgradeStatus{}
			},
		},

		messageDesc{
			call: func() error {
				return amqpHandler.SendSystemVersion(5, 1)
			},
			data: amqphandler.AOSMessage{
				Header: amqphandler.MessageHeader{MessageType: amqphandler.SystemVersionType, Version: amqphandler.ProtocolVersion},
				Data:   &amqphandler.SystemVersion{ImageVersion: 5, ResourcesConfigVersion: 1}},
			getDataType: func() interface{} {
				return &amqphandler.SystemVersion{}
			},
		},
	}

	for _, message := range testData {
		if err = message.call(); err != nil {
			t.Errorf("Can't perform call: %s", err)
			continue
		}

		select {
		case delivery := <-testClient.delivery:
			var rawData json.RawMessage
			receiveData := amqphandler.AOSMessage{Data: &rawData}

			if err = json.Unmarshal(delivery.Body, &receiveData); err != nil {
				t.Errorf("Error parsing message: %s", err)
				continue
			}

			if message.correlationID != delivery.CorrelationId {
				t.Errorf("Wrong correlation ID received: %s %s", message.correlationID, delivery.CorrelationId)
			}

			if !reflect.DeepEqual(receiveData.Header, message.data.Header) {
				t.Errorf("Wrong Header received: %v != %v", receiveData.Header, message.data.Header)
				continue
			}

			decodedMsg := message.getDataType()

			if err = json.Unmarshal(rawData, &decodedMsg); err != nil {
				t.Errorf("Error parsing message: %s", err)
				continue
			}

			if !reflect.DeepEqual(message.data.Data, decodedMsg) {
				t.Errorf("Wrong data received: %v != %v", decodedMsg, message.data.Data)
			}

		case err = <-testClient.errChannel:
			t.Fatalf("AMQP error: %s", err)
			return

		case <-time.After(5 * time.Second):
			t.Error("Waiting data timeout")
			continue
		}
	}
}
