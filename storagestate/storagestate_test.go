// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2022 Renesas Electronics Corporation.
// Copyright (C) 2022 EPAM Systems, Inc.
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

package storagestate_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/storagestate"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/sha3"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const waitChannelTimeout = 100 * time.Millisecond

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type testStorageInterface struct {
	data              map[string]storagestate.StorageStateInstanceInfo
	errorSave         bool
	errorSaveCheckSum bool
	errorGet          bool
	errorAdd          bool
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

var (
	tmpDir                 string
	storageDir             string
	stateDir               string
	stateStorageQuotaLimit uint64
)

/***********************************************************************************************************************
 * Init
 **********************************************************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/***********************************************************************************************************************
 * Main
 **********************************************************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	ret := m.Run()

	cleanup()

	os.Exit(ret)
}

/***********************************************************************************************************************
 * Tests
 **********************************************************************************************************************/

func TestSetupClean(t *testing.T) {
	type testSetupCleanInfo struct {
		storagestate.SetupParams
		instanceID                string
		expectedStorageQuotaLimit uint64
		expectedStateQuotaLimit   uint64
		expectedStoragePath       string
		expectedStatePath         string
		expectReceiveStateChanged bool
		errorSaveStorage          bool
		errorGet                  bool
		errorAdd                  bool
		cleanState                bool
		state                     string
	}

	testsData := []testSetupCleanInfo{
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   2000,
				StorageQuota: 1000,
			},
			instanceID:                "instance1",
			expectedStorageQuotaLimit: 3000,
			expectedStateQuotaLimit:   3000,
			expectedStoragePath:       path.Join(storageDir, "instance1"),
			expectedStatePath:         path.Join(stateDir, "instance1_state.dat"),
			expectReceiveStateChanged: true,
			state:                     "new state",
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   2000,
				StorageQuota: 1000,
			},
			instanceID:                "instance1",
			expectedStorageQuotaLimit: 3000,
			expectedStateQuotaLimit:   3000,
			expectedStoragePath:       path.Join(storageDir, "instance1"),
			expectedStatePath:         path.Join(stateDir, "instance1_state.dat"),
			expectReceiveStateChanged: false,
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   0,
				StorageQuota: 0,
			},
			instanceID:                "instance1",
			expectedStorageQuotaLimit: 0,
			expectedStateQuotaLimit:   0,
			expectedStoragePath:       "",
			expectedStatePath:         "",
			expectReceiveStateChanged: false,
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  2,
				},
				UID:          1004,
				GID:          1004,
				StateQuota:   1000,
				StorageQuota: 1000,
			},
			instanceID:                "instance2",
			expectedStorageQuotaLimit: 2000,
			expectedStateQuotaLimit:   2000,
			expectedStoragePath:       path.Join(storageDir, "instance2"),
			expectedStatePath:         path.Join(stateDir, "instance2_state.dat"),
			expectReceiveStateChanged: true,
			state:                     "new state",
		},
	}

	storage := testStorageInterface{
		data: make(map[string]storagestate.StorageStateInstanceInfo),
	}

	instance, err := storagestate.New(&config.Config{
		StorageDir: storageDir,
		StateDir:   stateDir,
	}, &storage)
	if err != nil {
		t.Fatalf("Can't create storagestate instance: %v", err)
	}
	defer instance.Close()

	for _, testData := range testsData {
		storagePath, statePath, checksum, err := instance.Setup(testData.instanceID, testData.SetupParams)
		if err != nil {
			t.Fatalf("Can't setup instance: %v", err)
		}

		expectedCheckSum, err := getStateFileChecksum(
			path.Join(stateDir, fmt.Sprintf("%s_state.dat", testData.instanceID)))
		if err != nil {
			t.Fatalf("Can't get checksum from state file: %v", err)
		}

		if !testData.expectReceiveStateChanged {
			if !bytes.Equal(expectedCheckSum, checksum) {
				t.Error("Incorrect checksum")
			}
		}

		if storagePath != testData.expectedStoragePath {
			t.Error("Unexpected storage path")
		}

		if storagePath != "" && !isPathExist(storagePath) {
			t.Error("Storage dir doesn't exist")
		}

		if statePath != testData.expectedStatePath {
			t.Error("Unexpected state path")
		}

		if statePath != "" && !isPathExist(statePath) {
			t.Error("State dir doesn't exist")
		}

		if stateStorageQuotaLimit != testData.expectedStorageQuotaLimit {
			t.Error("Unexpected storage quota limit")
		}

		if stateStorageQuotaLimit != testData.expectedStateQuotaLimit {
			t.Error("Unexpected state quota limit")
		}

		select {
		case stateRequest := <-instance.StateRequestChannel():
			if !testData.expectReceiveStateChanged {
				t.Error("Should not receive a state request")
			}

			if testData.InstanceIdent != stateRequest.InstanceIdent {
				t.Error("Incorrect state changed instance ID")
			}

			calcSum := sha3.Sum224([]byte(testData.state))

			if err = instance.UpdateState(cloudprotocol.UpdateState{
				InstanceIdent: testData.InstanceIdent,
				Checksum:      hex.EncodeToString(calcSum[:]),
				State:         testData.state,
			}); err != nil {
				t.Fatalf("Can't send update state: %v", err)
			}

			select {
			case stateChanged := <-instance.StateChangedChannel():
				if stateChanged.InstanceID != testData.instanceID {
					t.Error("Unexpected instanceID")
				}

				if !bytes.Equal(stateChanged.Checksum, calcSum[:]) {
					t.Error("Incorrect checksum")
				}

			case <-time.After(waitChannelTimeout):
				if testData.expectReceiveStateChanged {
					t.Error("Expected state change to be received")
				}
			}

		case <-time.After(waitChannelTimeout):
			if testData.expectReceiveStateChanged {
				t.Error("Expected state request to be received")
			}
		}
	}

	testsData = []testSetupCleanInfo{
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  2,
				},
				UID:          1004,
				GID:          1004,
				StateQuota:   500,
				StorageQuota: 500,
			},
			instanceID:                "instance2",
			expectedStorageQuotaLimit: 1000,
			expectedStateQuotaLimit:   1000,
			expectedStoragePath:       path.Join(storageDir, "instance2"),
			expectedStatePath:         path.Join(stateDir, "instance2_state.dat"),
			errorGet:                  true,
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  2,
				},
				UID:          1004,
				GID:          1004,
				StateQuota:   500,
				StorageQuota: 500,
			},
			instanceID:                "instance2",
			expectedStorageQuotaLimit: 1000,
			expectedStateQuotaLimit:   1000,
			expectedStoragePath:       path.Join(storageDir, "instance2"),
			expectedStatePath:         path.Join(stateDir, "instance2_state.dat"),
			errorSaveStorage:          true,
			cleanState:                true,
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  2,
				},
				UID:          1004,
				GID:          1004,
				StateQuota:   500,
				StorageQuota: 500,
			},
			instanceID:                "instance2",
			expectedStorageQuotaLimit: 1000,
			expectedStateQuotaLimit:   1000,
			expectedStoragePath:       path.Join(storageDir, "instance2"),
			expectedStatePath:         path.Join(stateDir, "instance2_state.dat"),
			errorAdd:                  true,
		},
	}

	for _, testData := range testsData {
		storage.errorAdd = testData.errorAdd
		storage.errorGet = testData.errorGet
		storage.errorSave = testData.errorSaveStorage

		_, _, _, err := instance.Setup(testData.instanceID, testData.SetupParams)
		if err == nil {
			t.Fatalf("Should not be a setup instance")
		}

		if testData.cleanState {
			if err = instance.Cleanup(testData.instanceID); err != nil {
				t.Fatalf("Can't cleanup storage state: %v", err)
			}

			if err = instance.Remove(testData.instanceID); err != nil {
				t.Fatalf("Can't remove storage state: %v", err)
			}
		}
	}

	for instaceID := range storage.data {
		if err = instance.Cleanup(instaceID); err != nil {
			t.Fatalf("Can't cleanup storage state: %v", err)
		}

		if err = instance.Remove(instaceID); err != nil {
			t.Fatalf("Can't remove storage state: %v", err)
		}

		if _, ok := storage.data[instaceID]; ok {
			t.Fatal("Storage state info should not be exist in storage")
		}
	}
}

func TestUpdateState(t *testing.T) {
	type testUpdateState struct {
		storagestate.SetupParams
		instanceID                string
		expectReceiveStateRequest bool
		stateData                 string
	}

	testsData := []testUpdateState{
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   2000,
				StorageQuota: 1000,
			},
			instanceID:                "instance1",
			expectReceiveStateRequest: true,
			stateData:                 "state1",
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   2000,
				StorageQuota: 1000,
			},
			instanceID:                "instance1",
			expectReceiveStateRequest: false,
			stateData:                 "state2",
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  2,
				},
				UID:          1004,
				GID:          1004,
				StateQuota:   1000,
				StorageQuota: 1000,
			},
			instanceID:                "instance2",
			expectReceiveStateRequest: true,
			stateData:                 "state3",
		},
	}

	storage := testStorageInterface{
		data: make(map[string]storagestate.StorageStateInstanceInfo),
	}

	instance, err := storagestate.New(&config.Config{
		StorageDir: storageDir,
		StateDir:   stateDir,
	}, &storage)
	if err != nil {
		t.Fatalf("Can't create storagestate instance: %v", err)
	}
	defer instance.Close()

	for _, testData := range testsData {
		_, _, checksum, err := instance.Setup(testData.instanceID, testData.SetupParams)
		if err != nil {
			t.Fatalf("Can't setup instance: %v", err)
		}

		if !testData.expectReceiveStateRequest {
			expectedCheckSum, err := getStateFileChecksum(
				path.Join(stateDir, fmt.Sprintf("%s_state.dat", testData.instanceID)))
			if err != nil {
				t.Fatalf("Can't get checksum from state file: %v", err)
			}

			if !bytes.Equal(expectedCheckSum, checksum) {
				t.Error("Incorrect checksum")
			}
		}

		select {
		case stateRequest := <-instance.StateRequestChannel():
			if !testData.expectReceiveStateRequest {
				t.Error("Should not receive a state request")
			}

			if testData.InstanceIdent != stateRequest.InstanceIdent {
				t.Error("Incorrect state changed instance ID")
			}

			calcSum := sha3.Sum224([]byte(testData.stateData))

			if err = instance.UpdateState(cloudprotocol.UpdateState{
				InstanceIdent: testData.InstanceIdent,
				State:         testData.stateData,
				Checksum:      hex.EncodeToString(calcSum[:]),
			}); err != nil {
				t.Fatalf("Can't send update state: %v", err)
			}

			select {
			case stateChanged := <-instance.StateChangedChannel():
				if stateChanged.InstanceID != testData.instanceID {
					t.Error("Incorrect instance id")
				}

				if !bytes.Equal(calcSum[:], stateChanged.Checksum) {
					t.Error("Incorrect state changed checksum")
				}

				expectedCheckSum, err := getStateFileChecksum(path.Join(
					stateDir, fmt.Sprintf("%s_state.dat", testData.instanceID)))
				if err != nil {
					t.Fatalf("Can't get checksum from state file: %v", err)
				}

				if !bytes.Equal(expectedCheckSum, calcSum[:]) {
					t.Error("Incorrect checksum")
				}

				select {
				case <-instance.NewStateChannel():
					t.Fatal("Request on new state should not be send")

				case <-time.After(storagestate.StateChangeTimeout * 2):
				}

			case <-time.After(waitChannelTimeout):
				t.Fatal("Timeout to wait state changed notification")
			}

		case <-time.After(waitChannelTimeout):
			if testData.expectReceiveStateRequest {
				t.Error("Expected state change to be received")
			}
		}
	}

	for instaceID := range storage.data {
		if err = instance.Cleanup(instaceID); err != nil {
			t.Fatalf("Can't cleanup storage state: %v", err)
		}

		if err = instance.Remove(instaceID); err != nil {
			t.Fatalf("Can't remove storage state: %v", err)
		}
	}
}

func TestUpdateStateFailed(t *testing.T) {
	type testUpdateStateFailed struct {
		storagestate.SetupParams
		instanceID        string
		updateState       cloudprotocol.UpdateState
		errorSaveCheckSum bool
	}

	sumByte := sha3.Sum224([]byte("state"))

	testsData := []testUpdateStateFailed{
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   2000,
				StorageQuota: 1000,
			},
			instanceID: "instance1",
			updateState: cloudprotocol.UpdateState{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service2",
					SubjectID: "subject2",
					Instance:  1,
				},
			},
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   7,
				StorageQuota: 1000,
			},
			instanceID: "instance1",
			updateState: cloudprotocol.UpdateState{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				State: "new state",
			},
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   7,
				StorageQuota: 1000,
			},
			instanceID: "instance1",
			updateState: cloudprotocol.UpdateState{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				State:    "state",
				Checksum: "decodingError",
			},
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   7,
				StorageQuota: 1000,
			},
			instanceID: "instance1",
			updateState: cloudprotocol.UpdateState{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				State:    "state",
				Checksum: hex.EncodeToString([]byte("bad checksum")),
			},
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   7,
				StorageQuota: 1000,
			},
			instanceID:        "instance1",
			errorSaveCheckSum: true,
			updateState: cloudprotocol.UpdateState{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				State:    "state",
				Checksum: hex.EncodeToString(sumByte[:]),
			},
		},
	}

	storage := testStorageInterface{
		data: make(map[string]storagestate.StorageStateInstanceInfo),
	}

	instance, err := storagestate.New(&config.Config{
		StorageDir: storageDir,
		StateDir:   stateDir,
	}, &storage)
	if err != nil {
		t.Fatalf("Can't create storagestate instance: %v", err)
	}
	defer instance.Close()

	for _, testData := range testsData {
		storage.errorSaveCheckSum = testData.errorSaveCheckSum

		_, _, _, err := instance.Setup(testData.instanceID, testData.SetupParams)
		if err != nil {
			t.Fatalf("Can't setup instance: %v", err)
		}

		if err = instance.UpdateState(testData.updateState); err == nil {
			t.Fatal("State should not be updated")
		}
	}

	for instaceID := range storage.data {
		if err = instance.Cleanup(instaceID); err != nil {
			t.Fatalf("Can't cleanup storage state: %v", err)
		}

		if err = instance.Remove(instaceID); err != nil {
			t.Fatalf("Can't remove storage state: %v", err)
		}
	}
}

func TestStateAcceptanceFailed(t *testing.T) {
	type testStateAcceptanceFailed struct {
		storagestate.SetupParams
		instanceID        string
		stateAcceptance   cloudprotocol.StateAcceptance
		errorSaveCheckSum bool
	}

	setupParams := storagestate.SetupParams{
		InstanceIdent: cloudprotocol.InstanceIdent{
			ServiceID: "service1",
			SubjectID: "subject1",
			Instance:  1,
		},
		UID:          1003,
		GID:          1003,
		StateQuota:   2000,
		StorageQuota: 1000,
	}

	testsData := []testStateAcceptanceFailed{
		{
			SetupParams: setupParams,
			instanceID:  "instance1",
			stateAcceptance: cloudprotocol.StateAcceptance{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service2",
					SubjectID: "subject2",
					Instance:  1,
				},
			},
		},
		{
			SetupParams: setupParams,
			instanceID:  "instance1",
			stateAcceptance: cloudprotocol.StateAcceptance{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				Checksum: "decodingError",
			},
		},
		{
			SetupParams: setupParams,
			instanceID:  "instance1",
			stateAcceptance: cloudprotocol.StateAcceptance{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				Checksum: hex.EncodeToString([]byte("bad checksum")),
			},
		},
		{
			SetupParams:       setupParams,
			instanceID:        "instance1",
			errorSaveCheckSum: true,
			stateAcceptance: cloudprotocol.StateAcceptance{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				Result: "accepted",
			},
		},
	}

	storage := testStorageInterface{
		data: make(map[string]storagestate.StorageStateInstanceInfo),
	}

	instance, err := storagestate.New(&config.Config{
		StorageDir: storageDir,
		StateDir:   stateDir,
	}, &storage)
	if err != nil {
		t.Fatalf("Can't create storagestate instance: %v", err)
	}
	defer instance.Close()

	for _, testData := range testsData {
		storage.errorSaveCheckSum = testData.errorSaveCheckSum

		_, _, checksum, err := instance.Setup(testData.instanceID, testData.SetupParams)
		if err != nil {
			t.Fatalf("Can't setup instance: %v", err)
		}

		if testData.errorSaveCheckSum {
			testData.stateAcceptance.Checksum = hex.EncodeToString(checksum)
		}

		if err = instance.StateAcceptance(testData.stateAcceptance); err == nil {
			t.Fatalf("State should not be acceptance")
		}
	}

	for instaceID := range storage.data {
		if err = instance.Cleanup(instaceID); err != nil {
			t.Fatalf("Can't cleanup storage state: %v", err)
		}

		if err = instance.Remove(instaceID); err != nil {
			t.Fatalf("Can't remove storage state: %v", err)
		}
	}
}

func TestNewStateCancel(t *testing.T) {
	setupParams := storagestate.SetupParams{
		InstanceIdent: cloudprotocol.InstanceIdent{
			ServiceID: "service1",
			SubjectID: "subject1",
			Instance:  1,
		},
		UID:          1003,
		GID:          1003,
		StateQuota:   2000,
		StorageQuota: 1000,
	}

	storage := testStorageInterface{
		data: make(map[string]storagestate.StorageStateInstanceInfo),
	}

	instance, err := storagestate.New(&config.Config{
		StorageDir: storageDir,
		StateDir:   stateDir,
	}, &storage)
	if err != nil {
		t.Fatalf("Can't create storagestate instance: %v", err)
	}
	defer instance.Close()

	if _, _, _, err = instance.Setup("instance1", setupParams); err != nil {
		t.Fatalf("Can't setup instance: %v", err)
	}

	if err := ioutil.WriteFile(path.Join(stateDir, "instance1_state.dat"), []byte("New state"), 0o600); err != nil {
		t.Fatalf("Can't write state file: %v", err)
	}

	if err = instance.Cleanup("instance1"); err != nil {
		t.Fatalf("Can't cleanup storage state: %v", err)
	}

	if err = instance.Remove("instance1"); err != nil {
		t.Fatalf("Can't remove storage state: %v", err)
	}

	select {
	case <-instance.NewStateChannel():
		t.Fatal("New state request should not be send")

	case <-time.After(storagestate.StateChangeTimeout * 2):
	}
}

func TestStateChangedChannelFull(t *testing.T) {
	sumByte := sha3.Sum224([]byte("state"))

	storage := testStorageInterface{
		data: make(map[string]storagestate.StorageStateInstanceInfo),
	}

	instance, err := storagestate.New(&config.Config{
		StorageDir: storageDir,
		StateDir:   stateDir,
	}, &storage)
	if err != nil {
		t.Fatalf("Can't create storagestate instance: %v", err)
	}
	defer instance.Close()

	setupParams := storagestate.SetupParams{
		InstanceIdent: cloudprotocol.InstanceIdent{
			ServiceID: "service1",
			SubjectID: "subject1",
			Instance:  1,
		},
		UID:          1003,
		GID:          1003,
		StateQuota:   7,
		StorageQuota: 1000,
	}

	updateState := cloudprotocol.UpdateState{
		InstanceIdent: cloudprotocol.InstanceIdent{
			ServiceID: "service1",
			SubjectID: "subject1",
			Instance:  1,
		},
		State:    "state",
		Checksum: hex.EncodeToString(sumByte[:]),
	}

	stateAcceptance := cloudprotocol.StateAcceptance{
		InstanceIdent: cloudprotocol.InstanceIdent{
			ServiceID: "service1",
			SubjectID: "subject1",
			Instance:  1,
		},
		Result:   "accepted",
		Checksum: hex.EncodeToString(sumByte[:]),
	}

	stateChannelSize := 32

	if _, _, _, err = instance.Setup("instance1", setupParams); err != nil {
		t.Fatalf("Can't setup instance: %v", err)
	}

	for i := 0; i < stateChannelSize; i++ {
		if err = instance.UpdateState(updateState); err != nil {
			t.Fatalf("Can't send update state: %v", err)
		}
	}

	if len(instance.StateChangedChannel()) != 32 {
		t.Fatal("Unexpected state changed channel size")
	}

	if err = instance.UpdateState(updateState); err == nil {
		t.Fatal("State should not be updated")
	}

	if err = instance.StateAcceptance(stateAcceptance); err == nil {
		t.Fatal("State should not be accepted")
	}

	select {
	case <-instance.StateChangedChannel():

	case <-time.After(waitChannelTimeout):
		t.Fatal("Timeout to get state changed")
	}

	if err = instance.UpdateState(updateState); err != nil {
		t.Fatalf("Can't send update state: %v", err)
	}

	if err = instance.Cleanup("instance1"); err != nil {
		t.Fatalf("Can't cleanup storage state: %v", err)
	}

	if err = instance.Remove("instance1"); err != nil {
		t.Fatalf("Can't remove storage state: %v", err)
	}
}

func TestNewStateChannelFull(t *testing.T) {
	storage := testStorageInterface{
		data: make(map[string]storagestate.StorageStateInstanceInfo),
	}

	instance, err := storagestate.New(&config.Config{
		StorageDir: storageDir,
		StateDir:   stateDir,
	}, &storage)
	if err != nil {
		t.Fatalf("Can't create storagestate instance: %v", err)
	}
	defer instance.Close()

	setupParams := storagestate.SetupParams{
		InstanceIdent: cloudprotocol.InstanceIdent{
			ServiceID: "service1",
			SubjectID: "subject1",
			Instance:  1,
		},
		UID:          1003,
		GID:          1003,
		StateQuota:   15,
		StorageQuota: 1000,
	}

	stateChannelSize := 32

	if _, _, _, err = instance.Setup("instance1", setupParams); err != nil {
		t.Fatalf("Can't setup instance: %v", err)
	}

	pathToStateFile := path.Join(stateDir, "instance1_state.dat")

	for i := 0; i < stateChannelSize; i++ {
		if err := ioutil.WriteFile(pathToStateFile, []byte(fmt.Sprintf("state%d", i)), 0o600); err != nil {
			t.Fatalf("Can't write state file: %v", err)
		}

		time.Sleep(storagestate.StateChangeTimeout * 2)
	}

	if len(instance.NewStateChannel()) != 32 {
		t.Fatal("Unexpected new state channel size")
	}

	if err := ioutil.WriteFile(pathToStateFile, []byte("new state"), 0o600); err != nil {
		t.Fatalf("Can't write state file: %v", err)
	}

	time.Sleep(storagestate.StateChangeTimeout * 2)

	if len(instance.NewStateChannel()) != 32 {
		t.Fatal("Unexpected new state channel size")
	}

	select {
	case <-instance.NewStateChannel():

	case <-time.After(waitChannelTimeout):
		t.Fatal("Timeout to get new state")
	}

	if len(instance.NewStateChannel()) != 31 {
		t.Fatal("Unexpected new state channel size")
	}

	if err := ioutil.WriteFile(pathToStateFile, []byte("new state3"), 0o600); err != nil {
		t.Fatalf("Can't write state file: %v", err)
	}

	time.Sleep(storagestate.StateChangeTimeout * 2)

	if len(instance.NewStateChannel()) != 32 {
		t.Fatal("Unexpected new state channel size")
	}

	if err = instance.Cleanup("instance1"); err != nil {
		t.Fatalf("Can't cleanup storage state: %v", err)
	}

	if err = instance.Remove("instance1"); err != nil {
		t.Fatalf("Can't remove storage state: %v", err)
	}
}

func TestStateRequestChannelFull(t *testing.T) {
	setupParams := storagestate.SetupParams{
		InstanceIdent: cloudprotocol.InstanceIdent{
			ServiceID: "service1",
			SubjectID: "subject1",
			Instance:  1,
		},
		UID:          1004,
		GID:          1004,
		StateQuota:   1000,
		StorageQuota: 1000,
	}

	stateAcceptance := cloudprotocol.StateAcceptance{
		InstanceIdent: cloudprotocol.InstanceIdent{
			ServiceID: "service1",
			SubjectID: "subject1",
			Instance:  1,
		},
		Result: "reject",
		Reason: "test",
	}

	storage := testStorageInterface{
		data: make(map[string]storagestate.StorageStateInstanceInfo),
	}

	instance, err := storagestate.New(&config.Config{
		StorageDir: storageDir,
		StateDir:   stateDir,
	}, &storage)
	if err != nil {
		t.Fatalf("Can't create storagestate instance: %v", err)
	}
	defer instance.Close()

	_, _, checksum, err := instance.Setup("instance1", setupParams)
	if err != nil {
		t.Fatalf("Can't setup instance: %v", err)
	}

	if checksum != nil {
		t.Fatal("Unexpected checksum")
	}

	calcSum := sha3.Sum224([]byte("new state"))

	select {
	case stateRequest := <-instance.StateRequestChannel():
		if setupParams.InstanceIdent != stateRequest.InstanceIdent {
			t.Error("Incorrect state changed instance ID")
		}

		if err = instance.UpdateState(cloudprotocol.UpdateState{
			InstanceIdent: setupParams.InstanceIdent,
			Checksum:      hex.EncodeToString(calcSum[:]),
			State:         "new state",
		}); err != nil {
			t.Fatalf("Can't send update state: %v", err)
		}

		select {
		case stateChanged := <-instance.StateChangedChannel():
			if stateChanged.InstanceID != "instance1" {
				t.Error("Unexpected instanceID")
			}

			if !bytes.Equal(stateChanged.Checksum, calcSum[:]) {
				t.Error("Incorrect checksum")
			}

		case <-time.After(waitChannelTimeout):
			t.Error("Expected state change to be received")
		}

	case <-time.After(waitChannelTimeout):
		t.Error("Expected state request to be received")
	}

	stateAcceptance.Checksum = hex.EncodeToString(calcSum[:])
	stateChannelSize := 32

	for i := 0; i < stateChannelSize; i++ {
		if err = instance.StateAcceptance(stateAcceptance); err != nil {
			t.Fatalf("Can't send accepted state: %v", err)
		}
	}

	if len(instance.StateRequestChannel()) != 32 {
		t.Fatal("Unexpected state request channel size")
	}

	if err = instance.StateAcceptance(stateAcceptance); err == nil {
		t.Fatal("State should not be updated")
	}

	select {
	case <-instance.StateRequestChannel():

	case <-time.After(waitChannelTimeout):
		t.Fatal("Timeout to get state request")
	}

	if err = instance.StateAcceptance(stateAcceptance); err != nil {
		t.Fatalf("Can't send accepted state: %v", err)
	}

	if err = instance.Cleanup("instance1"); err != nil {
		t.Fatalf("Can't cleanup storage state: %v", err)
	}

	if err = instance.Remove("instance1"); err != nil {
		t.Fatalf("Can't remove storage state: %v", err)
	}
}

func TestStateAcceptance(t *testing.T) {
	type testStateAcceptance struct {
		storagestate.SetupParams
		instanceID                string
		stateData                 string
		result                    string
		expectReceiveStateRequest bool
	}

	testsData := []testStateAcceptance{
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   2000,
				StorageQuota: 1000,
			},
			instanceID:                "instance1",
			stateData:                 "state1",
			result:                    "accepted",
			expectReceiveStateRequest: true,
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  1,
				},
				UID:          1003,
				GID:          1003,
				StateQuota:   2000,
				StorageQuota: 1000,
			},
			instanceID:                "instance1",
			stateData:                 "state2",
			result:                    "rejected",
			expectReceiveStateRequest: false,
		},
		{
			SetupParams: storagestate.SetupParams{
				InstanceIdent: cloudprotocol.InstanceIdent{
					ServiceID: "service1",
					SubjectID: "subject1",
					Instance:  2,
				},
				UID:          1004,
				GID:          1004,
				StateQuota:   1000,
				StorageQuota: 1000,
			},
			instanceID:                "instance2",
			stateData:                 "state3",
			result:                    "accepted",
			expectReceiveStateRequest: true,
		},
	}

	storage := testStorageInterface{
		data: make(map[string]storagestate.StorageStateInstanceInfo),
	}

	instance, err := storagestate.New(&config.Config{
		StorageDir: storageDir,
		StateDir:   stateDir,
	}, &storage)
	if err != nil {
		t.Fatalf("Can't create storagestate instance: %v", err)
	}
	defer instance.Close()

	for _, testData := range testsData {
		_, _, checksum, err := instance.Setup(testData.instanceID, testData.SetupParams)
		if err != nil {
			t.Fatalf("Can't setup instance: %v", err)
		}

		pathToStateFile := path.Join(stateDir, fmt.Sprintf("%s_state.dat", testData.instanceID))

		if !testData.expectReceiveStateRequest {
			expectedCheckSum, err := getStateFileChecksum(pathToStateFile)
			if err != nil {
				t.Fatalf("Can't get checksum from state file: %v", err)
			}

			if !bytes.Equal(expectedCheckSum, checksum) {
				t.Error("Incorrect checksum")
			}
		}

		select {
		case stateRequest := <-instance.StateRequestChannel():
			if !testData.expectReceiveStateRequest {
				t.Error("Should not receive a state request")
			}

			if stateRequest.InstanceIdent != testData.InstanceIdent {
				t.Error("Incorrect instance ident")
			}

		case <-time.After(waitChannelTimeout):
			if testData.expectReceiveStateRequest {
				t.Fatal("Expected state request to be received")
			}
		}

		if err := ioutil.WriteFile(pathToStateFile, []byte(testData.stateData), 0o600); err != nil {
			t.Fatalf("Can't write state file: %v", err)
		}

		select {
		case newState := <-instance.NewStateChannel():
			if testData.InstanceIdent != newState.InstanceIdent {
				t.Error("Incorrect state changed instance ID")
			}

			if testData.stateData != newState.State {
				t.Error("Incorrect state data")
			}

			sumBytes, err := hex.DecodeString(newState.Checksum)
			if err != nil {
				t.Errorf("Problem to decode new state checksum: %v", err)
			}

			calcSum := sha3.Sum224([]byte(testData.stateData))

			if !bytes.Equal(calcSum[:], sumBytes) {
				t.Error("Incorrect new state checksum")
			}

			if err = instance.StateAcceptance(
				cloudprotocol.StateAcceptance{
					InstanceIdent: testData.InstanceIdent,
					Checksum:      newState.Checksum,
					Result:        testData.result,
					Reason:        "test",
				}); err != nil {
				t.Fatalf("Can't send accepted state: %v", err)
			}

			select {
			case stateChanged := <-instance.StateChangedChannel():
				if testData.result != "accepted" {
					t.Fatal("Unexpected state changed request")
				}

				if stateChanged.InstanceID != testData.instanceID {
					t.Error("Incorrect instance id")
				}

				if !bytes.Equal(calcSum[:], stateChanged.Checksum) {
					t.Error("Incorrect state changed checksum")
				}

				expectedCheckSum, err := getStateFileChecksum(pathToStateFile)
				if err != nil {
					t.Fatalf("Can't get checksum from state file: %v", err)
				}

				if !bytes.Equal(expectedCheckSum, calcSum[:]) {
					t.Error("Incorrect checksum")
				}

			case stateRequest := <-instance.StateRequestChannel():
				if testData.result == "accepted" {
					t.Fatal("Unexpected state request")
				}

				if stateRequest.InstanceIdent != testData.InstanceIdent {
					t.Error("Incorrect instance id")
				}

				stateData := "new state"
				calcSum := sha3.Sum224([]byte(stateData))

				if err = instance.UpdateState(cloudprotocol.UpdateState{
					InstanceIdent: testData.InstanceIdent,
					State:         stateData,
					Checksum:      hex.EncodeToString(calcSum[:]),
				}); err != nil {
					t.Fatalf("Can't send update state: %v", err)
				}

				select {
				case stateChanged := <-instance.StateChangedChannel():
					if stateChanged.InstanceID != testData.instanceID {
						t.Error("Incorrect instance id")
					}

					if !bytes.Equal(calcSum[:], stateChanged.Checksum) {
						t.Error("Incorrect state changed checksum")
					}

					expectedCheckSum, err := getStateFileChecksum(pathToStateFile)
					if err != nil {
						t.Fatalf("Can't get checksum from state file: %v", err)
					}

					if !bytes.Equal(expectedCheckSum, calcSum[:]) {
						t.Error("Incorrect checksum")
					}

				case <-time.After(waitChannelTimeout):
					t.Fatal("Timeout to wait state changed")
				}

			case <-time.After(waitChannelTimeout):
				t.Fatal("Timeout to wait requests")
			}

		case <-time.After(storagestate.StateChangeTimeout * 2):
			t.Fatal("Timeout to wait new state request")
		}
	}
}

/***********************************************************************************************************************
 * Interfaces
 **********************************************************************************************************************/

func (storage *testStorageInterface) GetStorageStateInfoByID(
	instanceID string,
) (storageStateInfo storagestate.StorageStateInstanceInfo, err error) {
	if storage.errorGet {
		return storageStateInfo, aoserrors.New("can't get storagestate info")
	}

	data, ok := storage.data[instanceID]
	if !ok {
		return storageStateInfo, storagestate.ErrNotExist
	}

	return data, nil
}

func (storage *testStorageInterface) SetStorageStateQuotasByID(
	instanceID string, storageQuota, stateQuota uint64,
) error {
	if storage.errorSave {
		return aoserrors.New("can't save storage state info")
	}

	data := storage.data[instanceID]

	data.StateQuota = stateQuota
	data.StorageQuota = storageQuota

	storage.data[instanceID] = data

	return nil
}

func (storage *testStorageInterface) AddStorageStateInfo(
	instanceID string, storageStateInfo storagestate.StorageStateInstanceInfo,
) error {
	if storage.errorAdd {
		return aoserrors.New("can't add statestorage entry")
	}

	storage.data[instanceID] = storageStateInfo

	return nil
}

func (storage *testStorageInterface) SetStateChecksumByID(instanceID string, checksum []byte) error {
	if storage.errorSaveCheckSum {
		return aoserrors.New("can't save checksum")
	}

	storageStateInfo, ok := storage.data[instanceID]
	if !ok {
		return aoserrors.New("instance not found")
	}

	storageStateInfo.StateChecksum = checksum

	storage.data[instanceID] = storageStateInfo

	return nil
}

func (storage *testStorageInterface) RemoveStorageStateInfoByID(instanceID string) error {
	if _, ok := storage.data[instanceID]; !ok {
		return aoserrors.New("instance not found")
	}

	delete(storage.data, instanceID)

	return nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func setup() (err error) {
	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return aoserrors.Wrap(err)
	}

	storagestate.SetUserFSQuota = setUserFSQuota

	storagestate.StateChangeTimeout = 100 * time.Millisecond

	storageDir = path.Join(tmpDir, "storage")
	stateDir = path.Join(tmpDir, "state")

	return nil
}

func cleanup() {
	if err := os.RemoveAll(tmpDir); err != nil {
		log.Errorf("Can't remove tmp folder: %s", err)
	}
}

func setUserFSQuota(path string, limit uint64, uid, gid uint32) (err error) {
	stateStorageQuotaLimit = limit

	return nil
}

func getStateFileChecksum(fileName string) (checksum []byte, err error) {
	if _, err = os.Stat(fileName); err != nil {
		if !os.IsNotExist(err) {
			return nil, aoserrors.Wrap(err)
		}

		return nil, nil
	}

	data, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	calcSum := sha3.Sum224(data)

	return calcSum[:], nil
}

func isPathExist(dir string) bool {
	if _, err := os.Stat(dir); err != nil {
		return false
	}

	return true
}
