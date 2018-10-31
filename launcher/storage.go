package launcher

import (
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sync"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	storageDir = "storages"  // storages directory
	stateFile  = "state.dat" // stat file name
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type storageHandler struct {
	db          database.ServiceItf
	storagePath string
	mutex       sync.Mutex
}

/*******************************************************************************
 * Storage related API
 ******************************************************************************/

func newStorageHandler(workingDir string, db database.ServiceItf) (handler *storageHandler, err error) {
	handler = &storageHandler{db: db, storagePath: path.Join(workingDir, storageDir)}

	if _, err = os.Stat(handler.storagePath); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}

		if err = os.MkdirAll(handler.storagePath, 0755); err != nil {
			return nil, err
		}
	}

	return handler, nil
}

func (handler *storageHandler) MountStorageFolder(users []string, service database.ServiceEntry) (err error) {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()

	log.WithFields(log.Fields{
		"serviceID":    service.ID,
		"storageLimit": service.StorageLimit,
		"stateLimit":   service.StateLimit}).Debug("Mount storage folder")

	entry, err := handler.db.GetUsersEntry(users, service.ID)
	if err != nil {
		return err
	}

	configFile := path.Join(service.Path, "config.json")

	if service.StorageLimit == 0 {
		if entry.StorageFolder != "" {
			os.RemoveAll(entry.StorageFolder)
		}

		if err = handler.db.SetUsersStorageFolder(users, service.ID, ""); err != nil {
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

		if err = handler.db.SetUsersStorageFolder(users, service.ID, entry.StorageFolder); err != nil {
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

		if err = handler.db.SetUsersStateChecksum(users, service.ID, []byte{}); err != nil {
			return err
		}
	}

	if err = updateMountSpec(configFile, entry.StorageFolder); err != nil {
		return err
	}

	return nil
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
