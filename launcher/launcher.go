// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"path"

	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	serviceDatabase = "services.db"
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
	db *database
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New(workingDir string) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	db, err := newDatabase(path.Join(workingDir, serviceDatabase))

	if err != nil {
		return nil, err
	}

	return &Launcher{db}, nil
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
func (launcher *Launcher) InstallService(path string) (status <-chan error) {
	log.WithField("path", path).Debug("Install service")

	statusChannel := make(chan error, 1)

	go func() {
		statusChannel <- nil
	}()

	return statusChannel
}

// RemoveService stops and removes service
func (launcher *Launcher) RemoveService(id string) (status <-chan error) {
	log.WithField("id", id).Debug("Remove service")

	statusChannel := make(chan error, 1)
	go func() {
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
