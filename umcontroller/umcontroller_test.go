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

package umcontroller_test

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	pb "gitpct.epam.com/epmd-aepr/aos_common/api/updatemanager"

	"aos_servicemanager/config"
	"aos_servicemanager/umcontroller"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	serverURL = "localhost:8091"
)

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Main
 ******************************************************************************/

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestConnection(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	umCtrlConfig := config.UmController{
		ServerURL: "localhost:8091",
		UmClients: []config.UmClientConfig{
			config.UmClientConfig{UmID: "umID1", Priority: 10},
			config.UmClientConfig{UmID: "umID2", Priority: 0}},
		UpdateDir: tmpDir,
	}
	smConfig := config.Config{UmController: umCtrlConfig}

	umCtrl, err := umcontroller.New(&smConfig, true)
	if err != nil {
		t.Fatalf("Can't create: UM controller %s", err)
	}

	streamUM1, connUM1, err := createClientConnection("umID1", []string{"component1", "component2"})
	if err != nil {
		t.Errorf("Error connect %s", err)
	}

	streamUM2, connUM2, err := createClientConnection("umID2", []string{"component3", "component4"})
	if err != nil {
		t.Errorf("Error connect %s", err)
	}

	streamUM1_copy, connUM1_copy, err := createClientConnection("umID1", []string{"component1", "component2"})
	if err != nil {
		t.Errorf("Error connect %s", err)
	}

	umCtrl.Close()

	components, err := umCtrl.GetSystemComponents()
	if err != nil {
		t.Errorf("Can't get system components %s", err)
	}

	if len(components) != 4 {
		t.Errorf("Incorrect count of components %d", len(components))
	}

	streamUM1.CloseSend()
	connUM1.Close()

	streamUM2.CloseSend()
	connUM2.Close()

	streamUM1_copy.CloseSend()
	connUM1_copy.Close()

	time.Sleep(1 * time.Second)
}

func createClientConnection(clientID string, components []string) (stream pb.UpdateController_RegisterUMClient, conn *grpc.ClientConn, err error) {
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithInsecure())
	opts = append(opts, grpc.WithBlock())

	conn, err = grpc.Dial(serverURL, opts...)
	if err != nil {
		return stream, nil, err
	}

	client := pb.NewUpdateControllerClient(conn)
	stream, err = client.RegisterUM(context.Background())
	if err != nil {
		log.Errorf("Fail call RegisterUM %s", err)
		return stream, nil, err
	}

	umMsg := &pb.UpdateStatus{UmId: clientID, UmState: pb.UmState_IDLE}

	for _, componentId := range components {
		umMsg.Components = append(umMsg.Components, &pb.SystemComponent{Id: componentId, VendorVersion: "1",
			Status: pb.ComponentStatus_INSTALLED,
		})
	}

	umMsg.Components = append(umMsg.Components, nil)

	if err = stream.Send(umMsg); err != nil {
		log.Errorf("Fail send update status message %s", err)
	}
	time.Sleep(1 * time.Second)

	return stream, conn, nil
}
