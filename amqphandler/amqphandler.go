package amqphandler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	//	"net/url"
	//"os"
	//"time"
	log "github.com/sirupsen/logrus"

	"github.com/streadway/amqp"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/fcrypt"
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
var amqpChan = make(chan PackageInfo, 100)

// connection for sending data
var exchangeInfo amqpLocalSenderConnectionInfo

// connection for receiving data
var consumerInfo amqpLocalConsumerConnectionInfo

//service discovery implementation
func getAmqpConnInfo(url string, request serviseDiscoveryRequest) (reqbbitConnectioninfo, error) {

	var jsonResp serviseDiscoveryResp

	reqJson, err := json.Marshal(request)
	if err != nil {
		log.Warn("erroe :%v", err)
		return jsonResp.Connection, err
	}

	log.Info("request :%v", string(reqJson))

	tlsConfig, err := fcrypt.GetTlsConfig()
	if err != nil {
		log.Warn("GetTlsConfig error : ", err)
		return jsonResp.Connection, err
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport}

	resp, err := client.Post("https://someurl1111.com", "text", bytes.NewBuffer(reqJson)) //todo: define service descovery url

	if err != nil {
		log.Warn("Post error : ", err)
		return jsonResp.Connection, err
	}
	defer resp.Body.Close()

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

	tlsConfig, err := fcrypt.GetTlsConfig()
	if err != nil {
		log.Warn("GetTlsConfig error : ", err)
		return retData, err
	}
	//conn, err := amqp.Dial("amqp://localhost:5672/")
	conn, err := amqp.DialTLS("amqps://"+params.Host+"/", tlsConfig)
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

func getConsumerConnectionInfo(param receiveParams) (amqpLocalConsumerConnectionInfo, error) {
	var retData amqpLocalConsumerConnectionInfo

	tlsConfig, err := fcrypt.GetTlsConfig()
	if err != nil {
		log.Warn("GetTlsConfig error : ", err)
		return retData, err
	}

	conn, err := amqp.DialTLS("amqps://"+param.Host+"/", tlsConfig)
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

func startConumer(consumerInfo *amqpLocalConsumerConnectionInfo) {
	if consumerInfo.valid != true {
		return
	}
	for d := range consumerInfo.ch {
		log.Printf("Received a message: %s", d.Body)
		//todo make json
		//todo decript
		//todo push to channel
	}
}

//todo add return error
func InitAmqphandler(sdURL string) (chan PackageInfo, error) {

	//todo get VIn users form VIS
	servRequst := serviseDiscoveryRequest{
		Version: 1,
		VIN:     "12345ZXCVBNMA1234",
		Users:   []string{"user1", "vendor2"}}

	amqpConn, err := getAmqpConnInfo(sdURL, servRequst)
	if err != nil {
		log.Error("NO connection info: ", err)
		//todo add return
	}
	log.Printf("Results: %v\n", amqpConn)

	exchangeInfo, err := getSendConnectionInfo(amqpConn.SendParam)
	if err != nil {
		log.Error("error get exchage info ", err)
		//todo add return
	}

	log.Info("exchange ", exchangeInfo.valid)

	consumerInfo, err := getConsumerConnectionInfo(amqpConn.ReceiveParams)
	if err != nil {
		exchangeInfo.conn.Close()
		exchangeInfo.valid = false
		log.Error("error get consumer info ", err)
		//todo add return
	}

	log.Info("consumer %v", consumerInfo.valid)

	go startConumer(&consumerInfo)

	//main logic
	// 	2) onCloseExcahnge connection
	// 	3) onClose qiue
	// }

	//implment closeAll

	log.Printf(" [.] Got ")

	return amqpChan, err
}
