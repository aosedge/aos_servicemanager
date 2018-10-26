// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sync"

	"github.com/coreos/go-systemd/dbus"
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

type downloadItf interface {
	downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error)
}

// Launcher instance
type Launcher struct {
	// StatusChannel used to return execute command statuses
	StatusChannel chan ActionStatus

	db      database.ServiceItf
	monitor monitoring.ServiceMonitoringItf
	systemd *dbus.Conn
	config  *config.Config

	actionHandler *actionHandler

	downloader downloadItf

	users []string

	closeChannel chan bool

	services sync.Map

	mutex sync.Mutex

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

	if launcher.actionHandler, err = newActionHandler(); err != nil {
		return nil, err
	}

	launcher.downloader = launcher

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

	if err := launcher.systemd.Unsubscribe(); err != nil {
		log.Warn("Can't unsubscribe from systemd: ", err)
	}

	launcher.closeChannel <- true

	launcher.systemd.Close()
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

	if err = launcher.cleanUsersDB(); err != nil {
		log.Errorf("Error cleaning DB: %s", err)
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
			if installDir != "" {
				os.RemoveAll(installDir)
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
		if err = launcher.startService(service.ID, service.ServiceName); err != nil {
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

	if err := launcher.stopService(service.ID, service.ServiceName); err != nil {
		return version, err
	}

	if err = launcher.db.RemoveUsersService(launcher.users, service.ID); err != nil {
		return version, err
	}

	return version, nil
}
