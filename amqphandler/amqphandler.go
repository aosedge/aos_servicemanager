package amqphandler

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
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
 * Consts
 ******************************************************************************/

const (
	sendChannelSize    = 32
	receiveChannelSize = 16
	retryChannelSize   = 8

	connectionRetry = 3
)

const (
	desiredStatusStr          = "desiredStatus"
	stateAcceptanceStr        = "stateAcceptance"
	updateStateStr            = "updateState"
	requestServiceLogStr      = "requestServiceLog"
	requestServiceCrashLogStr = "requestServiceCrashLog"

	vehicleStatusStr  = "vehicleStatus"
	serviceStatusStr  = "serviceStatus"
	monitoringDataStr = "monitoringData"
	newStateStr       = "newState"
	stateRequestStr   = "stateRequest"
	pushServiceLogStr = "pushServiceLog"
)

// Alert tags
const (
	AlertSystemError = "systemError"
	AlertResource    = "resourceAlert"
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

// ServiceMonitoringData monitoring data for service
type ServiceMonitoringData struct {
	ServiceID  string `json:"serviceId"`
	RAM        uint64 `json:"ram"`
	CPU        uint64 `json:"cpu"`
	UsedDisk   uint64 `json:"usedDisk"`
	InTraffic  uint64 `json:"inTraffic"`
	OutTraffic uint64 `json:"outTraffic"`
}

// MonitoringData define monitoring data structure
type MonitoringData struct {
	Timestamp time.Time `json:"timestamp"`
	Data      struct {
		Global struct {
			RAM        uint64 `json:"ram"`
			CPU        uint64 `json:"cpu"`
			UsedDisk   uint64 `json:"usedDisk"`
			InTraffic  uint64 `json:"inTraffic"`
			OutTraffic uint64 `json:"outTraffic"`
		} `json:"global"`
		ServicesData []ServiceMonitoringData `json:"servicesData"`
	} `json:"data"`
}

// ServiceInfo struct with service information
type ServiceInfo struct {
	ID            string `json:"id"`
	Version       uint64 `json:"version"`
	Status        string `json:"status"`
	Error         string `json:"error"`
	StateChecksum string `json:"stateChecksum"`
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

// RequestServiceLog request service log message
type RequestServiceLog struct {
	ServiceID string     `json:"serviceId"`
	LogID     string     `json:"logID"`
	From      *time.Time `json:"from"`
	Till      *time.Time `json:"till"`
}

// RequestServiceCrashLog request service crash log message
type RequestServiceCrashLog struct {
	ServiceID string `json:"serviceId"`
	LogID     string `json:"logID"`
}

// PushServiceLog push service log structure
type PushServiceLog struct {
	LogID     string  `json:"logID"`
	PartCount *uint64 `json:"partCount,omitempty"`
	Part      *uint64 `json:"part,omitempty"`
	Data      *[]byte `json:"data,omitempty"`
	Error     *string `json:"error,omitempty"`
}

// Alerts alerts message structure
type Alerts struct {
	Data []AlertItem `json:"data"`
}

// AlertItem alert item structure
type AlertItem struct {
	Timestamp time.Time   `json:"timestamp"`
	Tag       string      `json:"tag"`
	Source    string      `json:"source"`
	Version   *uint64     `json:"version,omitempty"`
	Payload   interface{} `json:"payload"`
}

// SystemAlert system alert structure
type SystemAlert struct {
	Message string `json:"message"`
}

// ResourceAlert resource alert structure
type ResourceAlert struct {
	Parameter string `json:"paramater"`
	Value     uint64 `json:"value"`
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

type messageHeader struct {
	Version     uint64 `json:"version"`
	MessageType string `json:"messageType"`
}

type vehicleStatus struct {
	Services []ServiceInfo `json:"services"`
}

type desiredStatus struct {
	Services []byte `json:"services"`
}

type newState struct {
	ServiceID string `json:"serviceId"`
	Checksum  string `json:"stateChecksum"`
	State     string `json:"state"`
}

type stateRequest struct {
	ServiceID string `json:"serviceId"`
	Default   bool   `json:"default"`
}

type pushServiceLog struct {
	PushServiceLog
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

/*******************************************************************************
 * Variables
 ******************************************************************************/

var messageMap = map[string]func() interface{}{
	desiredStatusStr:          func() interface{} { return &desiredStatus{} },
	stateAcceptanceStr:        func() interface{} { return &StateAcceptance{} },
	updateStateStr:            func() interface{} { return &UpdateState{} },
	requestServiceLogStr:      func() interface{} { return &RequestServiceLog{} },
	requestServiceCrashLogStr: func() interface{} { return &RequestServiceCrashLog{} },
}

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

	if err = handler.setupConnections("amqps", connectionInfo, tlsConfig); err != nil {
		return err
	}

	return nil
}

// ConnectRabbit connects directly to RabbitMQ server without service discovery
func (handler *AmqpHandler) ConnectRabbit(host, user, password, exchange, consumer, queue string) (err error) {
	log.WithFields(log.Fields{
		"host": host,
		"user": user}).Debug("AMQP direct connect")

	connectionInfo := rabbitConnectioninfo{
		SendParams: sendParams{
			Host:     host,
			User:     user,
			Password: password,
			Exchange: exchangeParams{Name: exchange}},
		ReceiveParams: receiveParams{
			Host:     host,
			User:     user,
			Password: password,
			Consumer: consumer,
			Queue:    queueInfo{Name: queue}}}

	if err = handler.setupConnections("amqp", connectionInfo, nil); err != nil {
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

// SendInitialSetup sends initial list of available services
func (handler *AmqpHandler) SendInitialSetup(serviceList []ServiceInfo) (err error) {
	handler.sendChannel <- Message{"", struct {
		messageHeader
		vehicleStatus
	}{
		messageHeader: messageHeader{
			Version:     1,
			MessageType: vehicleStatusStr},
		vehicleStatus: vehicleStatus{
			Services: serviceList}}}

	return nil
}

// SendServiceStatus sends message with service status
func (handler *AmqpHandler) SendServiceStatus(serviceStatus ServiceInfo) (err error) {
	handler.sendChannel <- Message{"", struct {
		messageHeader
		vehicleStatus
	}{
		messageHeader: messageHeader{
			Version:     1,
			MessageType: serviceStatusStr},
		vehicleStatus: vehicleStatus{
			Services: []ServiceInfo{serviceStatus}}}}

	return nil
}

// SendMonitoringData sends monitoring data
func (handler *AmqpHandler) SendMonitoringData(monitoringData MonitoringData) (err error) {
	handler.sendChannel <- Message{"", struct {
		messageHeader
		MonitoringData
	}{
		messageHeader: messageHeader{
			Version:     1,
			MessageType: monitoringDataStr},
		MonitoringData: monitoringData}}

	return nil
}

// SendNewState sends new state message
func (handler *AmqpHandler) SendNewState(serviceID, state, checksum, correlationID string) (err error) {
	handler.sendChannel <- Message{correlationID, struct {
		messageHeader
		newState
	}{
		messageHeader: messageHeader{
			Version:     1,
			MessageType: newStateStr},
		newState: newState{
			ServiceID: serviceID,
			State:     state,
			Checksum:  checksum}}}

	return nil
}

// SendStateRequest sends state request message
func (handler *AmqpHandler) SendStateRequest(serviceID string, defaultState bool) (err error) {
	handler.sendChannel <- Message{"", struct {
		messageHeader
		stateRequest
	}{
		messageHeader: messageHeader{
			Version:     1,
			MessageType: stateRequestStr},
		stateRequest: stateRequest{
			ServiceID: serviceID,
			Default:   defaultState}}}

	return nil
}

// SendServiceLog sends service logs
func (handler *AmqpHandler) SendServiceLog(serviceLog PushServiceLog) (err error) {
	handler.sendChannel <- Message{"", struct {
		messageHeader
		pushServiceLog
	}{
		messageHeader: messageHeader{
			Version:     1,
			MessageType: pushServiceLogStr},
		pushServiceLog: pushServiceLog{serviceLog}}}

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

	if resp.StatusCode != 200 {
		return info, fmt.Errorf("%s: %s", resp.Status, string(htmlData))
	}

	var jsonResp serviceDiscoveryResp

	err = json.Unmarshal(htmlData, &jsonResp) // TODO: add check
	if err != nil {
		return info, err
	}

	return jsonResp.Connection, nil
}

func (handler *AmqpHandler) setupConnections(scheme string, info rabbitConnectioninfo, tlsConfig *tls.Config) (err error) {
	if err := retryHelper(func() (err error) {
		return handler.setupSendConnection(scheme, info.SendParams, tlsConfig)
	}); err != nil {
		return err
	}

	if err := retryHelper(func() (err error) {
		return handler.setupReceiveConnection(scheme, info.ReceiveParams, tlsConfig)
	}); err != nil {
		return err
	}

	return nil
}

func (handler *AmqpHandler) setupSendConnection(scheme string, params sendParams, tlsConfig *tls.Config) (err error) {
	urlRabbitMQ := url.URL{
		Scheme: scheme,
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
	defer log.Info("AMQP sender closed")

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

func (handler *AmqpHandler) setupReceiveConnection(scheme string, params receiveParams, tlsConfig *tls.Config) (err error) {
	urlRabbitMQ := url.URL{
		Scheme: scheme,
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

		case delivery, ok := <-deliveryChannel:
			if !ok {
				return
			}

			log.WithFields(log.Fields{
				"message":      string(delivery.Body),
				"corrlationId": delivery.CorrelationId}).Debug("AMQP received message")

			header := messageHeader{}

			if err := json.Unmarshal(delivery.Body, &header); err != nil {
				log.Errorf("Can't parse message body: %s", err)
				continue
			}

			messageType, ok := messageMap[header.MessageType]
			if !ok {
				log.Errorf("AMQP unsupported message type: %s", header.MessageType)
				continue
			}

			data := messageType()

			if err := json.Unmarshal(delivery.Body, data); err != nil {
				log.Errorf("Can't parse message body: %s", err)
				continue
			}

			if header.MessageType == desiredStatusStr {
				var err error

				encodedData, ok := data.(*desiredStatus)
				if !ok {
					log.Error("Wrong data type: expect desired status")
					continue
				}

				if data, err = decodeServices(encodedData.Services); err != nil {
					log.Errorf("Can't decode services: %s", err)
					continue
				}
			}

			handler.MessageChannel <- Message{delivery.CorrelationId, data}
		}
	}
}

func decodeServices(data []byte) (services []ServiceInfoFromCloud, err error) {
	decryptData, err := fcrypt.DecryptMetadata(data)
	if err != nil {
		return nil, err
	}

	log.WithField("data", string(decryptData)).Debug("Decrypted data")

	if err = json.Unmarshal(decryptData, &services); err != nil {
		return nil, err
	}

	return services, nil
}
