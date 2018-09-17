// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"container/list"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cavaliercoder/grab"
	"github.com/coreos/go-systemd/dbus"
	"github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/fcrypt"
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
func New(config *config.Config, db *database.Database) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	var localLauncher Launcher

	localLauncher.db = db
	localLauncher.config = config

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

// GetServiceIPAddress returns service ip address
func (launcher *Launcher) GetServiceIPAddress(id string) (address string, err error) {
	service, err := launcher.db.GetService(id)
	if err != nil {
		return address, err
	}

	data, err := ioutil.ReadFile(path.Join(service.Path, ".ip"))
	if err != nil {
		return address, err
	}

	address = string(data)

	log.WithField("id", id).Debugf("Get ip address: %s", address)

	return address, nil
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
				statusChannel <- launcher.removeService(id)
			}(service.ID)
		}
	}

	// Wait all services are removed
	for i := 0; i < servicesToBeRemoved; i++ {
		<-statusChannel
	}

	return nil
}

func (launcher *Launcher) cleanUsersDB() (err error) {
	log.Debug("Clean users DB")

	usersList, err := launcher.db.GetUsersList()
	if err != nil {
		return err
	}

	for _, users := range usersList {
		services, err := launcher.db.GetUsersServices(users)
		if err != nil {
			log.WithField("users", users).Errorf("Can't get users services: %s", err)
		}
		if len(services) == 0 {
			log.WithField("users", users).Debug("Delete users from DB")
			err = launcher.db.DeleteUsers(users)
			if err != nil {
				log.WithField("users", users).Errorf("Can't delete users: %s", err)
			}
		}
	}

	return nil
}

func (launcher *Launcher) doActionInstall(serviceInfo amqp.ServiceInfoFromCloud) (err error) {
	if launcher.users == nil {
		return errors.New("Users are not set")
	}

	service, err := launcher.db.GetService(serviceInfo.ID)
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
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

func (launcher *Launcher) doActionRemove(id string) (version uint, err error) {
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

// downloadService downloads service
func (launcher *Launcher) downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	client := grab.NewClient()

	destDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		log.Error("Can't create tmp dir : ", err)
		return outputFile, err
	}
	req, err := grab.NewRequest(destDir, serviceInfo.DownloadURL)
	if err != nil {
		log.Error("Can't download package: ", err)
		return outputFile, err
	}

	// start download
	resp := client.Do(req)
	defer os.RemoveAll(destDir)

	log.WithField("filename", resp.Filename).Debug("Start downloading")

	// wait when finished
	resp.Wait()

	if err = resp.Err(); err != nil {
		log.Error("Can't download package: ", err)
		return outputFile, err
	}

	imageSignature, err := hex.DecodeString(serviceInfo.ImageSignature)
	if err != nil {
		log.Error("Error decoding HEX string for signature: ", err)
		return outputFile, err
	}

	encryptionKey, err := hex.DecodeString(serviceInfo.EncryptionKey)
	if err != nil {
		log.Error("Error decoding HEX string for key: ", err)
		return outputFile, err
	}

	encryptionModeParams, err := hex.DecodeString(serviceInfo.EncryptionModeParams)
	if err != nil {
		log.Error("Error decoding HEX string for IV: ", err)
		return outputFile, err
	}

	certificateChain := strings.Replace(serviceInfo.CertificateChain, "\\n", "", -1)
	outputFile, err = fcrypt.DecryptImage(
		resp.Filename,
		imageSignature,
		encryptionKey,
		encryptionModeParams,
		serviceInfo.SignatureAlgorithm,
		serviceInfo.SignatureAlgorithmHash,
		serviceInfo.SignatureScheme,
		serviceInfo.EncryptionAlgorithm,
		serviceInfo.EncryptionMode,
		certificateChain)

	if err != nil {
		log.Error("Can't decrypt image: ", err)
		return outputFile, err
	}

	log.WithField("filename", outputFile).Debug("Decrypt image")

	return outputFile, nil
}

func (launcher *Launcher) isIDInWorkQueue(id string) (result bool) {
	for item := launcher.workQueue.Front(); item != nil; item = item.Next() {
		if item.Value.(serviceAction).id == id {
			return true
		}
	}

	return false
}

func (launcher *Launcher) putInQueue(action serviceAction) {
	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	if launcher.isIDInWorkQueue(action.id) {
		launcher.waitQueue.PushBack(action)
		return
	}

	if launcher.workQueue.Len() >= maxExecutedActions {
		launcher.waitQueue.PushBack(action)
		return
	}

	go launcher.processAction(launcher.workQueue.PushBack(action))
}

func (launcher *Launcher) processAction(item *list.Element) {
	action := item.Value.(serviceAction)
	status := ActionStatus{Action: action.action, ID: action.id}

	switch action.action {
	case ActionInstall:
		serviceInfo := action.data.(amqp.ServiceInfoFromCloud)
		status.Version = serviceInfo.Version

		status.Err = launcher.doActionInstall(serviceInfo)

	case ActionRemove:
		status.Version, status.Err = launcher.doActionRemove(status.ID)
	}

	launcher.StatusChannel <- status

	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	launcher.workQueue.Remove(item)

	for item := launcher.waitQueue.Front(); item != nil; item = item.Next() {
		if launcher.isIDInWorkQueue(item.Value.(serviceAction).id) {
			continue
		}

		go launcher.processAction(launcher.workQueue.PushBack(launcher.waitQueue.Remove(item)))
		break
	}
}

func (launcher *Launcher) downloadAndUnpackImage(serviceInfo amqp.ServiceInfoFromCloud) (installDir string, err error) {
	// download image
	image, err := launcher.downloader.downloadService(serviceInfo)
	if image != "" {
		defer os.Remove(image)
	}
	if err != nil {
		return installDir, err
	}

	// create install dir
	installDir, err = ioutil.TempDir(path.Join(launcher.config.WorkingDir, serviceDir), "")
	if err != nil {
		return installDir, err
	}
	log.WithField("dir", installDir).Debug("Create install dir")

	// unpack image there
	if err = unpackImage(image, installDir); err != nil {
		return installDir, err
	}

	return installDir, nil
}

func (launcher *Launcher) createUser(id string) (userName string, err error) {
	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	// convert id to hashed u32 value
	hash := fnv.New32a()
	hash.Write([]byte(id))

	// create user
	userName = "user_" + strconv.FormatUint(uint64(hash.Sum32()), 16)
	// if user exists
	if _, err = user.Lookup(userName); err == nil {
		return userName, errors.New("User already exists")
	}

	log.WithField("user", userName).Debug("Create user")

	if err = exec.Command("useradd", "-M", userName).Run(); err != nil {
		return userName, fmt.Errorf("Error creating user: %s", err)
	}

	return userName, nil
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

func (launcher *Launcher) installService(serviceInfo amqp.ServiceInfoFromCloud) (installDir string, err error) {

	log.WithFields(log.Fields{"id": serviceInfo.ID, "version": serviceInfo.Version}).Debug("Install service")

	// download and unpack
	installDir, err = launcher.downloadAndUnpackImage(serviceInfo)
	if err != nil {
		return installDir, err
	}

	// check if service already installed
	oldService, err := launcher.db.GetService(serviceInfo.ID)
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		return installDir, err
	}
	serviceExists := err == nil

	// create OS user
	userName, err := launcher.createUser(serviceInfo.ID)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return installDir, err
	}

	// update config.json
	spec, err := launcher.updateServiceSpec(installDir, userName)
	if err != nil {
		return installDir, err
	}

	serviceName := "aos_" + serviceInfo.ID + ".service"

	setNetLimitCmd, clearNetLimitCmd := launcher.generateNetLimitsCmds(spec)
	err = launcher.createSystemdService(installDir, serviceName, serviceInfo.ID, setNetLimitCmd, clearNetLimitCmd)
	if err != nil {
		return installDir, err
	}

	ttl, err := strconv.ParseUint(spec.Annotations[aosProductPrefix+"service.TTL"], 10, 64)
	if err != nil {
		return installDir, err
	}

	newService := database.ServiceEntry{
		ID:          serviceInfo.ID,
		Version:     serviceInfo.Version,
		Path:        installDir,
		ServiceName: serviceName,
		UserName:    userName,
		Permissions: spec.Annotations[aosProductPrefix+"vis.permissions"],
		State:       stateInit,
		Status:      statusOk,
		TTL:         uint(ttl)}

	if !serviceExists {
		if err = launcher.addServiceToDB(newService); err != nil {
			return installDir, err
		}
	}

	if err = launcher.restartService(newService.ID, newService.ServiceName); err != nil {
		// TODO: try to restore old service
		return installDir, err
	}

	// remove if exists
	if serviceExists {
		if err = launcher.db.UpdateService(newService); err != nil {
			return installDir, err
		}

		if err = os.RemoveAll(oldService.Path); err != nil {
			// indicate error, can continue
			log.WithField("path", oldService.Path).Error("Can't remove service path")
		}
	}

	return installDir, nil
}

func (launcher *Launcher) removeService(id string) (err error) {
	service, err := launcher.db.GetService(id)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{"id": service.ID, "version": service.Version}).Debug("Remove service")

	if err := launcher.stopService(service.ID, service.ServiceName); err != nil {
		log.WithField("name", service.ServiceName).Error("Can't stop service: ", err)
	}

	if _, err := launcher.systemd.DisableUnitFiles([]string{service.ServiceName}, false); err != nil {
		log.WithField("name", service.ServiceName).Error("Can't disable systemd unit: ", err)
	}

	if err := launcher.db.RemoveService(service.ID); err != nil {
		log.WithField("name", service.ServiceName).Error("Can't remove service from db: ", err)
	}

	if err := os.RemoveAll(service.Path); err != nil {
		log.WithField("path", service.Path).Error("Can't remove service path")
	}

	launcher.mutex.Lock()

	log.WithField("user", service.UserName).Debug("Delete user")

	if err := exec.Command("userdel", service.UserName).Run(); err != nil {
		log.WithField("user", service.UserName).Error("Can't remove user")
	}
	launcher.mutex.Unlock()

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
ExecStart=${RUNC} run -d --pid-file ${SERVICEPATH}/${ID}.pid -b ${SERVICEPATH} ${ID}
ExecStartPost=${SETNETLIMIT}

ExecStopPre=${CLEARNETLIMIT}
ExecStop=${RUNC} kill ${ID} SIGKILL
ExecStopPost=${RUNC} delete -f ${ID}
PIDFile=${SERVICEPATH}/${ID}.pid

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

func (launcher *Launcher) handleSystemdSubscription() {
	unitStatus, errorChannel := launcher.systemd.SubscribeUnitsCustom(time.Millisecond*1000,
		2,
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

					log.WithField("name", unit.Name).Debugf("Set service state: %s, status: %s", stateStr[state], statusStr[status])

					id, exist := launcher.services.Load(unit.Name)

					if exist {
						if err := launcher.db.SetServiceState(id.(string), state); err != nil {
							log.WithField("name", unit.Name).Error("Can't set service state: ", err)
						}

						if err := launcher.db.SetServiceStatus(id.(string), status); err != nil {
							log.WithField("name", unit.Name).Error("Can't set service status: ", err)
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
	user, err := user.Lookup(userName)
	if err != nil {
		return spec, err
	}

	uid, err := strconv.ParseUint(user.Uid, 10, 32)
	if err != nil {
		return spec, err
	}

	gid, err := strconv.ParseUint(user.Gid, 10, 32)
	if err != nil {
		return spec, err
	}

	localSpec.Process.User.UID = uint32(uid)
	localSpec.Process.User.GID = uint32(gid)

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

	// update service TTL
	_, exist := localSpec.Annotations[aosProductPrefix+"service.TTL"]
	if !exist {
		localSpec.Annotations[aosProductPrefix+"service.TTL"] = strconv.FormatUint(uint64(launcher.config.DefaultServiceTTL), 10)
	}

	// write config.json
	if err = writeServiceSpec(&localSpec, configFile); err != nil {
		return spec, err
	}

	return &localSpec, nil
}

func (launcher *Launcher) restartService(id, serviceName string) (err error) {
	channel := make(chan string)
	if _, err = launcher.systemd.RestartUnit(serviceName, "replace", channel); err != nil {
		return err
	}
	status := <-channel

	log.WithFields(log.Fields{"name": serviceName, "status": status}).Debug("Restart service")

	if err = launcher.db.SetServiceState(id, stateRunning); err != nil {
		log.WithField("id", id).Warnf("Can't set service state: %s", err)
	}

	if err = launcher.db.SetServiceStatus(id, statusOk); err != nil {
		log.WithField("id", id).Warnf("Can't set service status: %s", err)
	}

	if err = launcher.db.SetServiceStartTime(id, time.Now()); err != nil {
		log.WithField("id", id).Warnf("Can't set service start time: %s", err)
	}

	launcher.services.Store(serviceName, id)

	return nil
}

func (launcher *Launcher) startService(id, serviceName string) (err error) {
	channel := make(chan string)
	if _, err = launcher.systemd.StartUnit(serviceName, "replace", channel); err != nil {
		return err
	}
	status := <-channel

	log.WithFields(log.Fields{"name": serviceName, "status": status}).Debug("Start service")

	if err = launcher.db.SetServiceState(id, stateRunning); err != nil {
		log.WithField("id", id).Warnf("Can't set service state: %s", err)
	}

	if err = launcher.db.SetServiceStatus(id, statusOk); err != nil {
		log.WithField("id", id).Warnf("Can't set service status: %s", err)
	}

	if err = launcher.db.SetServiceStartTime(id, time.Now()); err != nil {
		log.WithField("id", id).Warnf("Can't set service start time: %s", err)
	}

	launcher.services.Store(serviceName, id)

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
			err := launcher.startService(service.ID, service.ServiceName)
			if err != nil {
				log.Errorf("Can't start service %s: %s", service.ID, err)
			}
			statusChannel <- err
		}(service)
	}

	// Wait all services are started
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}
}

func (launcher *Launcher) stopService(id, serviceName string) (err error) {
	launcher.services.Delete(serviceName)

	channel := make(chan string)
	if _, err := launcher.systemd.StopUnit(serviceName, "replace", channel); err != nil {
		return err
	}
	status := <-channel

	log.WithFields(log.Fields{"name": serviceName, "status": status}).Debug("Stop service")

	if err = launcher.db.SetServiceState(id, stateStopped); err != nil {
		log.WithField("id", id).Warnf("Can't set service state: %s", err)
	}

	if err = launcher.db.SetServiceStatus(id, statusOk); err != nil {
		log.WithField("id", id).Warnf("Can't set service status: %s", err)
	}

	return nil
}

func (launcher *Launcher) stopServices() {
	log.WithField("users", launcher.users).Debug("Stop user services")

	services, err := launcher.db.GetUsersServices(launcher.users)
	if err != nil {
		log.Errorf("Can't stop services: %s", err)
	}

	statusChannel := make(chan error, len(services))

	// Stop all services in parallel
	for _, service := range services {
		go func(service database.ServiceEntry) {
			err := launcher.stopService(service.ID, service.ServiceName)
			if err != nil {
				log.Errorf("Can't stop service %s: %s", service.ID, err)
			}
			statusChannel <- err
		}(service)
	}

	// Wait all services are stopped
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}
}

func (launcher *Launcher) generateNetLimitsCmds(spec *specs.Spec) (setCmd, clearCmd string) {
	value, exist := spec.Annotations[aosProductPrefix+"network.download"]
	if exist {
		setCmd = setCmd + " -d " + value
	}
	value, exist = spec.Annotations[aosProductPrefix+"network.upload"]
	if exist {
		setCmd = setCmd + " -u " + value
	}
	if setCmd != "" {
		setCmd = launcher.wonderShaperPath + " -a netnsv0-${MAINPID}" + setCmd
		clearCmd = launcher.wonderShaperPath + " -c -a netnsv0-${MAINPID}"

		log.Debugf("Set net limit cmd: %s", setCmd)
		log.Debugf("Clear net limit cmd: %s", clearCmd)
	}

	return setCmd, clearCmd
}

func (launcher *Launcher) createSystemdService(installDir, serviceName, id string, setNetLimitCmd, clearNetLimitCmd string) (err error) {
	f, err := os.Create(path.Join(installDir, serviceName))
	if err != nil {
		return err
	}
	defer f.Close()

	absServicePath, err := filepath.Abs(installDir)
	if err != nil {
		return err
	}

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
