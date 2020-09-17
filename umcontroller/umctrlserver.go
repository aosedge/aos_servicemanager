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
	"errors"
	"io"
	"net"
	"strings"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "gitpct.epam.com/epmd-aepr/aos_common/api/updatemanager"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// UmCtrlServer gRPC update managers controller server
type umCtrlServer struct {
	url          string
	grpcServer   *grpc.Server
	listener     net.Listener
	controllerCh chan umCtrlInternalMsg
}

/*******************************************************************************
 * public
 ******************************************************************************/

// NewServer create update controller server
func newServer(cfg config.UmController, ch chan umCtrlInternalMsg, insecure bool) (server *umCtrlServer, err error) {
	log.Debug("newServer on ", cfg.ServerURL)
	server = &umCtrlServer{controllerCh: ch}

	var opts []grpc.ServerOption

	if insecure == false {
		creds, err := credentials.NewServerTLSFromFile(cfg.Cert, cfg.Key)
		if err != nil {
			return nil, err
		}

		opts = []grpc.ServerOption{grpc.Creds(creds)}
	} else {
		log.Info("GRPC server starts in insecure mode")
	}

	server.url = cfg.ServerURL

	server.grpcServer = grpc.NewServer(opts...)

	pb.RegisterUpdateControllerServer(server.grpcServer, server)

	return server, nil
}

// Start start update controller server
func (server *umCtrlServer) Start() (err error) {
	server.listener, err = net.Listen("tcp", server.url)
	if err != nil {
		return err
	}

	go server.grpcServer.Serve(server.listener)
	return nil
}

// Stop stop update controller server
func (server *umCtrlServer) Stop() {
	if server.grpcServer != nil {
		server.grpcServer.Stop()
	}

	if server.listener != nil {
		server.listener.Close()
	}
}

// RegisterUM stop update controller server call back
func (server *umCtrlServer) RegisterUM(stream pb.UpdateController_RegisterUMServer) (err error) {
	statusMsg, err := stream.Recv()
	if err == io.EOF {
		log.Warn("Unexpected end of UM stream")
	}

	if err != nil {
		log.Error("Error receive message from UM ", err)
		return err
	}

	log.Debugf("Register UM id %s status %s", statusMsg.GetUmId(), statusMsg.GetUmState().String())

	handler, ch, err := newUmHandler(statusMsg.GetUmId(), stream, server.controllerCh, statusMsg.GetUmState())
	if err != nil {
		err = errors.New("can't create handler")
		return err
	}

	openConnectionMsg := umCtrlInternalMsg{
		umID:        statusMsg.GetUmId(),
		handler:     handler,
		requestType: openConnection,
		status:      getUmStatusFromUmMessage(statusMsg),
	}

	server.controllerCh <- openConnectionMsg

	//wait for close
	<-ch

	closeConnectionMsg := umCtrlInternalMsg{
		umID:        statusMsg.GetUmId(),
		requestType: closeConnection,
	}
	server.controllerCh <- closeConnectionMsg

	return nil
}

func getUmStatusFromUmMessage(msg *pb.UpdateStatus) (status umStatus) {
	status.umState = msg.GetUmState().String()

	for _, component := range msg.GetComponents() {
		if component.GetId() == "" {
			continue
		}

		status.componsStatus = append(status.componsStatus, systemComponentStatus{
			id:            component.GetId(),
			vendorVersion: component.GetVendorVersion(),
			aosVersion:    component.GetAosVersion(),
			status:        strings.ToLower(component.GetStatus().String()),
			err:           component.GetError(),
		})
	}

	return status
}
