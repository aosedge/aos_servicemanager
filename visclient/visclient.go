package visclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	websocketTimeout        = 3 * time.Second
	usersChangedChannelSize = 1
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// UserChangedNtf user changed notification
type UserChangedNtf struct {
	Users []string
}

// VisClient VIS client object
type VisClient struct {
	UsersChangedChannel chan []string

	webConn *websocket.Conn

	requests sync.Map

	vin   string
	users []string

	mutex     sync.Mutex
	requestID uint64

	subscribeMap sync.Map
}

type errorInfo struct {
	Number  int
	Reason  string
	Message string
}

type visRequest struct {
	Action    string      `json:"action"`
	Path      string      `json:"path"`
	RequestID string      `json:"requestId"`
	Value     interface{} `json:"value"`
}

type visResponse struct {
	Action         string      `json:"action"`
	RequestID      *string     `json:"requestId"`
	Value          interface{} `json:"value"`
	Error          *errorInfo  `json:"error"`
	TTL            int64       `json:"TTL"`
	SubscriptionID *string     `json:"subscriptionId"`
	Timestamp      int64       `json:"timestamp"`
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new visclient
func New(urlStr string) (vis *VisClient, err error) {
	log.WithField("url", urlStr).Debug("New VIS client")

	webConn, _, err := websocket.DefaultDialer.Dial(urlStr, nil)
	if err != nil {
		return vis, err
	}

	vis = &VisClient{webConn: webConn}

	vis.UsersChangedChannel = make(chan []string, usersChangedChannelSize)

	go vis.processMessages()

	if err = vis.subscribe("Attribute.Vehicle.UserIdentification.Users", vis.handleUsersChanged); err != nil {
		return vis, err
	}

	return vis, err
}

// GetVIN returns VIN
func (vis *VisClient) GetVIN() (vin string, err error) {
	if vis.vin == "" {
		rsp, err := vis.processRequest(&visRequest{Action: "get",
			Path: "Attribute.Vehicle.VehicleIdentification.VIN"})
		if err != nil {
			return vin, err
		}

		value, err := getValueFromResponse("Attribute.Vehicle.VehicleIdentification.VIN", rsp)
		if err != nil {
			return vin, err
		}

		ok := false
		if vis.vin, ok = value.(string); !ok {
			return vin, errors.New("Wrong VIN type")
		}
	}

	log.WithField("VIN", vis.vin).Debug("Get VIN")

	return vis.vin, err
}

// GetUsers returns user list
func (vis *VisClient) GetUsers() (users []string, err error) {
	if vis.users == nil {
		rsp, err := vis.processRequest(&visRequest{Action: "get",
			Path: "Attribute.Vehicle.UserIdentification.Users"})
		if err != nil {
			return users, err
		}

		if err = vis.setUsers(rsp); err != nil {
			return users, err
		}
	}

	log.WithField("users", vis.users).Debug("Get users")

	return vis.users, err
}

// Close closes vis client
func (vis *VisClient) Close() (err error) {
	log.Debug("Close VIS client")

	// unsubscribe from all subscriptions
	if _, err := vis.processRequest(&visRequest{Action: "unsubscribeAll"}); err != nil {
		log.Errorf("Can't unsubscribe from subscriptions: %s", err)
	}

	if err := vis.webConn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		log.Errorf("Can't send close message: %s", err)
	}

	if err := vis.webConn.Close(); err != nil {
		return err
	}

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func getValueFromResponse(path string, rsp *visResponse) (value interface{}, err error) {
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

func (vis *VisClient) processRequest(req *visRequest) (rsp *visResponse, err error) {
	// Generate request ID
	vis.mutex.Lock()
	requestID := vis.requestID
	vis.requestID++
	vis.mutex.Unlock()

	req.RequestID = strconv.FormatUint(requestID, 10)

	message, err := json.Marshal(req)
	if err != nil {
		return rsp, err
	}

	// Store channel in the requests map
	rspChannel := make(chan visResponse)
	vis.requests.Store(requestID, rspChannel)

	log.WithField("request", string(message)).Debug("VIS request")

	err = vis.webConn.WriteMessage(websocket.TextMessage, []byte(message))
	if err != nil {
		vis.requests.Delete(requestID)
		return rsp, err
	}

	// Wait response or timeout
	select {
	case <-time.After(websocketTimeout):
		err = errors.New("Wait response timeout")
	case r := <-rspChannel:
		if r.Error != nil {
			return rsp, fmt.Errorf("Error: %d, message: %s, reason: %s",
				r.Error.Number, r.Error.Message, r.Error.Reason)
		}
		rsp = &r
	}

	vis.requests.Delete(requestID)

	return rsp, err
}

func (vis *VisClient) processResponse(rsp *visResponse) {
	requestID, err := strconv.ParseUint(*rsp.RequestID, 10, 64)
	if err != nil {
		log.Errorf("Error parsing VIS request ID: %s", err)
		return
	}

	// serve pending request
	requestFound := false
	vis.requests.Range(func(key, value interface{}) bool {
		if key.(uint64) == requestID {
			requestFound = true
			value.(chan visResponse) <- *rsp
			return false
		}
		return true
	})

	if !requestFound {
		log.Warningf("Unexpected request id: %v", requestID)
	}
}

func (vis *VisClient) processSubscriptions(rsp *visResponse) {
	// serve subscriptions
	subscriptionFound := false
	vis.subscribeMap.Range(func(key, value interface{}) bool {
		if key.(string) == *rsp.SubscriptionID {
			subscriptionFound = true
			value.(func(*visResponse))(rsp)
			return false
		}
		return true
	})

	if !subscriptionFound {
		log.Warningf("Unexpected subscription id: %v", rsp.SubscriptionID)
	}
}

func (vis *VisClient) processMessages() {
	for {
		_, message, err := vis.webConn.ReadMessage()
		if err != nil {
			// Don't show error no connection close
			if !strings.Contains(err.Error(), "use of closed network connection") &&
				!websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				log.Errorf("Error reading VIS message: %s", err)
			}
			return
		}

		log.WithField("response", string(message)).Debug("VIS response")

		var rsp visResponse

		err = json.Unmarshal(message, &rsp)
		if err != nil {
			log.Errorf("Error parsing VIS response: %s", err)
			continue
		}

		if rsp.RequestID != nil {
			vis.processResponse(&rsp)
		} else if rsp.Action == "subscription" {
			vis.processSubscriptions(&rsp)
		}
	}
}

func (vis *VisClient) setUsers(rsp *visResponse) (err error) {
	vis.mutex.Lock()
	defer vis.mutex.Unlock()

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

func (vis *VisClient) handleUsersChanged(rsp *visResponse) {
	if err := vis.setUsers(rsp); err != nil {
		log.Errorf("Can't set users: %s", err)
		return
	}

	vis.UsersChangedChannel <- vis.users

	log.WithField("users", vis.users).Debug("Users changed")
}

func (vis *VisClient) subscribe(path string, callback func(*visResponse)) (err error) {
	resp, err := vis.processRequest(&visRequest{Action: "subscribe", Path: path})
	if err != nil {
		return err
	}

	if resp.SubscriptionID == nil {
		return errors.New("No subscriptionID in response")
	}

	vis.subscribeMap.Store(*resp.SubscriptionID, callback)

	return nil
}
