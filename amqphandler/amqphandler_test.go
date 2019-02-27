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
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
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
	conn    *amqp.Connection
	channel *amqp.Channel
}

type sendData struct {
	version     uint64
	messageType string
	data        interface{}
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
}

func sendMessage(correlationID string, version uint64, messageType string, data interface{}) (err error) {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}

	tmpData := make(map[string]interface{})

	if err = json.Unmarshal(dataJSON, &tmpData); err != nil {
		return err
	}

	tmpData["version"] = version
	tmpData["messageType"] = messageType

	if dataJSON, err = json.Marshal(tmpData); err != nil {
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
	amqpHandler, err := amqphandler.New()
	if err != nil {
		t.Fatalf("Can't create amqp: %s", err)
	}
	defer amqpHandler.Close()

	if err = amqpHandler.ConnectRabbit("localhost", "guest", "guest",
		exchangeName, consumerName, outQueueName); err != nil {
		t.Fatalf("Can't connect to server: %s", err)
	}

	testData := []sendData{
		sendData{1, "stateAcceptance", &amqphandler.StateAcceptance{
			ServiceID: "service0", Checksum: "0123456890", Result: "accepted", Reason: "just because"}},
		sendData{1, "updateState", &amqphandler.UpdateState{
			ServiceID: "service1", Checksum: "0993478847", State: "This is new state"}},
		sendData{1, "requestServiceLog", &amqphandler.RequestServiceLog{
			ServiceID: "service2", LogID: uuid.New().String(), From: &time.Time{}, Till: &time.Time{}}},
		sendData{1, "requestServiceCrashLog", &amqphandler.RequestServiceCrashLog{
			ServiceID: "service3", LogID: uuid.New().String()}},
	}

	for _, message := range testData {
		correlationID := uuid.New().String()

		if err = sendMessage(correlationID, message.version, message.messageType, message.data); err != nil {
			t.Errorf("Can't send message: %s", err)
			continue
		}

		select {
		case receiveMessage := <-amqpHandler.MessageChannel:
			if !reflect.DeepEqual(message.data, receiveMessage.Data) {
				t.Errorf("Wrong data received: %v %v", message.data, receiveMessage.Data)
				continue
			}

			if correlationID != receiveMessage.CorrelationID {
				t.Errorf("Wrong correlation ID received: %s %s", correlationID, receiveMessage.CorrelationID)
				continue
			}

		case <-time.After(5 * time.Second):
			t.Error("Waiting data timeout")
			continue
		}
	}
}
