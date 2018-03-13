package amqphandler

import (
	"testing"

	log "github.com/sirupsen/logrus"
	"gopkg.in/jarcoal/httpmock.v1"
	//"net/http"
	//	"github.com/streadway/amqp"
)

var retResponce string = `{
    "version": 1,
    "connection": {
        "sessionId" : "session_ID",
        "sendParams": {
            "host": "rabbit_send_host",
            "mandatory": false,
            "immediate": false,
            "exchange": {
                "name": "exchange name",
                "durable": true,
                "autoDetect": false,
                "internal": false,
                "noWait": false
            }
        },
        "receiveParams": {
            "host": "rabbit_receive_host",
            "consumer": "consumer name",
            "autoAck": false,
            "exclusive": false,
            "noLocal": false,
            "noWait": false,
            "queue": {
                "name": "receiveQueue",
                "durable": false,
                "deleteWhenUnused": false,
                "exclusive": false,
                "noWait": true
            }
        }
    }
}`

func Test_seviceDiscovery(t *testing.T) {
	log.Info("regetrate\n")
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()
	httpmock.RegisterResponder("POST", "https://someurl.com",
		httpmock.NewStringResponder(200, retResponce))
	// do stuff that makes a request to articles.json

	servRequst := serviseDiscoveryRequest{
		Version: 1,
		VIN:     "12345ZXCVBNMA1234",
		Users:   []string{"user1", "vendor2"}}

	amqpConn, err := getAmqpConnInfo(servRequst)
	if err != nil {
		t.Error(err)
		return
	}
	if amqpConn.SendParam.Host != "rabbit_send_host" {
		t.Error("incorret send params")
	}

	if amqpConn.ReceiveParams.Host != "rabbit_receive_host" {
		t.Error("incorret host receive params")
	}
	if amqpConn.ReceiveParams.Queue.Name != "receiveQueu" {
		t.Error("incorret queue receive params")
	}

	log.Info("%v", amqpConn, err)
}

// func makeTestServer() {
// 	conn, err := amqp.Dial("amqp://localhost:5672/")
// 	failOnError(err, "TEST Failed to connect to RabbitMQ")
// 	defer conn.Close()

// 	ch, err := conn.Channel()
// 	failOnError(err, "Failed to open a channel")
// 	defer ch.Close()

// 	err = ch.ExchangeDeclare(
// 		"logs",   // name
// 		"fanout", // type
// 		true,     // durable
// 		false,    // auto-deleted
// 		false,    // internal
// 		false,    // no-wait
// 		nil,      // arguments
// 	)
// 	failOnError(err, "Failed to declare an exchange")

// 	q, err := ch.QueueDeclare(
// 		"",    // name
// 		false, // durable
// 		false, // delete when usused
// 		true,  // exclusive
// 		false, // no-wait
// 		nil,   // arguments
// 	)
// 	failOnError(err, "Failed to declare a queue")

// 	err = ch.QueueBind(
// 		q.Name, // queue name
// 		"",     // routing key
// 		"logs", // exchange
// 		false,
// 		nil)
// 	failOnError(err, "Failed to bind a queue")

// 	msgs, err := ch.Consume(
// 		q.Name, // queue
// 		"",     // consumer
// 		true,   // auto-ack
// 		false,  // exclusive
// 		false,  // no-local
// 		false,  // no-wait
// 		nil,    // args
// 	)
// 	failOnError(err, "Failed to register a consumer")

// 	forever := make(chan bool)

// 	go func() {
// 		for d := range msgs {
// 			log.Printf(" [x] %s", d.Body)
// 		}
// 	}()

// 	log.Printf(" [*] Waiting for logs. To exit press CTRL+C")
// 	<-forever
// }

// func Test_exchangeInfo(t *testing.T) {

// 	go makeTestServer()

// 	var someParam exchangeParams

// 	exchangeInfo, err := getSendConnectionInfo(someParam)
// 	if err != nil {
// 		log.Error("error get exchage info ", err)
// 		//todo add return
// 	}
// 	if exchangeInfo.valid != true {
// 		t.Errorf("connection to exchage invalid")
// 	}

// 	log.Info("ex= ", exchangeInfo.valid)

// }
