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
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/streadway/amqp"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

// ProtocolVersion specifies supported protocol version
const ProtocolVersion = 3

const (
	sendChannelSize    = 32
	receiveChannelSize = 16
	retryChannelSize   = 8

	connectionRetry = 3
)

// amqp request types
const (
	DesiredStatusType                 = "desiredStatus"
	RequestServiceCrashLogType        = "requestServiceCrashLog"
	RequestServiceLogType             = "requestServiceLog"
	ServiceDiscoveryType              = "serviceDiscovery"
	StateAcceptanceType               = "stateAcceptance"
	UpdateStateType                   = "updateState"
	DeviceErrors                      = "deviceErrors"
	RenewCertificatesNotificationType = "renewCertificatesNotification"
	IssuedUnitCertificatesType        = "issuedUnitCertificates"
)

// amqp response types
const (
	AlertsType                              = "alerts"
	MonitoringDataType                      = "monitoringData"
	NewStateType                            = "newState"
	PushLogType                             = "pushLog"
	StateRequestType                        = "stateRequest"
	UnitStatusType                          = "unitStatus"
	IssueUnitCertificatesRequestType        = "issueUnitCertificates"
	InstallUnitCertificatesConfirmationType = "installUnitCertificatesConfirmation"
)

// Alert tags
const (
	AlertTagSystemError = "systemError"
	AlertTagResource    = "resourceAlert"
	AlertTagAosCore     = "aosCore"
)

const unitSecureVersion = 2

// Unit statuses
const (
	PendingStatus   = "pending"
	RemovedStatus   = "removed"
	InstalledStatus = "installed"
	ErrorStatus     = "error"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// AmqpHandler structure with all amqp connection info
type AmqpHandler struct {
	config *config.Config

	// MessageChannel channel for amqp messages
	MessageChannel chan Message

	sendChannel  chan Message
	retryChannel chan Message

	sendConnection    *amqp.Connection
	receiveConnection *amqp.Connection

	cryptoContext amqpCryptoContext

	systemID string

	currentUnitStatus UnitStatus
	unitStatusChanged bool
	stopChannel       chan bool
	unitStatusMutex   sync.Mutex
}

type amqpCryptoContext interface {
	GetTLSConfig() (config *tls.Config, err error)
	DecryptMetadata(input []byte) (output []byte, err error)
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
	BoardConfig       []byte             `json:"boardConfig"`
	Services          []byte             `json:"services"`
	Layers            []byte             `json:"layers"`
	Components        []byte             `json:"components"`
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

// DecryptionInfo update decryption info
type DecryptionInfo struct {
	BlockAlg     string `json:"blockAlg"`
	BlockIv      []byte `json:"blockIv"`
	BlockKey     []byte `json:"blockKey"`
	AsymAlg      string `json:"asymAlg"`
	ReceiverInfo *struct {
		Serial string `json:"serial"`
		Issuer []byte `json:"issuer"`
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

// DownloadAlert download alert structure
type DownloadAlert struct {
	Message         string `json:"message"`
	Progress        string `json:"progress"`
	URL             string `json:"url"`
	DownloadedBytes string `json:"downloadedBytes"`
	TotalBytes      string `json:"totalBytes"`
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
	Timestamp  time.Time   `json:"timestamp"`
	Tag        string      `json:"tag"`
	Source     string      `json:"source"`
	AosVersion *uint64     `json:"aosVersion,omitempty"`
	Payload    interface{} `json:"payload"`
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

// UnitStatus unit status structure
type UnitStatus struct {
	BoardConfig []BoardConfigInfo `json:"boardConfig"`
	Services    []ServiceInfo     `json:"services"`
	Layers      []LayerInfo       `json:"layers,omitempty"`
	Components  []ComponentInfo   `json:"components"`
}

// BoardConfigInfo board config information
type BoardConfigInfo struct {
	VendorVersion string `json:"vendorVersion"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
}

// ServiceInfo struct with service information
type ServiceInfo struct {
	ID            string `json:"id"`
	AosVersion    uint64 `json:"aosVersion"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	StateChecksum string `json:"stateChecksum,omitempty"`
}

//LayerInfo struct with layer info and status
type LayerInfo struct {
	ID         string `json:"id"`
	AosVersion uint64 `json:"aosVersion"`
	Digest     string `json:"digest"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

//ComponentInfo struct with system component info and status
type ComponentInfo struct {
	ID            string `json:"id"`
	AosVersion    uint64 `json:"aosVersion"`
	VendorVersion string `json:"vendorVersion"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
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

// VersionFromCloud common version structure
type VersionFromCloud struct {
	AosVersion    uint64 `json:"aosVersion"`
	VendorVersion string `json:"vendorVersion"`
	Description   string `json:"description"`
}

// ServiceInfoFromCloud decrypted service info
type ServiceInfoFromCloud struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerId"`
	VersionFromCloud
	AlertRules *ServiceAlertRules `json:"alertRules,omitempty"`
	DecryptDataStruct
}

// LayerInfoFromCloud decrypted layer info
type LayerInfoFromCloud struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	VersionFromCloud
	DecryptDataStruct
}

// ComponentInfoFromCloud decrypted component info
type ComponentInfoFromCloud struct {
	ID          string          `json:"id"`
	Annotations json.RawMessage `json:"annotations"`
	VersionFromCloud
	DecryptDataStruct
}

// DecodedDesiredStatus decoded desired status
type DecodedDesiredStatus struct {
	BoardConfig       json.RawMessage
	Layers            []LayerInfoFromCloud
	Services          []ServiceInfoFromCloud
	Components        []ComponentInfoFromCloud
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

// CertificateNotification info aboute certificate renew notification
type CertificateNotification struct {
	Type      string    `json:"type"`
	Serial    string    `json:"serial"`
	ValidTill time.Time `json:"validTill"`
}

// RenewCertificatesNotification renew certificate notification from cloud
type RenewCertificatesNotification struct {
	Certificates   []CertificateNotification `json:"certificates"`
	UnitSecureData []byte                    `json:"unitSecureData"`
}

// RenewCertificatesNotificationWithPwd renew certificate notification from cloud with extracted pwd
type RenewCertificatesNotificationWithPwd struct {
	Certificates []CertificateNotification
	Password     string
}

// CertificateRequest struct wit certificate request
type CertificateRequest struct {
	Type string `json:"type"`
	Csr  string `json:"csr"`
}

// IssueUnitCertificatesRequest struct request cert to cloud
type IssueUnitCertificatesRequest struct {
	Requests []CertificateRequest `json:"requests"`
}

// IssuedUnitCertificatesInfo info with certificate to applay on device
type IssuedUnitCertificatesInfo struct {
	Type             string `json:"type"`
	CertificateChain string `json:"certificateChain"`
}

// IssuedUnitCertificates struct with new cert from cloud
type IssuedUnitCertificates struct {
	Certificates []IssuedUnitCertificatesInfo `json:"certificates"`
}

// CertificateConfirmation info about certificate installation
type CertificateConfirmation struct {
	Type        string `json:"type"`
	Serial      string `json:"serial"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
}

// InstallUnitCertificatesConfirmation response to cloud
type InstallUnitCertificatesConfirmation struct {
	Certificates []CertificateConfirmation `json:"certificates"`
}

type unitSecret struct {
	Version int `json:"version"`
	Data    struct {
		OwnerPassword string `json:"ownerPassword"`
	} `json:"data"`
}

/*******************************************************************************
 * Variables
 ******************************************************************************/

var messageMap = map[string]func() interface{}{
	DesiredStatusType:                 func() interface{} { return &DesiredStatus{} },
	RequestServiceCrashLogType:        func() interface{} { return &RequestServiceCrashLog{} },
	RequestServiceLogType:             func() interface{} { return &RequestServiceLog{} },
	StateAcceptanceType:               func() interface{} { return &StateAcceptance{} },
	UpdateStateType:                   func() interface{} { return &UpdateState{} },
	RenewCertificatesNotificationType: func() interface{} { return &RenewCertificatesNotification{} },
	IssuedUnitCertificatesType:        func() interface{} { return &IssuedUnitCertificates{} },
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new amqp object
func New(cfg *config.Config) (handler *AmqpHandler, err error) {
	log.Debug("New AMQP")

	handler = &AmqpHandler{config: cfg}

	handler.sendChannel = make(chan Message, sendChannelSize)
	handler.retryChannel = make(chan Message, retryChannelSize)
	handler.stopChannel = make(chan bool, 1)

	return handler, nil
}

// SetSystemID sets system ID
func (handler *AmqpHandler) SetSystemID(systemID string) {
	handler.systemID = systemID
}

// SetCryptoContext set cryptoContext fro amqp handler
func (handler *AmqpHandler) SetCryptoContext(crypt amqpCryptoContext) {
	handler.cryptoContext = crypt
}

// Connect connects to cloud
func (handler *AmqpHandler) Connect(sdURL string, users []string) (err error) {
	log.WithFields(log.Fields{"url": sdURL, "users": users}).Debug("AMQP connect")

	tlsConfig, err := handler.cryptoContext.GetTLSConfig()
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
func (handler *AmqpHandler) SendInitialSetup(boardConfigList []BoardConfigInfo, serviceList []ServiceInfo, layersList []LayerInfo,
	components []ComponentInfo) (err error) {
	handler.unitStatusMutex.Lock()
	defer handler.unitStatusMutex.Unlock()

	handler.currentUnitStatus.BoardConfig = make([]BoardConfigInfo, len(boardConfigList))
	copy(handler.currentUnitStatus.BoardConfig, boardConfigList)

	handler.currentUnitStatus.Services = make([]ServiceInfo, len(serviceList))
	copy(handler.currentUnitStatus.Services, serviceList)

	handler.currentUnitStatus.Layers = make([]LayerInfo, len(layersList))
	copy(handler.currentUnitStatus.Layers, layersList)

	handler.currentUnitStatus.Components = make([]ComponentInfo, len(components))
	copy(handler.currentUnitStatus.Components, components)

	handler.unitStatusChanged = true

	go handler.processUnitStatusChanges()

	return nil
}

// SendServiceStatus sends message with service status
func (handler *AmqpHandler) SendServiceStatus(serviceStatus ServiceInfo) (err error) {
	handler.unitStatusMutex.Lock()
	defer handler.unitStatusMutex.Unlock()

	for i, value := range handler.currentUnitStatus.Services {
		if value.ID == serviceStatus.ID {
			handler.currentUnitStatus.Services[i] = serviceStatus
			handler.unitStatusChanged = true
			break
		}
	}

	return nil
}

// SendLayerStatus sends message with layer status
func (handler *AmqpHandler) SendLayerStatus(layers []LayerInfo) {
	handler.unitStatusMutex.Lock()
	defer handler.unitStatusMutex.Unlock()

	handler.currentUnitStatus.Layers = make([]LayerInfo, len(layers))
	copy(handler.currentUnitStatus.Layers, layers)

	handler.unitStatusChanged = true
}

// SendComponentStatus sends message with layer status
func (handler *AmqpHandler) SendComponentStatus(components []ComponentInfo) {
	handler.unitStatusMutex.Lock()
	defer handler.unitStatusMutex.Unlock()

	handler.currentUnitStatus.Components = make([]ComponentInfo, len(components))
	copy(handler.currentUnitStatus.Components, components)

	handler.unitStatusChanged = true
}

// SendBoardConfigStatus sends board config status
func (handler *AmqpHandler) SendBoardConfigStatus(info []BoardConfigInfo) {
	handler.unitStatusMutex.Lock()
	defer handler.unitStatusMutex.Unlock()

	handler.currentUnitStatus.BoardConfig = make([]BoardConfigInfo, len(info))
	copy(handler.currentUnitStatus.BoardConfig, info)

	handler.unitStatusChanged = true
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
	serviceLogMsg := handler.createAosMessage(PushLogType, serviceLog)

	handler.sendChannel <- Message{"", serviceLogMsg}

	return nil
}

// SendAlerts sends alerts message
func (handler *AmqpHandler) SendAlerts(alerts Alerts) (err error) {
	alertMsg := handler.createAosMessage(AlertsType, alerts)

	handler.sendChannel <- Message{"", alertMsg}

	return nil
}

//SendIssueUnitCertificatesRequest send request to issue certificate
func (handler *AmqpHandler) SendIssueUnitCertificatesRequest(requests []CertificateRequest) (err error) {
	request := handler.createAosMessage(IssueUnitCertificatesRequestType, IssueUnitCertificatesRequest{Requests: requests})

	handler.sendChannel <- Message{"", request}

	return nil
}

//SendInstallCertificatesConfirmation send status aboute certificate installation
func (handler *AmqpHandler) SendInstallCertificatesConfirmation(confirmation []CertificateConfirmation) (err error) {
	response := handler.createAosMessage(InstallUnitCertificatesConfirmationType,
		InstallUnitCertificatesConfirmation{Certificates: confirmation})

	handler.sendChannel <- Message{"", response}

	return nil
}

// Close closes all amqp connection
func (handler *AmqpHandler) Close() {
	log.Info("Close AMQP")

	handler.stopChannel <- true

	handler.Disconnect()
}

// UpdateUnitStatusWithDesiredFromCloud update current status
func (handler *AmqpHandler) UpdateUnitStatusWithDesiredFromCloud(desiredStatus *DecodedDesiredStatus) {
	newServices := []ServiceInfo{}

	handler.unitStatusMutex.Lock()
	defer handler.unitStatusMutex.Unlock()

	for _, desSrv := range desiredStatus.Services {
		wasFound := false
		pendingService := ServiceInfo{ID: desSrv.ID, AosVersion: desSrv.AosVersion, Status: PendingStatus}

		for _, curSrv := range handler.currentUnitStatus.Services {
			if curSrv.ID != desSrv.ID {
				continue
			}

			wasFound = true

			if curSrv.AosVersion != desSrv.AosVersion {
				newServices = append(newServices, pendingService)
			}
			break
		}

		if wasFound == false {
			newServices = append(newServices, pendingService)
		}
	}

	if len(newServices) > 0 {
		handler.unitStatusChanged = true
		handler.currentUnitStatus.Services = append(handler.currentUnitStatus.Services, newServices...)
	}
}

func (service ServiceInfoFromCloud) String() string {
	return fmt.Sprintf("{id: %s, vendorVersion: %s aosVersion: %d, description: %s}",
		service.ID, service.VendorVersion, service.AosVersion, service.Description)
}

func (layer LayerInfoFromCloud) String() string {
	return fmt.Sprintf("{id: %s, digest: %s, vendorVersion: %s aosVersion: %d, description: %s}",
		layer.ID, layer.Digest, layer.VendorVersion, layer.AosVersion, layer.Description)
}

func (component ComponentInfoFromCloud) String() string {
	return fmt.Sprintf("{id: %s, annotations: %s, vendorVersion: %s aosVersion: %d, description: %s}",
		component.ID, component.Annotations, component.VendorVersion, component.AosVersion, component.Description)
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
				encodedData, ok := data.(*DesiredStatus)
				if !ok {
					log.Error("Wrong data type: expect desired status")
					continue
				}

				desiredStatus := DecodedDesiredStatus{CertificateChains: encodedData.CertificateChains, Certificates: encodedData.Certificates}

				if err := handler.decodeDesiredStatusParts(encodedData.BoardConfig, &desiredStatus.BoardConfig); err != nil {
					log.Errorf("Can't decode board config: %s", err)
					continue
				}

				if err := handler.decodeDesiredStatusParts(encodedData.Services, &desiredStatus.Services); err != nil {
					log.Errorf("Can't decode services: %s", err)
					continue
				}

				if err := handler.decodeDesiredStatusParts(encodedData.Layers, &desiredStatus.Layers); err != nil {
					log.Errorf("Can't decode Layers: %s", err)
					continue
				}

				if err := handler.decodeDesiredStatusParts(encodedData.Components, &desiredStatus.Components); err != nil {
					log.Errorf("Can't decode Components: %s", err)
					continue
				}

				data = desiredStatus
			}

			if incomingMsg.Header.MessageType == RenewCertificatesNotificationType {
				msg, ok := data.(*RenewCertificatesNotification)
				if !ok {
					log.Error("Wrong data type: expect RenewCertificatesNotificationType")
					continue
				}

				secret := new(unitSecret)

				if len(msg.UnitSecureData) > 0 {
					rowSecret, err := handler.cryptoContext.DecryptMetadata(msg.UnitSecureData)
					if err != nil {
						log.Error("Can't decrypt UnitSecureData ", err)
						continue
					}

					if err := json.Unmarshal(rowSecret, secret); err != nil {
						log.Error("Can't unmarshal unitSecret ", err)
						continue
					}

					if secret.Version != unitSecureVersion {
						log.Error("unit secure version  missmatch ", secret.Version, " != ", unitSecureVersion)
						continue
					}
				}

				data = &RenewCertificatesNotificationWithPwd{Certificates: msg.Certificates,
					Password: secret.Data.OwnerPassword}
			}

			handler.MessageChannel <- Message{delivery.CorrelationId, data}
		}
	}
}

func (handler *AmqpHandler) decodeDesiredStatusParts(data []byte, result interface{}) (err error) {
	if len(data) == 0 {
		return nil
	}

	decryptData, err := handler.cryptoContext.DecryptMetadata(data)
	if err != nil {
		return err
	}

	if err = json.Unmarshal(decryptData, result); err != nil {
		return err
	}

	if rawJSON, ok := result.(*json.RawMessage); ok {
		log.WithField("data", string(*rawJSON)).Debug("Decrypted data")
	} else {
		log.WithField("data", result).Debug("Decrypted data")
	}

	return nil
}

func (handler *AmqpHandler) createAosMessage(msgType string, data interface{}) (msg AOSMessage) {
	msg = AOSMessage{
		Header: MessageHeader{Version: ProtocolVersion, SystemID: handler.systemID, MessageType: msgType},
		Data:   data}

	return msg
}

func (handler *AmqpHandler) processUnitStatusChanges() {
	timer := time.NewTicker(time.Duration(handler.config.UnitStatusTimeout) * time.Second)

	for {
		handler.unitStatusMutex.Lock()

		if handler.unitStatusChanged {
			statusMsg := handler.createAosMessage(UnitStatusType, handler.currentUnitStatus)
			handler.sendChannel <- Message{"", statusMsg}
			handler.unitStatusChanged = false

			handler.cleanupServiceStatusList()
		}

		handler.unitStatusMutex.Unlock()

		select {
		case <-timer.C:

		case <-handler.stopChannel:
			return
		}
	}
}

func (handler *AmqpHandler) cleanupServiceStatusList() {
	removedElements := []int{}

	for i, value := range handler.currentUnitStatus.Services {
		if value.Status == RemovedStatus {
			removedElements = append(removedElements, i)
		}
	}

	for i, value := range removedElements {
		handler.currentUnitStatus.Services = append(handler.currentUnitStatus.Services[:value-i],
			handler.currentUnitStatus.Services[value-i+1:]...)
	}
}
