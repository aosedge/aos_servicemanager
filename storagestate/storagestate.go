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

package storagestate

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/aoscloud/aos_common/utils/fs"
	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/fsnotify/fsnotify"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/sha3"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	stateFileFormat  = "%s_state.dat"
	stateChannelSize = 32
)

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

// These global variables are used to be able to mocking the functionality in tests.
// nolint:gochecknoglobals
var (
	SetUserFSQuota     = fs.SetUserFSQuota
	StateChangeTimeout = 1 * time.Second
)

// ErrNotExist is returned when requested entry not exist in DB.
var ErrNotExist = errors.New("entry does not exist")

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// StorageStateInstanceInfo storage state instance info.
type StorageStateInstanceInfo struct {
	StorageQuota  uint64
	StateQuota    uint64
	StateChecksum []byte
}

// Storage storage interface.
type Storage interface {
	GetStorageStateInfoByID(instanceID string) (StorageStateInstanceInfo, error)
	SetStorageStateQuotasByID(instanceID string, storageQuota, stateQuota uint64) error
	AddStorageStateInfo(instanceID string, storageStateInfo StorageStateInstanceInfo) error
	SetStateChecksumByID(instanceID string, checksum []byte) error
	RemoveStorageStateInfoByID(instanceID string) error
}

// SetupParams setup storage state instance params.
type SetupParams struct {
	cloudprotocol.InstanceIdent
	UID          int
	GID          int
	StateQuota   uint64
	StorageQuota uint64
}

// StateChangedInfo contains state changed information.
type StateChangedInfo struct {
	InstanceID string
	Checksum   []byte
}

// StorageState storage state instance.
type StorageState struct {
	sync.Mutex
	storageDir          string
	stateDir            string
	storage             Storage
	statesMap           map[cloudprotocol.InstanceIdent]*stateParams
	watcher             *fsnotify.Watcher
	stateChangedChannel chan StateChangedInfo
	newStateChannel     chan cloudprotocol.NewState
	stateRequestChannel chan cloudprotocol.StateRequest
	isSamePartition     bool
}

type stateParams struct {
	instanceID         string
	stateFilePath      string
	quota              uint64
	checksum           []byte
	changeTimer        *time.Timer
	changeTimerChannel chan bool
}

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates storagestate instance.
func New(cfg *config.Config, storage Storage) (storageState *StorageState, err error) {
	log.Debug("Create storagestate")

	storageState = &StorageState{
		storage:             storage,
		storageDir:          cfg.StorageDir,
		stateDir:            cfg.StateDir,
		statesMap:           make(map[cloudprotocol.InstanceIdent]*stateParams),
		stateChangedChannel: make(chan StateChangedInfo, stateChannelSize),
		newStateChannel:     make(chan cloudprotocol.NewState, stateChannelSize),
		stateRequestChannel: make(chan cloudprotocol.StateRequest, stateChannelSize),
	}

	if err = os.MkdirAll(storageState.storageDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err = os.MkdirAll(storageState.stateDir, 0o755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if storageState.watcher, err = fsnotify.NewWatcher(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	storageMountPoint, err := fs.GetMountPoint(cfg.StorageDir)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	stateMountPoint, err := fs.GetMountPoint(cfg.StateDir)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	storageState.isSamePartition = storageMountPoint == stateMountPoint

	go storageState.processWatcher()

	return storageState, nil
}

// Close close storagestate instance.
func (storageState *StorageState) Close() {
	log.Debug("Close storagestate")

	storageState.watcher.Close()
}

// Setup setup storagestate instance.
func (storageState *StorageState) Setup(
	instanceID string, params SetupParams,
) (storagePath string, statePath string, stateChecksum []byte, err error) {
	storageState.Lock()
	defer storageState.Unlock()

	log.WithFields(log.Fields{
		"instaceID":    instanceID,
		"storageQuota": params.StorageQuota,
		"stateQuota":   params.StateQuota,
	}).Debug("Setup storage and state")

	storageStateInfo, err := storageState.storage.GetStorageStateInfoByID(instanceID)
	if err != nil {
		if !errors.Is(err, ErrNotExist) {
			return "", "", nil, aoserrors.Wrap(err)
		}

		storageStateInfo = StorageStateInstanceInfo{}

		if err = storageState.storage.AddStorageStateInfo(
			instanceID, storageStateInfo); err != nil {
			return "", "", nil, aoserrors.Wrap(err)
		}
	}

	if storagePath, err = storageState.prepareStorage(instanceID, params); err != nil {
		return "", "", nil, aoserrors.Wrap(err)
	}

	if err := storageState.stopStateWatching(params.InstanceIdent); err != nil {
		return "", "", nil, aoserrors.Wrap(err)
	}

	if storageStateInfo.StateQuota != params.StateQuota || storageStateInfo.StorageQuota != params.StorageQuota {
		if err = storageState.setQuotasFS(params); err != nil {
			return "", "", nil, aoserrors.Wrap(err)
		}

		if err = storageState.storage.SetStorageStateQuotasByID(
			instanceID, params.StorageQuota, params.StateQuota); err != nil {
			return "", "", nil, aoserrors.Wrap(err)
		}
	}

	if params.StateQuota != 0 {
		defer func() {
			if err != nil {
				if err := storageState.stopStateWatching(params.InstanceIdent); err != nil {
					log.Warnf("Can't stop watching state file: %v", err)
				}
			}
		}()

		if statePath, err = storageState.prepareState(instanceID, params, storageStateInfo.StateChecksum); err != nil {
			return "", "", nil, aoserrors.Wrap(err)
		}

		stateChecksum = storageStateInfo.StateChecksum
	} else {
		if err := os.RemoveAll(path.Join(storageState.stateDir, fmt.Sprintf(stateFileFormat, instanceID))); err != nil {
			return "", "", nil, aoserrors.Wrap(err)
		}
	}

	return storagePath, statePath, stateChecksum, nil
}

// Cleanup clean storagestate instance.
func (storageState *StorageState) Cleanup(instanceID string) error {
	storageState.Lock()
	defer storageState.Unlock()

	log.WithField("instaceID", instanceID).Debug("Clean storage and state")

	for instanceIdent, state := range storageState.statesMap {
		if state.instanceID == instanceID {
			if err := storageState.stopStateWatching(instanceIdent); err != nil {
				return aoserrors.Wrap(err)
			}
		}
	}

	return nil
}

// Remove remove storagestate dirs.
func (storageState *StorageState) Remove(instanceID string) error {
	log.WithField("instaceID", instanceID).Debug("Remove storage and state")

	if err := storageState.Cleanup(instanceID); err != nil {
		return aoserrors.Wrap(err)
	}

	return storageState.remove(instanceID)
}

// StateChangedChannel get state changed channel.
func (storageState *StorageState) StateChangedChannel() <-chan StateChangedInfo {
	return storageState.stateChangedChannel
}

// NewStateChannel get new state channel.
func (storageState *StorageState) NewStateChannel() <-chan cloudprotocol.NewState {
	return storageState.newStateChannel
}

// NewStateChannel get new state channel.
func (storageState *StorageState) StateRequestChannel() <-chan cloudprotocol.StateRequest {
	return storageState.stateRequestChannel
}

// NewStateChannel update state.
func (storageState *StorageState) UpdateState(updateState cloudprotocol.UpdateState) error {
	storageState.Lock()
	defer storageState.Unlock()

	state, ok := storageState.statesMap[updateState.InstanceIdent]
	if !ok {
		return aoserrors.New("instance not found")
	}

	log.WithField("instaceID", state.instanceID).Debug("Update state")

	if len(updateState.State) > int(state.quota) {
		return aoserrors.New("update state is too big")
	}

	sumBytes, err := hex.DecodeString(updateState.Checksum)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err := checkChecksum([]byte(updateState.State), sumBytes); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := storageState.storage.SetStateChecksumByID(state.instanceID, sumBytes); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := ioutil.WriteFile(state.stateFilePath, []byte(updateState.State), 0o600); err != nil {
		return aoserrors.Wrap(err)
	}

	state.checksum = sumBytes

	if err = storageState.pushStateChangedMessage(state.instanceID, sumBytes); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

// StateAcceptance acceptance state.
func (storageState *StorageState) StateAcceptance(updateState cloudprotocol.StateAcceptance) error {
	storageState.Lock()
	defer storageState.Unlock()

	state, ok := storageState.statesMap[updateState.InstanceIdent]
	if !ok {
		return aoserrors.New("instance not found")
	}

	sumBytes, err := hex.DecodeString(updateState.Checksum)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if !bytes.Equal(state.checksum, sumBytes) {
		return aoserrors.New("unexpected checksum")
	}

	if strings.ToLower(updateState.Result) == "accepted" {
		log.WithField("instanceID", state.instanceID).Debug("State is accepted")

		if err = storageState.pushStateChangedMessage(state.instanceID, sumBytes); err != nil {
			return aoserrors.Wrap(err)
		}

		if err := storageState.storage.SetStateChecksumByID(state.instanceID, sumBytes); err != nil {
			return aoserrors.Wrap(err)
		}
	} else {
		log.WithField("instanceID", state.instanceID).Errorf("State is rejected due to: %s", updateState.Reason)

		if err = storageState.pushStateRequestMessage(updateState.InstanceIdent, false); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (storageState *StorageState) prepareState(
	instanceID string, params SetupParams, checksum []byte,
) (stateFilePath string, err error) {
	stateFilePath = path.Join(storageState.stateDir, fmt.Sprintf(stateFileFormat, instanceID))

	if err = storageState.setupStateWatching(instanceID, stateFilePath, params); err != nil {
		return "", aoserrors.Wrap(err)
	}

	storageState.statesMap[params.InstanceIdent].checksum = checksum

	if err = storageState.checkChecksumAndSendUpdateRequest(
		stateFilePath, params.InstanceIdent); err != nil {
		return "", aoserrors.Wrap(err)
	}

	return stateFilePath, nil
}

func (storageState *StorageState) remove(instanceID string) error {
	storageState.Lock()
	defer storageState.Unlock()

	if err := os.RemoveAll(path.Join(storageState.storageDir, instanceID)); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := os.RemoveAll(path.Join(storageState.stateDir, fmt.Sprintf(stateFileFormat, instanceID))); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := storageState.storage.RemoveStorageStateInfoByID(instanceID); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (storageState *StorageState) checkChecksumAndSendUpdateRequest(
	stateFilePath string, instanceIdent cloudprotocol.InstanceIdent,
) (err error) {
	state, ok := storageState.statesMap[instanceIdent]
	if !ok {
		return aoserrors.New("instance not found")
	}

	_, checksum, err := getFileDataChecksum(stateFilePath)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if !bytes.Equal(checksum, state.checksum) {
		if err = storageState.pushStateRequestMessage(instanceIdent, false); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (storageState *StorageState) setupStateWatching(instanceID, stateFilePath string, params SetupParams) error {
	if err := createStateFileIfNotExist(stateFilePath, params.UID, params.GID); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := storageState.startStateWatching(instanceID, stateFilePath, params); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (storageState *StorageState) startStateWatching(
	instanceID string, stateFilePath string, params SetupParams,
) (err error) {
	if err = storageState.watcher.Add(stateFilePath); err != nil {
		return aoserrors.Wrap(err)
	}

	storageState.statesMap[params.InstanceIdent] = &stateParams{
		instanceID:         instanceID,
		stateFilePath:      stateFilePath,
		quota:              params.StateQuota,
		changeTimerChannel: make(chan bool, 1),
	}

	return nil
}

func (storageState *StorageState) stopStateWatching(instanceIdent cloudprotocol.InstanceIdent) (err error) {
	if state, ok := storageState.statesMap[instanceIdent]; ok {
		if err = storageState.watcher.Remove(state.stateFilePath); err != nil {
			return aoserrors.Wrap(err)
		}

		if state.changeTimer != nil {
			if state.changeTimer.Stop() {
				state.changeTimerChannel <- false
			}
		}

		delete(storageState.statesMap, instanceIdent)
	}

	return nil
}

func (storageState *StorageState) pushStateChangedMessage(instanceID string, checksum []byte) (err error) {
	if len(storageState.stateChangedChannel) >= cap(storageState.stateChangedChannel) {
		return aoserrors.New("state changed channel is full")
	}

	storageState.stateChangedChannel <- StateChangedInfo{
		InstanceID: instanceID,
		Checksum:   checksum,
	}

	return nil
}

func (storageState *StorageState) pushNewStateMessage(
	instanceIdent cloudprotocol.InstanceIdent, checksum, stateData string,
) (err error) {
	if len(storageState.newStateChannel) >= cap(storageState.newStateChannel) {
		return aoserrors.New("new state channel is full")
	}

	storageState.newStateChannel <- cloudprotocol.NewState{
		InstanceIdent: instanceIdent,
		Checksum:      checksum,
		State:         stateData,
	}

	return nil
}

func (storageState *StorageState) pushStateRequestMessage(
	instanceIdent cloudprotocol.InstanceIdent, defaultState bool,
) (err error) {
	if len(storageState.stateRequestChannel) >= cap(storageState.stateRequestChannel) {
		return aoserrors.New("state request channel is full")
	}

	storageState.stateRequestChannel <- cloudprotocol.StateRequest{
		InstanceIdent: instanceIdent,
		Default:       defaultState,
	}

	return nil
}

func (storageState *StorageState) processWatcher() {
	for {
		select {
		case event, ok := <-storageState.watcher.Events:
			if !ok {
				return
			}

			storageState.Lock()

			for instanceIdent, state := range storageState.statesMap {
				if state.stateFilePath == event.Name {
					log.WithFields(log.Fields{
						"file":       event.Name,
						"instanceID": state.instanceID,
					}).Debug("State file changed")

					if state.changeTimer == nil {
						state.changeTimer = time.NewTimer(StateChangeTimeout)

						go func() {
							select {
							case <-state.changeTimer.C:
								storageState.stateChanged(event.Name, instanceIdent, state)

							case <-state.changeTimerChannel:
								log.WithField("instanceID", state.instanceID).Debug("Send state change cancel")
							}
						}()
					} else {
						state.changeTimer.Reset(StateChangeTimeout)
					}

					break
				}
			}

			storageState.Unlock()

		case err, ok := <-storageState.watcher.Errors:
			if !ok {
				return
			}

			log.Errorf("FS watcher error: %s", err)
		}
	}
}

func (storageState *StorageState) stateChanged(
	fileName string, instanceIdent cloudprotocol.InstanceIdent, state *stateParams,
) {
	storageState.Lock()
	defer storageState.Unlock()

	state.changeTimer = nil

	stateData, checksum, err := getFileDataChecksum(fileName)
	if err != nil {
		log.Errorf("Can't get state and checksum: %s", err)

		return
	}

	// This check is necessary because in UpdateState the data is saved to the file
	// and at the same time the file change event occurs
	if bytes.Equal(checksum, state.checksum) {
		return
	}

	state.checksum = checksum

	if err := storageState.pushNewStateMessage(
		instanceIdent, hex.EncodeToString(checksum), string(stateData)); err != nil {
		log.Errorf("Can't send new state message: %s", err)
	}
}

func (storageState *StorageState) prepareStorage(
	instanceID string, params SetupParams,
) (storagePath string, err error) {
	storagePath = path.Join(storageState.storageDir, instanceID)

	if params.StorageQuota == 0 {
		if err := os.RemoveAll(storagePath); err != nil {
			return "", aoserrors.Wrap(err)
		}

		return "", nil
	}

	if err := os.MkdirAll(storagePath, 0o755); err != nil {
		return "", aoserrors.Wrap(err)
	}

	if err = os.Chown(storagePath, params.UID, params.GID); err != nil {
		return "", aoserrors.Wrap(err)
	}

	return storagePath, nil
}

func (storageState *StorageState) setQuotasFS(params SetupParams) error {
	if storageState.isSamePartition {
		if err := SetUserFSQuota(
			storageState.storageDir, params.StorageQuota+params.StateQuota,
			uint32(params.UID), uint32(params.GID)); err != nil {
			return aoserrors.Wrap(err)
		}
	} else {
		if err := SetUserFSQuota(
			storageState.storageDir, params.StorageQuota, uint32(params.UID), uint32(params.GID)); err != nil {
			return aoserrors.Wrap(err)
		}

		if err := SetUserFSQuota(
			storageState.stateDir, params.StateQuota, uint32(params.UID), uint32(params.GID)); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func createStateFileIfNotExist(path string, uid, gid int) error {
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return aoserrors.Wrap(err)
		}

		if err := createStateFile(path, uid, gid); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func createStateFile(path string, uid, gid int) (err error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer file.Close()

	if err = os.Chown(path, uid, gid); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func getFileDataChecksum(fileName string) (data []byte, checksum []byte, err error) {
	if _, err = os.Stat(fileName); err != nil {
		if !os.IsNotExist(err) {
			return nil, nil, aoserrors.Wrap(err)
		}

		return nil, nil, nil
	}

	if data, err = ioutil.ReadFile(fileName); err != nil {
		return nil, nil, aoserrors.Wrap(err)
	}

	calcSum := sha3.Sum224(data)

	return data, calcSum[:], nil
}

func checkChecksum(state []byte, checksum []byte) (err error) {
	calcSum := sha3.Sum224(state)

	if !bytes.Equal(calcSum[:], checksum) {
		return aoserrors.New("wrong checksum")
	}

	return nil
}
