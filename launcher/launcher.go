// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	// services database name
	serviceDatabase = "services.db"
	// services directory
	serviceDir = "services"
)

// Service status
const (
	statusOk = iota
	statusError
)

// Service state
const (
	stateInit = iota
	stateRunning
	stateStopped
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type serviceStatus int
type serviceState int

type ServiceInfo struct {
	Id      string `json:id`
	Version uint   `json:version`
	Status  string `json:status`
}

// Launcher instance
type Launcher struct {
	db         *database
	workingDir string
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New(workingDir string) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	// Check and create service dir
	dir := path.Join(workingDir, serviceDir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}

	// Create new database instance
	db, err := newDatabase(path.Join(workingDir, serviceDatabase))
	if err != nil {
		return nil, err
	}

	return &Launcher{db, workingDir}, nil
}

// Close closes launcher
func (launcher *Launcher) Close() {
	log.Debug("Close launcher")
	launcher.db.close()
}

// GetServiceVersion returns installed version of requested service
func (launcher *Launcher) GetServiceVersion(id string) (version uint, err error) {
	log.WithField("id", id).Debug("Get service version")

	return 1, nil
}

// InstallService installs and runs service
func (launcher *Launcher) InstallService(image string) (status <-chan error) {
	log.WithField("path", image).Debug("Install service")

	statusChannel := make(chan error, 1)

	go func() {
		// TODO: do we need install to /tmp dir first?
		// In case something wrong, artifacts will be removed after system reboot
		// but it will introduce additional io operations.

		// create install dir
		installDir, err := ioutil.TempDir(path.Join(launcher.workingDir, serviceDir), "")
		if err != nil {
			statusChannel <- err
			return
		}
		log.WithField("dir", installDir).Debug("Create install dir")

		defer func() {
			if err := recover(); err != nil {
				os.RemoveAll(installDir)
				statusChannel <- err.(error)
			}
		}()

		// unpack image there
		if err := UnpackImage(image, installDir); err != nil {
			panic(err)
		}

		// get id and version from config.json
		id, version, err := getConfigServiceInfo(path.Join(installDir, "config.json"))
		if err != nil {
			panic(err)
		}
		log.WithFields(log.Fields{"id": id, "version": version}).Debug("Found service")

		// check if service already installed
		// TODO: check version?
		service, err := launcher.db.getService(id)
		if err != nil && !strings.Contains(err.Error(), "does not exist") {
			panic(err)
		}
		serviceExists := err == nil

		if serviceExists {
			if err := launcher.updateService(service, version, installDir); err != nil {
				panic(err)
			}
		} else {
			if err := launcher.newService(id, version, installDir); err != nil {
				panic(err)
			}
		}

		log.WithField("id", id).Info("Service successfully installed")

		statusChannel <- nil
	}()

	return statusChannel
}

// RemoveService stops and removes service
func (launcher *Launcher) RemoveService(id string) (status <-chan error) {
	log.WithField("id", id).Debug("Remove service")

	statusChannel := make(chan error, 1)

	go func() {
		// get service
		service, err := launcher.db.getService(id)
		if err != nil {
			statusChannel <- err
			return
		}

		// stop service
		if err := launcher.stopService(service.id); err != nil {
			statusChannel <- err
			return
		}

		// remove service
		if err := launcher.db.removeService(service.id); err != nil {
			// try to start it again
			if err := launcher.startService(service.id, service.path); err != nil {
				log.WithField("id", service.id).Error("Can't start service")
			}
			statusChannel <- err
			return
		}

		// remove service path
		if err := os.RemoveAll(service.path); err != nil {
			log.WithField("path", service.path).Error("Can't remove service path")
		}

		log.WithField("id", id).Info("Service successfully removed")

		statusChannel <- nil
	}()

	return statusChannel
}

// GetServicesInfo returns informaion about all installed services
func (launcher *Launcher) GetServicesInfo() (info []ServiceInfo, err error) {
	log.Debug("Get services info")

	return []ServiceInfo{}, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func getConfigServiceInfo(config string) (id string, version uint, err error) {
	id, version = "", 0

	raw, err := ioutil.ReadFile(config)
	if err != nil {
		return id, version, err
	}

	var spec specs.Spec

	if err = json.Unmarshal(raw, &spec); err != nil {
		return id, version, err
	}

	id, isPresent := spec.Annotations["packageName"]
	if !isPresent {
		return id, version, errors.New("No service id provided")
	}

	strVersion, isPresent := spec.Annotations["version"]
	if !isPresent {
		return id, version, errors.New("No service version provided")
	}
	result, err := strconv.ParseUint(strVersion, 10, 32)
	if err != nil {
		return id, version, err
	}
	version = uint(result)

	return id, version, nil
}

func (launcher *Launcher) updateService(service serviceEntry, version uint, installDir string) (err error) {
	// stop service
	if err := launcher.stopService(service.id); err != nil {
		return err
	}
	// remove from db
	if err := launcher.db.removeService(service.id); err != nil {
		// try to start it again
		if err := launcher.startService(service.id, service.path); err != nil {
			log.WithField("id", service.id).Error("Can't start service")
		}
		return err
	}

	if err := launcher.newService(service.id, version, installDir); err != nil {
		// try to restore old service
		if err := launcher.db.addService(service); err != nil {
			log.WithField("id", service.id).Error("Can't add service to db")
		}
		if err := launcher.startService(service.id, service.path); err != nil {
			log.WithField("id", service.id).Error("Can't start service")
		}
		return err
	}

	// remove old service path
	if err := os.RemoveAll(service.path); err != nil {
		log.WithField("path", service.path).Error("Can't remove service path")
	}

	return nil
}

func (launcher *Launcher) newService(id string, version uint, installDir string) (err error) {
	service := serviceEntry{
		id:      id,
		version: version,
		path:    installDir,
		state:   StateInit,
		status:  StatusOk}

	if err := launcher.db.addService(service); err != nil {
		return err
	}

	if err := launcher.startService(id, installDir); err != nil {
		// try to remove from db in case error
		if err := launcher.db.removeService(id); err != nil {
			log.WithField("id", id).Error("Can't remove service")
		}
		return err
	}

	return nil
}

func (launcher *Launcher) startService(id string, serviceDir string) (err error) {
	log.WithField("id", id).Debug("Start service")

	return nil
}

func (launcher *Launcher) stopService(id string) (err error) {
	log.WithField("id", id).Debug("Stop service")

	return nil
}

/*
func (launcher *Launcher) run(name string) {
	log.WithField("name", name).Debug("Start service")

	ctx := context.Background()

	io, err := runc.NewNullIO()

	if err != nil {
		log.Fatal("Can't create IO: ", err)
	}

	result, err := runtime.Run(ctx, "test", path.Join(ServicePath, name), &runc.CreateOpts{IO: io})

	if err != nil {
		log.Fatal("Can't run service: ", err)
	}

	log.Debug("Service finished with status: ", result)
}
*/
