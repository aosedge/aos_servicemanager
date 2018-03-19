// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"context"
	"errors"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/go-runc"
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

var (
	statusStr []string = []string{"OK", "Error"}
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
	runtime    runc.Runc
	workingDir string
	mutex      *sync.Mutex
	services   map[string]*service
}

type service struct {
	context context.Context
	socket  *runc.Socket
	status  chan serviceStatus
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
			return launcher, err
		}
	}

	// Create new database instance
	db, err := newDatabase(path.Join(workingDir, serviceDatabase))
	if err != nil {
		return launcher, err
	}

	// Create runtime
	runtime := runc.Runc{
		LogFormat:    runc.JSON,
		PdeathSignal: syscall.SIGKILL}

	// Initialize services map
	services := make(map[string]*service)

	launcher = &Launcher{db, runtime, workingDir, &sync.Mutex{}, services}

	if err := launcher.startInstalledServices(); err != nil {
		return launcher, err
	}

	return launcher, nil
}

// Close closes launcher
func (launcher *Launcher) Close() {
	log.Debug("Close launcher")

	// stop all running services
	for id, _ := range launcher.services {
		if err := launcher.stopService(id); err != nil {
			log.WithField("id", id).Errorf("Can't stop service: %s", err)
		}
	}

	launcher.db.close()
}

// GetServiceVersion returns installed version of requested service
func (launcher *Launcher) GetServiceVersion(id string) (version uint, err error) {
	log.WithField("id", id).Debug("Get service version")

	service, err := launcher.db.getService(id)
	if err != nil {
		return version, err
	}

	version = service.version

	return version, nil
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

		configFile := path.Join(installDir, "config.json")

		// get service spec
		spec, err := GetServiceSpec(configFile)
		if err != nil {
			panic(err)
		}

		// update config.json
		if err := launcher.updateServiceSpec(&spec); err != nil {
			panic(err)
		}

		// update config.json
		if err := WriteServiceSpec(&spec, configFile); err != nil {
			panic(err)
		}

		// get id and version from config.json
		id, version, err := getServiceInfo(&spec)
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

	services, err := launcher.db.getServices()
	if err != nil {
		return info, err
	}

	info = make([]ServiceInfo, len(services))

	for i, service := range services {
		info[i] = ServiceInfo{service.id, service.version, statusStr[service.status]}
	}

	return info, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (launcher *Launcher) updateServiceSpec(spec *specs.Spec) (err error) {
	mounts := []specs.Mount{
		specs.Mount{"/etc/resolv.conf", "bind", "/etc/resolv.conf", []string{"bind", "ro"}},
		specs.Mount{"/bin", "bind", "/bin", []string{"bind", "ro"}},
		specs.Mount{"/sbin", "bind", "/sbin", []string{"bind", "ro"}},
		specs.Mount{"/lib", "bind", "/lib", []string{"bind", "ro"}},
		specs.Mount{"/usr", "bind", "/usr", []string{"bind", "ro"}}}
	spec.Mounts = append(spec.Mounts, mounts...)
	// add lib64 if exists
	if _, err := os.Stat("/lib64"); err == nil {
		spec.Mounts = append(spec.Mounts, specs.Mount{"/lib64", "bind", "/lib64", []string{"bind", "ro"}})
	}
	// add hosts if exists
	hosts, _ := filepath.Abs(path.Join(launcher.workingDir, "hosts"))
	if _, err := os.Stat(hosts); err == nil {
		spec.Mounts = append(spec.Mounts, specs.Mount{"/etc/hosts/", "bind", hosts, []string{"bind", "ro"}})
	}

	// add netns hook
	// TODO: consider env variable or config to netns path
	if spec.Hooks == nil {
		spec.Hooks = &specs.Hooks{}
	}
	spec.Hooks.Prestart = append(spec.Hooks.Prestart, specs.Hook{Path: "/usr/local/bin/netns"})

	return nil
}

func getServiceInfo(spec *specs.Spec) (id string, version uint, err error) {
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
		if !strings.Contains(err.Error(), "not started") {
			return err
		}
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
		state:   stateInit,
		status:  statusOk}

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
	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	log.WithField("id", id).Debug("Start service")

	_, ok := launcher.services[id]
	if ok {
		return errors.New("Service already started")
	}

	ctx := context.Background()

	// check current status of the container
	container, err := launcher.runtime.State(ctx, id)
	if err != nil {
		if !strings.Contains(err.Error(), "does not exist") {
			return err
		}
	}

	if container != nil {
		switch container.Status {
		case "running":
			log.WithField(id, "id").Warning("Service is already running")
			if err := launcher.runtime.Kill(ctx, id, int(syscall.SIGKILL), &runc.KillOpts{}); err != nil {
				return err
			}
			if err := waitProcessFinished(container.Pid); err != nil {
				return err
			}
		}
		if err := launcher.runtime.Delete(ctx, id, &runc.DeleteOpts{}); err != nil {
			return err
		}
	}

	// create io
	runIO, err := runc.NewSTDIO()
	if err != nil {
		return err
	}

	consoleSocket, err := runc.NewTempConsoleSocket()
	if err != nil {
		return err
	}

	opts := runc.CreateOpts{
		IO:            runIO,
		ConsoleSocket: consoleSocket}

	// create container
	if err := launcher.runtime.Create(ctx, id, serviceDir, &opts); err != nil {
		consoleSocket.Close()
		return err
	}

	// create container
	if err := launcher.runtime.Start(ctx, id); err != nil {
		consoleSocket.Close()
		return err
	}

	launcher.services[id] = &service{
		context: ctx,
		socket:  consoleSocket,
		status:  make(chan serviceStatus, 1)}

	return nil
}

func (launcher *Launcher) stopService(id string) (err error) {
	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	log.WithField("id", id).Debug("Stop service")

	service, ok := launcher.services[id]
	if !ok {
		return errors.New("Service is not started")
	}

	if err := launcher.runtime.Kill(service.context, id, int(syscall.SIGKILL), &runc.KillOpts{}); err != nil {
		if !strings.Contains(err.Error(), "does not exist") {
			return err
		}
	}

	// check current status of the container
	container, err := launcher.runtime.State(service.context, id)
	if err != nil {
		if !strings.Contains(err.Error(), "does not exist") {
			return err
		}
	}

	if container != nil && container.Status == "running" {
		log.WithField("id", id).Debugf("Wait for service finished, pid: %d", container.Pid)
		waitProcessFinished(container.Pid)
	}

	if err := launcher.runtime.Delete(service.context, id, &runc.DeleteOpts{}); err != nil {
		if !strings.Contains(err.Error(), "does not exist") {
			return err
		}
	}

	delete(launcher.services, id)

	return nil
}

func (launcher *Launcher) startInstalledServices() (err error) {
	services, err := launcher.db.getServices()
	if err != nil {
		return err
	}

	for _, service := range services {
		if err := launcher.startService(service.id, service.path); err != nil {
			log.WithField("id", service.id).Error("Can't start service")
		}
	}

	return nil
}

func waitProcessFinished(pid int) (err error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	for {
		err := process.Signal(syscall.Signal(0))
		if err != nil && (strings.Contains(err.Error(), "no such process") ||
			strings.Contains(err.Error(), "process already finished")) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}
