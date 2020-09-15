// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2020 Renesas Inc.
// Copyright 2020 EPAM Systems Inc.
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

package umcontroller

import (
	"io"

	log "github.com/sirupsen/logrus"

	pb "gitpct.epam.com/epmd-aepr/aos_common/api/updatemanager"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type umHandler struct {
	umID           string
	stream         pb.UpdateController_RegisterUMServer
	messageChannel chan umCtrlInternalMsg
	closeChannel   chan bool
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// NewUmHandler create update manager connection handler
func newUmHandler(id string, umStream pb.UpdateController_RegisterUMServer, messageChannel chan umCtrlInternalMsg) (handler *umHandler,
	closeChannel chan bool, err error) {

	handler = &umHandler{umID: id, stream: umStream, messageChannel: messageChannel}
	handler.closeChannel = make(chan bool)

	go handler.receiveData()

	return handler, handler.closeChannel, err
}

// Close close connection
func (handler *umHandler) Close() {
	log.Debug("Close umhandler with UMID = ", handler.umID)
	handler.closeChannel <- true
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (handler *umHandler) receiveData() {
	defer func() { handler.closeChannel <- true }()

	for {
		_, err := handler.stream.Recv()
		if err == io.EOF {
			log.Debug("End of connection ", handler.umID)
			return
		}

		if err != nil {
			log.Debugf("End of connection %s with error: %s", handler.umID, err)
			return
		}
	}
}
