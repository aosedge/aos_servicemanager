package visclient

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/wsclient"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	usersChangedChannelSize = 1
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// UserChangedNtf user changed notification
type UserChangedNtf struct {
	Users []string
}

// Client VIS client object
type Client struct {
	UsersChangedChannel chan []string
	ErrorChannel        chan error

	wsClient *wsclient.Client

	vin   string
	users []string

	subscribeMap sync.Map

	sync.Mutex
}

// ErrorInfo VIS error info message
type ErrorInfo struct {
	Number  int
	Reason  string
	Message string
}

// Request VIS request message
type Request struct {
	Action    string      `json:"action"`
	Path      string      `json:"path"`
	RequestID string      `json:"requestId"`
	Value     interface{} `json:"value,omitempty"`
}

// Response VIS response message
type Response struct {
	Action         string      `json:"action"`
	RequestID      *string     `json:"requestId"`
	Value          interface{} `json:"value"`
	Error          *ErrorInfo  `json:"error"`
	TTL            int64       `json:"TTL"`
	SubscriptionID *string     `json:"subscriptionId"`
	Timestamp      int64       `json:"timestamp"`
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new visclient
func New() (vis *Client, err error) {
	vis = &Client{}

	vis.wsClient, err = wsclient.New("VIS", vis.messageHandler)

	vis.UsersChangedChannel = make(chan []string, usersChangedChannelSize)
	vis.ErrorChannel = vis.wsClient.ErrorChannel

	return vis, nil
}

// Connect connects to the VIS
func (vis *Client) Connect(url string) (err error) {
	if err = vis.wsClient.Connect(url); err != nil {
		return err
	}

	vis.subscribeMap = sync.Map{}

	if err = vis.subscribe("Attribute.Vehicle.UserIdentification.Users", vis.handleUsersChanged); err != nil {
		if err := vis.wsClient.Disconnect(); err != nil {
			log.Errorf("Can't disconnect from VIS: %s", err)
		}

		return err
	}

	vis.users = nil
	vis.vin = ""

	return nil
}

// Disconnect disconnects from the VIS
func (vis *Client) Disconnect() (err error) {
	return vis.wsClient.Disconnect()
}

// IsConnected returns true if connected to VIS
func (vis *Client) IsConnected() (result bool) {
	return vis.wsClient.IsConnected()
}

// GetVIN returns VIN
func (vis *Client) GetVIN() (vin string, err error) {
	var rsp Response

	if err = vis.wsClient.SendRequest("RequestID", &Request{Action: "get", RequestID: uuid.New().String(),
		Path: "Attribute.Vehicle.VehicleIdentification.VIN"}, &rsp); err != nil {
		return "", err
	}

	value, err := getValueFromResponse("Attribute.Vehicle.VehicleIdentification.VIN", &rsp)
	if err != nil {
		return "", err
	}

	ok := false
	if vis.vin, ok = value.(string); !ok {
		return "", errors.New("Wrong VIN type")
	}

	log.WithField("VIN", vis.vin).Debug("Get VIN")

	return vis.vin, err
}

// GetUsers returns user list
func (vis *Client) GetUsers() (users []string, err error) {
	if vis.users == nil {
		var rsp Response

		if err = vis.wsClient.SendRequest("RequestID", &Request{Action: "get", RequestID: uuid.New().String(),
			Path: "Attribute.Vehicle.UserIdentification.Users"}, &rsp); err != nil {
			return nil, err
		}

		vis.Lock()
		defer vis.Unlock()

		if err = vis.setUsers(&rsp); err != nil {
			return nil, err
		}
	}

	log.WithField("users", vis.users).Debug("Get users")

	return vis.users, err
}

// Close closes vis client
func (vis *Client) Close() (err error) {
	return vis.wsClient.Close()
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (vis *Client) messageHandler(message []byte) {
	var rsp Response

	if err := json.Unmarshal(message, &rsp); err != nil {
		log.Errorf("Error parsing VIS response: %s", err)
		return
	}

	if rsp.Action == "subscription" {
		vis.processSubscriptions(&rsp)
	}
}

func getValueFromResponse(path string, rsp *Response) (value interface{}, err error) {
	if valueMap, ok := rsp.Value.(map[string]interface{}); ok {
		if value, ok = valueMap[path]; !ok {
			return value, errors.New("Path not found")
		}
		return value, nil
	}

	if rsp.Value == nil {
		return value, errors.New("No value found")
	}

	return rsp.Value, nil
}

func (vis *Client) processSubscriptions(rsp *Response) {
	// serve subscriptions
	subscriptionFound := false
	vis.subscribeMap.Range(func(key, value interface{}) bool {
		if key.(string) == *rsp.SubscriptionID {
			subscriptionFound = true
			value.(func(*Response))(rsp)
			return false
		}
		return true
	})

	if !subscriptionFound {
		log.Warningf("Unexpected subscription id: %v", rsp.SubscriptionID)
	}
}

func (vis *Client) setUsers(rsp *Response) (err error) {
	value, err := getValueFromResponse("Attribute.Vehicle.UserIdentification.Users", rsp)
	if err != nil {
		return err
	}

	itfs, ok := value.([]interface{})
	if !ok {
		return errors.New("Wrong users type")
	}

	vis.users = make([]string, len(itfs))

	for i, itf := range itfs {
		item, ok := itf.(string)
		if !ok {
			return errors.New("Wrong users type")
		}
		vis.users[i] = item
	}

	return nil
}

func (vis *Client) handleUsersChanged(rsp *Response) {
	vis.Lock()
	defer vis.Unlock()

	if err := vis.setUsers(rsp); err != nil {
		log.Errorf("Can't set users: %s", err)
		return
	}

	vis.UsersChangedChannel <- vis.users

	log.WithField("users", vis.users).Debug("Users changed")
}

func (vis *Client) subscribe(path string, callback func(*Response)) (err error) {
	var rsp Response

	if err = vis.wsClient.SendRequest("RequestID", &Request{Action: "subscribe", RequestID: uuid.New().String(), Path: path}, &rsp); err != nil {
		return err
	}

	if rsp.SubscriptionID == nil {
		return errors.New("No subscriptionID in response")
	}

	vis.subscribeMap.Store(*rsp.SubscriptionID, callback)

	return nil
}
