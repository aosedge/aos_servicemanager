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

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/fcrypt"
)

//TODO: list
// - close/erase channel
// - correlation ID

/*******************************************************************************
 * Types
 ******************************************************************************/

// AmqpHandler structure with all amqp connection info
type AmqpHandler struct {
	// MessageChannel channel for amqp messages
	MessageChannel chan interface{}

	sendChannel  chan []byte                     // send channel
	exchangeInfo amqpLocalSenderConnectionInfo   // connection for sending data
	consumerInfo amqpLocalConsumerConnectionInfo // connection for receiving data
}

// ServiceInfoFromCloud structure with Encripted Service information
type ServiceInfoFromCloud struct {
	ID                     string             `json:"id"`
	Version                uint               `json:"version"`
	UpdateType             string             `json:"updateType"`
	DownloadURL            string             `json:"downloadUrl"`
	URLExpiration          string             `json:"urlExpiration"`
	SignatureAlgorithm     string             `json:"signatureAlgorithm"`
	SignatureAlgorithmHash string             `json:"signatureAlgorithmHash"`
	SignatureScheme        string             `json:"signatureScheme"`
	ImageSignature         string             `json:"imageSignature"`
	CertificateChain       string             `json:"certificateChain"`
	EncryptionKey          string             `json:"encryptionKey"`
	EncryptionAlgorithm    string             `json:"encryptionAlgorithm"`
	EncryptionMode         string             `json:"encryptionMode"`
	EncryptionModeParams   string             `json:"encryptionModeParams"`
	ServiceMonitoring      *ServiceAlertRules `json:"serviceMonitoring,omitempty"`
}

// ServiceAlertRules define service monitoring alerts rules
type ServiceAlertRules struct {
	RAM        *config.AlertRule `json:"ram,omitempty"`
	CPU        *config.AlertRule `json:"cpu,omitempty"`
	UsedDisk   *config.AlertRule `json:"usedDisk,omitempty"`
	OutTraffic *config.AlertRule `json:"outTraffic,omitempty"`
}

// AlertData alert element
type AlertData struct {
	Value     uint64    `json:"value"`
	Timestamp time.Time `json:"timestamp"`
}

// MonitoringAlerts arras with alerts
type MonitoringAlerts struct {
	RAM        []AlertData `json:"ram"`
	CPU        []AlertData `json:"cpu"`
	UsedDisk   []AlertData `json:"usedDisk"`
	InTraffic  []AlertData `json:"inTraffic"`
	OutTraffic []AlertData `json:"outTraffic"`
}

// ServiceMonitoringData monitoring data for service
type ServiceMonitoringData struct {
	ServiceID  string           `json:"serviceId"`
	RAM        uint64           `json:"ram"`
	CPU        uint64           `json:"cpu"`
	UsedDisk   uint64           `json:"usedDisk"`
	InTraffic  uint64           `json:"inTraffic"`
	OutTraffic uint64           `json:"outTraffic"`
	Alerts     MonitoringAlerts `json:"alerts"`
}

// MonitoringData define monitoring data structure
type MonitoringData struct {
	Global struct {
		RAM        uint64           `json:"ram"`
		CPU        uint64           `json:"cpu"`
		UsedDisk   uint64           `json:"usedDisk"`
		InTraffic  uint64           `json:"inTraffic"`
		OutTraffic uint64           `json:"outTraffic"`
		Alerts     MonitoringAlerts `json:"alerts"`
	} `json:"global"`
	ServicesData []ServiceMonitoringData `json:"servicesData"`
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

type serviceDiscoveryRequest struct {
	Version int      `json:"version"`
	VIN     string   `json:"VIN"`
	Users   []string `json:"users"`
}

// messageMonitor structure which define AOS monitoring message
type messageMonitor struct {
	Version     int            `json:"version"`
	MessageType string         `json:"messageType"` //monitoringData
	Timestamp   time.Time      `json:"timestamp"`
	Data        MonitoringData `json:"data"`
}

type vehicleStatus struct {
	Version     uint          `json:"version"`
	MessageType string        `json:"messageType"`
	Services    []ServiceInfo `json:"services"`
}

type desiredStatus struct {
	Version     uint   `json:"version"`
	MessageType string `json:"messageType"`
	Services    string `json:"services"`
}

type serviceDiscoveryResp struct {
	Version    uint                 `json:"version"`
	Connection rabbitConnectioninfo `json:"connection"`
}

type rabbitConnectioninfo struct {
	SendParam     sendParam     `json:"sendParams"`
	ReceiveParams receiveParams `json:"receiveParams"`
}

type sendParam struct {
	Host      string        `json:"host"`
	User      string        `json:"user"`
	Password  string        `json:"password"`
	Mandatory bool          `json:"mandatory"`
	Immediate bool          `json:"immediate"`
	Exchange  exchangeParam `json:"exchange"`
}

type exchangeParam struct {
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

const (
	connectionRetry  = 3
	vehicleStatusStr = "vehicleStatus"
	serviceStatusStr = "serviceStatus"
)

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new amqp object
func New() (handler *AmqpHandler, err error) {
	handler = &AmqpHandler{}

	handler.MessageChannel = make(chan interface{}, 100)
	handler.sendChannel = make(chan []byte, 100)

	return handler, nil
}

// InitAmqphandler initialization of rabbit amqp handler
func (handler *AmqpHandler) InitAmqphandler(sdURL string, vin string, users []string) (err error) {
	log.Debug("Init AMQP")

	amqpConn, err := getAmqpConnInfo(sdURL, serviceDiscoveryRequest{
		Version: 1,
		VIN:     vin,
		Users:   users})
	if err != nil {
		return err
	}

	tlsConfig, err := fcrypt.GetTLSConfig()
	if err != nil {
		return err
	}

	go handler.startSendConnection(&amqpConn.SendParam, tlsConfig)
	go handler.startConsumerConnection(&amqpConn.ReceiveParams, tlsConfig)

	return nil
}

// SendInitialSetup sends initial list oaf available services
func (handler *AmqpHandler) SendInitialSetup(serviceList []ServiceInfo) (err error) {
	log.WithField("services", serviceList).Debug("Send initial setup")

	reqJSON, err := json.Marshal(vehicleStatus{
		Version:     1,
		MessageType: vehicleStatusStr,
		Services:    serviceList})
	if err != nil {
		return err
	}

	handler.sendChannel <- reqJSON

	return nil
}

// SendServiceStatusMsg sends message with service status
func (handler *AmqpHandler) SendServiceStatusMsg(serviceStatus ServiceInfo) (err error) {
	log.WithField("status", serviceStatus).Debug("Send service status")

	reqJSON, err := json.Marshal(vehicleStatus{
		Version:     1,
		MessageType: serviceStatusStr,
		Services:    []ServiceInfo{serviceStatus}})
	if err != nil {
		return err
	}

	handler.sendChannel <- reqJSON

	return nil
}

// CloseAllConnections closes all amqp connection
func (handler *AmqpHandler) CloseAllConnections() {
	log.Info("AMQP: close all connections")

	handler.exchangeInfo.valid = false
	handler.consumerInfo.valid = false

	if handler.exchangeInfo.conn != nil {
		log.Debug("AMQP: close exchange connection")
		handler.exchangeInfo.conn.Close()
		handler.exchangeInfo.conn = nil
	}

	if handler.consumerInfo.conn != nil {
		log.Debug("AMQP: close consumer connection")
		handler.consumerInfo.conn.Close()
		handler.consumerInfo.conn = nil
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

// service discovery implementation
func getAmqpConnInfo(url string, request serviceDiscoveryRequest) (connection rabbitConnectioninfo, err error) {
	reqJSON, err := json.Marshal(request)
	if err != nil {
		return connection, err
	}

	log.WithField("request", string(reqJSON)).Info("AMQP service discovery request")

	tlsConfig, err := fcrypt.GetTLSConfig()
	if err != nil {
		return connection, err
	}

	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport}

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(reqJSON))
	if err != nil {
		return connection, err
	}
	defer resp.Body.Close()

	htmlData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return connection, err
	}
	defer resp.Body.Close()

	var jsonResp serviceDiscoveryResp

	err = json.Unmarshal(htmlData, &jsonResp) // TODO: add check
	if err != nil {
		return connection, err
	}

	return jsonResp.Connection, nil
}

func (handler *AmqpHandler) startSendConnection(params *sendParam, tlsConfig *tls.Config) {
	urlRabbitMQ := url.URL{
		Scheme: "amqps",
		User:   url.UserPassword(params.User, params.Password),
		Host:   params.Host,
	}

	log.WithField("url", urlRabbitMQ.String()).Debug("Sender connection url")

	for i := 0; i < connectionRetry; i++ {
		conn, err := amqp.DialConfig(urlRabbitMQ.String(), amqp.Config{
			TLSClientConfig: tlsConfig,
			SASL:            nil,
			Heartbeat:       10 * time.Second})
		if err != nil {
			log.WithField("retry", i).Errorf("AMQP sender dial error: %s", err)
			continue
		}

		ch, err := conn.Channel()
		if err != nil {
			log.WithField("retry", i).Errorf("Failed to open sender channel: %s", err)
			continue
		}

		handler.exchangeInfo = amqpLocalSenderConnectionInfo{
			conn:         conn,
			ch:           ch,
			valid:        true,
			mandatory:    params.Mandatory,
			immediate:    params.Immediate,
			exchangeName: params.Exchange.Name,
			userID:       params.User}

		if err := handler.startSender(&handler.exchangeInfo); err != nil {
			log.Errorf("AMQP sender error: %s", err)
		}

		if handler.exchangeInfo.valid == false {
			break
		}
	}

	if handler.exchangeInfo.valid == true {
		log.Debug("Generate sender error connection close to Exchange")

		handler.CloseAllConnections()
		handler.MessageChannel <- errors.New("Connection close to Exchange")
	}
}

func (handler *AmqpHandler) startSender(info *amqpLocalSenderConnectionInfo) (err error) {
	log.Info("Start AMQP sender")

	errch := info.conn.NotifyClose(make(chan *amqp.Error))

	for {
		select {
		case amqpErr := <-errch:
			if amqpErr != nil {
				return errors.New(amqpErr.Error())
			}

		case sendData := <-handler.sendChannel:
			if info.valid != true {
				return errors.New("Invalid Sender connection")
			}

			if err = info.ch.Publish(
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
				return err
			}
		}
	}
}

func (handler *AmqpHandler) startConsumerConnection(param *receiveParams, tlsConfig *tls.Config) {
	urlRabbitMQ := url.URL{
		Scheme: "amqps",
		User:   url.UserPassword(param.User, param.Password),
		Host:   param.Host,
	}

	log.WithField("url", urlRabbitMQ.String()).Debug("Consumer connection url")

	for i := 0; i < connectionRetry; i++ {
		conn, err := amqp.DialConfig(urlRabbitMQ.String(), amqp.Config{
			TLSClientConfig: tlsConfig,
			SASL:            nil,
			Heartbeat:       10 * time.Second})
		if err != nil {
			log.WithField("retry", i).Errorf("AMQP consumer dial error: %s", err)
			continue
		}

		ch, err := conn.Channel()
		if err != nil {
			log.WithField("retry", i).Errorf("Failed to open sender channel: %s", err)
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
			log.WithField("retry", i).Errorf("Failed to register consumer: %s", err)
			continue
		}

		handler.consumerInfo = amqpLocalConsumerConnectionInfo{
			ch:    msgs,
			conn:  conn,
			valid: true}

		handler.startConsumer(&handler.consumerInfo)

		if handler.consumerInfo.valid == false {
			break
		}
	}

	if handler.consumerInfo.valid == true {
		log.Debug("Generate Error connection close to consumer")

		handler.CloseAllConnections()
		handler.MessageChannel <- errors.New("Connection close to consumer")
	}
}

func (handler *AmqpHandler) startConsumer(consumerInfo *amqpLocalConsumerConnectionInfo) {
	log.Info("Start AMQP consumer")

	for d := range consumerInfo.ch {
		log.WithFields(log.Fields{
			"message":      string(d.Body),
			"corrlationId": d.CorrelationId}).Debug("AMQP received message")

		header := struct {
			MessageType string `json:"messageType"`
		}{}

		if err := json.Unmarshal(d.Body, &header); err != nil {
			log.Errorf("AMQP consumer error: %s", err)
			continue
		}

		if header.MessageType != "desiredStatus" {
			log.Warnf("AMQP unsupported message type: %s", header.MessageType)
			continue
		}

		var encryptList desiredStatus

		if err := json.Unmarshal(d.Body, &encryptList); err != nil { // TODO: add check
			log.Errorf("AMQP consumer error: %s", err)
			continue
		}

		var servInfoArray []ServiceInfoFromCloud

		cmsData, err := base64.StdEncoding.DecodeString(encryptList.Services)
		if err != nil {
			log.Errorf("Can't decode base64 data from element: %s", err)
			continue
		}

		decryptData, err := fcrypt.DecryptMetadata(cmsData)
		if err != nil {
			log.Errorf("Decryption metadata error: %s", err)
			continue
		}

		log.WithField("data", string(decryptData)).Debug("Decrypted data")

		if err := json.Unmarshal(decryptData, &servInfoArray); err != nil {
			log.Errorf("Can't make json from decrypt data: %s", err)
			continue
		}

		handler.MessageChannel <- servInfoArray
	}

	log.Debugf("AMQP consumer closed")
}
