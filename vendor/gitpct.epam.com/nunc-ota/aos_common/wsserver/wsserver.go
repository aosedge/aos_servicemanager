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

package wsserver

import (
	"container/list"
	"context"
	"net/http"
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
	writeSocketTimeout = 10 * time.Second
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// MessageProcessor specifies ws client interface
type MessageProcessor interface {
	ProcessMessage(messageType int, message []byte) (response []byte, err error)
}

// SendMessage send message function
type SendMessage func(messageType int, message []byte) (err error)

// NewMessageProcessor function called to create new message processor
type NewMessageProcessor func(sendMessage SendMessage) (processor MessageProcessor, err error)

// Server websocket server structure
type Server struct {
	name                string
	newMessageProcessor NewMessageProcessor
	httpServer          *http.Server
	upgrader            websocket.Upgrader
	sync.Mutex
	clients *list.List
}

type clientHandler struct {
	MessageProcessor
	name       string
	connection *websocket.Conn
	sync.Mutex
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new Web socket server
func New(name, url, cert, key string, newMessageProcessor NewMessageProcessor) (server *Server, err error) {
	server = &Server{
		name:                name,
		newMessageProcessor: newMessageProcessor,
		upgrader: websocket.Upgrader{
			// TODO: Implement proper check origin validation
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	log.WithField("server", server.name).Debug("Create ws server")

	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/", server.handleConnection)

	server.httpServer = &http.Server{Addr: url, Handler: serveMux}
	server.clients = list.New()

	go func(crt, key string) {
		log.WithFields(log.Fields{"address": url, "crt": crt, "key": key}).Debug("Listen for clients")

		if err := server.httpServer.ListenAndServeTLS(crt, key); err != http.ErrServerClosed {
			log.Error("Server listening error: ", err)
			return
		}
	}(cert, key)

	return server, nil
}

// Close closes web socket server and all connections
func (server *Server) Close() {
	log.WithField("server", server.name).Debug("Close ws server")

	server.Lock()
	defer server.Unlock()

	var next *list.Element
	for element := server.clients.Front(); element != nil; element = next {
		element.Value.(*clientHandler).close(true)
		next = element.Next()
		server.clients.Remove(element)
	}

	server.httpServer.Shutdown(context.Background())
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (handler *clientHandler) close(sendCloseMessage bool) (err error) {
	log.WithFields(log.Fields{
		"RemoteAddr": handler.connection.RemoteAddr(),
		"server":     handler.name}).Info("Close client")

	if sendCloseMessage {
		handler.sendMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}

	return handler.connection.Close()
}

func (handler *clientHandler) run() {
	for {
		messageType, message, err := handler.connection.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure) &&
				!strings.Contains(err.Error(), "use of closed network connection") {
				log.Errorf("Error reading socket: %s", err)
			}

			break
		}

		if messageType == websocket.TextMessage {
			log.WithFields(log.Fields{
				"message": string(message),
				"server":  handler.name}).Debug("Receive message")
		} else {
			log.WithFields(log.Fields{
				"message": message,
				"server":  handler.name}).Debug("Receive message")
		}

		response, err := handler.ProcessMessage(messageType, message)
		if err != nil {
			log.Errorf("Can't process message: %s", err)
			continue
		}

		if response != nil {
			if err := handler.sendMessage(messageType, response); err != nil {
				log.Errorf("Can't send message: %s", err)
			}
		}
	}
}

func (handler *clientHandler) sendMessage(messageType int, data []byte) (err error) {
	handler.Lock()
	defer handler.Unlock()

	if messageType == websocket.TextMessage {
		log.WithFields(log.Fields{
			"message": string(data),
			"server":  handler.name}).Debug("Send message")
	} else {
		log.WithFields(log.Fields{
			"message": data,
			"server":  handler.name}).Debug("Send message")
	}

	if writeSocketTimeout != 0 {
		handler.connection.SetWriteDeadline(time.Now().Add(writeSocketTimeout))
	}

	if err = handler.connection.WriteMessage(messageType, data); err != nil {
		log.Errorf("Can't write message: %s", err)

		handler.connection.Close()

		return err
	}

	return nil
}

func (server *Server) handleConnection(w http.ResponseWriter, r *http.Request) {
	log.WithFields(log.Fields{
		"remoteAddr": r.RemoteAddr,
		"server":     server.name}).Debug("New connection request")

	if websocket.IsWebSocketUpgrade(r) != true {
		log.Error("New connection is not websocket")
		return
	}

	connection, err := server.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("Can't make websocket connection: ", err)
		return
	}

	handler := &clientHandler{name: server.name, connection: connection}

	processor, err := server.newMessageProcessor(handler.sendMessage)
	if err != nil {
		log.Error("Can't create websocket client connection: ", err)
		connection.Close()
		return
	}

	handler.MessageProcessor = processor

	server.Lock()
	clientElement := server.clients.PushBack(handler)
	server.Unlock()

	handler.run()

	server.Lock()
	defer server.Unlock()

	for element := server.clients.Front(); element != nil; element = element.Next() {
		if element == clientElement {
			handler.close(false)
			server.clients.Remove(clientElement)
			break
		}
	}
}
