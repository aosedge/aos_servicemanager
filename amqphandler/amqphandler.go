package amqphandler

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
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

	sendChannel chan []byte // send channel

	sendConnection    *amqp.Connection
	receiveConnection *amqp.Connection
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
	SendParams    sendParams    `json:"sendParams"`
	ReceiveParams receiveParams `json:"receiveParams"`
}

type sendParams struct {
	Host      string         `json:"host"`
	User      string         `json:"user"`
	Password  string         `json:"password"`
	Mandatory bool           `json:"mandatory"`
	Immediate bool           `json:"immediate"`
	Exchange  exchangeParams `json:"exchange"`
}

type exchangeParams struct {
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

/*******************************************************************************
 * Variables
 ******************************************************************************/

const (
	connectionRetry   = 3
	vehicleStatusStr  = "vehicleStatus"
	serviceStatusStr  = "serviceStatus"
	monitoringDataStr = "monitoringData"
)

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new amqp object
func New(sdURL string, vin string, users []string) (handler *AmqpHandler, err error) {
	handler = &AmqpHandler{}

	handler.MessageChannel = make(chan interface{}, 100)
	handler.sendChannel = make(chan []byte, 100)

	tlsConfig, err := fcrypt.GetTLSConfig()
	if err != nil {
		return nil, err
	}

	amqpConn, err := getAmqpConnInfo(sdURL, serviceDiscoveryRequest{
		Version: 1,
		VIN:     vin,
		Users:   users}, tlsConfig)
	if err != nil {
		return nil, err
	}

	if err = handler.setupSendConnection(amqpConn.SendParams, tlsConfig); err != nil {
		return nil, err
	}

	if err = handler.setupReceiveConnection(amqpConn.ReceiveParams, tlsConfig); err != nil {
		return nil, err
	}

	return handler, nil
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

// SendMonitoringData sends monitoring data
func (handler *AmqpHandler) SendMonitoringData(monitoringData MonitoringData) (err error) {
	reqJSON, err := json.Marshal(messageMonitor{
		Version:     1,
		MessageType: monitoringDataStr,
		Timestamp:   time.Now(),
		Data:        monitoringData})
	if err != nil {
		return err
	}

	log.WithField("monitoringData", string(reqJSON)).Debug("Send monitoring data")

	handler.sendChannel <- reqJSON

	return nil
}

// Close closes all amqp connection
func (handler *AmqpHandler) Close() {
	log.Info("Close AMQP")

	handler.sendConnection.Close()
	handler.receiveConnection.Close()
}

/*******************************************************************************
 * Private
 ******************************************************************************/

// service discovery implementation
func getAmqpConnInfo(url string, request serviceDiscoveryRequest, tlsConfig *tls.Config) (connection rabbitConnectioninfo, err error) {
	reqJSON, err := json.Marshal(request)
	if err != nil {
		return connection, err
	}

	log.WithField("request", string(reqJSON)).Info("AMQP service discovery request")

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

func (handler *AmqpHandler) setupSendConnection(params sendParams, tlsConfig *tls.Config) (err error) {
	urlRabbitMQ := url.URL{
		Scheme: "amqps",
		User:   url.UserPassword(params.User, params.Password),
		Host:   params.Host,
	}

	log.WithField("url", urlRabbitMQ.String()).Debug("Sender connection url")

	connection, err := amqp.DialConfig(urlRabbitMQ.String(), amqp.Config{
		TLSClientConfig: tlsConfig,
		SASL:            nil,
		Heartbeat:       10 * time.Second})
	if err != nil {
		return err
	}

	amqpChannel, err := connection.Channel()
	if err != nil {
		return err
	}

	handler.sendConnection = connection

	go handler.runSender(params, amqpChannel)

	return nil
}

func (handler *AmqpHandler) runSender(params sendParams, amqpChannel *amqp.Channel) {
	log.Info("Start AMQP sender")

	errorChannel := handler.sendConnection.NotifyClose(make(chan *amqp.Error))

	for {
		select {
		case err := <-errorChannel:
			if err != nil {
				handler.MessageChannel <- err
			}

			log.Debugf("AMQP sender closed")

			return

		case sendData := <-handler.sendChannel:
			if err := amqpChannel.Publish(
				params.Exchange.Name, // exchange
				"",                   // routing key
				params.Mandatory,     // mandatory
				params.Immediate,     // immediate
				amqp.Publishing{
					ContentType:   "application/json",
					DeliveryMode:  2,
					CorrelationId: "100", //TODO: add processing CorelationID
					UserId:        params.User,
					Body:          sendData,
				}); err != nil {
				log.Errorf("AMQP can't publish message: %", err)
			}
		}
	}
}

func (handler *AmqpHandler) setupReceiveConnection(params receiveParams, tlsConfig *tls.Config) (err error) {
	urlRabbitMQ := url.URL{
		Scheme: "amqps",
		User:   url.UserPassword(params.User, params.Password),
		Host:   params.Host,
	}

	log.WithField("url", urlRabbitMQ.String()).Debug("Consumer connection url")

	connection, err := amqp.DialConfig(urlRabbitMQ.String(), amqp.Config{
		TLSClientConfig: tlsConfig,
		SASL:            nil,
		Heartbeat:       10 * time.Second})
	if err != nil {
		return err
	}

	channel, err := connection.Channel()
	if err != nil {
		return err
	}

	deliveryChannel, err := channel.Consume(
		params.Queue.Name, // queue
		params.Consumer,   // consumer
		true,              // auto-ack param.AutoAck
		params.Exclusive,  // exclusive
		params.NoLocal,    // no-local
		params.NoWait,     // no-wait
		nil,               // args
	)
	if err != nil {
		return err
	}

	handler.receiveConnection = connection

	go handler.runReceiver(params, deliveryChannel)

	return nil
}

func (handler *AmqpHandler) runReceiver(param receiveParams, delivaryChannel <-chan amqp.Delivery) {
	log.Info("Start AMQP consumer")

	for d := range delivaryChannel {
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

	log.Debugf("AMQP receiver closed")
}
