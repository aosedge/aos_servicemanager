// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"container/list"
	"encoding/hex"
	"errors"
	"fmt"
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

	db               *database.Database
	systemd          *dbus.Conn
	downloader       downloadItf
	closeChannel     chan bool
	services         sync.Map
	mutex            sync.Mutex
	waitQueue        *list.List
	workQueue        *list.List
	serviceTemplate  string
	workingDir       string
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
func New(workingDir string, db *database.Database) (launcher *Launcher, err error) {
	log.Debug("New launcher")

	var localLauncher Launcher

	localLauncher.db = db
	localLauncher.workingDir = workingDir

	localLauncher.closeChannel = make(chan bool)
	localLauncher.StatusChannel = make(chan ActionStatus, maxExecutedActions)

	localLauncher.waitQueue = list.New()
	localLauncher.workQueue = list.New()

	localLauncher.downloader = &localLauncher

	// Check and create service dir
	dir := path.Join(workingDir, serviceDir)
	if _, err = os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return launcher, err
		}
		if err = os.MkdirAll(dir, 0755); err != nil {
			return launcher, err
		}
	}

	// Load all installed services
	services, err := localLauncher.db.GetServices()
	if err != nil {
		return launcher, err
	}
	for _, service := range services {
		localLauncher.services.Store(service.ServiceName, service.ID)
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
	localLauncher.serviceTemplate, err = getSystemdServiceTemplate(workingDir)
	if err != nil {
		return launcher, err
	}

	// Retrieve runc abs path
	localLauncher.runcPath, err = exec.LookPath(runcName)
	if err != nil {
		return launcher, err
	}

	// Retrieve netns abs path
	localLauncher.netnsPath, _ = filepath.Abs(path.Join(workingDir, netnsName))
	if _, err := os.Stat(localLauncher.netnsPath); err != nil {
		// check system PATH
		localLauncher.netnsPath, err = exec.LookPath(netnsName)
		if err != nil {
			return launcher, err
		}
	}

	// Retrieve wondershaper abs path
	localLauncher.wonderShaperPath, _ = filepath.Abs(path.Join(workingDir, wonderShaperName))
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

	services, err := launcher.db.GetServices()
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

/*******************************************************************************
 * Private
 ******************************************************************************/

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

		// check installed service version
		if service, err := launcher.db.GetService(serviceInfo.ID); err == nil && serviceInfo.Version <= service.Version {
			status.Err = errors.New("Version mistmatch")
			break
		}

		imageFile, err := launcher.downloader.downloadService(serviceInfo)
		if imageFile != "" {
			defer os.Remove(imageFile)
		}
		if err != nil {
			status.Err = err
			break
		}

		if installDir, err := launcher.installService(imageFile, serviceInfo.ID, serviceInfo.Version); err != nil {
			os.RemoveAll(installDir)
			status.Err = err
			break
		}

	case ActionRemove:
		if err := launcher.removeService(status.ID); err != nil {
			status.Err = err
			break
		}
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

// InstallService installs and runs service
func (launcher *Launcher) installService(image string, id string, version uint) (installDir string, err error) {
	log.WithFields(log.Fields{"path": image, "id": id, "version": version}).Debug("Install service")

	// TODO: do we need install to /tmp dir first?
	// In case something wrong, artifacts will be removed after system reboot
	// but it will introduce additional io operations.

	// create install dir
	installDir, err = ioutil.TempDir(path.Join(launcher.workingDir, serviceDir), "")
	if err != nil {
		return installDir, err
	}
	log.WithField("dir", installDir).Debug("Create install dir")

	// unpack image there
	if err = unpackImage(image, installDir); err != nil {
		return installDir, err
	}

	// check if service already installed
	service, err := launcher.db.GetService(id)
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		return installDir, err
	}
	serviceExists := err == nil

	// create user
	userName := "user_" + id
	// if user exists
	if _, err = user.Lookup(userName); err != nil {
		log.WithField("user", userName).Debug("Create user")

		launcher.mutex.Lock()
		if err = exec.Command("useradd", "-M", userName).Run(); err != nil {
			launcher.mutex.Unlock()
			return installDir, err
		}
		launcher.mutex.Unlock()
	} else if !serviceExists {
		log.WithField("user", userName).Warning("User already exists")
	}

	configFile := path.Join(installDir, "config.json")

	// get service spec
	spec, err := getServiceSpec(configFile)
	if err != nil {
		return installDir, err
	}

	// update config.json
	if err = launcher.updateServiceSpec(&spec, userName); err != nil {
		return installDir, err
	}

	// update config.json
	if err = writeServiceSpec(&spec, configFile); err != nil {
		return installDir, err
	}

	serviceName := "aos_" + id + ".service"

	serviceFile, err := launcher.createSystemdService(installDir, serviceName, id, &spec)
	if err != nil {
		return installDir, err
	}

	if err = launcher.startService(serviceFile, serviceName); err != nil {
		// TODO: try to restore old service
		return installDir, err
	}

	// remove if exists
	if serviceExists {
		if err = launcher.db.RemoveService(service.ID); err != nil {
			return installDir, err
		}
		if err = os.RemoveAll(service.Path); err != nil {
			// indicate error, can continue
			log.WithField("path", service.Path).Error("Can't remove service path")
		}
	}

	service = database.ServiceEntry{
		ID:          id,
		Version:     version,
		Path:        installDir,
		ServiceName: serviceName,
		UserName:    userName,
		Permissions: spec.Annotations[aosProductPrefix+"vis.permissions"],
		State:       stateInit,
		Status:      statusOk}

	// add to database
	if err = launcher.db.AddService(service); err != nil {
		if err = launcher.stopService(serviceName); err != nil {
			log.WithField("name", serviceName).Warn("Can't stop service: ", err)
		}
		// TODO: delete linux user?
		// TODO: try to restore old
		return installDir, err
	}

	launcher.services.Store(serviceName, id)

	return installDir, nil
}

// RemoveService stops and removes service
// TODO: consider what to do with errors on remove: pass it to servicemanager or
// just display
func (launcher *Launcher) removeService(id string) (err error) {
	service, err := launcher.db.GetService(id)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{"id": service.ID, "version": service.Version}).Debug("Remove service")

	launcher.services.Delete(service.ServiceName)

	if err := launcher.stopService(service.ServiceName); err != nil {
		log.WithField("name", service.ServiceName).Error("Can't stop service: ", err)
	}

	if err := launcher.db.RemoveService(service.ID); err != nil {
		log.WithField("name", service.ServiceName).Error("Can't remove service from db: ", err)
	}

	if err := os.RemoveAll(service.Path); err != nil {
		log.WithField("path", service.Path).Error("Can't remove service path")
	}

	log.WithField("user", service.UserName).Debug("Delete user")

	launcher.mutex.Lock()
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
	serviceChannel, errorChannel := launcher.systemd.SubscribeUnitsCustom(time.Millisecond*1000,
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
			case services := <-serviceChannel:
				for _, service := range services {
					var (
						state  int
						status int
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

					id, exist := launcher.services.Load(service.Name)

					if exist {
						if err := launcher.db.SetServiceState(id.(string), state); err != nil {
							log.WithField("name", service.Name).Error("Can't set service state: ", err)
						}

						if err := launcher.db.SetServiceStatus(id.(string), status); err != nil {
							log.WithField("name", service.Name).Error("Can't set service status: ", err)
						}
					} else {
						log.WithField("name", service.Name).Warning("Can't update state or status. Service is not installed.")
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

func (launcher *Launcher) updateServiceSpec(spec *specs.Spec, userName string) (err error) {
	// disable terminal
	spec.Process.Terminal = false

	// assign UID, GID
	user, err := user.Lookup(userName)
	if err != nil {
		return err
	}

	uid, err := strconv.ParseUint(user.Uid, 10, 32)
	if err != nil {
		return err
	}

	gid, err := strconv.ParseUint(user.Gid, 10, 32)
	if err != nil {
		return err
	}

	spec.Process.User.UID = uint32(uid)
	spec.Process.User.GID = uint32(gid)

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
	spec.Mounts = append(spec.Mounts, mounts...)
	// add lib64 if exists
	if _, err := os.Stat("/lib64"); err == nil {
		spec.Mounts = append(spec.Mounts, specs.Mount{Destination: "/lib64", Type: "bind", Source: "/lib64", Options: []string{"bind", "ro"}})
	}
	// add hosts
	hosts, _ := filepath.Abs(path.Join(launcher.workingDir, "etc", "hosts"))
	if _, err := os.Stat(hosts); err != nil {
		hosts = "/etc/hosts"
	}
	spec.Mounts = append(spec.Mounts, specs.Mount{Destination: path.Join("/etc", "hosts"), Type: "bind", Source: hosts, Options: []string{"bind", "ro"}})
	// add resolv.conf
	resolvConf, _ := filepath.Abs(path.Join(launcher.workingDir, "etc", "resolv.conf"))
	if _, err := os.Stat(resolvConf); err != nil {
		resolvConf = "/etc/resolv.conf"
	}
	spec.Mounts = append(spec.Mounts, specs.Mount{Destination: path.Join("/etc", "resolv.conf"), Type: "bind", Source: resolvConf, Options: []string{"bind", "ro"}})
	// add nsswitch.conf
	nsswitchConf, _ := filepath.Abs(path.Join(launcher.workingDir, "etc", "nsswitch.conf"))
	if _, err := os.Stat(nsswitchConf); err != nil {
		nsswitchConf = "/etc/nsswitch.conf"
	}
	spec.Mounts = append(spec.Mounts, specs.Mount{Destination: path.Join("/etc", "nsswitch.conf"), Type: "bind", Source: nsswitchConf, Options: []string{"bind", "ro"}})

	// TODO: all services should have their own certificates
	// this mound for demo only and should be removed
	// mount /etc/ssl
	spec.Mounts = append(spec.Mounts, specs.Mount{Destination: path.Join("/etc", "ssl"), Type: "bind", Source: path.Join("/etc", "ssl"), Options: []string{"bind", "ro"}})

	// add netns hook
	if spec.Hooks == nil {
		spec.Hooks = &specs.Hooks{}
	}
	spec.Hooks.Prestart = append(spec.Hooks.Prestart, specs.Hook{Path: launcher.netnsPath})

	return nil
}

func (launcher *Launcher) startService(serviceFile, serviceName string) (err error) {
	launcher.services.Delete(serviceName)

	if _, _, err = launcher.systemd.EnableUnitFiles([]string{serviceFile}, false, true); err != nil {
		return err
	}

	if err = launcher.systemd.Reload(); err != nil {
		return err
	}

	channel := make(chan string)
	if _, err = launcher.systemd.RestartUnit(serviceName, "replace", channel); err != nil {
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

func (launcher *Launcher) createSystemdService(installDir, serviceName, id string, spec *specs.Spec) (fileName string, err error) {
	f, err := os.Create(path.Join(installDir, serviceName))
	if err != nil {
		return fileName, err
	}
	defer f.Close()

	absServicePath, err := filepath.Abs(installDir)
	if err != nil {
		return fileName, err
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

	fileName, err = filepath.Abs(f.Name())
	return fileName, err
}
