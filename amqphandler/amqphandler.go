package amqphandler

import (
	"bytes"
	//"crypto/tls"
	//"crypto/x509"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	//	"net/url"
	//"os"
	//	"time"

	log "github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

const DEFAULT_CONFIG_FILE = "/etc/demo-application/demo_config.json"

///API structures
type serviseDiscoveryRequest struct {
	Version int      `json:"version"`
	VIN     string   `json:"VIN"`
	Users   []string `json:"users"`
}

type serviseDiscoveryResp struct {
	Version    int                   `json:"version"`
	Connection reqbbitConnectioninfo `json:"connection"`
}
type reqbbitConnectioninfo struct {
	SessionId     string        `json:"sessionId"`
	SendParam     sendParam     `json:"sendParams"`
	ReceiveParams receiveParams `json:"receiveParams"`
}

type sendParam struct {
	Host      string        `json:"host"`
	Mandatory bool          `json:"mandatory"`
	Immediate bool          `json:"immediate"`
	Exchange  excahngeParam `json:"exchange"`
}

type excahngeParam struct {
	Name       string `json:"name"`
	Durable    bool   `json:"durable"`
	AutoDetect bool   `json:"autoDetect"`
	Internal   bool   `json:"internal"`
	NoWait     bool   `json:"noWait"`
}

type receiveParams struct {
	Host      string    `json:"host"`
	Consumer  string    `json:"consumer"`
	AutoAck   bool      `json:"autoAck"`
	Exclusive bool      `json:"exclusive"`
	MoLocal   bool      `json:"noLocal"`
	NoWait    bool      `json:"noWait"`
	Queue     queueInfo `json:"queue"`
}

type queueInfo struct {
	Name             string `json:"name"`
	Durable          bool   `json:"durable"`
	DeleteWhenUnused bool   `json:"deleteWhenUnused"`
	Exclusive        bool   `json:"exclusive"`
	NoWait           bool   `json:"noWait"`
}

/// internal structures
type amqpLocalSenderConnectionInfo struct {
	conn  *amqp.Connection
	ch    *amqp.Channel
	valid bool
}

type amqpLocalConsumerConnectionInfo struct {
	conn  *amqp.Connection
	ch    <-chan amqp.Delivery
	valid bool
}

type PackageInfo struct {
	Name        string
	Version     string
	DownloadUrl string
}

// channel for return packages
var amqpChan chan PackageInfo

// connection for sending data
var exchangeInfo amqpLocalSenderConnectionInfo

// connection for receiving data
var consumerInfo amqpLocalConsumerConnectionInfo

//service discovery implementation
func getAmqpConnInfo(request serviseDiscoveryRequest) (reqbbitConnectioninfo, error) {

	var jsonResp serviseDiscoveryResp

	reqJson, err := json.Marshal(request)
	if err != nil {
		log.Printf("erroe :%v", err)
	}

	log.Printf("request :%v", string(reqJson))

	// caCert, err := ioutil.ReadFile("server.crt") //todo add path to cerificates
	// if err != nil {
	// 	log.Error(err)
	// }
	// caCertPool := x509.NewCertPool()
	// caCertPool.AppendCertsFromPEM(caCert)

	// cert, err := tls.LoadX509KeyPair("client.crt", "client.key")
	// if err != nil {
	// 	log.Error(err)
	// 	return jsonResp.Connection, err
	// }

	// client := &http.Client{
	// 	Transport: &http.Transport{
	// 		TLSClientConfig: &tls.Config{
	// 			RootCAs:      caCertPool,
	// 			Certificates: []tls.Certificate{cert},
	// 		},
	// 	},
	// }
	// resp, err := client.Post("https://someurl.com", "application/json", bytes.NewBuffer(reqJson)) //todo: define service descovery url
	// if err != nil {
	// 	log.Error(err)
	// 	return jsonResp.Connection, err
	// }
	log.Info("Try to send: %v\n")

	resp, err := http.Post("https://someurl.com", "application/json", bytes.NewBuffer(reqJson)) //todo: define service descovery url
	log.Info("Send OK: %v\n")

	htmlData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Error("error Read ", err)
		return jsonResp.Connection, err
	}
	defer resp.Body.Close()

	err = json.Unmarshal(htmlData, &jsonResp) // todo add check
	if err != nil {
		log.Error("receive ", string(htmlData), err)
		return jsonResp.Connection, err
	}

	return jsonResp.Connection, nil
}

func getSendConnectionInfo(params sendParam) (amqpLocalSenderConnectionInfo, error) {
	//TODO map input params to configs
	var retData amqpLocalSenderConnectionInfo
	conn, err := amqp.Dial("amqp://localhost:5672/")
	if err != nil {
		log.Warning("amqp.Dial to exchange ", err)
		return retData, err
	}

	ch, err := conn.Channel()
	if err != nil {
		log.Warning("Failed to open a send channel ", err)
		return retData, err
	}

	err = ch.ExchangeDeclare(
		"logs",   // name
		"fanout", // type
		true,     // durable
		false,    // auto-deleted
		false,    // internal
		false,    // no-wait
		nil,      // arguments
	)
	if err != nil {
		log.Warning("Failed to declare an exchangel", err)
		return retData, err
	}

	retData.conn = conn
	retData.ch = ch
	retData.valid = true
	log.Info("create excahnge OK\n")
	return retData, nil
}

/*func getConsumeInfo(param consumeeParams) (amqpLocalSenderConnectionInfo, error) {

}*/
func getConsumerConnectionInfo(param receiveParams) (amqpLocalConsumerConnectionInfo, error) {
	var retData amqpLocalConsumerConnectionInfo
	conn, err := amqp.Dial("amqp://localhost:5672/")
	if err != nil {
		log.Warning("amqp.Dial to exchange ", err)
		return retData, err
	}

	ch, err := conn.Channel()
	if err != nil {
		log.Warning("Failed to open receive channel", err)
		return retData, err
	}

	q, err := ch.QueueDeclare(
		"task_queue", // name
		true,         // durable
		false,        // delete when unused
		false,        // exclusive
		false,        // no-wait
		nil,          // arguments
	)
	if err != nil {
		log.Warning("Failed to declare a queue", err)
		return retData, err
	}

	msgs, err := ch.Consume(
		q.Name, // queue
		"",     // consumer
		false,  // auto-ack
		false,  // exclusive
		false,  // no-local
		false,  // no-wait
		nil,    // args
	)
	if err != nil {
		log.Warning("Failed to register a consumer", err)
		return retData, err
	}
	retData.ch = msgs
	retData.conn = conn
	retData.valid = true
	return retData, nil
}

func publishMessage(data []byte, correlationId string) error {
	if exchangeInfo.valid != true {
		return errors.New("invalid connection")
	}

	err := exchangeInfo.ch.Publish(
		"logs", // exchange
		"",     // routing key
		false,  // mandatory
		false,  // immediate
		amqp.Publishing{
			ContentType:   "application/json",
			DeliveryMode:  2,
			CorrelationId: correlationId,
			Body:          data,
		})
	if err != nil {
		log.Warning("error publish", err)
	}
	return err
}

//todo add return error
func InitAmqphandler(outputchan chan PackageInfo) {

	amqpChan = outputchan

	//todo get VIn users form VIS
	servRequst := serviseDiscoveryRequest{
		Version: 1,
		VIN:     "12345ZXCVBNMA1234",
		Users:   []string{"user1", "vendor2"}}

	amqpConn, err2 := getAmqpConnInfo(servRequst)
	if err2 != nil {
		log.Error("NO connection info: ", err2)
		//todo add return
	}
	log.Printf("Results: %v\n", amqpConn)

	exchangeInfo, err := getSendConnectionInfo(amqpConn.SendParam)
	if err != nil {
		log.Error("error get exchage info ", err)
		//todo add return
	}

	log.Info("ex %v", exchangeInfo.valid)

	consumerInfo, err := getConsumerConnectionInfo(amqpConn.ReceiveParams)

	log.Info("ex %v", consumerInfo.valid)

	//main logic
	// getSendQueuq()
	// selsect{
	// 	1) receive data
	// 	2) onCloseExcahnge connection
	// 	3) onClose qiue
	// }

	//implment closeAll

	//log.Printf("strucrt %v", servRequst)

	// file2, e := ioutil.ReadFile("./test.json")
	// if e != nil {
	// 	log.Printf("!!!!!!!!!!!!File error: %v\n", e)
	// 	os.Exit(1)
	// }

	// var jsonResp serviseDiscoveryResp
	// json.Unmarshal(file2, &jsonResp)
	// log.Printf("Results: %v\n", jsonResp.Connection.Exchange)
	// log.Printf("Results: %v\n", jsonResp)

	//str := ("[{\"Name\":\"Demo application\",\"DownloadUrl\":\"https://fusionpoc1storage.blob.core.windows.net/images/Demo_application_1.2_demo_1.txt\",\"Version\":\"1.2\",\"TimeStamp\":\"2017-12-14T17:12:58.1443792Z\",\"ResponseCount\":0}]")
	//str2 := ("{\"Name\":\"Demo application\",\"DownloadUrl\":\"https://fusionpoc1storage.blob.core.windows.net/images/Demo_application_1.2_demo_1.txt\",\"Version\":\"1.2\",\"TimeStamp\":\"2017-12-14T17:12:58.1443792Z\",\"ResponseCount\":0}")
	//dec := json.NewDecoder(strings.NewReader(str))

	log.Printf(" [.] Got ")
}
