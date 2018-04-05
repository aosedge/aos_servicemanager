// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-systemd/dbus"
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
	stateStr  []string = []string{"Init", "Running", "Stopped"}
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
	db              *database
	systemd         *dbus.Conn
	closeChannel    chan bool
	services        sync.Map
	serviceTemplate string
	workingDir      string
	runcPath        string
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New(workingDir string) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	var localLauncher Launcher

	localLauncher.workingDir = workingDir
	localLauncher.closeChannel = make(chan bool)

	// Check and create service dir
	dir := path.Join(workingDir, serviceDir)
	if _, err := os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return launcher, err
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return launcher, err
		}
	}

	// Create new database instance
	localLauncher.db, err = newDatabase(path.Join(workingDir, serviceDatabase))
	if err != nil {
		return launcher, err
	}

	// Load all installed services
	services, err := localLauncher.db.getServices()
	if err != nil {
		return launcher, err
	}
	for _, service := range services {
		localLauncher.services.Store(service.serviceName, service.id)
	}

	// Create systemd connection
	localLauncher.systemd, err = dbus.NewSystemConnection()
	if err != nil {
		return launcher, err
	}
	if err = localLauncher.systemd.Subscribe(); err != nil {
		return launcher, err
	}
	serviceChannel, errorChannel := localLauncher.systemd.SubscribeUnitsCustom(time.Millisecond*1000,
		2,
		func(u1, u2 *dbus.UnitStatus) bool { return *u1 != *u2 },
		func(serviceName string) bool {
			if _, exist := localLauncher.services.Load(serviceName); exist {
				return false
			}
			return true
		})
	go func() {
		for {
			select {
			case services := <-serviceChannel:
				for _, service := range services {
					var (
						state  serviceState
						status serviceStatus
					)

					if service == nil {
						continue
					}

					log.WithField("name", service.Name).Debugf(
						"Service state changed. Load state: %s, active state: %s, sub state: %s",
						service.LoadState,
						service.ActiveState,
						service.SubState)

					switch service.SubState {
					case "running":
						state = stateRunning
						status = statusOk
					default:
						state = stateStopped
						status = statusError
					}

					log.WithField("name", service.Name).Debugf("Set service state: %s, status: %s", stateStr[state], statusStr[status])

					id, exist := localLauncher.services.Load(service.Name)

					if exist {
						if err := localLauncher.db.setServiceState(id.(string), state); err != nil {
							log.WithField("name", service.Name).Error("Can't set service state: ", err)
						}

						if err := localLauncher.db.setServiceStatus(id.(string), status); err != nil {
							log.WithField("name", service.Name).Error("Can't set service status: ", err)
						}
					} else {
						log.WithField("name", service.Name).Warning("Can't update state or status. Service is not installed.")
					}
				}
			case err := <-errorChannel:
				log.Error("Subscription error: ", err)
			case <-localLauncher.closeChannel:
				return
			}
		}
	}()

	// Get systemd service template
	localLauncher.serviceTemplate, err = getSystemdServiceTemplate(workingDir)
	if err != nil {
		return launcher, err
	}

	// Retreive runc abs path
	localLauncher.runcPath, err = exec.LookPath("runc")
	if err != nil {
		return launcher, err
	}

	launcher = &localLauncher

	return launcher, nil
}

// Close closes launcher
func (launcher *Launcher) Close() {
	log.Debug("Close launcher")

	if err := launcher.systemd.Unsubscribe(); err != nil {
		log.Warn("Can't unsubscribe from systemd: ", err)
	}

	launcher.closeChannel <- true

	launcher.systemd.Close()
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

		// unpack image there
		if err := UnpackImage(image, installDir); err != nil {
			statusChannel <- err
			return
		}

		if err := launcher.installService(installDir); err != nil {
			statusChannel <- err
			return
		}

		statusChannel <- nil
	}()

	return statusChannel
}

// RemoveService stops and removes service
func (launcher *Launcher) RemoveService(id string) (status <-chan error) {
	statusChannel := make(chan error, 1)

	go func() {
		// remove service
		if err := launcher.removeService(id); err != nil {
			statusChannel <- err
			return
		}

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

func getSystemdServiceTemplate(workingDir string) (template string, err error) {
	template = `[Unit]
Description=AOS Service
After=network.target

[Service]
Type=forking
Restart=always
RestartSec=1
ExecStart=%s
ExecStop=%s
ExecStopPost=%s
PIDFile=%s

[Install]
WantedBy=multi-user.target
`
	fileName := path.Join(workingDir, "template.service")
	fileContent, err := ioutil.ReadFile(fileName)
	if err != nil {
		if !os.IsNotExist(err) {
			return template, err
		}

		log.Warnf("Service template file does not exist. Creating %s", fileName)

		if err = ioutil.WriteFile(fileName, []byte(template), 0644); err != nil {
			return template, err
		}
	} else {
		template = string(fileContent)
	}

	return template, nil
}

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
	id, isPresent := spec.Annotations["id"]
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

func (launcher *Launcher) startService(serviceFile, serviceName string) (err error) {
	if _, _, err := launcher.systemd.EnableUnitFiles([]string{serviceFile}, false, true); err != nil {
		return err
	}

	if err := launcher.systemd.Reload(); err != nil {
		return err
	}

	channel := make(chan string)
	if _, err = launcher.systemd.StartUnit(serviceName, "replace", channel); err != nil {
		return err
	}
	status := <-channel

	log.WithFields(log.Fields{"name": serviceName, "status": status}).Debug("Start service")

	return nil
}

func (launcher *Launcher) stopService(serviceName string) (err error) {
	channel := make(chan string)
	if _, err := launcher.systemd.StopUnit(serviceName, "replace", channel); err != nil {
		return err
	}
	status := <-channel

	log.WithFields(log.Fields{"name": serviceName, "status": status}).Debug("Stop service")

	if _, err := launcher.systemd.DisableUnitFiles([]string{serviceName}, false); err != nil {
		return err
	}

	if err := launcher.systemd.Reload(); err != nil {
		return err
	}

	return nil
}

func (launcher *Launcher) createSystemdService(installDir, serviceName, id string) (fileName string, err error) {
	f, err := os.Create(path.Join(installDir, serviceName))
	if err != nil {
		return fileName, err
	}
	defer f.Close()

	absServicePath, err := filepath.Abs(installDir)
	if err != nil {
		return fileName, err
	}

	pidFile := path.Join(absServicePath, id+".pid")
	execStartString := launcher.runcPath + " run -d --pid-file " + pidFile + " -b " + absServicePath + " " + id
	execStopString := launcher.runcPath + " kill -a " + id + " SIGKILL"
	execStopPostString := launcher.runcPath + " delete " + id

	lines := strings.SplitAfter(launcher.serviceTemplate, "\n")
	for _, line := range lines {
		switch {
		// the order is important for example: execstoppost should be evaluated
		// before execstop as execstop is substring of execstoppost
		case strings.Contains(strings.ToLower(line), "execstart"):
			if _, err := fmt.Fprintf(f, line, execStartString); err != nil {
				return fileName, err
			}
		case strings.Contains(strings.ToLower(line), "execstoppost"):
			if _, err := fmt.Fprintf(f, line, execStopPostString); err != nil {
				return fileName, err
			}
		case strings.Contains(strings.ToLower(line), "execstop"):
			if _, err := fmt.Fprintf(f, line, execStopString); err != nil {
				return fileName, err
			}
		case strings.Contains(strings.ToLower(line), "pidfile"):
			if _, err := fmt.Fprintf(f, line, pidFile); err != nil {
				return fileName, err
			}
		default:
			fmt.Fprint(f, line)
		}
	}

	if fileName, err = filepath.Abs(f.Name()); err != nil {
		return fileName, err
	}

	return fileName, nil
}

func (launcher *Launcher) installService(installDir string) (err error) {
	configFile := path.Join(installDir, "config.json")

	// get service spec
	spec, err := GetServiceSpec(configFile)
	if err != nil {
		return err
	}

	// update config.json
	if err := launcher.updateServiceSpec(&spec); err != nil {
		return err
	}

	// update config.json
	if err := WriteServiceSpec(&spec, configFile); err != nil {
		return err
	}

	// get id and version from config.json
	id, version, err := getServiceInfo(&spec)
	if err != nil {
		return err
	}
	log.WithFields(log.Fields{"id": id, "version": version}).Debug("Install service")

	// check if service already installed
	// TODO: check version?
	service, err := launcher.db.getService(id)
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		return err
	}

	// remove if exists
	if err == nil {
		log.WithField("name", id).Debug("Service exists.")

		if err := launcher.removeService(id); err != nil {
			return err
		}
	}

	serviceName := "aos_" + id + ".service"

	serviceFile, err := launcher.createSystemdService(installDir, serviceName, id)
	if err != nil {
		return err
	}

	if err := launcher.startService(serviceFile, serviceName); err != nil {
		return err
	}

	service = serviceEntry{
		id:          id,
		version:     version,
		path:        installDir,
		serviceName: serviceName,
		state:       stateInit,
		status:      statusOk}

	// add to database
	if err := launcher.db.addService(service); err != nil {
		if err := launcher.stopService(serviceName); err != nil {
			log.WithField("name", serviceName).Warn("Can't stop service: ", err)
		}
		return err
	}

	launcher.services.Store(serviceName, id)

	log.WithFields(log.Fields{"id": id, "version": version}).Info("Service successfully installed")

	return nil
}

func (launcher *Launcher) removeService(id string) (err error) {
	service, err := launcher.db.getService(id)
	if err != nil {
		return err
	}
	log.WithFields(log.Fields{"id": service.id, "version": service.version}).Debug("Remove service")

	launcher.services.Delete(service.serviceName)

	if err := launcher.stopService(service.serviceName); err != nil {
		log.WithField("name", service.serviceName).Warn("Can't stop service: ", err)
	}

	if err := launcher.db.removeService(service.id); err != nil {
		log.WithField("name", service.serviceName).Warn("Can't remove service from db: ", err)
	}

	if err := os.RemoveAll(service.path); err != nil {
		log.WithField("path", service.path).Error("Can't remove service path")
	}

	log.WithFields(log.Fields{"id": id, "version": service.version}).Info("Service successfully removed")

	return nil
}
