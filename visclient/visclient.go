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
	websocketTimeout = 3
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
	webConn *websocket.Conn

	requests sync.Map

	vin   string
	users []string

	mutex     sync.Mutex
	requestID uint64
}

type errorInfo struct {
	Number  int
	Reason  string
	Message string
}

type visRequest struct {
	Action    string `json:"action"`
	Path      string `json:"path"`
	RequestID string `json:"requestId"`
}

type visResponse struct {
	Action         string      `json:"action"`
	RequestID      string      `json:"requestId"`
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

	go vis.processMessages()

	return vis, err
}

// GetVIN returns VIN
func (vis *VisClient) GetVIN() (vin string, err error) {
	if vis.vin == "" {
		resp, err := vis.processRequest(&visRequest{Action: "get",
			Path: "Attribute.Vehicle.VehicleIdentification.VIN"})
		if err != nil {
			return vin, err
		}

		var ok bool

		if vis.vin, ok = resp.Value.(string); !ok {
			return vin, errors.New("Wrong VIN type")
		}
	}

	log.WithField("VIN", vis.vin).Debug("Get VIN")

	return vis.vin, err
}

// GetUsers returns user list
func (vis *VisClient) GetUsers() (users []string, err error) {
	if vis.users == nil {
		resp, err := vis.processRequest(&visRequest{Action: "get",
			Path: "Attribute.Vehicle.UserIdentification.Users"})
		if err != nil {
			return users, err
		}

		itfs, ok := resp.Value.([]interface{})
		if !ok {
			return users, errors.New("Wrong users type")
		}

		vis.users = make([]string, len(itfs))

		for i, itf := range itfs {
			value, ok := itf.(string)
			if !ok {
				return users, errors.New("Wrong users type")
			}
			vis.users[i] = value
		}
	}

	log.WithField("users", vis.users).Debug("Get users")

	return vis.users, err
}

// Close closes vis client
func (vis *VisClient) Close() (err error) {
	log.Debug("Close VIS client")

	if err := vis.webConn.Close(); err != nil {
		return err
	}

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

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
	respChannel := make(chan visResponse)
	vis.requests.Store(requestID, respChannel)

	log.WithField("request", string(message)).Debug("VIS request")

	err = vis.webConn.WriteMessage(websocket.TextMessage, []byte(message))
	if err != nil {
		vis.requests.Delete(requestID)
		return rsp, err
	}

	// Wait response or timeout
	select {
	case <-time.After(websocketTimeout * time.Second):
		err = errors.New("Wait response timeout")
	case r := <-respChannel:
		if r.Error != nil {
			return rsp, fmt.Errorf("Error: %d, message: %s, reason: %s",
				r.Error.Number, r.Error.Message, r.Error.Reason)
		}
		rsp = &r
	}

	vis.requests.Delete(requestID)

	return rsp, err
}

func (vis *VisClient) processMessages() {
	for {
		_, message, err := vis.webConn.ReadMessage()
		if err != nil {
			// Don't show error no connection close
			if !strings.Contains(err.Error(), "use of closed network connection") {
				log.Errorf("Error reading VIS message: %s", err)
			}
			return
		}

		log.WithField("response", string(message)).Debug("VIS response")

		var resp visResponse

		err = json.Unmarshal(message, &resp)
		if err != nil {
			log.Errorf("Error parsing VIS response: %s", err)
			continue
		}

		requestID, err := strconv.ParseUint(resp.RequestID, 10, 64)
		if err != nil {
			log.Errorf("Error parsing VIS request ID: %s", err)
			continue
		}

		// serve pending request

		requestFound := false
		vis.requests.Range(func(key, value interface{}) bool {
			if key.(uint64) == requestID {
				requestFound = true
				value.(chan visResponse) <- resp
				return false
			}
			return true
		})

		if !requestFound {
			log.Warningf("Unexpected request id: %v", requestID)
		}
	}
}
