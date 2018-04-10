package amqphandler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/url"
	//"os"
	//"time"
	log "github.com/sirupsen/logrus"

	"github.com/streadway/amqp"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/fcrypt"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/launcher"
)

//TODO: list
// - close/erase channel
// - correlation ID
// - reconnect 3 times for each connection

/*******************************************************************************
 * Types
 ******************************************************************************/

type AmqpHandler struct {
	//sendChan       chan []byte
	exchangeInfo   amqpLocalSenderConnectionInfo   // connection for sending data
	consumerInfo   amqpLocalConsumerConnectionInfo // connection for receiving data
	localSessionID string
}

type ServiceInfoFromCloud struct {
	Id                     string `json:"id"`
	Version                uint   `json:"version"`
	UpdateType             string `json:"updateType"`
	DownloadUrl            string `json:"downloadUrl"`
	UrlExpiration          string `json:"urlExpiration"`
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
	Version     uint   `json:"version"`
	MessageType string `json:"messageType"`
	SessionId   string `json:"sessionId"`
	Sevices     string `json:"services"`
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
	User      string        `json:"user"`
	Password  string        `json:"password"`
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
	User      string    `json:"user"`
	Password  string    `json:"password"`
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

/*******************************************************************************
 * Variables
 ******************************************************************************/

// channel for return packages
var amqpChan = make(chan interface{}, 100)

var sendChan = make(chan []byte, 100)

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New() (handler *AmqpHandler, err error) {
	handler = &AmqpHandler{}
	return handler, nil
}

func (handler *AmqpHandler) InitAmqphandler(sdURL string) (chan interface{}, error) {

	//TODO:do get VIN users form VIS
	servRequst := serviseDiscoveryRequest{
		Version: 1,
		VIN:     "12345ZXCVBNMA1234",
		Users:   []string{"user1", "OEM2"}}

	amqpConn, err := getAmqpConnInfo(sdURL, servRequst)
	if err != nil {
		log.Error("NO connection info: ", err)
		return amqpChan, err
	}
	log.Debug("Results: \n", amqpConn)

	handler.exchangeInfo, err = handler.getSendConnectionInfo(&amqpConn.SendParam)
	if err != nil {
		log.Error("Error get exchange info ", err)
		return amqpChan, err
	}

	log.Info("Exchange ", handler.exchangeInfo.valid)

	handler.consumerInfo, err = handler.getConsumerConnectionInfo(&amqpConn.ReceiveParams)
	if err != nil {
		//TODO: call CloseAllConnections
		if handler.exchangeInfo.valid == true {
			handler.exchangeInfo.conn.Close()
			handler.exchangeInfo.valid = false
		}
		log.Error("Error get consumer info ", err)
		return amqpChan, err
	}

	handler.localSessionID = amqpConn.SessionId
	log.Info("Current SessionID  ", handler.localSessionID)

	go startConsumer(&handler.consumerInfo)
	go startSender(&handler.exchangeInfo)

	return amqpChan, nil
}

//todo add return errors
func (handler *AmqpHandler) SendInitialSetup(serviceList []launcher.ServiceInfo) error {
	log.Info("SendInitialSetup ", serviceList)
	msg := vehicleStatus{Version: 1, MessageType: "vehicleStatus", SessionId: handler.localSessionID, Sevices: serviceList}
	reqJson, err := json.Marshal(msg)
	if err != nil {
		log.Warn("Error :%v", err)
		return err
	}
	sendChan <- reqJson
	return nil
}

func (handler *AmqpHandler) CloseAllConnections() {
	log.Info("CloseAllConnections")
	switch {
	case handler.exchangeInfo.valid == true:
		handler.exchangeInfo.valid = false
		fallthrough

	case handler.exchangeInfo.conn != nil:
		handler.exchangeInfo.conn.Close()
		handler.exchangeInfo.conn = nil
		fallthrough

	case handler.consumerInfo.valid == true:
		handler.consumerInfo.valid = false
		fallthrough

	case handler.consumerInfo.conn != nil:
		handler.consumerInfo.conn.Close()
		handler.consumerInfo.conn = nil

	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

//service discovery implementation
func getAmqpConnInfo(url string, request serviseDiscoveryRequest) (connection reqbbitConnectioninfo, err error) {

	var jsonResp serviseDiscoveryResp

	reqJson, err := json.Marshal(request)
	if err != nil {
		log.Warn("Error :", err)
		return connection, err
	}

	log.Info("Request :", string(reqJson))

	tlsConfig, err := fcrypt.GetTlsConfig()
	if err != nil {
		log.Warn("GetTlsConfig error : ", err)
		return connection, err
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport}

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(reqJson))

	if err != nil {
		log.Warn("Post error : ", err)
		return connection, err
	}
	defer resp.Body.Close()

	log.Info("HTTP POST Send OK: \n")

	htmlData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Error("Error Read ", err)
		return connection, err
	}
	defer resp.Body.Close()

	err = json.Unmarshal(htmlData, &jsonResp) // TODO: add check
	if err != nil {
		log.Error("Receive ", string(htmlData), err)
		return connection, err
	}
	return jsonResp.Connection, nil
}

func (handler *AmqpHandler) getSendConnectionInfo(params *sendParam) (retData amqpLocalSenderConnectionInfo, err error) {

	tlsConfig, err := fcrypt.GetTlsConfig()
	if err != nil {
		log.Warn("GetTlsConfig error : ", err)
		return retData, err
	}

	config := amqp.Config{TLSClientConfig: tlsConfig,
		SASL: nil}

	urlRabbitMQ := url.URL{Scheme: "amqps",
		User: url.UserPassword(params.User, params.Password),
		Host: params.Host,
	}
	log.Info("Connection url: ", urlRabbitMQ.String())

	conn, err := amqp.DialConfig(urlRabbitMQ.String(), config)
	if err != nil {
		log.Warning("Amqp.Dial to exchange ", err)
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
		log.Warning("Failed to declare an exchange", err)
		return retData, err
	}

	go func() {
		err := <-conn.NotifyClose(make(chan *amqp.Error))
		log.Warning("Exchange connection closing: ", err)
		if handler.exchangeInfo.valid != false {
			handler.exchangeInfo.valid = false
			amqpChan <- err
		}
	}()

	retData.conn = conn
	retData.ch = ch
	retData.valid = true

	retData.mandatory = params.Mandatory
	retData.immediate = params.Immediate
	retData.exchangeName = params.Exchange.Name

	log.Info("Create exchange OK")
	return retData, nil
}

func (handler *AmqpHandler) getConsumerConnectionInfo(param *receiveParams) (retData amqpLocalConsumerConnectionInfo, err error) {

	tlsConfig, err := fcrypt.GetTlsConfig()
	if err != nil {
		log.Warn("GetTlsConfig error : ", err)
		return retData, err
	}
	config := amqp.Config{TLSClientConfig: tlsConfig,
		SASL: nil}

	urlRabbitMQ := url.URL{Scheme: "amqps",
		User: url.UserPassword(param.User, param.Password),
		Host: param.Host,
	}
	log.Info("Connection url: ", urlRabbitMQ.String())

	conn, err := amqp.DialConfig(urlRabbitMQ.String(), config)
	if err != nil {
		log.Warning("Amqp.Dial to exchange ", err)
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
		log.Warning("Consumer connection closing: ", err)
		if handler.consumerInfo.valid != false {
			handler.consumerInfo.valid = false
			amqpChan <- err
		}
	}()

	retData.ch = msgs
	retData.conn = conn
	retData.valid = true
	return retData, nil
}

func startSender(info *amqpLocalSenderConnectionInfo) {
	log.Info("Start Sender ")
	for sendData := range sendChan {
		if info.valid != true {
			log.Error("Invalid Sender connection")
			return
		}
		if err := info.ch.Publish(
			info.exchangeName, // exchange
			"",                // routing key
			info.mandatory,    // mandatory
			info.immediate,    // immediate
			amqp.Publishing{
				ContentType:   "application/json",
				DeliveryMode:  2,
				CorrelationId: "100", //TODO: add processing CorelationID
				Body:          sendData,
			}); err != nil {
			log.Warning("Error publish", err)
			return
		}
		log.Info("SNED OK ", string(sendData))
	}
}

func startConsumer(consumerInfo *amqpLocalConsumerConnectionInfo) {
	if consumerInfo.valid != true {
		log.Error("Invalid consumer connection ")
		return
	}
	log.Info("Start listen")
	for d := range consumerInfo.ch {
		log.Info("Received a message: ", string(d.Body))
		log.Info("CorrelationId: ", d.CorrelationId)
		var ecriptList desiredStatus

		err := json.Unmarshal(d.Body, &ecriptList) // TODO: add check
		if err != nil {
			log.Error("Receive ", string(d.Body), err)
			continue
		}

		if ecriptList.MessageType != "desiredStatus" {
			log.Warning("Incorrect msg type ", ecriptList.MessageType)
			continue
		}

		var servInfoArray []ServiceInfoFromCloud
		cms_data, err := base64.StdEncoding.DecodeString(ecriptList.Sevices)
		if err != nil {
			log.Error("Can't decode base64 data from element: ", err)
			continue
		}

		decriptData, err := fcrypt.DecryptMetadata(cms_data)
		if err != nil {
			log.Error("Decryption metadata error")
			continue
		}
		log.Info("Decrypted data:", string(decriptData))

		err = json.Unmarshal(decriptData, &servInfoArray)
		if err != nil {
			log.Error("Can't make json from decrypt data", string(decriptData), err)
			continue
		}
		amqpChan <- servInfoArray
	}
	log.Warning("END listen") //TODO: add return error to channel
}
