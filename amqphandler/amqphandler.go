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
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/launcher"
)

//TODO: list
// - close/erase channel
// - add cahnnel for send data
// - remove global variables
// - change ServiseInfoFromCloud according to KB when will be available
// - sessionID
// - coleration ID
// - erro ahndling
// - reconnect 3 times for each connection

const DEFAULT_CONFIG_FILE = "/etc/demo-application/demo_config.json"

///API structures
type serviseDiscoveryRequest struct {
	Version int      `json:"version"`
	VIN     string   `json:"VIN"`
	Users   []string `json:"users"`
}

type vehicleStatus struct {
	Version     uint                   `json:"version"`
	MessageType string                 `json:"messageType"`
	SessionId   string                 `json:"sessionId"`
	Sevices     []launcher.ServiceInfo `json:"services"`
}

type desiredStatus struct {
	Version     uint     `json:"version"`
	MessageType string   `json:"messageType"`
	SessionId   string   `json:"sessionId"`
	Sevices     []string `json:"services"`
}
type serviseDiscoveryResp struct {
	Version    uint                  `json:"version"`
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
	NoLocal   bool      `json:"noLocal"`
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

type ServiseInfoFromCloud struct {
	Id                     string `json:"id"`
	Version                uint   `json:"version"`
	UpdateType             string `json:"updateType"`
	DownloadUrl            string `json:"downloadUrl"`
	UrlExpiration          string `json:"urlExpiration"`
	Hash                   uint   `json:"hash"`
	Size                   uint   `json:"size"`
	SignatureAlgorithm     string `json:"signatureAlgorithm"`
	SignatureAlgorithmHash string `json:"signatureAlgorithmHash"`
	SignatureScheme        string `json:"signatureScheme"`
	ImageSignature         string `json:"imageSignature"`
	CertificateChain       string `json:"certificateChain"`
	EncryptionKey          string `json:"encryptionKey"`
	EncryptionAlgorythm    string `json:"encryptionAlgorythm"`
	EncryptionMode         string `json:"encryptionMode"`
	EncryptionModeParams   string `json:"encryptionModeParams"`
}

/// internal structures
type amqpLocalSenderConnectionInfo struct {
	conn         *amqp.Connection
	ch           *amqp.Channel
	valid        bool
	exchangeName string
	mandatory    bool
	immediate    bool
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
var amqpChan = make(chan interface{}, 100)

// connection for sending data
var exchangeInfo amqpLocalSenderConnectionInfo

// connection for receiving data
var consumerInfo amqpLocalConsumerConnectionInfo

//TODO: redmove from global
var localSessionID string

//service discovery implementation
func getAmqpConnInfo(url string, request serviseDiscoveryRequest) (reqbbitConnectioninfo, error) {

	var jsonResp serviseDiscoveryResp

	reqJson, err := json.Marshal(request)
	if err != nil {
		log.Warn("erroe :", err)
		return jsonResp.Connection, err
	}

	log.Info("request :", string(reqJson))

	tlsConfig, err := fcrypt.GetTlsConfig()
	if err != nil {
		log.Warn("GetTlsConfig error : ", err)
		return jsonResp.Connection, err
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport}

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(reqJson))

	if err != nil {
		log.Warn("Post error : ", err)
		return jsonResp.Connection, err
	}
	defer resp.Body.Close()

	log.Info("Send OK: \n")

	htmlData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Error("error Read ", err)
		return jsonResp.Connection, err
	}
	defer resp.Body.Close()

	err = json.Unmarshal(htmlData, &jsonResp) // TODO: add check
	if err != nil {
		log.Error("receive ", string(htmlData), err)
		return jsonResp.Connection, err
	}
	localSessionID = jsonResp.Connection.SessionId
	return jsonResp.Connection, nil
}

type amqpExtAuth struct{}

func (a amqpExtAuth) Mechanism() string {
	return "EXTERNAL"
}

func (a amqpExtAuth) Response() string {
	return ""
}

func getSendConnectionInfo(params sendParam) (amqpLocalSenderConnectionInfo, error) {
	//TODO: map input params to configs
	var retData amqpLocalSenderConnectionInfo

	tlsConfig, err := fcrypt.GetTlsConfig()
	if err != nil {
		log.Warn("GetTlsConfig error : ", err)
		return retData, err
	}

	authentication := []amqp.Authentication{amqpExtAuth{}}
	config := amqp.Config{TLSClientConfig: tlsConfig,
		SASL: authentication}
	//conn, err := amqp.Dial("amqp://localhost:5672/")
	conn, err := amqp.DialConfig("amqps://"+params.Host+"/", config)
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
		params.Exchange.Name,       // name
		"fanout",                   // type
		params.Exchange.Durable,    // durable
		params.Exchange.AutoDetect, // auto-deleted
		params.Exchange.Internal,   // internal
		params.Exchange.NoWait,     // no-wait
		nil, // arguments
	)
	if err != nil {
		log.Warning("Failed to declare an exchangel", err)
		return retData, err
	}

	go func() {
		err := <-conn.NotifyClose(make(chan *amqp.Error))
		log.Printf("Excahnge connection closing: %s \n")
		if exchangeInfo.valid != false {
			exchangeInfo.valid = false
			amqpChan <- err
		}
	}()

	retData.conn = conn
	retData.ch = ch
	retData.valid = true

	retData.mandatory = params.Mandatory
	retData.immediate = params.Immediate
	retData.exchangeName = params.Exchange.Name

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
	authentication := []amqp.Authentication{amqpExtAuth{}}
	config := amqp.Config{TLSClientConfig: tlsConfig,
		SASL: authentication}

	conn, err := amqp.DialConfig("amqps://"+param.Host+"/", config)
	if err != nil {
		log.Warning("amqp.Dial to exchange ", err)
		return retData, err
	}

	ch, err := conn.Channel()
	if err != nil {
		log.Warning("Failed to open receive channel", err)
		return retData, err
	}

	msgs, err := ch.Consume(
		param.Queue.Name, // queue
		param.Consumer,   // consumer
		true,             // auto-ack param.AutoAck
		param.Exclusive,  // exclusive
		param.NoLocal,    // no-local
		param.NoWait,     // no-wait
		nil,              // args
	)
	if err != nil {
		log.Warning("Failed to register a consumer", err)
		return retData, err
	}
	go func() {
		err := <-conn.NotifyClose(make(chan *amqp.Error))
		log.Printf("closing: %s \n")
		if consumerInfo.valid != false {
			consumerInfo.valid = false
			amqpChan <- err
		}
	}()

	retData.ch = msgs
	retData.conn = conn
	retData.valid = true
	return retData, nil
}

func publishMessage(data []byte, correlationId string) error {

	if exchangeInfo.valid != true {
		log.Error("invalid Sender connection", string(data))
		return errors.New("invalid Sender connection")
	}

	if err := exchangeInfo.ch.Publish(
		exchangeInfo.exchangeName, // exchange
		"", // routing key
		exchangeInfo.mandatory, // mandatory
		exchangeInfo.immediate, // immediate
		amqp.Publishing{
			ContentType:   "application/json",
			DeliveryMode:  2,
			CorrelationId: correlationId,
			Body:          data,
		}); err != nil {
		log.Warning("error publish", err)
		return err
	}
	log.Info("SNED OK ", string(data))
	return nil
}

func startConumer(consumerInfo *amqpLocalConsumerConnectionInfo) {
	if consumerInfo.valid != true {
		return
	}
	for d := range consumerInfo.ch {
		log.Printf("Received a message: %s", d.Body)
		var ecriptList desiredStatus

		err := json.Unmarshal(d.Body, &ecriptList) // TODO: add check
		if err != nil {
			log.Error("receive ", string(d.Body), err)
			continue
		}

		if ecriptList.MessageType != "desiredStatus" {
			log.Warning("incorrect msg type ", ecriptList.MessageType)
			continue
		}

		for _, element := range ecriptList.Sevices {
			decriptData, err := fcrypt.DecryptMetadata([]byte(element))
			if err != nil {
				log.Warning(" decript metadta erroe")
				continue
			}
			var servInfo ServiseInfoFromCloud
			err = json.Unmarshal(decriptData, &servInfo) // TODO: add check
			if err != nil {
				log.Error("Cand make json from decripted data", string(decriptData), err)
				continue
			}
			amqpChan <- servInfo
		}
	}
}

func SendInitialSetup(serviceList []launcher.ServiceInfo) {
	log.Info("SendInitialSetup ", serviceList)
	msg := vehicleStatus{Version: 1, MessageType: "vehicleStatus", SessionId: localSessionID, Sevices: serviceList}
	reqJson, err := json.Marshal(msg)
	if err != nil {
		log.Warn("erroe :%v", err)
		return
	}

	log.Info("some info ", exchangeInfo.valid, exchangeInfo.exchangeName)

	publishMessage(reqJson, "100")
}

func CloseAllConnections() {
	switch {
	case exchangeInfo.valid == true:
		exchangeInfo.valid = false

	case exchangeInfo.conn != nil:
		exchangeInfo.conn.Close()

	case consumerInfo.valid == true:
		consumerInfo.valid = false

	case consumerInfo.conn != nil:
		consumerInfo.conn.Close()
	}
}

//TODO: add return error
func InitAmqphandler(sdURL string) (chan interface{}, error) {

	//TODO:do get VIn users form VIS
	servRequst := serviseDiscoveryRequest{
		Version: 1,
		VIN:     "12345ZXCVBNMA1234",
		Users:   []string{"user1", "vendor2"}}

	amqpConn, err := getAmqpConnInfo(sdURL, servRequst)
	if err != nil {
		log.Error("NO connection info: ", err)
		return amqpChan, err
	}
	log.Printf("Results: \n", amqpConn)

	exchangeInfo, err = getSendConnectionInfo(amqpConn.SendParam)
	if err != nil {
		log.Error("error get exchage info ", err)
		return amqpChan, err
	}

	log.Info("exchange ", exchangeInfo.valid)

	consumerInfo, err = getConsumerConnectionInfo(amqpConn.ReceiveParams)
	if err != nil {
		if exchangeInfo.valid == true {
			exchangeInfo.conn.Close()
			exchangeInfo.valid = false
		}
		log.Error("error get consumer info ", err)
		return amqpChan, err
	}

	log.Info("consumer ", consumerInfo.valid)

	go startConumer(&consumerInfo)

	//TODO: implment closeAll
	log.Printf(" [.] Got ")

	return amqpChan, nil
}
