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

/*******************************************************************************
 * Types
 ******************************************************************************/

// AmqpHandler structur with all amqp connection infor
type AmqpHandler struct {
	exchangeInfo amqpLocalSenderConnectionInfo   // connection for sending data
	consumerInfo amqpLocalConsumerConnectionInfo // connection for receiving data
}

// ServiceInfoFromCloud structure with Encripted Service information
type ServiceInfoFromCloud struct {
	ID                     string `json:"id"`
	Version                uint   `json:"version"`
	UpdateType             string `json:"updateType"`
	DownloadURL            string `json:"downloadUrl"`
	URLExpiration          string `json:"urlExpiration"`
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

// ServiceInfo struct with service information
type ServiceInfo struct {
	ID      string        `json:"id"`
	Version uint          `json:"version"`
	Status  string        `json:"status"`
	Error   *ServiceError `json:"error"`
}

// ServiceError error structure
type ServiceError struct {
	ID      int    `json:"id"`
	Message string `json:"message"`
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
	Sevices     []ServiceInfo `json:"services"`
}

type desiredStatus struct {
	Version     uint   `json:"version"`
	MessageType string `json:"messageType"`
	Sevices     string `json:"services"`
}
type serviseDiscoveryResp struct {
	Version    uint                  `json:"version"`
	Connection reqbbitConnectioninfo `json:"connection"`
}
type reqbbitConnectioninfo struct {
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
	userID       string
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
	connectionRety   = 3
	vehicleStatusStr = "vehicleStatus"
	servoceSatusStr  = "serviceStatus"
)

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new amqp object
func New() (handler *AmqpHandler, err error) {
	handler = &AmqpHandler{}
	return handler, nil
}

// InitAmqphandler initialisation of rabbit amqp handler
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

	tlsConfig, err := fcrypt.GetTLSConfig()
	if err != nil {
		log.Error("GetTlsConfig error : ", err)
		return amqpChan, err
	}

	go handler.startSendConnection(&amqpConn.SendParam, tlsConfig)
	go handler.startConsumerConnection(&amqpConn.ReceiveParams, tlsConfig)

	return amqpChan, nil
}

// SendInitialSetup send initila list oaf available servises
func (handler *AmqpHandler) SendInitialSetup(serviceList []ServiceInfo) error {
	log.Info("SendInitialSetup ", serviceList)
	msg := vehicleStatus{Version: 1, MessageType: vehicleStatusStr, Sevices: serviceList}
	reqJSON, err := json.Marshal(msg)
	if err != nil {
		log.Warn("Error marshall json: ", err)
		return err
	}
	sendChan <- reqJSON
	return nil
}

// SendServiceStatusMsg send message with service status
func (handler *AmqpHandler) SendServiceStatusMsg(serviceStatus ServiceInfo) error {
	log.Info("SendServiceStatusMsg ", serviceStatus)
	var list []ServiceInfo
	list = append(list, serviceStatus)
	msg := vehicleStatus{Version: 1, MessageType: servoceSatusStr, Sevices: list}
	reqJSON, err := json.Marshal(msg)
	if err != nil {
		log.Warn("Error marshall json: ", err)
		return err
	}
	sendChan <- reqJSON
	return nil
}

// CloseAllConnections cloase all amqp connection
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

// service discovery implementation
func getAmqpConnInfo(url string, request serviseDiscoveryRequest) (connection reqbbitConnectioninfo, err error) {

	var jsonResp serviseDiscoveryResp

	reqJSON, err := json.Marshal(request)
	if err != nil {
		log.Warn("Error :", err)
		return connection, err
	}

	log.Info("Request :", string(reqJSON))

	tlsConfig, err := fcrypt.GetTLSConfig()
	if err != nil {
		log.Warn("GetTlsConfig error : ", err)
		return connection, err
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport}

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(reqJSON))

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
	log.Info("Sender connection url: ", urlRabbitMQ.String())

	for i := 0; i < connectionRety; i++ {
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

		handler.exchangeInfo.conn = conn
		handler.exchangeInfo.ch = ch
		handler.exchangeInfo.valid = true
		handler.exchangeInfo.mandatory = params.Mandatory
		handler.exchangeInfo.immediate = params.Immediate
		handler.exchangeInfo.exchangeName = params.Exchange.Name
		handler.exchangeInfo.userID = params.User

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
					UserId:        info.userID,
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
	log.Info("Consumer connection url: ", urlRabbitMQ.String())

	for i := 0; i < connectionRety; i++ {
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
		cmsData, err := base64.StdEncoding.DecodeString(ecriptList.Sevices)
		if err != nil {
			log.Error("Can't decode base64 data from element: ", err)
			continue
		}

		decriptData, err := fcrypt.DecryptMetadata(cmsData)
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
