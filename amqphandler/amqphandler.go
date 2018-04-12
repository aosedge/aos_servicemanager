package amqphandler

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/streadway/amqp"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/fcrypt"
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

type ServiceInfo struct {
	Id      string `json:id`
	Version uint   `json:version`
	Status  string `json:status`
}

///API structures
type serviseDiscoveryRequest struct {
	Version int      `json:"version"`
	VIN     string   `json:"VIN"`
	Users   []string `json:"users"`
}

type vehicleStatus struct {
	Version     uint          `json:"version"`
	MessageType string        `json:"messageType"`
	SessionId   string        `json:"sessionId"`
	Sevices     []ServiceInfo `json:"services"`
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

const (
	CONNECTION_RETRY = 3
)

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new amqp object
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
	log.Debug("Results: ", amqpConn)

	handler.localSessionID = amqpConn.SessionId
	log.Info("Current SessionID  ", handler.localSessionID)

	tlsConfig, err := fcrypt.GetTlsConfig()
	if err != nil {
		log.Error("GetTlsConfig error : ", err)
		return amqpChan, err
	}

	go handler.startSendConnection(&amqpConn.SendParam, tlsConfig)
	go handler.startConsumerConnection(&amqpConn.ReceiveParams, tlsConfig)

	return amqpChan, nil
}

//todo add return errors
func (handler *AmqpHandler) SendInitialSetup(serviceList []ServiceInfo) error {
	log.Info("SendInitialSetup ", serviceList)
	msg := vehicleStatus{Version: 1, MessageType: "vehicleStatus", SessionId: handler.localSessionID, Sevices: serviceList}
	reqJson, err := json.Marshal(msg)
	if err != nil {
		log.Warn("Error marshall json: ", err)
		return err
	}
	sendChan <- reqJson
	return nil
}

func (handler *AmqpHandler) CloseAllConnections() {
	log.Info("CloseAllConnections")
	handler.exchangeInfo.valid = false
	handler.consumerInfo.valid = false

	if handler.exchangeInfo.conn != nil {
		log.Debug("Close exchange connection")
		handler.exchangeInfo.conn.Close()
		handler.exchangeInfo.conn = nil
	}

	if handler.consumerInfo.conn != nil {
		log.Debug("Close consumer connection")
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

func (handler *AmqpHandler) startSendConnection(params *sendParam, tlsConfig *tls.Config) {

	config := amqp.Config{TLSClientConfig: tlsConfig,
		SASL:      nil,
		Heartbeat: 10 * time.Second}

	urlRabbitMQ := url.URL{Scheme: "amqps",
		User: url.UserPassword(params.User, params.Password),
		Host: params.Host,
	}
	log.Info("Connection url: ", urlRabbitMQ.String())

	for i := 0; i < CONNECTION_RETRY; i++ {
		conn, err := amqp.DialConfig(urlRabbitMQ.String(), config)
		if err != nil {
			log.Warning("Amqp.Dial to exchange #", i, err)
			continue
		}

		ch, err := conn.Channel()
		if err != nil {
			log.Warning("Failed to open a send channel #", i, err)
			continue
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
			log.Warning("Failed to declare an exchange #", i, err)
			continue
		}

		handler.exchangeInfo.conn = conn
		handler.exchangeInfo.ch = ch
		handler.exchangeInfo.valid = true
		handler.exchangeInfo.mandatory = params.Mandatory
		handler.exchangeInfo.immediate = params.Immediate
		handler.exchangeInfo.exchangeName = params.Exchange.Name

		log.Info("Create exchange OK")

		startSender(&handler.exchangeInfo)
		log.Warning("Stop sender #", i)

		if handler.exchangeInfo.valid == false {
			break
		}
	}
	log.Warning("Connection close to Exchange")
	if handler.exchangeInfo.valid == true {
		handler.CloseAllConnections()
		log.Error("Generate sender error Connection close to Exchange")
		amqpChan <- errors.New("Connection close to Exchange")
	}
}

func startSender(info *amqpLocalSenderConnectionInfo) {
	log.Info("Start Sender ")
	errch := info.conn.NotifyClose(make(chan *amqp.Error))
	for {
		select {
		case err := <-errch:
			log.Warning("Exchange connection closing: ", err)
			return
		case sendData := <-sendChan:
			if info.valid != true {
				log.Warning("Invalid Sender connection")
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
			log.Info("Send OK ", string(sendData))
		}
	}
}

func (handler *AmqpHandler) startConsumerConnection(param *receiveParams, tlsConfig *tls.Config) {

	config := amqp.Config{TLSClientConfig: tlsConfig,
		SASL:      nil,
		Heartbeat: 10 * time.Second}
	urlRabbitMQ := url.URL{Scheme: "amqps",
		User: url.UserPassword(param.User, param.Password),
		Host: param.Host,
	}
	log.Info("Connection url: ", urlRabbitMQ.String())

	for i := 0; i < CONNECTION_RETRY; i++ {
		conn, err := amqp.DialConfig(urlRabbitMQ.String(), config)
		if err != nil {
			log.Warning("Fail Amqp.Dial to Consumer #", i, err)
			continue
		}

		ch, err := conn.Channel()
		if err != nil {
			log.Warning("Failed to open Consumer channel #", i, err)
			continue
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
			log.Warning("Failed to register a consumer #", i, err)
			continue
		}

		handler.consumerInfo.ch = msgs
		handler.consumerInfo.conn = conn
		handler.consumerInfo.valid = true

		startConsumer(&handler.consumerInfo)
		log.Warning("Stop Consumer connection #", i)
		if handler.consumerInfo.valid == false {
			break
		}
	}
	log.Warning("Connection close to Consumer")
	if handler.consumerInfo.valid == true {
		handler.CloseAllConnections()
		log.Error("Generate Error Connection close to consumer")
		amqpChan <- errors.New("Connection close to consumer")
	}
}

func startConsumer(consumerInfo *amqpLocalConsumerConnectionInfo) {
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
