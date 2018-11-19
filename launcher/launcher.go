// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"encoding/json"
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
	specs "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/monitoring"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	serviceDir = "services" // services directory

	runcName         = "runc"         // runc file name
	netnsName        = "netns"        // netns file name
	wonderShaperName = "wondershaper" // wondershaper name

	aosProductPrefix = "com.epam.aos." //prefix used in annotations to get aos related entries
)

const (
	systemdSubscribeBuffers  = 32
	systemdSubscribeInterval = 500 * time.Millisecond
)

const (
	stateChannelSize = 32
)

var (
	statusStr = []string{"OK", "Error"}
	stateStr  = []string{"Init", "Running", "Stopped"}
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

// Action
const (
	ActionInstall = iota
	ActionRemove
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type actionType int

// ActionStatus status of performed action
type ActionStatus struct {
	Action  actionType
	ID      string
	Version uint64
	Err     error
}

// NewState new state message
type NewState struct {
	CorrelationID string
	ServiceID     string
	State         string
	Checksum      string
}

// StateRequest state request message
type StateRequest struct {
	ServiceID string
	Default   bool
}

type downloadItf interface {
	downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error)
}

// Launcher instance
type Launcher struct {
	// StatusChannel used to return execute command statuses
	StatusChannel chan ActionStatus
	// NewStateChannel used to notify about new service state
	NewStateChannel chan NewState
	// StateRequestChannel used to request last or default service state
	StateRequestChannel chan StateRequest

	db      database.ServiceItf
	monitor monitoring.ServiceMonitoringItf
	systemd *dbus.Conn
	config  *config.Config

	actionHandler  *actionHandler
	storageHandler *storageHandler

	downloader downloadItf

	users []string

	closeChannel chan bool

	services sync.Map

	serviceTemplate  string
	runcPath         string
	netnsPath        string
	wonderShaperPath string
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New(config *config.Config, db database.ServiceItf,
	monitoring monitoring.ServiceMonitoringItf) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	launcher = &Launcher{}

	launcher.db = db
	launcher.config = config
	launcher.monitor = monitoring

	launcher.closeChannel = make(chan bool)
	launcher.StatusChannel = make(chan ActionStatus, maxExecutedActions)
	launcher.NewStateChannel = make(chan NewState, stateChannelSize)
	launcher.StateRequestChannel = make(chan StateRequest, stateChannelSize)

	if launcher.actionHandler, err = newActionHandler(); err != nil {
		return nil, err
	}

	if launcher.storageHandler, err = newStorageHandler(config.WorkingDir, db,
		launcher.NewStateChannel, launcher.StateRequestChannel); err != nil {
		return nil, err
	}

	launcher.downloader = &imageHandler{}

	// Check and create service dir
	dir := path.Join(config.WorkingDir, serviceDir)
	if _, err = os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if err = os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}

	// Create systemd connection
	launcher.systemd, err = dbus.NewSystemConnection()
	if err != nil {
		return nil, err
	}
	if err = launcher.systemd.Subscribe(); err != nil {
		return nil, err
	}

	launcher.handleSystemdSubscription()

	// Get systemd service template
	launcher.serviceTemplate, err = getSystemdServiceTemplate(config.WorkingDir)
	if err != nil {
		return nil, err
	}

	// Retrieve runc abs path
	launcher.runcPath, err = exec.LookPath(runcName)
	if err != nil {
		return nil, err
	}

	// Retrieve netns abs path
	launcher.netnsPath, _ = filepath.Abs(path.Join(config.WorkingDir, netnsName))
	if _, err := os.Stat(launcher.netnsPath); err != nil {
		// check system PATH
		launcher.netnsPath, err = exec.LookPath(netnsName)
		if err != nil {
			return nil, err
		}
	}

	// Retrieve wondershaper abs path
	launcher.wonderShaperPath, _ = filepath.Abs(path.Join(config.WorkingDir, wonderShaperName))
	if _, err := os.Stat(launcher.wonderShaperPath); err != nil {
		// check system PATH
		launcher.wonderShaperPath, err = exec.LookPath(wonderShaperName)
		if err != nil {
			return nil, err
		}
	}

	return launcher, nil
}

// Close closes launcher
func (launcher *Launcher) Close() {
	log.Debug("Close launcher")

	launcher.stopServices()

	if err := launcher.systemd.Unsubscribe(); err != nil {
		log.Warn("Can't unsubscribe from systemd: ", err)
	}

	launcher.closeChannel <- true

	launcher.systemd.Close()

	launcher.storageHandler.Close()
}

// GetServiceVersion returns installed version of requested service
func (launcher *Launcher) GetServiceVersion(id string) (version uint64, err error) {
	log.WithField("id", id).Debug("Get service version")

	service, err := launcher.db.GetService(id)
	if err != nil {
		return version, err
	}

	version = service.Version

	return version, nil
}

// InstallService installs and runs service
func (launcher *Launcher) InstallService(serviceInfo amqp.ServiceInfoFromCloud) {
	launcher.actionHandler.PutInQueue(serviceAction{ActionInstall, serviceInfo.ID, serviceInfo, launcher.doAction})
}

// RemoveService stops and removes service
func (launcher *Launcher) RemoveService(id string) {
	launcher.actionHandler.PutInQueue(serviceAction{ActionRemove, id, nil, launcher.doAction})
}

// GetServicesInfo returns information about all installed services
func (launcher *Launcher) GetServicesInfo() (info []amqp.ServiceInfo, err error) {
	log.Debug("Get services info")

	services, err := launcher.db.GetUsersServices(launcher.users)
	if err != nil {
		return info, err
	}

	info = make([]amqp.ServiceInfo, len(services))

	for i, service := range services {
		info[i] = amqp.ServiceInfo{ID: service.ID, Version: service.Version, Status: statusStr[service.Status]}
	}

	return info, nil
}

// SetUsers sets users for services
func (launcher *Launcher) SetUsers(users []string) (err error) {
	log.WithFields(log.Fields{"new": users, "old": launcher.users}).Debug("Set users")

	if isUsersEqual(launcher.users, users) {
		return nil
	}

	launcher.stopServices()

	launcher.users = users

	launcher.startServices()

	if err = launcher.cleanServicesDB(); err != nil {
		log.Errorf("Error cleaning DB: %s", err)
	}

	return nil
}

// StateAcceptance notifies launcher about new state acceptance
func (launcher *Launcher) StateAcceptance(acceptance amqp.StateAcceptance, correlationID string) (err error) {
	return launcher.storageHandler.StateAcceptance(acceptance, correlationID)
}

// UpdateState updates service state
func (launcher *Launcher) UpdateState(state amqp.UpdateState) (err error) {
	service, err := launcher.db.GetService(state.ServiceID)
	if err != nil {
		return err
	}

	if err = launcher.stopService(service); err != nil {
		return err
	}

	if err = launcher.storageHandler.UpdateState(launcher.users, service, state.State, state.Checksum); err != nil {
		return err
	}

	if err = launcher.startService(service); err != nil {
		return err
	}

	return nil
}

// Cleanup deletes all AOS services, their storages and states
func Cleanup(workingDir string) (err error) {
	systemd, err := dbus.NewSystemConnection()
	if err != nil {
		log.Errorf("Can't connect to systemd: %s", err)
	}

	if systemd != nil {
		units, err := systemd.ListUnits()
		if err != nil {
			log.Errorf("Can't list systemd units: %s", err)
		} else {
			for _, unit := range units {
				if !strings.HasPrefix(unit.Name, "aos_") {
					continue
				}

				desc, err := systemd.GetUnitProperty(unit.Name, "Description")
				if err != nil {
					log.WithField("name", unit.Name).Errorf("Can't get unit property: %s", err)
					continue
				}

				value, ok := desc.Value.Value().(string)
				if !ok {
					log.WithField("name", unit.Name).Error("Can't convert description")
					continue
				}

				if value == "AOS Service" {
					log.WithField("name", unit.Name).Debug("Deleting systemd service")

					channel := make(chan string)
					if _, err := systemd.StopUnit(unit.Name, "replace", channel); err != nil {
						log.WithField("name", unit.Name).Errorf("Can't stop unit: %s", err)
					} else {
						<-channel
					}

					if _, err := systemd.DisableUnitFiles([]string{unit.Name}, false); err != nil {
						log.WithField("name", unit.Name).Error("Can't disable unit: ", err)
					}
				}
			}
		}
	}

	serviceDir := path.Join(workingDir, serviceDir)

	log.WithField("dir", serviceDir).Debug("Remove service dir")

	if err := os.RemoveAll(serviceDir); err != nil {
		log.Fatalf("Can't remove service folder: %s", err)
	}

	storageDir := path.Join(workingDir, storageDir)

	log.WithField("dir", storageDir).Debug("Remove storage dir")

	if err := os.RemoveAll(storageDir); err != nil {
		log.Fatalf("Can't remove storage folder: %s", err)
	}

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func isUsersEqual(users1, users2 []string) (result bool) {
	if users1 == nil && users2 == nil {
		return true
	}

	if users1 == nil || users2 == nil {
		return false
	}

	if len(users1) != len(users2) {
		return false
	}

	for i := range users1 {
		if users1[i] != users2[i] {
			return false
		}
	}

	return true
}

func (launcher *Launcher) doAction(action actionType, id string, data interface{}) {
	status := ActionStatus{Action: action, ID: id}

	switch action {
	case ActionInstall:
		serviceInfo := data.(amqp.ServiceInfoFromCloud)
		status.Version = serviceInfo.Version

		status.Err = launcher.doActionInstall(serviceInfo)

	case ActionRemove:
		status.Version, status.Err = launcher.doActionRemove(status.ID)
	}

	launcher.StatusChannel <- status
}

func (launcher *Launcher) doActionInstall(serviceInfo amqp.ServiceInfoFromCloud) (err error) {
	if launcher.users == nil {
		return errors.New("Users are not set")
	}

	service, err := launcher.db.GetService(serviceInfo.ID)
	if err != nil && err != database.ErrNotExist {
		return err
	}

	// Skip incorrect version
	if err == nil && serviceInfo.Version < service.Version {
		return errors.New("Version mistmatch")
	}

	installed := false

	// Check if we need to install
	if err != nil || serviceInfo.Version > service.Version {
		if installDir, err := launcher.installService(serviceInfo); err != nil {
			if err := launcher.removeService(serviceInfo.ID); err != nil {
				if err == database.ErrNotExist {
					if installDir != "" {
						os.RemoveAll(installDir)
					}
				} else {
					log.WithField("serviceID", serviceInfo.ID).Errorf("Can't cleanup service: %s", err)
				}
			}

			return err
		}
		installed = true
	}

	// Update users DB
	exist, err := launcher.db.IsUsersService(launcher.users, serviceInfo.ID)
	if err != nil {
		return err
	}

	if !exist {
		if err := launcher.db.AddUsersService(launcher.users, serviceInfo.ID); err != nil {
			return err
		}
	}

	// Start service
	if !installed {
		if err = launcher.startService(service); err != nil {
			return err
		}
	}

	return nil
}

func (launcher *Launcher) doActionRemove(id string) (version uint64, err error) {
	service, err := launcher.db.GetService(id)
	if err != nil {
		return 0, err
	}

	version = service.Version

	if launcher.users == nil {
		return version, errors.New("Users are not set")
	}

	if err := launcher.stopService(service); err != nil {
		return version, err
	}

	entry, err := launcher.db.GetUsersEntry(launcher.users, service.ID)
	if err != nil {
		return version, err
	}

	if entry.StorageFolder != "" {
		log.WithFields(log.Fields{
			"folder":    entry.StorageFolder,
			"serviceID": service.ID}).Debug("Remove storage folder")

		if err = os.RemoveAll(entry.StorageFolder); err != nil {
			return version, err
		}
	}

	if err = launcher.db.RemoveUsersService(launcher.users, service.ID); err != nil {
		return version, err
	}

	return version, nil
}

func (launcher *Launcher) updateServiceState(id string, state int, status int) (err error) {
	service, err := launcher.db.GetService(id)
	if err != nil {
		return err
	}

	if service.State != state {
		if launcher.monitor != nil {
			if err = launcher.updateMonitoring(service, state); err != nil {
				log.WithField("id", id).Error("Can't update monitoring: ", err)
			}
		}

		log.WithField("id", id).Debugf("Set service state: %s", stateStr[state])

		if err = launcher.db.SetServiceState(id, state); err != nil {
			return err
		}
	}

	if service.Status != status {
		log.WithField("id", id).Debugf("Set service status: %s", statusStr[status])

		if err = launcher.db.SetServiceStatus(id, status); err != nil {
			return err
		}
	}

	return nil
}

func (launcher *Launcher) restartService(service database.ServiceEntry) (err error) {
	if err = launcher.storageHandler.MountStorageFolder(launcher.users, service); err != nil {
		return err
	}

	channel := make(chan string)
	if _, err = launcher.systemd.RestartUnit(service.ServiceName, "replace", channel); err != nil {
		return err
	}
	status := <-channel

	log.WithFields(log.Fields{"name": service.ServiceName, "status": status}).Debug("Restart service")

	if err = launcher.updateServiceState(service.ID, stateRunning, statusOk); err != nil {
		log.WithField("id", service.ID).Warnf("Can't update service state: %s", err)
	}

	if err = launcher.db.SetServiceStartTime(service.ID, time.Now()); err != nil {
		log.WithField("id", service.ID).Warnf("Can't set service start time: %s", err)
	}

	launcher.services.Store(service.ServiceName, service.ID)

	return nil
}

func (launcher *Launcher) startService(service database.ServiceEntry) (err error) {
	if err = launcher.storageHandler.MountStorageFolder(launcher.users, service); err != nil {
		return err
	}

	channel := make(chan string)
	if _, err = launcher.systemd.StartUnit(service.ServiceName, "replace", channel); err != nil {
		return err
	}
	status := <-channel

	log.WithFields(log.Fields{"name": service.ServiceName, "status": status}).Debug("Start service")

	if err = launcher.updateServiceState(service.ID, stateRunning, statusOk); err != nil {
		log.WithField("id", service.ID).Warnf("Can't update service state: %s", err)
	}

	if err = launcher.db.SetServiceStartTime(service.ID, time.Now()); err != nil {
		log.WithField("id", service.ID).Warnf("Can't set service start time: %s", err)
	}

	launcher.services.Store(service.ServiceName, service.ID)

	return nil
}

func (launcher *Launcher) startServices() {
	log.WithField("users", launcher.users).Debug("Start user services")

	services, err := launcher.db.GetUsersServices(launcher.users)
	if err != nil {
		log.Errorf("Can't start services: %s", err)
	}

	statusChannel := make(chan error, len(services))

	// Start all services in parallel
	for _, service := range services {
		go func(service database.ServiceEntry) {
			err := launcher.startService(service)
			if err != nil {
				log.WithField("id", service.ID).Errorf("Can't start service: %s", err)
			}

			if service.State == stateRunning && launcher.monitor != nil {
				if err = launcher.updateMonitoring(service, stateRunning); err != nil {
					log.WithField("id", service.ID).Errorf("Can't update monitoring: %s", err)
				}
			}

			statusChannel <- err
		}(service)
	}

	// Wait all services are started
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}
}

func (launcher *Launcher) stopService(service database.ServiceEntry) (retErr error) {
	launcher.services.Delete(service.ServiceName)

	if err := launcher.storageHandler.StopStateWatching(launcher.users, service); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't stop state watching: %s", err)
			retErr = err
		}
	}

	channel := make(chan string)
	if _, err := launcher.systemd.StopUnit(service.ServiceName, "replace", channel); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't stop systemd unit: %s", err)
			retErr = err
		}
	} else {
		status := <-channel
		log.WithFields(log.Fields{"id": service.ID, "status": status}).Debug("Stop service")
	}

	if err := launcher.updateServiceState(service.ID, stateStopped, statusOk); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't update service state: %s", err)
			retErr = err
		}
	}

	return retErr
}

func (launcher *Launcher) stopServices() {
	log.WithField("users", launcher.users).Debug("Stop user services")

	var services []database.ServiceEntry
	var err error

	if launcher.users == nil {
		services, err = launcher.db.GetServices()
		if err != nil {
			log.Errorf("Can't stop services: %s", err)
		}
	} else {
		services, err = launcher.db.GetUsersServices(launcher.users)
		if err != nil {
			log.Errorf("Can't stop services: %s", err)
		}
	}

	statusChannel := make(chan error, len(services))

	// Stop all services in parallel
	for _, service := range services {
		go func(service database.ServiceEntry) {
			statusChannel <- launcher.stopService(service)
		}(service)
	}

	// Wait all services are stopped
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}
}

func (launcher *Launcher) installService(serviceInfo amqp.ServiceInfoFromCloud) (installDir string, err error) {

	log.WithFields(log.Fields{"id": serviceInfo.ID, "version": serviceInfo.Version}).Debug("Install service")

	// create install dir
	installDir, err = ioutil.TempDir(path.Join(launcher.config.WorkingDir, serviceDir), "")
	if err != nil {
		return installDir, err
	}
	log.WithField("dir", installDir).Debug("Create install dir")

	// download and unpack
	err = downloadAndUnpackImage(launcher.downloader, serviceInfo, installDir)
	if err != nil {
		return installDir, err
	}

	// check if service already installed
	oldService, err := launcher.db.GetService(serviceInfo.ID)
	if err != nil && err != database.ErrNotExist {
		return installDir, err
	}
	serviceExists := err == nil

	// create OS user
	userName, err := createUser(serviceInfo.ID)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return installDir, err
	}

	// update config.json
	spec, err := launcher.updateServiceSpec(installDir, userName)
	if err != nil {
		return installDir, err
	}

	serviceName := "aos_" + serviceInfo.ID + ".service"

	err = launcher.createSystemdService(installDir, serviceName, serviceInfo.ID, spec)
	if err != nil {
		return installDir, err
	}

	alertRules, err := json.Marshal(serviceInfo.ServiceMonitoring)
	if err != nil {
		return installDir, err
	}

	newService := database.ServiceEntry{
		ID:          serviceInfo.ID,
		Version:     serviceInfo.Version,
		Path:        installDir,
		ServiceName: serviceName,
		UserName:    userName,
		State:       stateInit,
		Status:      statusOk,
		AlertRules:  string(alertRules)}

	if err := launcher.updateServiceFromSpec(&newService, spec); err != nil {
		return installDir, err
	}

	if serviceExists {
		launcher.services.Delete(serviceName)

		if err = launcher.updateServiceState(serviceInfo.ID, stateStopped, statusOk); err != nil {
			return installDir, err
		}

		if err = launcher.db.RemoveService(serviceInfo.ID); err != nil {
			return installDir, err
		}
	}

	if err = launcher.addServiceToDB(newService); err != nil {
		return installDir, err
	}

	if err := setUserFSQuota(launcher.config.WorkingDir, newService.StorageLimit, userName); err != nil {
		return installDir, err
	}

	if err = launcher.restartService(newService); err != nil {
		// TODO: try to restore old service
		return installDir, err
	}

	// remove if exists
	if serviceExists {
		if err = os.RemoveAll(oldService.Path); err != nil {
			// indicate error, can continue
			log.WithField("path", oldService.Path).Error("Can't remove service path")
		}

		launcher.StatusChannel <- ActionStatus{
			Action:  ActionRemove,
			ID:      oldService.ID,
			Version: oldService.Version,
			Err:     nil}
	}

	return installDir, nil
}

func (launcher *Launcher) removeService(id string) (err error) {
	service, err := launcher.db.GetService(id)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{"id": service.ID, "version": service.Version}).Debug("Remove service")

	if err := launcher.stopService(service); err != nil {
		log.WithField("name", service.ServiceName).Error("Can't stop service: ", err)
	}

	if _, err := launcher.systemd.DisableUnitFiles([]string{service.ServiceName}, false); err != nil {
		log.WithField("name", service.ServiceName).Error("Can't disable systemd unit: ", err)
	}

	entries, err := launcher.db.GetUsersEntriesByServiceID(service.ID)
	if err != nil {
		log.WithField("name", service.ServiceName).Error("Can't get users entries: ", err)
	}

	for _, entry := range entries {
		if entry.StorageFolder != "" {
			log.WithFields(log.Fields{
				"folder":    entry.StorageFolder,
				"serviceID": service.ID}).Debug("Remove storage folder")

			if err = os.RemoveAll(entry.StorageFolder); err != nil {
				log.WithField("name", service.ServiceName).Error("Can't remove storage folder: ", err)
			}
		}
	}

	if err := launcher.db.DeleteUsersByServiceID(service.ID); err != nil {
		log.WithField("name", service.ServiceName).Error("Can't delete users from db: ", err)
	}

	if err := launcher.db.RemoveService(service.ID); err != nil {
		log.WithField("name", service.ServiceName).Error("Can't remove service from db: ", err)
	}

	if err := os.RemoveAll(service.Path); err != nil {
		log.WithField("path", service.Path).Error("Can't remove service path")
	}

	if err := deleteUser(service.UserName); err != nil {
		log.WithField("user", service.UserName).Error("Can't remove user")
	}

	return nil
}

func getSystemdServiceTemplate(workingDir string) (template string, err error) {
	template = `# This is template file used to launch AOS services
# Known variables:
# * ${ID}            - service id
# * ${SERVICEPATH}   - path to service dir
# * ${RUNC}          - path to runc
# * ${SETNETLIMIT}   - command to set net limit
# * ${CLEARNETLIMIT} - command to clear net limit
[Unit]
Description=AOS Service
After=network.target

[Service]
Type=forking
Restart=always
RestartSec=1
ExecStartPre=${RUNC} delete -f ${ID}
ExecStart=${RUNC} run -d --pid-file ${SERVICEPATH}/.pid -b ${SERVICEPATH} ${ID}
ExecStartPost=${SETNETLIMIT}

ExecStop=${CLEARNETLIMIT}
ExecStop=${RUNC} kill ${ID} SIGKILL
ExecStopPost=${RUNC} delete -f ${ID}
PIDFile=${SERVICEPATH}/.pid

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

func (launcher *Launcher) createSystemdService(installDir, serviceName, id string, spec *specs.Spec) (err error) {
	f, err := os.Create(path.Join(installDir, serviceName))
	if err != nil {
		return err
	}
	defer f.Close()

	absServicePath, err := filepath.Abs(installDir)
	if err != nil {
		return err
	}

	setNetLimitCmd, clearNetLimitCmd := launcher.generateNetLimitsCmds(spec)

	lines := strings.SplitAfter(launcher.serviceTemplate, "\n")
	for _, line := range lines {
		// skip comments
		if strings.HasPrefix(line, "#") {
			continue
		}

		// replaces variables with values
		line = strings.Replace(line, "${RUNC}", launcher.runcPath, -1)
		line = strings.Replace(line, "${ID}", id, -1)
		line = strings.Replace(line, "${SERVICEPATH}", absServicePath, -1)
		line = strings.Replace(line, "${SETNETLIMIT}", setNetLimitCmd, -1)
		line = strings.Replace(line, "${CLEARNETLIMIT}", clearNetLimitCmd, -1)

		fmt.Fprint(f, line)
	}

	fileName, err := filepath.Abs(f.Name())
	if err != nil {
		return err
	}

	// Use launcher.systemd.EnableUnitFiles if services should be started automatically
	// on system restart
	if _, err = launcher.systemd.LinkUnitFiles([]string{fileName}, false, true); err != nil {
		return err
	}

	if err = launcher.systemd.Reload(); err != nil {
		return err
	}

	return err
}

func (launcher *Launcher) getServiceIPAddress(servicePath string) (address string, err error) {
	data, err := ioutil.ReadFile(path.Join(servicePath, ".ip"))
	if err != nil {
		return address, err
	}

	address = string(data)

	return address, nil
}

func (launcher *Launcher) getServicePid(servicePath string) (pid int32, err error) {
	pidStr, err := ioutil.ReadFile(path.Join(servicePath, ".pid"))
	if err != nil {
		return pid, err
	}

	pid64, err := strconv.ParseInt(string(pidStr), 10, 0)
	if err != nil {
		return pid, err
	}

	return int32(pid64), nil
}

func (launcher *Launcher) updateMonitoring(service database.ServiceEntry, state int) (err error) {
	switch state {
	case stateRunning:
		pid, err := launcher.getServicePid(service.Path)
		if err != nil {
			return err
		}

		ipAddress, err := launcher.getServiceIPAddress(service.Path)
		if err != nil {
			return err
		}

		var rules amqp.ServiceAlertRules

		if err := json.Unmarshal([]byte(service.AlertRules), &rules); err != nil {
			return err
		}

		entry, err := launcher.db.GetUsersEntry(launcher.users, service.ID)
		if err != nil {
			return err
		}

		if err = launcher.monitor.StartMonitorService(service.ID, monitoring.ServiceMonitoringConfig{
			Pid:           pid,
			IPAddress:     ipAddress,
			WorkingDir:    entry.StorageFolder,
			UploadLimit:   uint64(service.UploadLimit),
			DownloadLimit: uint64(service.DownloadLimit),
			ServiceRules:  &rules}); err != nil {
			return err
		}

	case stateStopped:
		if err = launcher.monitor.StopMonitorService(service.ID); err != nil {
			return err
		}
	}

	return nil
}

func (launcher *Launcher) handleSystemdSubscription() {
	unitStatus, errorChannel := launcher.systemd.SubscribeUnitsCustom(
		systemdSubscribeInterval,
		systemdSubscribeBuffers,
		func(u1, u2 *dbus.UnitStatus) bool { return *u1 != *u2 },
		func(serviceName string) bool {
			if _, exist := launcher.services.Load(serviceName); exist {
				return false
			}
			return true
		})
	go func() {
		for {
			select {
			case units := <-unitStatus:
				for _, unit := range units {
					var (
						state  int
						status int
					)

					if unit == nil {
						continue
					}

					log.WithField("name", unit.Name).Debugf(
						"Service state changed. Load state: %s, active state: %s, sub state: %s",
						unit.LoadState,
						unit.ActiveState,
						unit.SubState)

					switch unit.SubState {
					case "running":
						state = stateRunning
						status = statusOk
					default:
						state = stateStopped
						status = statusError
					}

					id, exist := launcher.services.Load(unit.Name)

					if exist {
						if err := launcher.updateServiceState(id.(string), state, status); err != nil {
							log.WithField("id", id.(string)).Error("Can't update service state: ", err)
						}
					} else {
						log.WithField("name", unit.Name).Warning("Can't update state or status. Service is not installed.")
					}
				}
			case err := <-errorChannel:
				log.Error("Subscription error: ", err)
			case <-launcher.closeChannel:
				return
			}
		}
	}()
}

func (launcher *Launcher) updateServiceFromSpec(service *database.ServiceEntry, spec *specs.Spec) (err error) {
	service.TTL = launcher.config.DefaultServiceTTL

	if ttlString, ok := spec.Annotations[aosProductPrefix+"service.TTL"]; ok {
		if service.TTL, err = strconv.ParseUint(ttlString, 10, 64); err != nil {
			return err
		}
	}

	if uploadLimitString, ok := spec.Annotations[aosProductPrefix+"network.uploadLimit"]; ok {
		if service.UploadLimit, err = strconv.ParseUint(uploadLimitString, 10, 64); err != nil {
			return err
		}
	}

	if downloadLimitString, ok := spec.Annotations[aosProductPrefix+"network.downloadLimit"]; ok {
		if service.DownloadLimit, err = strconv.ParseUint(downloadLimitString, 10, 64); err != nil {
			return err
		}
	}

	service.Permissions = spec.Annotations[aosProductPrefix+"vis.permissions"]

	if storageLimitString, ok := spec.Annotations[aosProductPrefix+"storage.limit"]; ok {
		if service.StorageLimit, err = strconv.ParseUint(storageLimitString, 10, 64); err != nil {
			return err
		}
	}

	if stateLimitString, ok := spec.Annotations[aosProductPrefix+"state.limit"]; ok {
		if service.StateLimit, err = strconv.ParseUint(stateLimitString, 10, 64); err != nil {
			return err
		}
	}

	if service.StateLimit > service.StorageLimit {
		return errors.New("Wrong state limit value")
	}

	return nil
}

func (launcher *Launcher) updateServiceSpec(dir string, userName string) (spec *specs.Spec, err error) {
	configFile := path.Join(dir, "config.json")

	// get service spec
	localSpec, err := getServiceSpec(configFile)
	if err != nil {
		return spec, err
	}

	// disable terminal
	localSpec.Process.Terminal = false

	// assign UID, GID
	if localSpec.Process.User.UID, localSpec.Process.User.GID, err = getUserUIDGID(userName); err != nil {
		return spec, err
	}

	mounts := []specs.Mount{
		specs.Mount{Destination: "/bin", Type: "bind", Source: "/bin", Options: []string{"bind", "ro"}},
		specs.Mount{Destination: "/sbin", Type: "bind", Source: "/sbin", Options: []string{"bind", "ro"}},
		specs.Mount{Destination: "/lib", Type: "bind", Source: "/lib", Options: []string{"bind", "ro"}},
		specs.Mount{Destination: "/usr", Type: "bind", Source: "/usr", Options: []string{"bind", "ro"}},
		// TODO: mount individual tmp
		// "destination": "/tmp",
		// "type": "tmpfs",
		// "source": "tmpfs",
		// "options": ["nosuid","strictatime","mode=755","size=65536k"]
		specs.Mount{Destination: "/tmp", Type: "bind", Source: "/tmp", Options: []string{"bind", "rw"}}}
	localSpec.Mounts = append(localSpec.Mounts, mounts...)
	// add lib64 if exists
	if _, err := os.Stat("/lib64"); err == nil {
		localSpec.Mounts = append(localSpec.Mounts, specs.Mount{Destination: "/lib64", Type: "bind", Source: "/lib64", Options: []string{"bind", "ro"}})
	}
	// add hosts
	hosts, _ := filepath.Abs(path.Join(launcher.config.WorkingDir, "etc", "hosts"))
	if _, err := os.Stat(hosts); err != nil {
		hosts = "/etc/hosts"
	}
	localSpec.Mounts = append(localSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "hosts"), Type: "bind", Source: hosts, Options: []string{"bind", "ro"}})
	// add resolv.conf
	resolvConf, _ := filepath.Abs(path.Join(launcher.config.WorkingDir, "etc", "resolv.conf"))
	if _, err := os.Stat(resolvConf); err != nil {
		resolvConf = "/etc/resolv.conf"
	}
	localSpec.Mounts = append(localSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "resolv.conf"), Type: "bind", Source: resolvConf, Options: []string{"bind", "ro"}})
	// add nsswitch.conf
	nsswitchConf, _ := filepath.Abs(path.Join(launcher.config.WorkingDir, "etc", "nsswitch.conf"))
	if _, err := os.Stat(nsswitchConf); err != nil {
		nsswitchConf = "/etc/nsswitch.conf"
	}
	localSpec.Mounts = append(localSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "nsswitch.conf"), Type: "bind", Source: nsswitchConf, Options: []string{"bind", "ro"}})

	// TODO: all services should have their own certificates
	// this mound for demo only and should be removed
	// mount /etc/ssl
	localSpec.Mounts = append(localSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "ssl"), Type: "bind", Source: path.Join("/etc", "ssl"), Options: []string{"bind", "ro"}})

	// add netns hook
	if localSpec.Hooks == nil {
		localSpec.Hooks = &specs.Hooks{}
	}
	localSpec.Hooks.Prestart = append(localSpec.Hooks.Prestart, specs.Hook{Path: launcher.netnsPath})

	// create annotations
	if localSpec.Annotations == nil {
		localSpec.Annotations = make(map[string]string)
	}

	// write config.json
	if err = writeServiceSpec(&localSpec, configFile); err != nil {
		return spec, err
	}

	return &localSpec, nil
}

func (launcher *Launcher) generateNetLimitsCmds(spec *specs.Spec) (setCmd, clearCmd string) {
	value, exist := spec.Annotations[aosProductPrefix+"network.downloadSpeed"]
	if exist {
		setCmd = setCmd + " -d " + value
	}
	value, exist = spec.Annotations[aosProductPrefix+"network.uploadSpeed"]
	if exist {
		setCmd = setCmd + " -u " + value
	}
	if setCmd != "" {
		setCmd = "-" + launcher.wonderShaperPath + " -a netnsv0-${MAINPID}" + setCmd
		clearCmd = "-" + launcher.wonderShaperPath + " -c -a netnsv0-${MAINPID}"

		log.Debugf("Set net limit cmd: %s", setCmd)
		log.Debugf("Clear net limit cmd: %s", clearCmd)
	}

	return setCmd, clearCmd
}

func (launcher *Launcher) addServiceToDB(service database.ServiceEntry) (err error) {
	// add to database
	if err = launcher.db.AddService(service); err != nil {
		// TODO: delete linux user?
		// TODO: try to restore old
		return err
	}

	exist, err := launcher.db.IsUsersService(launcher.users, service.ID)
	if err != nil {
		return err
	}
	if !exist {
		if err = launcher.db.AddUsersService(launcher.users, service.ID); err != nil {
			return err
		}
	}

	return nil
}

func (launcher *Launcher) cleanServicesDB() (err error) {
	log.Debug("Clean services DB")

	startedServices, err := launcher.db.GetUsersServices(launcher.users)
	if err != nil {
		return err
	}

	allServices, err := launcher.db.GetServices()
	if err != nil {
		return err
	}

	now := time.Now()

	servicesToBeRemoved := 0
	statusChannel := make(chan error, len(allServices))

	for _, service := range allServices {
		// check if service just started
		justStarted := false

		for _, startedService := range startedServices {
			if service.ID == startedService.ID {
				justStarted = true
				break
			}
		}

		if justStarted {
			continue
		}

		if service.StartAt.Add(time.Hour*24*time.Duration(service.TTL)).Before(now) == true {
			servicesToBeRemoved++

			go func(id string) {
				err := launcher.removeService(id)
				if err != nil {
					log.WithField("id", id).Errorf("Can't remove service: %s", err)
				}
				statusChannel <- err
			}(service.ID)
		}
	}

	// Wait all services are removed
	for i := 0; i < servicesToBeRemoved; i++ {
		<-statusChannel
	}

	return nil
}
