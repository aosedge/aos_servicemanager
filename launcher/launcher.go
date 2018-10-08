// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"container/list"
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
	serviceDir         = "services" // services directory
	maxExecutedActions = 10         // max number of actions processed simultaneously

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

type action int

//ActionStatus status of performed action
type ActionStatus struct {
	Action  action
	ID      string
	Version uint
	Err     error
}

type downloadItf interface {
	downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error)
}

// Launcher instance
type Launcher struct {
	// StatusChannel used to return execute command statuses
	StatusChannel chan ActionStatus

	db      *database.Database
	monitor monitoring.ServiceMonitoringItf
	systemd *dbus.Conn
	config  *config.Config

	downloader downloadItf

	users []string

	closeChannel chan bool

	services sync.Map

	mutex sync.Mutex

	waitQueue *list.List
	workQueue *list.List

	serviceTemplate  string
	runcPath         string
	netnsPath        string
	wonderShaperPath string
}

type serviceAction struct {
	action action
	id     string
	data   interface{}
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New(config *config.Config, db *database.Database,
	monitoring monitoring.ServiceMonitoringItf) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	var localLauncher Launcher

	localLauncher.db = db
	localLauncher.config = config
	localLauncher.monitor = monitoring

	localLauncher.closeChannel = make(chan bool)
	localLauncher.StatusChannel = make(chan ActionStatus, maxExecutedActions)

	localLauncher.waitQueue = list.New()
	localLauncher.workQueue = list.New()

	localLauncher.downloader = &localLauncher

	// Check and create service dir
	dir := path.Join(config.WorkingDir, serviceDir)
	if _, err = os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return launcher, err
		}
		if err = os.MkdirAll(dir, 0755); err != nil {
			return launcher, err
		}
	}

	// Create systemd connection
	localLauncher.systemd, err = dbus.NewSystemConnection()
	if err != nil {
		return launcher, err
	}
	if err = localLauncher.systemd.Subscribe(); err != nil {
		return launcher, err
	}

	localLauncher.handleSystemdSubscription()

	// Get systemd service template
	localLauncher.serviceTemplate, err = getSystemdServiceTemplate(config.WorkingDir)
	if err != nil {
		return launcher, err
	}

	// Retrieve runc abs path
	localLauncher.runcPath, err = exec.LookPath(runcName)
	if err != nil {
		return launcher, err
	}

	// Retrieve netns abs path
	localLauncher.netnsPath, _ = filepath.Abs(path.Join(config.WorkingDir, netnsName))
	if _, err := os.Stat(localLauncher.netnsPath); err != nil {
		// check system PATH
		localLauncher.netnsPath, err = exec.LookPath(netnsName)
		if err != nil {
			return launcher, err
		}
	}

	// Retrieve wondershaper abs path
	localLauncher.wonderShaperPath, _ = filepath.Abs(path.Join(config.WorkingDir, wonderShaperName))
	if _, err := os.Stat(localLauncher.wonderShaperPath); err != nil {
		// check system PATH
		localLauncher.wonderShaperPath, err = exec.LookPath(wonderShaperName)
		if err != nil {
			return launcher, err
		}
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
}

// GetServiceVersion returns installed version of requested service
func (launcher *Launcher) GetServiceVersion(id string) (version uint, err error) {
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
	launcher.putInQueue(serviceAction{ActionInstall, serviceInfo.ID, serviceInfo})
}

// RemoveService stops and removes service
func (launcher *Launcher) RemoveService(id string) {
	launcher.putInQueue(serviceAction{ActionRemove, id, nil})
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
