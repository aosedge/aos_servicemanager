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

/*******************************************************************************
 * Types
 ******************************************************************************/

// AmqpHandler structure with all amqp connection info
type AmqpHandler struct {
	// MessageChannel channel for amqp messages
	MessageChannel chan Message

	sendChannel  chan Message
	retryChannel chan Message

	sendConnection    *amqp.Connection
	receiveConnection *amqp.Connection
}

// ServiceInfoFromCloud structure with Encripted Service information
type ServiceInfoFromCloud struct {
	ID                     string             `json:"id"`
	Version                uint64             `json:"version"`
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
	InTraffic  *config.AlertRule `json:"inTraffic,omitempty"`
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
	Version uint64        `json:"version"`
	Status  string        `json:"status"`
	Error   *ServiceError `json:"error"`
}

// ServiceError error structure
type ServiceError struct {
	ID      int    `json:"id"`
	Message string `json:"message"`
}

// StateAcceptance state acceptance message
type StateAcceptance struct {
	ServiceID string `json:"serviceId"`
	Checksum  string `json:"checksum"`
	Result    string `json:"result"`
	Reason    string `json:"reason"`
}

// UpdateState state update message
type UpdateState struct {
	ServiceID string `json:"serviceId"`
	Checksum  string `json:"stateChecksum"`
	State     string `json:"state"`
}

// Message structure used to send/receive data by amqp
type Message struct {
	CorrelationID string
	Data          interface{}
}

type serviceDiscoveryRequest struct {
	Version uint64   `json:"version"`
	VIN     string   `json:"VIN"`
	Users   []string `json:"users"`
}

// messageMonitor structure which define AOS monitoring message
type messageMonitor struct {
	Version     uint64         `json:"version"`
	MessageType string         `json:"messageType"`
	Timestamp   time.Time      `json:"timestamp"`
	Data        MonitoringData `json:"data"`
}

type vehicleStatus struct {
	Version     uint64        `json:"version"`
	MessageType string        `json:"messageType"`
	Services    []ServiceInfo `json:"services"`
}

type desiredStatus struct {
	Version     uint64 `json:"version"`
	MessageType string `json:"messageType"`
	Services    string `json:"services"`
}

type newState struct {
	Version     uint64 `json:"version"`
	MessageType string `json:"messageType"`
	ServiceID   string `json:"serviceId"`
	Checksum    string `json:"stateChecksum"`
	State       string `json:"state"`
}

type stateRequest struct {
	Version     uint64 `json:"version"`
	MessageType string `json:"messageType"`
	ServiceID   string `json:"serviceId"`
	Default     bool   `json:"default"`
}

type serviceDiscoveryResp struct {
	Version    uint64               `json:"version"`
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

type stateAcceptance struct {
	Version     uint64 `json:"version"`
	MessageType string `json:"messageType"`
	StateAcceptance
}

type updateState struct {
	Version     uint64 `json:"version"`
	MessageType string `json:"messageType"`
	UpdateState
}

/*******************************************************************************
 * Variables
 ******************************************************************************/

const (
	sendChannelSize    = 32
	receiveChannelSize = 16
	retryChannelSize   = 8

	connectionRetry = 3

	vehicleStatusStr  = "vehicleStatus"
	serviceStatusStr  = "serviceStatus"
	monitoringDataStr = "monitoringData"
	newStateStr       = "newState"
	stateRequestStr   = "stateRequest"
)

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new amqp object
func New() (handler *AmqpHandler, err error) {
	log.Debug("New AMQP")

	handler = &AmqpHandler{}

	handler.MessageChannel = make(chan Message, receiveChannelSize)
	handler.sendChannel = make(chan Message, sendChannelSize)
	handler.retryChannel = make(chan Message, retryChannelSize)

	return handler, nil
}

// Connect connects to cloud
func (handler *AmqpHandler) Connect(sdURL string, vin string, users []string) (err error) {
	log.WithFields(log.Fields{"url": sdURL, "vin": vin, "users": users}).Debug("AMQP connect")

	tlsConfig, err := fcrypt.GetTLSConfig()
	if err != nil {
		return err
	}

	var connectionInfo rabbitConnectioninfo

	if err := retryHelper(func() (err error) {
		connectionInfo, err = getConnectionInfo(sdURL, serviceDiscoveryRequest{
			Version: 1,
			VIN:     vin,
			Users:   users}, tlsConfig)

		return err
	}); err != nil {
		return err
	}

	if err := retryHelper(func() (err error) {
		return handler.setupSendConnection(connectionInfo.SendParams, tlsConfig)
	}); err != nil {
		return err
	}

	if err := retryHelper(func() (err error) {
		return handler.setupReceiveConnection(connectionInfo.ReceiveParams, tlsConfig)
	}); err != nil {
		return err
	}

	return nil
}

// Disconnect disconnects from cloud
func (handler *AmqpHandler) Disconnect() (err error) {
	log.Debug("AMQP disconnect")

	if handler.sendConnection != nil {
		handler.sendConnection.Close()
	}

	if handler.receiveConnection != nil {
		handler.receiveConnection.Close()
	}

	return nil
}

// SendInitialSetup sends initial list oaf available services
func (handler *AmqpHandler) SendInitialSetup(serviceList []ServiceInfo) (err error) {
	handler.sendChannel <- Message{"", vehicleStatus{
		Version:     1,
		MessageType: vehicleStatusStr,
		Services:    serviceList}}

	return nil
}

// SendServiceStatusMsg sends message with service status
func (handler *AmqpHandler) SendServiceStatusMsg(serviceStatus ServiceInfo) (err error) {
	handler.sendChannel <- Message{"", vehicleStatus{
		Version:     1,
		MessageType: serviceStatusStr,
		Services:    []ServiceInfo{serviceStatus}}}

	return nil
}

// SendMonitoringData sends monitoring data
func (handler *AmqpHandler) SendMonitoringData(monitoringData MonitoringData) (err error) {
	handler.sendChannel <- Message{"", messageMonitor{
		Version:     1,
		MessageType: monitoringDataStr,
		Timestamp:   time.Now(),
		Data:        monitoringData}}

	return nil
}

// SendNewState sends new state message
func (handler *AmqpHandler) SendNewState(serviceID, state, checksum, correlationID string) (err error) {
	handler.sendChannel <- Message{correlationID, newState{
		Version:     1,
		MessageType: newStateStr,
		ServiceID:   serviceID,
		State:       state,
		Checksum:    checksum}}

	return nil
}

// SendStateRequest sends state request message
func (handler *AmqpHandler) SendStateRequest(serviceID string, defaultState bool) (err error) {
	handler.sendChannel <- Message{"", stateRequest{
		Version:     1,
		MessageType: stateRequestStr,
		ServiceID:   serviceID,
		Default:     defaultState}}

	return nil
}

// Close closes all amqp connection
func (handler *AmqpHandler) Close() {
	log.Info("Close AMQP")

	handler.Disconnect()
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func retryHelper(f func() error) (err error) {
	for try := 1; try <= connectionRetry; try++ {
		if err = f(); err == nil {
			return nil
		}

		if try < connectionRetry {
			log.Errorf("%s. Retry...", err)
		} else {
			log.Errorf("%s. Retry limit reached", err)
		}
	}

	return err
}

// service discovery implementation
func getConnectionInfo(url string, request serviceDiscoveryRequest, tlsConfig *tls.Config) (info rabbitConnectioninfo, err error) {
	reqJSON, err := json.Marshal(request)
	if err != nil {
		return info, err
	}

	log.WithField("request", string(reqJSON)).Info("AMQP service discovery request")

	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport}

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(reqJSON))
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()

	htmlData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()

	var jsonResp serviceDiscoveryResp

	err = json.Unmarshal(htmlData, &jsonResp) // TODO: add check
	if err != nil {
		return info, err
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

	if err = amqpChannel.Confirm(false); err != nil {
		return err
	}

	go handler.runSender(params, amqpChannel)

	return nil
}

func (handler *AmqpHandler) runSender(params sendParams, amqpChannel *amqp.Channel) {
	log.Info("Start AMQP sender")
	defer log.Debug("AMQP sender closed")

	errorChannel := handler.sendConnection.NotifyClose(make(chan *amqp.Error, 1))
	confirmChannel := amqpChannel.NotifyPublish(make(chan amqp.Confirmation, 1))

	for {
		var message Message

		select {
		case err := <-errorChannel:
			if err != nil {
				handler.MessageChannel <- Message{"", err}
			}

			return

		case message = <-handler.retryChannel:

		case message = <-handler.sendChannel:
		}

		if message.Data != nil {
			data, err := json.Marshal(message.Data)
			if err != nil {
				log.Errorf("Can't parse message: %s", err)
				continue
			}

			log.WithFields(log.Fields{
				"correlationID": message.CorrelationID,
				"data":          string(data)}).Debug("AMQP send message")

			if err := amqpChannel.Publish(
				params.Exchange.Name, // exchange
				"",                   // routing key
				params.Mandatory,     // mandatory
				params.Immediate,     // immediate
				amqp.Publishing{
					ContentType:   "application/json",
					DeliveryMode:  amqp.Persistent,
					CorrelationId: message.CorrelationID,
					UserId:        params.User,
					Body:          data,
				}); err != nil {
				handler.MessageChannel <- Message{"", err}
			}

			// Handle retry packets
			confirm, ok := <-confirmChannel
			if !ok || !confirm.Ack {
				log.WithFields(log.Fields{
					"correlationID": message.CorrelationID,
					"data":          string(data)}).Warning("AMQP data is not sent. Put into retry queue")

				handler.retryChannel <- message
			}

			if !ok {
				return
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

	amqpChannel, err := connection.Channel()
	if err != nil {
		return err
	}

	deliveryChannel, err := amqpChannel.Consume(
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

func (handler *AmqpHandler) runReceiver(param receiveParams, deliveryChannel <-chan amqp.Delivery) {
	log.Info("Start AMQP receiver")
	defer log.Info("AMQP receiver closed")

	errorChannel := handler.receiveConnection.NotifyClose(make(chan *amqp.Error, 1))

	for {
		select {
		case err := <-errorChannel:
			if err != nil {
				handler.MessageChannel <- Message{"", err}
			}

			return

		case data, ok := <-deliveryChannel:
			if !ok {
				return
			}

			log.WithFields(log.Fields{
				"message":      string(data.Body),
				"corrlationId": data.CorrelationId}).Debug("AMQP received message")

			header := struct {
				MessageType string `json:"messageType"`
			}{}

			if err := json.Unmarshal(data.Body, &header); err != nil {
				log.Errorf("AMQP consumer error: %s", err)
				break
			}

			switch header.MessageType {
			case "desiredStatus":
				var status desiredStatus

				if err := json.Unmarshal(data.Body, &status); err != nil {
					log.Errorf("Can't parse message body: %s", err)
					continue
				}

				services, err := decodeServices(status.Services)
				if err != nil {
					log.Errorf("Can't decode services: %s", err)
					continue
				}

				handler.MessageChannel <- Message{data.CorrelationId, services}

			case "stateAcceptance":
				var acceptance stateAcceptance

				if err := json.Unmarshal(data.Body, &acceptance); err != nil {
					log.Errorf("Can't parse message body: %s", err)
					continue
				}

				handler.MessageChannel <- Message{data.CorrelationId, acceptance.StateAcceptance}

			case "updateState":
				var update updateState

				if err := json.Unmarshal(data.Body, &update); err != nil {
					log.Errorf("Can't parse message body: %s", err)
					continue
				}

				handler.MessageChannel <- Message{data.CorrelationId, update.UpdateState}

			default:
				log.Warnf("AMQP unsupported message type: %s", header.MessageType)
				continue
			}
		}
	}
}

func decodeServices(data string) (services []ServiceInfoFromCloud, err error) {
	cmsData, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}

	decryptData, err := fcrypt.DecryptMetadata(cmsData)
	if err != nil {
		return nil, err
	}

	log.WithField("data", string(decryptData)).Debug("Decrypted data")

	if err = json.Unmarshal(decryptData, &services); err != nil {
		return nil, err
	}

	return services, nil
}
