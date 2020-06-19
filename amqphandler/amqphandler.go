// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package amqphandler

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/streadway/amqp"

	"aos_servicemanager/config"
	"aos_servicemanager/fcrypt"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

// ProtocolVersion specifies supported protocol version
const ProtocolVersion = 2

const (
	sendChannelSize    = 32
	receiveChannelSize = 16
	retryChannelSize   = 8

	connectionRetry = 3
)

// amqp request types
const (
	DesiredStatusType          = "desiredStatus"
	RequestServiceCrashLogType = "requestServiceCrashLog"
	RequestServiceLogType      = "requestServiceLog"
	ServiceDiscoveryType       = "serviceDiscovery"
	StateAcceptanceType        = "stateAcceptance"
	SystemRevertType           = "systemRevert"
	SystemUpgradeType          = "systemUpgrade"
	UpdateStateType            = "updateState"
	DeviceErrors               = "deviceErrors"
)

// amqp response types
const (
	AlertsType              = "alerts"
	MonitoringDataType      = "monitoringData"
	NewStateType            = "newState"
	PushServiceLogType      = "pushServiceLog"
	ServiceStatusType       = "serviceStatus"
	StateRequestType        = "stateRequest"
	SystemRevertStatusType  = "systemRevertStatus"
	SystemUpgradeStatusType = "systemUpgradeStatus"
	SystemVersionType       = "systemVersion"
	UnitStatusType          = "unitStatus"
)

// Alert tags
const (
	AlertTagSystemError = "systemError"
	AlertTagResource    = "resourceAlert"
	AlertTagAosCore     = "aosCore"
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

	systemID string
}

// MessageHeader message header
type MessageHeader struct {
	Version     uint64 `json:"version"`
	SystemID    string `json:"systemId"`
	MessageType string `json:"messageType"`
}

// AOSMessage structure for AOS messages
type AOSMessage struct {
	Header MessageHeader `json:"header"`
	Data   interface{}   `json:"data"`
}

// DesiredStatus desired status message
type DesiredStatus struct {
	Services          []byte             `json:"services"`
	Layers            []byte             `json:"layers"`
	CertificateChains []CertificateChain `json:"certificateChains,omitempty"`
	Certificates      []Certificate      `json:"certificates,omitempty"`
}

// RequestServiceCrashLog request service crash log message
type RequestServiceCrashLog struct {
	ServiceID string `json:"serviceId"`
	LogID     string `json:"logID"`
}

// RequestServiceLog request service log message
type RequestServiceLog struct {
	ServiceID string     `json:"serviceId"`
	LogID     string     `json:"logID"`
	From      *time.Time `json:"from"`
	Till      *time.Time `json:"till"`
}

// StateAcceptance state acceptance message
type StateAcceptance struct {
	ServiceID string `json:"serviceId"`
	Checksum  string `json:"checksum"`
	Result    string `json:"result"`
	Reason    string `json:"reason"`
}

// SystemRevert system revert structure
type SystemRevert struct {
	ImageVersion uint64 `json:"imageVersion"`
}

// DecryptionInfo upgrade decryption info
type DecryptionInfo struct {
	BlockAlg     string `json:"blockAlg"`
	BlockIv      []byte `json:"blockIv"`
	BlockKey     []byte `json:"blockKey"`
	AsymAlg      string `json:"asymAlg"`
	ReceiverInfo *struct {
		Serial string `json:"serial"`
		Issuer string `json:"issuer"`
	} `json:"receiverInfo"`
}

// Signs message signature
type Signs struct {
	ChainName        string   `json:"chainName"`
	Alg              string   `json:"alg"`
	Value            []byte   `json:"value"`
	TrustedTimestamp string   `json:"trustedTimestamp"`
	OcspValues       []string `json:"ocspValues"`
}

// CertificateChain  certificate chain
type CertificateChain struct {
	Name         string   `json:"name"`
	Fingerprints []string `json:"fingerprints"`
}

// Certificate certificate structure
type Certificate struct {
	Fingerprint string `json:"fingerprint"`
	Certificate []byte `json:"certificate"`
}

// SystemUpgrade system upgrade structure
type SystemUpgrade struct {
	ImageVersion uint64 `json:"imageVersion"`
	DecryptDataStruct
	CertificateChains []CertificateChain `json:"certificateChains,omitempty"`
	Certificates      []Certificate      `json:"certificates,omitempty"`
}

//DecryptDataStruct struct contains how to decrypt data
type DecryptDataStruct struct {
	URLs           []string        `json:"urls"`
	Sha256         []byte          `json:"sha256"`
	Sha512         []byte          `json:"sha512"`
	Size           uint64          `json:"size"`
	DecryptionInfo *DecryptionInfo `json:"decryptionInfo,omitempty"`
	Signs          *Signs          `json:"signs,omitempty"`
}

// UpdateState state update message
type UpdateState struct {
	ServiceID string `json:"serviceId"`
	Checksum  string `json:"stateChecksum"`
	State     string `json:"state"`
}

// SystemAlert system alert structure
type SystemAlert struct {
	Message string `json:"message"`
}

// ResourceAlert resource alert structure
type ResourceAlert struct {
	Parameter string `json:"parameter"`
	Value     uint64 `json:"value"`
}

// ResourceValidateErrors errors structure
type ResourceValidateErrors struct {
	Name   string   `json:"name"`
	Errors []string `json:"error"`
}

// Validate payload structure
type ResourseValidatePayload struct {
	Type   string                   `json:"type"`
	Errors []ResourceValidateErrors `json:"message"`
}

// AlertItem alert item structure
type AlertItem struct {
	Timestamp time.Time   `json:"timestamp"`
	Tag       string      `json:"tag"`
	Source    string      `json:"source"`
	Version   *uint64     `json:"version,omitempty"`
	Payload   interface{} `json:"payload"`
}

// Alerts alerts message structure
type Alerts []AlertItem

// NewState new state structure
type NewState struct {
	ServiceID string `json:"serviceId"`
	Checksum  string `json:"stateChecksum"`
	State     string `json:"state"`
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

// MonitoringData monitoring data structure
type MonitoringData struct {
	Timestamp time.Time `json:"timestamp"`
	Global    struct {
		RAM        uint64 `json:"ram"`
		CPU        uint64 `json:"cpu"`
		UsedDisk   uint64 `json:"usedDisk"`
		InTraffic  uint64 `json:"inTraffic"`
		OutTraffic uint64 `json:"outTraffic"`
	} `json:"global"`
	ServicesData []ServiceMonitoringData `json:"servicesData"`
}

// PushServiceLog push service log structure
type PushServiceLog struct {
	LogID     string  `json:"logID"`
	PartCount *uint64 `json:"partCount,omitempty"`
	Part      *uint64 `json:"part,omitempty"`
	Data      *[]byte `json:"data,omitempty"`
	Error     *string `json:"error,omitempty"`
}

// StateRequest state request structure
type StateRequest struct {
	ServiceID string `json:"serviceId"`
	Default   bool   `json:"default"`
}

// SystemRevertStatus system revert status structure
type SystemRevertStatus struct {
	Status       string  `json:"revertStatus"`
	Error        *string `json:"error,omitempty"`
	ImageVersion uint64  `json:"imageVersion"`
}

// SystemUpgradeStatus system upgrade status structure
type SystemUpgradeStatus struct {
	Status       string  `json:"upgradeStatus"`
	Error        *string `json:"error,omitempty"`
	ImageVersion uint64  `json:"imageVersion"`
}

// SystemVersion system version structure
type SystemVersion struct {
	ImageVersion uint64 `json:"imageVersion"`
}

// UnitStatus untit status structure
type UnitStatus struct {
	Services []ServiceInfo `json:"services"`
	Layers   []LayerInfo   `json:"layers,omitempty"`
}

// ServiceInfo struct with service information
type ServiceInfo struct {
	ID            string `json:"id"`
	Version       uint64 `json:"version"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	StateChecksum string `json:"stateChecksum,omitempty"`
}

//LayerInfo struct with layer info and status
type LayerInfo struct {
	LayerID string `json:"layerId"`
	Digest  string `json:"digest"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

// Message structure used to send/receive data by amqp
type Message struct {
	CorrelationID string
	Data          interface{}
}

type serviceDiscoveryRequestData struct {
	Users []string `json:"users"`
}

// ServiceAlertRules define service monitoring alerts rules
type ServiceAlertRules struct {
	RAM        *config.AlertRule `json:"ram,omitempty"`
	CPU        *config.AlertRule `json:"cpu,omitempty"`
	UsedDisk   *config.AlertRule `json:"usedDisk,omitempty"`
	InTraffic  *config.AlertRule `json:"inTraffic,omitempty"`
	OutTraffic *config.AlertRule `json:"outTraffic,omitempty"`
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
	AlertRules             *ServiceAlertRules `json:"alertRules,omitempty"`
}

// LayerInfoFromCloud service layer decryption info
type LayerInfoFromCloud struct {
	LayerID string `json:"layerId"`
	Digest  string `json:"digest"`
	Name    string `json:"name,omitempty"`
	DecryptDataStruct
}

// DecodedDesiredStatus decoded Desired configuration
type DecodedDesiredStatus struct {
	Layers            []LayerInfoFromCloud
	Services          []ServiceInfoFromCloud
	CertificateChains []CertificateChain
	Certificates      []Certificate
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
	DesiredStatusType:          func() interface{} { return &DesiredStatus{} },
	RequestServiceCrashLogType: func() interface{} { return &RequestServiceCrashLog{} },
	RequestServiceLogType:      func() interface{} { return &RequestServiceLog{} },
	StateAcceptanceType:        func() interface{} { return &StateAcceptance{} },
	SystemRevertType:           func() interface{} { return &SystemRevert{} },
	SystemUpgradeType:          func() interface{} { return &SystemUpgrade{} },
	UpdateStateType:            func() interface{} { return &UpdateState{} },
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new amqp object
func New() (handler *AmqpHandler, err error) {
	log.Debug("New AMQP")

	handler = &AmqpHandler{}

	handler.sendChannel = make(chan Message, sendChannelSize)
	handler.retryChannel = make(chan Message, retryChannelSize)

	return handler, nil
}

// Connect connects to cloud
func (handler *AmqpHandler) Connect(sdURL string, systemID string, users []string) (err error) {
	log.WithFields(log.Fields{"url": sdURL, "systemID": systemID, "users": users}).Debug("AMQP connect")
	handler.systemID = systemID

	tlsConfig, err := fcrypt.GetTLSConfig()
	if err != nil {
		return err
	}

	var connectionInfo rabbitConnectioninfo

	if err := retryHelper(func() (err error) {
		connectionInfo, err = getConnectionInfo(sdURL,
			handler.createAosMessage(ServiceDiscoveryType, serviceDiscoveryRequestData{Users: users}),
			tlsConfig)
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

// SendInitialSetup sends initial list of available services and layers
func (handler *AmqpHandler) SendInitialSetup(serviceList []ServiceInfo, layersList []LayerInfo) (err error) {
	statusMsg := handler.createAosMessage(UnitStatusType, UnitStatus{Services: serviceList, Layers: layersList})

	handler.sendChannel <- Message{"", statusMsg}

	return nil
}

// SendServiceStatus sends message with service status
func (handler *AmqpHandler) SendServiceStatus(serviceStatus ServiceInfo) (err error) {
	statusMsg := handler.createAosMessage(ServiceStatusType,
		UnitStatus{Services: []ServiceInfo{serviceStatus}})

	handler.sendChannel <- Message{"", statusMsg}

	return nil
}

// SendLayerStatus sends message with layer status
func (handler *AmqpHandler) SendLayerStatus(serviceStatus LayerInfo) (err error) {
	statusMsg := handler.createAosMessage(ServiceStatusType,
		UnitStatus{Layers: []LayerInfo{serviceStatus}})

	handler.sendChannel <- Message{"", statusMsg}

	return nil
}

// SendMonitoringData sends monitoring data
func (handler *AmqpHandler) SendMonitoringData(monitoringData MonitoringData) (err error) {
	monitoringMsg := handler.createAosMessage(MonitoringDataType, monitoringData)

	handler.sendChannel <- Message{"", monitoringMsg}

	return nil
}

// SendNewState sends new state message
func (handler *AmqpHandler) SendNewState(serviceID, state, checksum, correlationID string) (err error) {
	newStateMsg := handler.createAosMessage(NewStateType,
		NewState{ServiceID: serviceID, State: state, Checksum: checksum})

	handler.sendChannel <- Message{correlationID, newStateMsg}

	return nil
}

// SendStateRequest sends state request message
func (handler *AmqpHandler) SendStateRequest(serviceID string, defaultState bool) (err error) {
	stateRequestMsg := handler.createAosMessage(StateRequestType,
		StateRequest{ServiceID: serviceID, Default: defaultState})

	handler.sendChannel <- Message{"", stateRequestMsg}

	return nil
}

// SendServiceLog sends service logs
func (handler *AmqpHandler) SendServiceLog(serviceLog PushServiceLog) (err error) {
	serviceLogMsg := handler.createAosMessage(PushServiceLogType, serviceLog)

	handler.sendChannel <- Message{"", serviceLogMsg}

	return nil
}

// SendAlerts sends alerts message
func (handler *AmqpHandler) SendAlerts(alerts Alerts) (err error) {
	alertMsg := handler.createAosMessage(AlertsType, alerts)

	handler.sendChannel <- Message{"", alertMsg}

	return nil
}

// SendSystemRevertStatus sends system revert status
func (handler *AmqpHandler) SendSystemRevertStatus(revertStatus, revertError string, imageVersion uint64) (err error) {
	errorValue := &revertError

	if revertError == "" {
		errorValue = nil
	}

	revertStatusMsg := handler.createAosMessage(SystemRevertStatusType,
		SystemRevertStatus{Status: revertStatus, Error: errorValue, ImageVersion: imageVersion})

	handler.sendChannel <- Message{"", revertStatusMsg}

	return nil
}

// SendSystemUpgradeStatus sends system upgrade status
func (handler *AmqpHandler) SendSystemUpgradeStatus(upgradeStatus, upgradeError string, imageVersion uint64) (err error) {
	errorValue := &upgradeError

	if upgradeError == "" {
		errorValue = nil
	}

	upgradeStatusMsg := handler.createAosMessage(SystemUpgradeStatusType,
		SystemUpgradeStatus{Status: upgradeStatus, Error: errorValue, ImageVersion: imageVersion})

	handler.sendChannel <- Message{"", upgradeStatusMsg}

	return nil
}

// SendSystemVersion sends system version
func (handler *AmqpHandler) SendSystemVersion(imageVersion uint64) (err error) {
	systemVersionMsg := handler.createAosMessage(SystemVersionType, SystemVersion{ImageVersion: imageVersion})

	handler.sendChannel <- Message{"", systemVersionMsg}

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
func getConnectionInfo(url string, request AOSMessage, tlsConfig *tls.Config) (info rabbitConnectioninfo, err error) {
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
	handler.MessageChannel = make(chan Message, receiveChannelSize)

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
		retry := false

		select {
		case err := <-errorChannel:
			if err != nil {
				handler.MessageChannel <- Message{"", err}
			}

			return

		case message = <-handler.retryChannel:
			retry = true

		case message = <-handler.sendChannel:
		}

		if message.Data != nil {
			data, err := json.Marshal(message.Data)
			if err != nil {
				log.Errorf("Can't parse message: %s", err)
				continue
			}

			if retry {
				log.WithFields(log.Fields{
					"correlationID": message.CorrelationID,
					"data":          string(data)}).Debug("AMQP retry message")
			} else {
				log.WithFields(log.Fields{
					"correlationID": message.CorrelationID,
					"data":          string(data)}).Debug("AMQP send message")
			}

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
				handler.MessageChannel <- Message{"", errors.New("receive channel is closed")}
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

			var rawData json.RawMessage
			incomingMsg := AOSMessage{Data: &rawData}

			if err := json.Unmarshal(delivery.Body, &incomingMsg); err != nil {
				log.Errorf("Can't parse message header: %s", err)
				continue
			}

			if incomingMsg.Header.Version != ProtocolVersion {
				log.Errorf("Unsupported protocol version: %d", incomingMsg.Header.Version)
				continue
			}

			messageType, ok := messageMap[incomingMsg.Header.MessageType]
			if !ok {
				log.Warnf("AMQP unsupported message type: %s", incomingMsg.Header.MessageType)
				continue
			}

			data := messageType()

			if err := json.Unmarshal(rawData, data); err != nil {
				log.Errorf("Can't parse message body: %s", err)
				continue
			}

			if incomingMsg.Header.MessageType == DesiredStatusType {
				var err error

				encodedData, ok := data.(*DesiredStatus)
				if !ok {
					log.Error("Wrong data type: expect desired status")
					continue
				}

				layersList, err := decodeLayers(encodedData.Layers)
				if err != nil {
					log.Errorf("Can't decode layers: %s", err)
					continue
				}

				servicesList, err := decodeServices(encodedData.Services)
				if err != nil {
					log.Errorf("Can't decode services: %s", err)
					continue
				}

				data = DecodedDesiredStatus{Layers: layersList, Services: servicesList,
					CertificateChains: encodedData.CertificateChains, Certificates: encodedData.Certificates}
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

func decodeLayers(data []byte) (layers []LayerInfoFromCloud, err error) {
	if len(data) == 0 {
		return layers, nil
	}

	decryptData, err := fcrypt.DecryptMetadata(data)
	if err != nil {
		return nil, err
	}

	log.WithField("data", string(decryptData)).Debug("Decrypted data")

	if err = json.Unmarshal(decryptData, &layers); err != nil {
		return nil, err
	}

	return layers, nil
}

func (handler *AmqpHandler) createAosMessage(msgType string, data interface{}) (msg AOSMessage) {
	msg = AOSMessage{
		Header: MessageHeader{Version: ProtocolVersion, SystemID: handler.systemID, MessageType: msgType},
		Data:   data}

	return msg
}
