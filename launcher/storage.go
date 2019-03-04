package launcher

import (
	"encoding/hex"
	"errors"
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
	specs "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/sha3"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	storageDir = "storages"  // storages directory
	stateFile  = "state.dat" // stat file name

	stateChangeTimeout    = 1 * time.Second
	acceptanceWaitTimeout = 10 * time.Second
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type storageHandler struct {
	serviceProvider ServiceProvider
	storagePath     string
	sync.Mutex
	watcher             *fsnotify.Watcher
	statesMap           map[string]*stateParams
	newStateChannel     chan<- NewState
	stateRequestChannel chan<- StateRequest
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

func newStorageHandler(workingDir string, serviceProvider ServiceProvider,
	newStateChannel chan<- NewState, stateRequestChannel chan<- StateRequest) (handler *storageHandler, err error) {
	handler = &storageHandler{
		serviceProvider:     serviceProvider,
		storagePath:         path.Join(workingDir, storageDir),
		newStateChannel:     newStateChannel,
		stateRequestChannel: stateRequestChannel}

	if _, err = os.Stat(handler.storagePath); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}

		if err = os.MkdirAll(handler.storagePath, 0755); err != nil {
			return nil, err
		}
	}

	if handler.watcher, err = fsnotify.NewWatcher(); err != nil {
		return nil, err
	}

	handler.statesMap = make(map[string]*stateParams)

	go handler.processWatcher()

	return handler, nil
}

func (handler *storageHandler) Close() {
	handler.watcher.Close()
}

func (handler *storageHandler) MountStorageFolder(users []string, service database.ServiceEntry) (err error) {
	handler.Lock()
	defer handler.Unlock()

	log.WithFields(log.Fields{
		"serviceID":    service.ID,
		"storageLimit": service.StorageLimit,
		"stateLimit":   service.StateLimit}).Debug("Mount storage folder")

	entry, err := handler.serviceProvider.GetUsersEntry(users, service.ID)
	if err != nil {
		return err
	}

	configFile := path.Join(service.Path, "config.json")

	if service.StorageLimit == 0 {
		if entry.StorageFolder != "" {
			os.RemoveAll(entry.StorageFolder)
		}

		if err = handler.serviceProvider.SetUsersStorageFolder(users, service.ID, ""); err != nil {
			return err
		}

		return updateMountSpec(configFile, "")
	}

	if entry.StorageFolder != "" {
		if _, err = os.Stat(entry.StorageFolder); err != nil {
			if !os.IsNotExist(err) {
				return err
			}

			log.WithFields(log.Fields{
				"folder":    entry.StorageFolder,
				"serviceID": service.ID}).Warning("Storage folder doesn't exist")

			entry.StorageFolder = ""
		}
	}

	if entry.StorageFolder == "" {
		if entry.StorageFolder, err = createStorageFolder(handler.storagePath, service.UserName); err != nil {
			return err
		}

		if err = handler.serviceProvider.SetUsersStorageFolder(users, service.ID, entry.StorageFolder); err != nil {
			return err
		}

		log.WithFields(log.Fields{"folder": entry.StorageFolder, "serviceID": service.ID}).Debug("Create storage folder")
	}

	if service.StateLimit == 0 {
		if _, err = os.Stat(path.Join(entry.StorageFolder, stateFile)); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		}

		if err = handler.serviceProvider.SetUsersStateChecksum(users, service.ID, []byte{}); err != nil {
			return err
		}
	}

	if err = updateMountSpec(configFile, entry.StorageFolder); err != nil {
		return err
	}

	if service.StateLimit > 0 {
		if err = handler.startStateWatching(users, service); err != nil {
			return err
		}
	}

	return nil
}

func (handler *storageHandler) StopStateWatching(users []string, service database.ServiceEntry) (err error) {
	handler.Lock()
	defer handler.Unlock()

	if service.StateLimit == 0 {
		return nil
	}

	entry, err := handler.serviceProvider.GetUsersEntry(users, service.ID)
	if err != nil {
		if err == database.ErrNotExist {
			return nil
		}

		return err
	}

	return handler.stopStateWatching(path.Join(entry.StorageFolder, stateFile), entry.StorageFolder)
}

func (handler *storageHandler) StateAcceptance(acceptance amqp.StateAcceptance, correlationID string) (err error) {
	handler.Lock()
	defer handler.Unlock()

	for _, state := range handler.statesMap {
		if state.correlationID == correlationID {
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

	return errors.New("Correlation ID not found")
}

func (handler *storageHandler) UpdateState(users []string, service database.ServiceEntry,
	state, checksum string) (err error) {
	handler.Lock()
	defer handler.Unlock()

	log.WithFields(log.Fields{
		"serviceID":  service.ID,
		"checksum":   checksum,
		"stateLimit": service.StateLimit,
		"stateSize":  len(state)}).Debug("Update state")

	if err = checkChecksum(state, checksum); err != nil {
		return err
	}

	if len(state) > int(service.StateLimit) {
		return errors.New("State is too big")
	}

	entry, err := handler.serviceProvider.GetUsersEntry(users, service.ID)
	if err != nil {
		return err
	}

	if err = ioutil.WriteFile(path.Join(entry.StorageFolder, stateFile), []byte(state), 0644); err != nil {
		return err
	}

	sumBytes, err := hex.DecodeString(checksum)
	if err != nil {
		return err
	}

	if err = handler.serviceProvider.SetUsersStateChecksum(users, service.ID, sumBytes); err != nil {
		return err
	}

	return nil
}

func (handler *storageHandler) startStateWatching(users []string, service database.ServiceEntry) (err error) {
	// no mutex as it is called from locked context

	entry, err := handler.serviceProvider.GetUsersEntry(users, service.ID)
	if err != nil {
		return err
	}

	stateFileName := path.Join(entry.StorageFolder, stateFile)

	log.WithFields(log.Fields{"serviceID": service.ID, "stateFile": stateFileName}).Debug("Start state watching")

	if _, ok := handler.statesMap[stateFileName]; ok {
		if err = handler.stopStateWatching(stateFileName, entry.StorageFolder); err != nil {
			return err
		}
	}

	state := stateParams{users: users, serviceID: service.ID}

	state.changeTimerChannel = make(chan bool)
	state.acceptanceTimerChannel = make(chan bool)

	handler.statesMap[stateFileName] = &state

	_, checksum, err := getFileAndChecksum(stateFileName)
	if err != nil && err != os.ErrNotExist {
		return err
	}

	if !reflect.DeepEqual(entry.StateChecksum, checksum) {
		log.WithFields(log.Fields{
			"serviceID": service.ID,
			"checksum":  checksum}).Warn("State file checksum mistmatch. Send state request")
		// Send state request
		handler.stateRequestChannel <- StateRequest{ServiceID: state.serviceID}
	}

	if err = handler.watcher.Add(entry.StorageFolder); err != nil {
		return err
	}

	return nil
}

func (handler *storageHandler) stopStateWatching(stateFileName, storageFolder string) (err error) {

	if state, ok := handler.statesMap[stateFileName]; ok {
		if err = handler.watcher.Remove(storageFolder); err != nil {
			return err
		}

		if state.changeTimer != nil {
			if state.changeTimer.Stop() {
				state.changeTimerChannel <- true
			}
		}

		if state.acceptanceTimer != nil {
			if state.acceptanceTimer.Stop() {
				state.acceptanceTimerChannel <- true
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
		handler.stateRequestChannel <- StateRequest{ServiceID: state.serviceID}
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

	newState := NewState{state.correlationID, state.serviceID, string(stateData), hex.EncodeToString(checksum)}

	go func() {
		log.WithFields(log.Fields{
			"serviceID":     newState.ServiceID,
			"correlationID": newState.CorrelationID,
			"checksum":      newState.Checksum}).Debug("Send new state")

		// Send new state under unlocked context: when newStateChannel is full it blocks here.
		// As result, if in offline mode newStateChannel becomes full, we wait here till online mode.
		handler.newStateChannel <- newState

		select {
		case <-state.acceptanceTimer.C:
			log.WithField("serviceID", state.serviceID).Error("Waiting state acceptance timeout")

		case <-state.acceptanceTimerChannel:
		}

		handler.handleStateAcception(state, checksum)
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

func createStorageFolder(path, userName string) (folderName string, err error) {
	if folderName, err = ioutil.TempDir(path, ""); err != nil {
		return "", err
	}

	uid, gid, err := getUserUIDGID(userName)
	if err != nil {
		return "", err
	}

	if err = os.Chown(folderName, int(uid), int(gid)); err != nil {
		return "", err
	}

	return folderName, nil
}

func createStateFile(path, userName string) (err error) {
	if _, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0666); err != nil {
		return err
	}

	uid, gid, err := getUserUIDGID(userName)
	if err != nil {
		return err
	}

	if err = os.Chown(path, int(uid), int(gid)); err != nil {
		return err
	}

	return nil
}

func updateMountSpec(specFile, storageFolder string) (err error) {
	spec, err := getServiceSpec(specFile)
	if err != nil {
		return err
	}

	absStorageFolder, err := filepath.Abs(storageFolder)
	if err != nil {
		return err
	}

	newMount := specs.Mount{
		Destination: "/home/service/storage",
		Type:        "bind",
		Source:      absStorageFolder,
		Options:     []string{"bind", "rw"}}

	storageIndex := len(spec.Mounts)

	for i, mount := range spec.Mounts {
		if mount.Destination == "/home/service/storage" {
			storageIndex = i
			break
		}
	}

	specChanged := false

	if storageIndex == len(spec.Mounts) && storageFolder != "" {
		spec.Mounts = append(spec.Mounts, newMount)
		specChanged = true
	}

	if storageIndex < len(spec.Mounts) {
		if storageFolder == "" {
			spec.Mounts = append(spec.Mounts[:storageIndex], spec.Mounts[storageIndex+1:]...)
			specChanged = true
		} else if !reflect.DeepEqual(spec.Mounts[storageIndex], newMount) {
			spec.Mounts[storageIndex] = newMount
			specChanged = true
		}
	}

	if specChanged {
		return writeServiceSpec(&spec, specFile)
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
		return nil, nil, err
	}

	calcSum := sha3.Sum224(data)

	return data, calcSum[:], nil
}

func checkChecksum(state, checksum string) (err error) {
	sum, err := hex.DecodeString(checksum)
	if err != nil {
		return err
	}

	calcSum := sha3.Sum224([]byte(state))

	if !reflect.DeepEqual(calcSum[:], sum) {
		return errors.New("Wrong checksum")
	}

	return nil
}
