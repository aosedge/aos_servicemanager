// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
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
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/platform"
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

/*******************************************************************************
 * Types
 ******************************************************************************/

// ServiceProvider provides API to create, remove or access services DB
type ServiceProvider interface {
	AddService(entry database.ServiceEntry) (err error)
	UpdateService(entry database.ServiceEntry) (err error)
	RemoveService(id string) (err error)
	GetService(id string) (entry database.ServiceEntry, err error)
	GetServices() (entries []database.ServiceEntry, err error)
	GetServiceByServiceName(serviceName string) (entry database.ServiceEntry, err error)
	SetServiceStatus(id string, status int) (err error)
	SetServiceState(id string, state int) (err error)
	SetServiceStartTime(id string, time time.Time) (err error)
	AddUsersService(users []string, serviceID string) (err error)
	RemoveUsersService(users []string, serviceID string) (err error)
	GetUsersServices(users []string) (entries []database.ServiceEntry, err error)
	IsUsersService(users []string, id string) (result bool, err error)
	GetUsersList() (usersList [][]string, err error)
	DeleteUsersByServiceID(id string) (err error)
	GetUsersEntry(users []string, serviceID string) (entry database.UsersEntry, err error)
	GetUsersEntriesByServiceID(serviceID string) (entries []database.UsersEntry, err error)
	SetUsersStorageFolder(users []string, serviceID string, storageFolder string) (err error)
	SetUsersStateChecksum(users []string, serviceID string, checksum []byte) (err error)
}

// ServiceMonitor provides API to start/stop service monitoring
type ServiceMonitor interface {
	StartMonitorService(serviceID string, monitoringConfig monitoring.ServiceMonitoringConfig) (err error)
	StopMonitorService(serviceID string) (err error)
}

type actionType int

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

type downloader interface {
	downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error)
}

// Launcher instance
type Launcher struct {
	// StatusChannel used to return execute command statuses
	StatusChannel chan amqp.ServiceInfo
	// NewStateChannel used to notify about new service state
	NewStateChannel chan NewState
	// StateRequestChannel used to request last or default service state
	StateRequestChannel chan StateRequest

	serviceProvider ServiceProvider
	monitor         ServiceMonitor
	systemd         *dbus.Conn
	config          *config.Config

	actionHandler  *actionHandler
	storageHandler *storageHandler

	downloader downloader

	users []string

	services sync.Map

	serviceTemplate  string
	runcPath         string
	netnsPath        string
	wonderShaperPath string
}

type stateAcceptance struct {
	correlationID string
	acceptance    amqp.StateAcceptance
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New(config *config.Config, serviceProvider ServiceProvider,
	monitor ServiceMonitor) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	launcher = &Launcher{serviceProvider: serviceProvider, config: config, monitor: monitor}

	launcher.StatusChannel = make(chan amqp.ServiceInfo, maxExecutedActions)
	launcher.NewStateChannel = make(chan NewState, stateChannelSize)
	launcher.StateRequestChannel = make(chan StateRequest, stateChannelSize)

	if launcher.actionHandler, err = newActionHandler(); err != nil {
		return nil, err
	}

	if launcher.storageHandler, err = newStorageHandler(config.WorkingDir, serviceProvider,
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

	launcher.systemd.Close()

	launcher.storageHandler.Close()
}

// GetServiceVersion returns installed version of requested service
func (launcher *Launcher) GetServiceVersion(id string) (version uint64, err error) {
	log.WithField("id", id).Debug("Get service version")

	service, err := launcher.serviceProvider.GetService(id)
	if err != nil {
		return version, err
	}

	version = service.Version

	return version, nil
}

// InstallService installs and runs service
func (launcher *Launcher) InstallService(serviceInfo amqp.ServiceInfoFromCloud) {
	launcher.actionHandler.PutInQueue(serviceAction{serviceInfo.ID, serviceInfo, launcher.doActionInstall})
}

// UninstallService stops and removes service
func (launcher *Launcher) UninstallService(id string) {
	launcher.actionHandler.PutInQueue(serviceAction{id, nil, launcher.doActionUninstall})
}

// GetServicesInfo returns information about all installed services
func (launcher *Launcher) GetServicesInfo() (info []amqp.ServiceInfo, err error) {
	log.Debug("Get services info")

	services, err := launcher.serviceProvider.GetUsersServices(launcher.users)
	if err != nil {
		return info, err
	}

	info = make([]amqp.ServiceInfo, len(services))

	for i, service := range services {
		info[i] = amqp.ServiceInfo{ID: service.ID, Version: service.Version, Status: statusStr[service.Status]}

		entry, err := launcher.serviceProvider.GetUsersEntry(launcher.users, service.ID)
		if err != nil {
			return info, err
		}

		if service.StateLimit != 0 {
			info[i].StateChecksum = hex.EncodeToString(entry.StateChecksum)
		}
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
func (launcher *Launcher) StateAcceptance(acceptance amqp.StateAcceptance, correlationID string) {
	launcher.actionHandler.PutInQueue(serviceAction{acceptance.ServiceID,
		stateAcceptance{correlationID, acceptance}, launcher.doStateAcceptance})
}

// UpdateState updates service state
func (launcher *Launcher) UpdateState(state amqp.UpdateState) {
	launcher.actionHandler.PutInQueue(serviceAction{state.ServiceID, state, launcher.doUpdateState})
}

// Cleanup deletes all AOS services, their storages and states
func Cleanup(workingDir string) (err error) {
	systemd, err := dbus.NewSystemConnection()
	if err != nil {
		log.Errorf("Can't connect to systemd: %s", err)
	}

	if systemd != nil {
		unitFiles, err := systemd.ListUnitFiles()
		if err != nil {
			log.Errorf("Can't list systemd units: %s", err)
		} else {
			for _, unitFile := range unitFiles {
				serviceName := filepath.Base(unitFile.Path)

				if !strings.HasPrefix(serviceName, "aos_") {
					continue
				}

				desc, err := systemd.GetUnitProperty(serviceName, "Description")
				if err != nil {
					log.WithField("name", serviceName).Errorf("Can't get unit property: %s", err)
					continue
				}

				value, ok := desc.Value.Value().(string)
				if !ok {
					log.WithField("name", serviceName).Error("Can't convert description")
					continue
				}

				if value == "AOS Service" {
					log.WithField("name", serviceName).Debug("Deleting systemd service")

					channel := make(chan string)
					if _, err := systemd.StopUnit(serviceName, "replace", channel); err != nil {
						log.WithField("name", serviceName).Errorf("Can't stop unit: %s", err)
					} else {
						<-channel
					}

					if _, err := systemd.DisableUnitFiles([]string{serviceName}, false); err != nil {
						log.WithField("name", serviceName).Error("Can't disable unit: ", err)
					}

					// Delete service user
					serviceID := strings.TrimSuffix(strings.TrimPrefix(serviceName, "aos_"), ".service")

					if err := platform.DeleteUser(serviceID); err != nil {
						log.WithField("serviceID", serviceID).Error("Can't delete user: ", err)
					}
				}
			}
		}

		if err := systemd.Reload(); err != nil {
			log.Errorf("Can't reload systemd: %s", err)
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

func (launcher *Launcher) doActionInstall(id string, data interface{}) {
	status := amqp.ServiceInfo{ID: id, Status: "installed"}

	defer func() {
		if r := recover(); r != nil {
			status.Status = "error"
			status.Error = "Can't install service: " + r.(string)
			launcher.StatusChannel <- status
		}
	}()

	serviceInfo, ok := data.(amqp.ServiceInfoFromCloud)
	if !ok {
		panic("wrong data type")
	}

	status.Version = serviceInfo.Version

	if err := launcher.installService(serviceInfo); err != nil {
		panic(err.Error())
	}

	entry, err := launcher.serviceProvider.GetUsersEntry(launcher.users, id)
	if err != nil {
		panic(err.Error())
	}

	status.StateChecksum = hex.EncodeToString(entry.StateChecksum)

	launcher.StatusChannel <- status
}

func (launcher *Launcher) doActionUninstall(id string, data interface{}) {
	var err error

	status := amqp.ServiceInfo{ID: id, Status: "removed"}
	status.Version, err = launcher.uninstallService(status.ID)
	if err != nil {
		status.Status = "error"
		status.Error = err.Error()
	}

	launcher.StatusChannel <- status
}

func (launcher *Launcher) installService(serviceInfo amqp.ServiceInfoFromCloud) (err error) {
	if launcher.users == nil {
		return errors.New("Users are not set")
	}

	service, err := launcher.serviceProvider.GetService(serviceInfo.ID)
	if err != nil && err != database.ErrNotExist {
		return err
	}
	serviceExists := err == nil

	// Skip incorrect version
	if serviceExists && serviceInfo.Version < service.Version {
		return errors.New("Version mistmatch")
	}

	// If same service version exists, just start the service
	if serviceExists && serviceInfo.Version == service.Version {
		if err = launcher.addServiceToCurrentUsers(serviceInfo.ID); err != nil {
			return err
		}

		if err = launcher.startService(service); err != nil {
			return err
		}

		return nil
	}

	// We need to install or update the service

	// create install dir
	installDir, err := ioutil.TempDir(path.Join(launcher.config.WorkingDir, serviceDir), "")
	if err != nil {
		return err
	}

	defer func() {
		if r := recover(); r != nil {
			log.WithField("serviceID", serviceInfo.ID).Error(r)

			if !serviceExists {
				// Remove system user
				if platform.IsUserExist(serviceInfo.ID) {
					if err := platform.DeleteUser(serviceInfo.ID); err != nil {
						log.WithField("serviceID", serviceInfo.ID).Errorf("Can't delete user: %s", err)
					}
				}
			}

			// Remove install dir if exists
			if _, err := os.Stat(installDir); err == nil {
				if err := os.RemoveAll(installDir); err != nil {
					log.WithField("serviceID", serviceInfo.ID).Errorf("Can't remove service dir: %s", err)
				}
			}
		}
	}()

	log.WithFields(log.Fields{"dir": installDir, "serviceID": serviceInfo.ID}).Debug("Create install dir")

	// download and unpack
	if err = downloadAndUnpackImage(launcher.downloader, serviceInfo, installDir); err != nil {
		panic("Download failed")
	}

	var newService database.ServiceEntry

	if newService, err = launcher.prepareService(installDir, serviceInfo); err != nil {
		panic("Prepare failed")
	}

	if !serviceExists {
		if err = launcher.addService(newService); err != nil {
			panic("Install failed")
		}
	} else {
		if err = launcher.updateService(service, newService); err != nil {
			panic("Update failed")
		}
	}

	return nil
}

func (launcher *Launcher) uninstallService(id string) (version uint64, err error) {
	service, err := launcher.serviceProvider.GetService(id)
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

	entry, err := launcher.serviceProvider.GetUsersEntry(launcher.users, service.ID)
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

	if err = launcher.serviceProvider.RemoveUsersService(launcher.users, service.ID); err != nil {
		return version, err
	}

	return version, nil
}

func (launcher *Launcher) doUpdateState(id string, data interface{}) {
	service, err := launcher.serviceProvider.GetService(id)
	if err != nil {
		log.Errorf("Can't get service: %s", err)
		return
	}

	if err = launcher.stopService(service); err != nil {
		log.Errorf("Can't stop service: %s", err)
		return
	}

	state, ok := data.(amqp.UpdateState)
	if !ok {
		log.Error("Wrong data type")
		return
	}

	if err = launcher.storageHandler.UpdateState(launcher.users, service, state.State, state.Checksum); err != nil {
		log.Errorf("Can't update state: %s", err)
		return
	}

	if err = launcher.startService(service); err != nil {
		log.Errorf("Can't start service: %s", err)
		return
	}
}

func (launcher *Launcher) doStateAcceptance(id string, data interface{}) {
	stateAcceptance, ok := data.(stateAcceptance)
	if !ok {
		log.Error("Wrong data type")
		return
	}

	if err := launcher.storageHandler.StateAcceptance(stateAcceptance.acceptance, stateAcceptance.correlationID); err != nil {
		log.Errorf("Can't accept state: %s", err)
		return
	}
}

func (launcher *Launcher) updateServiceState(id string, state int, status int) (err error) {
	service, err := launcher.serviceProvider.GetService(id)
	if err != nil {
		return err
	}

	if launcher.monitor != nil && !reflect.ValueOf(launcher.monitor).IsNil() {
		if err = launcher.updateMonitoring(service, state); err != nil {
			log.WithField("id", id).Error("Can't update monitoring: ", err)
		}
	}

	if service.State != state {
		log.WithField("id", id).Debugf("Set service state: %s", stateStr[state])

		if err = launcher.serviceProvider.SetServiceState(id, state); err != nil {
			return err
		}
	}

	if service.Status != status {
		log.WithField("id", id).Debugf("Set service status: %s", statusStr[status])

		if err = launcher.serviceProvider.SetServiceStatus(id, status); err != nil {
			return err
		}
	}

	return nil
}

func (launcher *Launcher) restartService(service database.ServiceEntry) (err error) {
	fileName, err := filepath.Abs(path.Join(service.Path, service.ServiceName))
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

	if err = launcher.serviceProvider.SetServiceStartTime(service.ID, time.Now()); err != nil {
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

	if err = launcher.serviceProvider.SetServiceStartTime(service.ID, time.Now()); err != nil {
		log.WithField("id", service.ID).Warnf("Can't set service start time: %s", err)
	}

	launcher.services.Store(service.ServiceName, service.ID)

	return nil
}

func (launcher *Launcher) startServices() {
	log.WithField("users", launcher.users).Debug("Start user services")

	services, err := launcher.serviceProvider.GetUsersServices(launcher.users)
	if err != nil {
		log.Errorf("Can't start services: %s", err)
	}

	statusChannel := make(chan error, len(services))

	// Start all services in parallel
	for _, service := range services {
		launcher.actionHandler.PutInQueue(serviceAction{service.ID, service,
			func(id string, data interface{}) {
				service, ok := data.(database.ServiceEntry)
				if !ok {
					statusChannel <- errors.New("wrong data type")
					return
				}

				statusChannel <- launcher.startService(service)
			}})
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
		services, err = launcher.serviceProvider.GetServices()
		if err != nil {
			log.Errorf("Can't stop services: %s", err)
		}
	} else {
		services, err = launcher.serviceProvider.GetUsersServices(launcher.users)
		if err != nil {
			log.Errorf("Can't stop services: %s", err)
		}
	}

	statusChannel := make(chan error, len(services))

	// Stop all services in parallel
	for _, service := range services {
		launcher.actionHandler.PutInQueue(serviceAction{service.ID, service,
			func(id string, data interface{}) {
				service, ok := data.(database.ServiceEntry)
				if !ok {
					statusChannel <- errors.New("wrong data type")
					return
				}

				statusChannel <- launcher.stopService(service)
			}})
	}

	// Wait all services are stopped
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}
}

func (launcher *Launcher) restoreService(service database.ServiceEntry) (retErr error) {
	log.WithField("id", service.ID).Warn("Restore previous service version")

	if err := launcher.serviceProvider.UpdateService(service); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't update service in DB: %s", err)
			retErr = err
		}
	}

	if err := platform.SetUserFSQuota(launcher.config.WorkingDir, service.StorageLimit, service.UserName); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't set user FS quoate: %s", err)
			retErr = err
		}
	}

	if err := launcher.restartService(service); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't install service: %s", err)
			retErr = err
		}
	}

	return retErr
}

func (launcher *Launcher) prepareService(installDir string,
	serviceInfo amqp.ServiceInfoFromCloud) (service database.ServiceEntry, err error) {
	userName, err := platform.CreateUser(serviceInfo.ID)
	if err != nil {
		return service, err
	}

	// update config.json
	spec, err := launcher.updateServiceSpec(installDir, userName)
	if err != nil {
		return service, err
	}

	serviceName := "aos_" + serviceInfo.ID + ".service"

	if err = launcher.createSystemdService(installDir, serviceName, serviceInfo.ID, spec); err != nil {
		return service, err
	}

	alertRules, err := json.Marshal(serviceInfo.ServiceMonitoring)
	if err != nil {
		return service, err
	}

	service = database.ServiceEntry{
		ID:          serviceInfo.ID,
		Version:     serviceInfo.Version,
		Path:        installDir,
		ServiceName: serviceName,
		UserName:    userName,
		State:       stateInit,
		Status:      statusOk,
		AlertRules:  string(alertRules)}

	if err = launcher.updateServiceFromSpec(&service, spec); err != nil {
		return service, err
	}

	return service, nil
}

func (launcher *Launcher) addService(service database.ServiceEntry) (err error) {
	// We can't remove service if it is not in serviceProvider. Just return error and rollback will be
	// handled by parent function

	if err = platform.SetUserFSQuota(launcher.config.WorkingDir,
		service.StorageLimit+service.StateLimit, service.UserName); err != nil {
		return err
	}

	if err = launcher.serviceProvider.AddService(service); err != nil {
		return err
	}

	defer func() {
		if r := recover(); r != nil {
			log.WithField("id", service.ID).Error(r)
			launcher.removeService(service)
		}
	}()

	if err = launcher.addServiceToCurrentUsers(service.ID); err != nil {
		panic("Add service to users failed")
	}

	if err = launcher.restartService(service); err != nil {
		panic("Restart service failed")
	}

	return err
}

func (launcher *Launcher) updateService(oldService, newService database.ServiceEntry) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.WithField("id", newService.ID).Error(r)

			if err := launcher.restoreService(oldService); err != nil {
				launcher.removeService(oldService)

				launcher.StatusChannel <- amqp.ServiceInfo{
					ID:      oldService.ID,
					Version: oldService.Version,
					Status:  "removed"}
			}
		}
	}()

	launcher.services.Delete(oldService.ServiceName)

	if err = launcher.updateServiceState(oldService.ID, stateStopped, statusOk); err != nil {
		panic("Update service state failed")
	}

	if err = platform.SetUserFSQuota(launcher.config.WorkingDir, newService.StorageLimit, newService.UserName); err != nil {
		panic("Set service quota failed")
	}

	if err = launcher.serviceProvider.UpdateService(newService); err != nil {
		panic("Update service failed")
	}

	if err = launcher.restartService(newService); err != nil {
		panic("Restart service failed")
	}

	if err = os.RemoveAll(oldService.Path); err != nil {
		panic("Remove service dir failed")
	}

	launcher.StatusChannel <- amqp.ServiceInfo{
		ID:      oldService.ID,
		Version: oldService.Version,
		Status:  "removed"}

	return nil
}

func (launcher *Launcher) removeService(service database.ServiceEntry) (retErr error) {
	log.WithFields(log.Fields{"id": service.ID, "version": service.Version}).Debug("Remove service")

	if err := launcher.stopService(service); err != nil {
		if retErr == nil {
			retErr = err
		}
	}

	if _, err := launcher.systemd.DisableUnitFiles([]string{service.ServiceName}, false); err != nil {
		if retErr == nil {
			log.WithField("name", service.ID).Errorf("Can't disable systemd unit: %s", err)
			retErr = err
		}
	}

	entries, err := launcher.serviceProvider.GetUsersEntriesByServiceID(service.ID)
	if err != nil {
		if retErr == nil {
			log.WithField("name", service.ID).Errorf("Can't get users entry: %s", err)
			retErr = err
		}
	}

	for _, entry := range entries {
		if entry.StorageFolder != "" {
			log.WithFields(log.Fields{
				"folder":    entry.StorageFolder,
				"serviceID": service.ID}).Debug("Remove storage folder")

			if err := os.RemoveAll(entry.StorageFolder); err != nil {
				if retErr == nil {
					log.WithField("name", service.ID).Errorf("Can't remove storage folder: %s", err)
					retErr = err
				}
			}
		}
	}

	if err := launcher.serviceProvider.DeleteUsersByServiceID(service.ID); err != nil {
		if retErr == nil {
			log.WithField("name", service.ID).Errorf("Can't delete users from DB: %s", err)
			retErr = err
		}
	}

	if err := launcher.serviceProvider.RemoveService(service.ID); err != nil {
		if retErr == nil {
			log.WithField("name", service.ID).Errorf("Can't remove service from DB: %s", err)
			retErr = err
		}
	}

	if err := os.RemoveAll(service.Path); err != nil {
		if retErr == nil {
			log.WithField("name", service.ID).Errorf("Can't remove service folder: %s", err)
			retErr = err
		}
	}

	if err := platform.DeleteUser(service.ID); err != nil {
		if retErr == nil {
			log.WithField("name", service.ID).Errorf("Can't delete user: %s", err)
			retErr = err
		}
	}

	return retErr
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
SuccessExitStatus=SIGKILL

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

	return err
}

func (launcher *Launcher) updateMonitoring(service database.ServiceEntry, state int) (err error) {
	switch state {
	case stateRunning:
		var rules amqp.ServiceAlertRules

		if err := json.Unmarshal([]byte(service.AlertRules), &rules); err != nil {
			return err
		}

		if err = launcher.monitor.StartMonitorService(service.ID, monitoring.ServiceMonitoringConfig{
			ServiceDir:    service.Path,
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
	if localSpec.Process.User.UID, localSpec.Process.User.GID, err = platform.GetUserUIDGID(userName); err != nil {
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

func (launcher *Launcher) addServiceToCurrentUsers(serviceID string) (err error) {
	exist, err := launcher.serviceProvider.IsUsersService(launcher.users, serviceID)
	if err != nil {
		return err
	}
	if !exist {
		if err = launcher.serviceProvider.AddUsersService(launcher.users, serviceID); err != nil {
			return err
		}
	}

	return nil
}

func (launcher *Launcher) cleanServicesDB() (err error) {
	log.Debug("Clean services DB")

	startedServices, err := launcher.serviceProvider.GetUsersServices(launcher.users)
	if err != nil {
		return err
	}

	allServices, err := launcher.serviceProvider.GetServices()
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

			go func(service database.ServiceEntry) {
				statusChannel <- launcher.removeService(service)
			}(service)
		}
	}

	// Wait all services are removed
	for i := 0; i < servicesToBeRemoved; i++ {
		<-statusChannel
	}

	return nil
}
