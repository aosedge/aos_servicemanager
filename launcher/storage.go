// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2021 Renesas Electronics Corporation.
// Copyright (C) 2021 EPAM Systems, Inc.
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

package launcher

import (
	"encoding/hex"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/servicemanager/v1"
	"golang.org/x/crypto/sha3"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	stateFile = "state.dat" // state file name

	upperDirName = "upperdir"
	workDirName  = "workdir"

	stateChangeTimeout    = 1 * time.Second
	acceptanceWaitTimeout = 10 * time.Second
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type storageHandler struct {
	serviceProvider ServiceProvider
	storageDir      string
	sync.Mutex
	watcher      *fsnotify.Watcher
	statesMap    map[string]*stateParams
	stateChannel chan<- *pb.SMNotifications
}

type stateParams struct {
	users                  []string
	serviceID              string
	pendingChanges         bool
	stateAccepted          bool
	correlationID          string
	changeTimer            *time.Timer
	changeTimerChannel     chan bool
	acceptanceTimer        *time.Timer
	acceptanceTimerChannel chan bool
}

/*******************************************************************************
 * Storage related API
 ******************************************************************************/

func newStorageHandler(storageDir string, serviceProvider ServiceProvider,
	stateChannel chan<- *pb.SMNotifications) (handler *storageHandler, err error) {
	handler = &storageHandler{
		serviceProvider: serviceProvider,
		storageDir:      storageDir,
		stateChannel:    stateChannel}

	if _, err = os.Stat(handler.storageDir); err != nil {
		if !os.IsNotExist(err) {
			return nil, aoserrors.Wrap(err)
		}

		if err = os.MkdirAll(handler.storageDir, 0755); err != nil {
			return nil, aoserrors.Wrap(err)
		}
	}

	if handler.watcher, err = fsnotify.NewWatcher(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	handler.statesMap = make(map[string]*stateParams)

	go handler.processWatcher()

	return handler, nil
}

func (handler *storageHandler) Close() {
	handler.watcher.Close()
}

func (handler *storageHandler) PrepareStorageFolder(users []string, service Service,
	storageLimit, stateLimit uint64) (storageFolder string, err error) {
	handler.Lock()
	defer handler.Unlock()

	log.WithFields(log.Fields{
		"serviceID":    service.ID,
		"storageLimit": storageLimit,
		"stateLimit":   stateLimit}).Debug("Mount storage folder")

	usersService, err := handler.serviceProvider.GetUsersService(users, service.ID)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	if storageLimit == 0 {
		if usersService.StorageFolder != "" {
			os.RemoveAll(usersService.StorageFolder)
		}

		if err = handler.serviceProvider.SetUsersStorageFolder(users, service.ID, ""); err != nil {
			return "", aoserrors.Wrap(err)
		}

		return "", nil
	}

	if usersService.StorageFolder != "" {
		if _, err = os.Stat(usersService.StorageFolder); err != nil {
			if !os.IsNotExist(err) {
				return "", aoserrors.Wrap(err)
			}

			log.WithFields(log.Fields{
				"folder":    usersService.StorageFolder,
				"serviceID": service.ID}).Warning("Storage folder doesn't exist")

			usersService.StorageFolder = ""
		}
	}

	if usersService.StorageFolder == "" {
		if usersService.StorageFolder, err = createStorageFolder(handler.storageDir, service.UID, service.GID); err != nil {
			return "", aoserrors.Wrap(err)
		}

		if err = handler.serviceProvider.SetUsersStorageFolder(users, service.ID, usersService.StorageFolder); err != nil {
			return "", aoserrors.Wrap(err)
		}

		log.WithFields(log.Fields{"folder": usersService.StorageFolder, "serviceID": service.ID}).Debug("Create storage folder")
	}

	if stateLimit == 0 {
		if _, err = os.Stat(path.Join(usersService.StorageFolder, stateFile)); err != nil {
			if !os.IsNotExist(err) {
				return "", aoserrors.Wrap(err)
			}
		}

		if err = handler.serviceProvider.SetUsersStateChecksum(users, service.ID, []byte{}); err != nil {
			return "", aoserrors.Wrap(err)
		}
	}

	if stateLimit > 0 {
		if err = createStateFile(path.Join(usersService.StorageFolder, stateFile), service.UID, service.GID); err != nil {
			return "", aoserrors.Wrap(err)
		}

		if err = handler.startStateWatching(users, service); err != nil {
			return "", aoserrors.Wrap(err)
		}
	}

	return usersService.StorageFolder, nil
}

func (handler *storageHandler) StopStateWatching(users []string, service Service, stateLimit uint64) (err error) {
	handler.Lock()
	defer handler.Unlock()

	if stateLimit == 0 {
		return nil
	}

	usersService, err := handler.serviceProvider.GetUsersService(users, service.ID)
	if err != nil {
		if strings.Contains(err.Error(), "not exist") {
			return nil
		}

		return aoserrors.Wrap(err)
	}

	return aoserrors.Wrap(handler.stopStateWatching(path.Join(usersService.StorageFolder, stateFile), usersService.StorageFolder))
}

func (handler *storageHandler) StateAcceptance(acceptance *pb.StateAcceptance) (err error) {
	handler.Lock()
	defer handler.Unlock()

	for _, state := range handler.statesMap {
		if state.correlationID == acceptance.CorrelationId {
			if strings.ToLower(acceptance.Result) == "accepted" {
				state.stateAccepted = true
			} else {
				log.WithFields(log.Fields{
					"serviceID":     state.serviceID,
					"correlationID": state.correlationID}).Errorf("State is rejected due to: %s", acceptance.Reason)
			}

			if state.acceptanceTimer.Stop() {
				state.acceptanceTimerChannel <- true
			}

			return nil
		}
	}

	return aoserrors.New("correlation ID not found")
}

func (handler *storageHandler) UpdateState(users []string, service Service, state []byte, checksum string,
	stateLimit uint64) (err error) {
	handler.Lock()
	defer handler.Unlock()

	log.WithFields(log.Fields{
		"serviceID":  service.ID,
		"checksum":   checksum,
		"stateLimit": stateLimit,
		"stateSize":  len(state)}).Debug("Update state")

	if err = checkChecksum(state, checksum); err != nil {
		return aoserrors.Wrap(err)
	}

	if len(state) > int(stateLimit) {
		return aoserrors.New("state is too big")
	}

	usersService, err := handler.serviceProvider.GetUsersService(users, service.ID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = ioutil.WriteFile(path.Join(usersService.StorageFolder, stateFile), state, 0644); err != nil {
		return aoserrors.Wrap(err)
	}

	sumBytes, err := hex.DecodeString(checksum)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = handler.serviceProvider.SetUsersStateChecksum(users, service.ID, sumBytes); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (handler *storageHandler) startStateWatching(users []string, service Service) (err error) {
	// no mutex as it is called from locked context

	usersService, err := handler.serviceProvider.GetUsersService(users, service.ID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	stateFileName := path.Join(usersService.StorageFolder, stateFile)

	log.WithFields(log.Fields{"serviceID": service.ID, "stateFile": stateFileName}).Debug("Start state watching")

	if _, ok := handler.statesMap[stateFileName]; ok {
		if err = handler.stopStateWatching(stateFileName, usersService.StorageFolder); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	state := stateParams{users: users, serviceID: service.ID}

	state.changeTimerChannel = make(chan bool)
	state.acceptanceTimerChannel = make(chan bool)

	handler.statesMap[stateFileName] = &state

	_, checksum, err := getFileAndChecksum(stateFileName)
	if err != nil && err != os.ErrNotExist {
		return aoserrors.Wrap(err)
	}

	if !reflect.DeepEqual(usersService.StateChecksum, checksum) {
		log.WithFields(log.Fields{
			"serviceID": service.ID,
			"checksum":  hex.EncodeToString(checksum)}).Warn("State file checksum mistmatch. Send state request")

		// Send state request
		if err := handler.pushServiceStateMessage(&pb.SMNotifications{SMNotification: &pb.SMNotifications_ServiceStateRequest{
			ServiceStateRequest: &pb.ServiceStateRequest{
				ServiceId: state.serviceID, Default: false, Users: &pb.Users{Users: users},
			}}}); err != nil {
			log.Warn("Can't send service state request: ", err.Error())
		}
	}

	if err = handler.watcher.Add(usersService.StorageFolder); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (handler *storageHandler) stopStateWatching(stateFileName, storageFolder string) (err error) {
	log.WithFields(log.Fields{"stateFile": stateFileName}).Debug("Stop state watching")

	if state, ok := handler.statesMap[stateFileName]; ok {
		if err = handler.watcher.Remove(storageFolder); err != nil {
			return aoserrors.Wrap(err)
		}

		if state.changeTimer != nil {
			if state.changeTimer.Stop() {
				state.changeTimerChannel <- false
			}
		}

		if state.acceptanceTimer != nil {
			if state.acceptanceTimer.Stop() {
				state.acceptanceTimerChannel <- false
			}
		}

		delete(handler.statesMap, stateFileName)
	}

	return nil
}

func (handler *storageHandler) handleStateAcception(state *stateParams, checksum []byte) {
	handler.Lock()
	defer handler.Unlock()

	if state.stateAccepted {
		log.WithFields(log.Fields{
			"serviceID":     state.serviceID,
			"correlationID": state.correlationID}).Debug("State is accepted")

		if err := handler.serviceProvider.SetUsersStateChecksum(state.users, state.serviceID, checksum); err != nil {
			log.WithField("serviceID", state.serviceID).Errorf("Can't set state checksum: %s", err)
		}
	} else {
		// Send state request
		if err := handler.pushServiceStateMessage(&pb.SMNotifications{SMNotification: &pb.SMNotifications_ServiceStateRequest{
			ServiceStateRequest: &pb.ServiceStateRequest{
				ServiceId: state.serviceID, Default: false, Users: &pb.Users{Users: state.users},
			}}}); err != nil {
			log.Warn("Can't send service state request: ", err.Error())
		}
	}

	state.correlationID = ""
	state.acceptanceTimer = nil
}

func (handler *storageHandler) stateChanged(fileName string, state *stateParams) {
	handler.Lock()
	defer handler.Unlock()

	log.WithField("serviceID", state.serviceID).Debug("State changed")

	state.changeTimer = nil

	// If waiting for acceptance message, set pendingChanges flag and exit
	if state.acceptanceTimer != nil {
		state.pendingChanges = true
		return
	}

	// Prepate new state to send
	stateData, checksum, err := getFileAndChecksum(fileName)
	if err != nil {
		log.WithField("serviceID", state.serviceID).Errorf("Can't get state and checksum: %s", err)
		return
	}

	state.stateAccepted = false
	state.acceptanceTimer = time.NewTimer(acceptanceWaitTimeout)
	state.correlationID = uuid.New().String()

	go func() {
		log.WithFields(log.Fields{
			"serviceID":     state.serviceID,
			"correlationID": state.correlationID,
			"checksum":      hex.EncodeToString(checksum)}).Debug("Send new state")

		if err := handler.pushServiceStateMessage(&pb.SMNotifications{SMNotification: &pb.SMNotifications_NewServiceState{
			NewServiceState: &pb.NewServiceState{CorrelationId: state.correlationID,
				ServiceState: &pb.ServiceState{
					ServiceId:     state.serviceID,
					StateChecksum: hex.EncodeToString(checksum),
					State:         stateData,
				}}}}); err != nil {
			log.Warn(err.Error())

			handler.Lock()
			defer handler.Unlock()

			state.correlationID = ""
			state.acceptanceTimer = nil

			return
		}

		select {
		case <-state.acceptanceTimer.C:
			log.WithField("serviceID", state.serviceID).Error("Waiting state acceptance timeout")

		case value := <-state.acceptanceTimerChannel:
			if value {
				handler.handleStateAcception(state, checksum)
			}
		}
	}()
}

func (handler *storageHandler) processWatcher() {
	for {
		select {
		case event, ok := <-handler.watcher.Events:
			if !ok {
				return
			}

			handler.Lock()

			if state, ok := handler.statesMap[event.Name]; ok {
				log.WithField("file", event.Name).Debug("File changed")

				if state.changeTimer == nil {
					state.changeTimer = time.NewTimer(stateChangeTimeout)
					go func() {
						select {
						case <-state.changeTimer.C:
							handler.stateChanged(event.Name, state)

						case <-state.changeTimerChannel:
						}
					}()
				} else {
					state.changeTimer.Reset(stateChangeTimeout)
				}
			}

			handler.Unlock()

		case err, ok := <-handler.watcher.Errors:
			if !ok {
				return
			}

			log.Errorf("FS watcher error: %s", err)
		}
	}
}

func (handler *storageHandler) pushServiceStateMessage(msg *pb.SMNotifications) (err error) {
	// Send new state under unlocked context: when newStateChannel is full it blocks here.
	// As result, if in offline mode newStateChannel becomes full, we wait here till online mode.
	// Drop all changes if channel is full
	if len(handler.stateChannel) >= cap(handler.stateChannel) {
		return aoserrors.New("state channel is full")
	}

	handler.stateChannel <- msg

	return nil
}

func createStorageFolder(path string, uid, gid uint32) (folderName string, err error) {
	if folderName, err = ioutil.TempDir(path, ""); err != nil {
		return "", aoserrors.Wrap(err)
	}

	if err = os.Chown(folderName, int(uid), int(gid)); err != nil {
		return "", aoserrors.Wrap(err)
	}

	upperDir := filepath.Join(folderName, upperDirName)

	if err = os.MkdirAll(upperDir, 0755); err != nil {
		return "", aoserrors.Wrap(err)
	}

	if err = os.Chown(upperDir, int(uid), int(gid)); err != nil {
		return "", aoserrors.Wrap(err)
	}

	workDir := filepath.Join(folderName, workDirName)

	if err = os.MkdirAll(workDir, 0755); err != nil {
		return "", aoserrors.Wrap(err)
	}

	if err = os.Chown(workDir, int(uid), int(gid)); err != nil {
		return "", aoserrors.Wrap(err)
	}

	return folderName, nil
}

func createStateFile(path string, uid, gid uint32) (err error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer file.Close()

	if err = os.Chown(path, int(uid), int(gid)); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func getFileAndChecksum(fileName string) (data []byte, checksum []byte, err error) {
	if _, err = os.Stat(fileName); err != nil {
		if !os.IsNotExist(err) {
			return nil, nil, err
		}

		return nil, nil, nil
	}

	data, err = ioutil.ReadFile(fileName)
	if err != nil {
		return nil, nil, aoserrors.Wrap(err)
	}

	calcSum := sha3.Sum224(data)

	return data, calcSum[:], nil
}

func checkChecksum(state []byte, checksum string) (err error) {
	sum, err := hex.DecodeString(checksum)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	calcSum := sha3.Sum224(state)

	if !reflect.DeepEqual(calcSum[:], sum) {
		return aoserrors.New("wrong checksum")
	}

	return nil
}
