// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2021 Renesas Inc.
// Copyright 2021 EPAM Systems Inc.
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

package unitstatushandler_test

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/unitstatushandler"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type testSender struct {
	statusChannel chan amqp.UnitStatus
}

type testBoardConfigUpdater struct {
	boardConfigInfo []amqp.BoardConfigInfo
	updateVersion   string
	updateError     string
}

type testComponentUpdater struct {
	componentsInfo []amqp.ComponentInfo
	updateError    string
	statusChannel  chan amqp.ComponentInfo
}

type testLayerUpdater struct {
	layersInfo  []amqp.LayerInfo
	updateError string
}

type testServiceUpdater struct {
	servicesInfo []amqp.ServiceInfo
	updateError  string
}

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
 * Tests
 ******************************************************************************/

func TestSendInitialStatus(t *testing.T) {
	expectedUnitStatus := amqp.UnitStatus{
		BoardConfig: []amqp.BoardConfigInfo{
			{VendorVersion: "1.0", Status: amqp.InstalledStatus},
		},
		Components: []amqp.ComponentInfo{
			{ID: "comp0", VendorVersion: "1.0", Status: amqp.InstalledStatus},
			{ID: "comp1", VendorVersion: "1.1", Status: amqp.InstalledStatus},
			{ID: "comp2", VendorVersion: "1.2", Status: amqp.InstalledStatus},
		},
		Layers: []amqp.LayerInfo{
			{ID: "layer0", Digest: "digest0", AosVersion: 1, Status: amqp.InstalledStatus},
			{ID: "layer1", Digest: "digest1", AosVersion: 2, Status: amqp.InstalledStatus},
			{ID: "layer2", Digest: "digest2", AosVersion: 3, Status: amqp.InstalledStatus},
		},
		Services: []amqp.ServiceInfo{
			{ID: "service0", AosVersion: 1, Status: amqp.InstalledStatus},
			{ID: "service1", AosVersion: 1, Status: amqp.InstalledStatus},
			{ID: "service2", AosVersion: 1, Status: amqp.InstalledStatus},
		},
	}

	boardConfigUpdater := newTestBoardConfigUpdater(expectedUnitStatus.BoardConfig)
	componentUpdater := newTestComponentUpdater(expectedUnitStatus.Components)
	layerUpdater := newTestLayerUpdater(expectedUnitStatus.Layers)
	serviceUpdater := newTestServiceUpdater(expectedUnitStatus.Services)
	sender := newTestSender()

	statusHandler, err := unitstatushandler.New(&config.Config{UnitStatusTimeout: 3},
		boardConfigUpdater, componentUpdater, layerUpdater, serviceUpdater, sender)
	if err != nil {
		t.Fatalf("Can't create unit status handler: %s", err)
	}
	defer statusHandler.Close()

	if err = statusHandler.Init(); err != nil {
		t.Fatalf("Can't initialize status handler: %s", err)
	}

	receivedUnitStatus, err := sender.waitForStatus(5 * time.Second)
	if err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	if err = compareUnitStatus(receivedUnitStatus, expectedUnitStatus); err != nil {
		t.Errorf("Wrong unit status received: %v, expected: %v", receivedUnitStatus, expectedUnitStatus)
	}
}

func TestUpdateBoardConfig(t *testing.T) {
	boardConfigUpdater := newTestBoardConfigUpdater(
		[]amqp.BoardConfigInfo{{VendorVersion: "1.0", Status: amqp.InstalledStatus}})
	componentUpdater := newTestComponentUpdater(nil)
	layerUpdater := newTestLayerUpdater(nil)
	serviceUpdater := newTestServiceUpdater(nil)
	sender := newTestSender()

	statusHandler, err := unitstatushandler.New(&config.Config{UnitStatusTimeout: 3},
		boardConfigUpdater, componentUpdater, layerUpdater, serviceUpdater, sender)
	if err != nil {
		t.Fatalf("Can't create unit status handler: %s", err)
	}
	defer statusHandler.Close()

	if err = statusHandler.Init(); err != nil {
		t.Fatalf("Can't initialize status handler: %s", err)
	}

	if _, err = sender.waitForStatus(5 * time.Second); err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	// success update

	boardConfigUpdater.boardConfigInfo = []amqp.BoardConfigInfo{{VendorVersion: "1.1", Status: amqp.InstalledStatus}}
	expectedUnitStatus := amqp.UnitStatus{
		BoardConfig: boardConfigUpdater.boardConfigInfo,
		Components:  []amqp.ComponentInfo{},
		Layers:      []amqp.LayerInfo{},
		Services:    []amqp.ServiceInfo{},
	}

	boardConfigUpdater.updateVersion = "1.1"

	statusHandler.ProcessDesiredStatus(amqp.DecodedDesiredStatus{BoardConfig: json.RawMessage("{}")})

	receivedUnitStatus, err := sender.waitForStatus(35 * time.Second)
	if err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	if !reflect.DeepEqual(receivedUnitStatus, expectedUnitStatus) {
		t.Errorf("Wrong unit status received: %v", receivedUnitStatus)
	}

	// failed update

	boardConfigUpdater.boardConfigInfo = []amqp.BoardConfigInfo{{VendorVersion: "1.2", Status: amqp.ErrorStatus, Error: "some error occurs"}}
	expectedUnitStatus.BoardConfig = append(expectedUnitStatus.BoardConfig, boardConfigUpdater.boardConfigInfo[0])

	boardConfigUpdater.updateVersion = "1.2"
	boardConfigUpdater.updateError = "some error occurs"

	statusHandler.ProcessDesiredStatus(amqp.DecodedDesiredStatus{BoardConfig: json.RawMessage("{}")})

	if receivedUnitStatus, err = sender.waitForStatus(35 * time.Second); err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	if err = compareUnitStatus(receivedUnitStatus, expectedUnitStatus); err != nil {
		t.Errorf("Wrong unit status received: %v, expected: %v", receivedUnitStatus, expectedUnitStatus)
	}
}

func TestUpdateComponents(t *testing.T) {
	boardConfigUpdater := newTestBoardConfigUpdater([]amqp.BoardConfigInfo{
		{VendorVersion: "1.0", Status: amqp.InstalledStatus}})
	componentUpdater := newTestComponentUpdater([]amqp.ComponentInfo{
		{ID: "comp0", VendorVersion: "1.0", Status: amqp.InstalledStatus},
		{ID: "comp1", VendorVersion: "1.0", Status: amqp.InstalledStatus},
		{ID: "comp2", VendorVersion: "1.0", Status: amqp.InstalledStatus},
	})
	layerUpdater := newTestLayerUpdater(nil)
	serviceUpdater := newTestServiceUpdater(nil)
	sender := newTestSender()

	statusHandler, err := unitstatushandler.New(&config.Config{UnitStatusTimeout: 3},
		boardConfigUpdater, componentUpdater, layerUpdater, serviceUpdater, sender)
	if err != nil {
		t.Fatalf("Can't create unit status handler: %s", err)
	}
	defer statusHandler.Close()

	if err = statusHandler.Init(); err != nil {
		t.Fatalf("Can't initialize status handler: %s", err)
	}

	if _, err = sender.waitForStatus(5 * time.Second); err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	// success update

	expectedUnitStatus := amqp.UnitStatus{
		BoardConfig: boardConfigUpdater.boardConfigInfo,
		Components: []amqp.ComponentInfo{
			{ID: "comp0", VendorVersion: "2.0", Status: amqp.InstalledStatus},
			{ID: "comp1", VendorVersion: "1.0", Status: amqp.InstalledStatus},
			{ID: "comp2", VendorVersion: "2.0", Status: amqp.InstalledStatus},
		},
		Layers:   []amqp.LayerInfo{},
		Services: []amqp.ServiceInfo{},
	}

	statusHandler.ProcessDesiredStatus(amqp.DecodedDesiredStatus{
		Components: []amqp.ComponentInfoFromCloud{
			{ID: "comp0", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2.0"}},
			{ID: "comp2", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2.0"}},
		},
	})

	receivedUnitStatus, err := sender.waitForStatus(35 * time.Second)
	if err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	if err = compareUnitStatus(receivedUnitStatus, expectedUnitStatus); err != nil {
		t.Errorf("Wrong unit status received: %v, expected: %v", receivedUnitStatus, expectedUnitStatus)
	}

	// failed update

	componentUpdater.updateError = "some error occurs"

	expectedUnitStatus = amqp.UnitStatus{
		BoardConfig: boardConfigUpdater.boardConfigInfo,
		Components: []amqp.ComponentInfo{
			{ID: "comp0", VendorVersion: "2.0", Status: amqp.InstalledStatus},
			{ID: "comp1", VendorVersion: "1.0", Status: amqp.InstalledStatus},
			{ID: "comp1", VendorVersion: "2.0", Status: amqp.ErrorStatus, Error: componentUpdater.updateError},
			{ID: "comp2", VendorVersion: "2.0", Status: amqp.InstalledStatus},
		},
		Layers:   []amqp.LayerInfo{},
		Services: []amqp.ServiceInfo{},
	}

	statusHandler.ProcessDesiredStatus(amqp.DecodedDesiredStatus{
		Components: []amqp.ComponentInfoFromCloud{
			{ID: "comp1", VersionFromCloud: amqp.VersionFromCloud{VendorVersion: "2.0"}},
		}})

	if receivedUnitStatus, err = sender.waitForStatus(35 * time.Second); err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	if err = compareUnitStatus(receivedUnitStatus, expectedUnitStatus); err != nil {
		t.Errorf("Wrong unit status received: %v, expected: %v", receivedUnitStatus, expectedUnitStatus)
	}
}

func TestUpdateLayers(t *testing.T) {
	boardConfigUpdater := newTestBoardConfigUpdater(
		[]amqp.BoardConfigInfo{{VendorVersion: "1.0", Status: amqp.InstalledStatus}})
	componentUpdater := newTestComponentUpdater(nil)
	layerUpdater := newTestLayerUpdater([]amqp.LayerInfo{
		{ID: "layer0", Digest: "digest0", AosVersion: 0, Status: amqp.InstalledStatus},
		{ID: "layer1", Digest: "digest1", AosVersion: 0, Status: amqp.InstalledStatus},
		{ID: "layer2", Digest: "digest2", AosVersion: 0, Status: amqp.InstalledStatus},
	})
	serviceUpdater := newTestServiceUpdater(nil)
	sender := newTestSender()

	statusHandler, err := unitstatushandler.New(&config.Config{UnitStatusTimeout: 3},
		boardConfigUpdater, componentUpdater, layerUpdater, serviceUpdater, sender)
	if err != nil {
		t.Fatalf("Can't create unit status handler: %s", err)
	}
	defer statusHandler.Close()

	if err = statusHandler.Init(); err != nil {
		t.Fatalf("Can't initialize status handler: %s", err)
	}

	if _, err = sender.waitForStatus(5 * time.Second); err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	// success update

	expectedUnitStatus := amqp.UnitStatus{
		BoardConfig: boardConfigUpdater.boardConfigInfo,
		Components:  []amqp.ComponentInfo{},
		Layers: []amqp.LayerInfo{
			{ID: "layer1", Digest: "digest1", AosVersion: 0, Status: amqp.InstalledStatus},
			{ID: "layer3", Digest: "digest3", AosVersion: 1, Status: amqp.InstalledStatus},
			{ID: "layer4", Digest: "digest4", AosVersion: 1, Status: amqp.InstalledStatus},
			{Digest: "digest0", Status: amqp.RemovedStatus},
			{Digest: "digest2", Status: amqp.RemovedStatus},
		},
		Services: []amqp.ServiceInfo{},
	}

	statusHandler.ProcessDesiredStatus(amqp.DecodedDesiredStatus{
		Layers: []amqp.LayerInfoFromCloud{
			{ID: "layer1", Digest: "digest1", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 0}},
			{ID: "layer3", Digest: "digest3", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 1}},
			{ID: "layer4", Digest: "digest4", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 1}},
		}})

	receivedUnitStatus, err := sender.waitForStatus(35 * time.Second)
	if err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	if err = compareUnitStatus(receivedUnitStatus, expectedUnitStatus); err != nil {
		t.Errorf("Wrong unit status received: %v, expected: %v", receivedUnitStatus, expectedUnitStatus)
	}

	// failed update

	layerUpdater.updateError = "some error occurs"

	expectedUnitStatus = amqp.UnitStatus{
		BoardConfig: boardConfigUpdater.boardConfigInfo,
		Components:  []amqp.ComponentInfo{},
		Layers: []amqp.LayerInfo{
			{ID: "layer3", Digest: "digest3", AosVersion: 1, Status: amqp.InstalledStatus},
			{ID: "layer4", Digest: "digest4", AosVersion: 1, Status: amqp.InstalledStatus},
			{Digest: "digest0", Status: amqp.RemovedStatus},
			{Digest: "digest2", Status: amqp.RemovedStatus},
			{ID: "layer5", Digest: "digest5", AosVersion: 1, Status: amqp.ErrorStatus, Error: layerUpdater.updateError},
			{Digest: "digest1", Status: amqp.ErrorStatus, Error: layerUpdater.updateError},
		},
		Services: []amqp.ServiceInfo{},
	}

	statusHandler.ProcessDesiredStatus(amqp.DecodedDesiredStatus{
		Layers: []amqp.LayerInfoFromCloud{
			{ID: "layer3", Digest: "digest3", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 1}},
			{ID: "layer4", Digest: "digest4", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 1}},
			{ID: "layer5", Digest: "digest5", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 1}},
		}})

	if receivedUnitStatus, err = sender.waitForStatus(35 * time.Second); err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	if err = compareUnitStatus(receivedUnitStatus, expectedUnitStatus); err != nil {
		t.Errorf("Wrong unit status received: %v, expected: %v", receivedUnitStatus, expectedUnitStatus)
	}
}

func TestUpdateServices(t *testing.T) {
	boardConfigUpdater := newTestBoardConfigUpdater(
		[]amqp.BoardConfigInfo{{VendorVersion: "1.0", Status: amqp.InstalledStatus}})
	componentUpdater := newTestComponentUpdater(nil)
	layerUpdater := newTestLayerUpdater(nil)
	serviceUpdater := newTestServiceUpdater([]amqp.ServiceInfo{
		{ID: "service0", AosVersion: 0, Status: amqp.InstalledStatus},
		{ID: "service1", AosVersion: 0, Status: amqp.InstalledStatus},
		{ID: "service2", AosVersion: 0, Status: amqp.InstalledStatus},
	})
	sender := newTestSender()

	statusHandler, err := unitstatushandler.New(&config.Config{UnitStatusTimeout: 3},
		boardConfigUpdater, componentUpdater, layerUpdater, serviceUpdater, sender)
	if err != nil {
		t.Fatalf("Can't create unit status handler: %s", err)
	}
	defer statusHandler.Close()

	if err = statusHandler.Init(); err != nil {
		t.Fatalf("Can't initialize status handler: %s", err)
	}

	if _, err = sender.waitForStatus(5 * time.Second); err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	// success update

	expectedUnitStatus := amqp.UnitStatus{
		BoardConfig: boardConfigUpdater.boardConfigInfo,
		Components:  []amqp.ComponentInfo{},
		Layers:      []amqp.LayerInfo{},
		Services: []amqp.ServiceInfo{
			{ID: "service0", AosVersion: 0, Status: amqp.InstalledStatus},
			{ID: "service1", AosVersion: 1, Status: amqp.InstalledStatus},
			{ID: "service2", Status: amqp.RemovedStatus},
			{ID: "service3", AosVersion: 1, Status: amqp.InstalledStatus},
		},
	}

	statusHandler.ProcessDesiredStatus(amqp.DecodedDesiredStatus{
		Services: []amqp.ServiceInfoFromCloud{
			{ID: "service0", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 0}},
			{ID: "service1", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 1}},
			{ID: "service3", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 1}},
		}})

	receivedUnitStatus, err := sender.waitForStatus(35 * time.Second)
	if err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	if err = compareUnitStatus(receivedUnitStatus, expectedUnitStatus); err != nil {
		t.Errorf("Wrong unit status received: %v, expected: %v", receivedUnitStatus, expectedUnitStatus)
	}

	// failed update

	serviceUpdater.updateError = "some error occurs"

	expectedUnitStatus = amqp.UnitStatus{
		BoardConfig: boardConfigUpdater.boardConfigInfo,
		Components:  []amqp.ComponentInfo{},
		Layers:      []amqp.LayerInfo{},
		Services: []amqp.ServiceInfo{
			{ID: "service0", AosVersion: 0, Status: amqp.ErrorStatus, Error: serviceUpdater.updateError},
			{ID: "service1", AosVersion: 1, Status: amqp.InstalledStatus},
			{ID: "service2", Status: amqp.RemovedStatus},
			{ID: "service3", AosVersion: 1, Status: amqp.InstalledStatus},
			{ID: "service3", AosVersion: 2, Status: amqp.ErrorStatus, Error: serviceUpdater.updateError},
			{ID: "service4", AosVersion: 2, Status: amqp.ErrorStatus, Error: serviceUpdater.updateError},
		},
	}

	statusHandler.ProcessDesiredStatus(amqp.DecodedDesiredStatus{
		Services: []amqp.ServiceInfoFromCloud{
			{ID: "service1", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 1}},
			{ID: "service3", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 2}},
			{ID: "service4", VersionFromCloud: amqp.VersionFromCloud{AosVersion: 2}},
		}})

	if receivedUnitStatus, err = sender.waitForStatus(35 * time.Second); err != nil {
		t.Fatalf("Can't receive unit status: %s", err)
	}

	if err = compareUnitStatus(receivedUnitStatus, expectedUnitStatus); err != nil {
		t.Errorf("Wrong unit status received: %v, expected: %v", receivedUnitStatus, expectedUnitStatus)
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func compareStatus(len1, len2 int, compare func(index1, index2 int) bool) (err error) {
	if len1 != len2 {
		return aoserrors.New("data mismatch")
	}

	for index1 := 0; index1 < len1; index1++ {
		found := false

		for index2 := 0; index2 < len2; index2++ {
			if compare(index1, index2) {
				found = true
				break
			}
		}

		if !found {
			return aoserrors.New("data mismatch")
		}
	}

	for index2 := 0; index2 < len2; index2++ {
		found := false

		for index1 := 0; index1 < len1; index1++ {
			if compare(index1, index2) {
				found = true
				break
			}
		}

		if !found {
			return aoserrors.New("data mismatch")
		}
	}

	return nil
}

func compareUnitStatus(status1, status2 amqp.UnitStatus) (err error) {
	if err = compareStatus(len(status1.BoardConfig), len(status2.BoardConfig),
		func(index1, index2 int) (result bool) {
			return status1.BoardConfig[index1] == status2.BoardConfig[index2]
		}); err != nil {
		return err
	}

	if err = compareStatus(len(status1.Components), len(status2.Components),
		func(index1, index2 int) (result bool) {
			return status1.Components[index1] == status2.Components[index2]
		}); err != nil {
		return err
	}

	if err = compareStatus(len(status1.Layers), len(status2.Layers),
		func(index1, index2 int) (result bool) {
			return status1.Layers[index1] == status2.Layers[index2]
		}); err != nil {
		return err
	}

	if err = compareStatus(len(status1.Services), len(status2.Services),
		func(index1, index2 int) (result bool) {
			return status1.Services[index1] == status2.Services[index2]
		}); err != nil {
		return err
	}

	return nil
}

/*******************************************************************************
 * testSender
 ******************************************************************************/

func newTestSender() (sender *testSender) {
	return &testSender{statusChannel: make(chan amqp.UnitStatus, 1)}
}

func (sender *testSender) SendUnitStatus(unitStatus amqp.UnitStatus) (err error) {
	sender.statusChannel <- unitStatus

	return nil
}

func (sender *testSender) waitForStatus(timeout time.Duration) (status amqp.UnitStatus, err error) {
	select {
	case receivedUnitStatus := <-sender.statusChannel:
		return receivedUnitStatus, nil

	case <-time.After(timeout):
		return status, aoserrors.New("receive status timeout")
	}
}

/*******************************************************************************
 * testBoardConfigUpdater
 ******************************************************************************/

func newTestBoardConfigUpdater(boardConfigInfo []amqp.BoardConfigInfo) (updater *testBoardConfigUpdater) {
	return &testBoardConfigUpdater{boardConfigInfo: boardConfigInfo}
}

func (updater *testBoardConfigUpdater) GetBoardConfigInfo() (info []amqp.BoardConfigInfo, err error) {
	return updater.boardConfigInfo, nil
}

func (updater *testBoardConfigUpdater) CheckBoardConfig(configJSON json.RawMessage) (version string, err error) {
	if updater.updateError != "" {
		err = errors.New(updater.updateError)
	}

	return updater.updateVersion, err
}

func (updater *testBoardConfigUpdater) UpdateBoardConfig(configJSON json.RawMessage) (err error) {
	if updater.updateError != "" {
		err = errors.New(updater.updateError)
	}

	return err
}

/*******************************************************************************
 * testComponentUpdater
 ******************************************************************************/

func newTestComponentUpdater(componentsInfo []amqp.ComponentInfo) (updater *testComponentUpdater) {
	return &testComponentUpdater{componentsInfo: componentsInfo, statusChannel: make(chan amqp.ComponentInfo)}
}

func (updater *testComponentUpdater) GetComponentsInfo() (info []amqp.ComponentInfo, err error) {
	return updater.componentsInfo, nil
}

func (updater *testComponentUpdater) UpdateComponents(components []amqp.ComponentInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (err error) {
	for _, component := range components {
		componentInfo := amqp.ComponentInfo{
			ID:            component.ID,
			AosVersion:    component.AosVersion,
			VendorVersion: component.VendorVersion,
			Status:        amqp.InstalledStatus,
		}

		if updater.updateError != "" {
			componentInfo.Status = amqp.ErrorStatus
			componentInfo.Error = updater.updateError
		}

		updater.statusChannel <- componentInfo
	}

	return nil
}

func (updater *testComponentUpdater) UpdateStatus() (statusChannel <-chan amqp.ComponentInfo) {
	return updater.statusChannel
}

/*******************************************************************************
 * testLayerUpdater
 ******************************************************************************/

func newTestLayerUpdater(layersInfo []amqp.LayerInfo) (updater *testLayerUpdater) {
	return &testLayerUpdater{layersInfo: layersInfo}
}

func (updater *testLayerUpdater) GetLayersInfo() (layers []amqp.LayerInfo, err error) {
	return updater.layersInfo, nil
}

func (updater *testLayerUpdater) InstallLayer(layerInfo amqp.LayerInfoFromCloud, chains []amqp.CertificateChain,
	certs []amqp.Certificate) (statusChannel <-chan amqp.LayerInfo) {
	channel := make(chan amqp.LayerInfo)

	go func() {
		defer close(channel)

		layerStatus := amqp.LayerInfo{
			ID:         layerInfo.ID,
			Digest:     layerInfo.Digest,
			AosVersion: layerInfo.AosVersion,
			Status:     amqp.InstalledStatus,
		}

		if updater.updateError != "" {
			layerStatus.Status = amqp.ErrorStatus
			layerStatus.Error = updater.updateError
		}

		channel <- layerStatus
	}()

	return channel
}

func (updater *testLayerUpdater) UninstallLayer(digest string) (statusChannel <-chan amqp.LayerInfo) {
	channel := make(chan amqp.LayerInfo)

	go func() {
		defer close(channel)

		layerStatus := amqp.LayerInfo{
			Digest: digest,
			Status: amqp.RemovedStatus,
		}

		if updater.updateError != "" {
			layerStatus.Status = amqp.ErrorStatus
			layerStatus.Error = updater.updateError
		}

		channel <- layerStatus
	}()

	return channel
}

/*******************************************************************************
 * testServiceUpdater
 ******************************************************************************/

func newTestServiceUpdater(servicesInfo []amqp.ServiceInfo) (updater *testServiceUpdater) {
	return &testServiceUpdater{servicesInfo: servicesInfo}
}

func (updater *testServiceUpdater) GetServicesInfo() (info []amqp.ServiceInfo, err error) {
	return updater.servicesInfo, nil
}

func (updater *testServiceUpdater) InstallService(serviceInfo amqp.ServiceInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (statusChannel <-chan amqp.ServiceInfo) {
	channel := make(chan amqp.ServiceInfo)

	go func() {
		defer close(channel)

		serviceStatus := amqp.ServiceInfo{
			ID:         serviceInfo.ID,
			AosVersion: serviceInfo.AosVersion,
			Status:     amqp.InstalledStatus,
		}

		if updater.updateError != "" {
			serviceStatus.Status = amqp.ErrorStatus
			serviceStatus.Error = updater.updateError
		}

		channel <- serviceStatus
	}()

	return channel
}

func (updater *testServiceUpdater) UninstallService(id string) (statusChannel <-chan amqp.ServiceInfo) {
	channel := make(chan amqp.ServiceInfo)

	go func() {
		defer close(channel)

		serviceStatus := amqp.ServiceInfo{
			ID:     id,
			Status: amqp.RemovedStatus,
		}

		if updater.updateError != "" {
			serviceStatus.Status = amqp.ErrorStatus
			serviceStatus.Error = updater.updateError
		}

		channel <- serviceStatus
	}()

	return channel
}

func (updater *testServiceUpdater) StartServices() {
}

func (updater *testServiceUpdater) StopServices() {
}
