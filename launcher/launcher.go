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
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cavaliercoder/grab"
	"github.com/coreos/go-systemd/dbus"
	"github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/fcrypt"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	serviceDatabase    = "services.db" // services database name
	serviceDir         = "services"    // services directory
	maxExecutedActions = 10            // max number of actions processed simulanteously
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

// Action
const (
	ActionInstall = iota
	ActionRemove
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type serviceStatus int
type serviceState int

type action int

type ActionStatus struct {
	Action  action
	Id      string
	Version uint
	Err     error
}

type downloadItf interface {
	downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error)
}

// Launcher instance
type Launcher struct {
	db              *database
	systemd         *dbus.Conn
	downloader      downloadItf
	closeChannel    chan bool
	statusChannel   chan ActionStatus
	services        sync.Map
	mutex           sync.Mutex
	waitQueue       *list.List
	workQueue       *list.List
	serviceTemplate string
	workingDir      string
	runcPath        string
	netnsPath       string
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
func New(workingDir string) (launcher *Launcher, executeChannel <-chan ActionStatus, err error) {
	log.Debug("New launcher")

	var localLauncher Launcher

	localLauncher.workingDir = workingDir

	localLauncher.closeChannel = make(chan bool)
	localLauncher.statusChannel = make(chan ActionStatus, maxExecutedActions)

	localLauncher.waitQueue = list.New()
	localLauncher.workQueue = list.New()

	localLauncher.downloader = &localLauncher

	// Check and create service dir
	dir := path.Join(workingDir, serviceDir)
	if _, err := os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return launcher, executeChannel, err
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return launcher, executeChannel, err
		}
	}

	// Create new database instance
	localLauncher.db, err = newDatabase(path.Join(workingDir, serviceDatabase))
	if err != nil {
		return launcher, executeChannel, err
	}

	// Load all installed services
	services, err := localLauncher.db.getServices()
	if err != nil {
		return launcher, executeChannel, err
	}
	for _, service := range services {
		localLauncher.services.Store(service.serviceName, service.id)
	}

	// Create systemd connection
	localLauncher.systemd, err = dbus.NewSystemConnection()
	if err != nil {
		return launcher, executeChannel, err
	}
	if err = localLauncher.systemd.Subscribe(); err != nil {
		return launcher, executeChannel, err
	}

	localLauncher.handleSystemdSubscription()

	// Get systemd service template
	localLauncher.serviceTemplate, err = getSystemdServiceTemplate(workingDir)
	if err != nil {
		return launcher, executeChannel, err
	}

	// Retreive runc abs path
	localLauncher.runcPath, err = exec.LookPath("runc")
	if err != nil {
		return launcher, executeChannel, err
	}

	// Retreive netns abs path
	localLauncher.netnsPath, _ = filepath.Abs(path.Join(workingDir, "netns"))
	if _, err := os.Stat(localLauncher.netnsPath); err != nil {
		// check system PATH
		localLauncher.netnsPath, err = exec.LookPath("netns")
		if err != nil {
			return launcher, executeChannel, err
		}
	}

	launcher = &localLauncher

	return launcher, launcher.statusChannel, nil
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
func (launcher *Launcher) InstallService(serviceInfo amqp.ServiceInfoFromCloud) {
	launcher.putInQueue(serviceAction{ActionInstall, serviceInfo.Id, serviceInfo})
}

// RemoveService stops and removes service
func (launcher *Launcher) RemoveService(id string) {
	launcher.putInQueue(serviceAction{ActionRemove, id, nil})
}

// GetServicesInfo returns informaion about all installed services
func (launcher *Launcher) GetServicesInfo() (info []amqp.ServiceInfo, err error) {
	log.Debug("Get services info")

	services, err := launcher.db.getServices()
	if err != nil {
		return info, err
	}

	info = make([]amqp.ServiceInfo, len(services))

	for i, service := range services {
		info[i] = amqp.ServiceInfo{Id: service.id, Version: service.version, Status: statusStr[service.status]}
	}

	return info, nil
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
	req, err := grab.NewRequest(destDir, serviceInfo.DownloadUrl)
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

	if err := resp.Err(); err != nil {
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
		serviceInfo.EncryptionAlgorythm,
		serviceInfo.EncryptionMode,
		certificateChain)

	if err != nil {
		log.Error("Can't decrypt image: ", err)
		return outputFile, err
	}

	log.WithField("filename", outputFile).Debug("Decrypt image")

	return outputFile, nil
}

func (launcher *Launcher) isIdInWorkQueue(id string) (result bool) {
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

	if launcher.isIdInWorkQueue(action.id) {
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
	status := ActionStatus{Action: action.action, Id: action.id}

	switch action.action {
	case ActionInstall:
		serviceInfo := action.data.(amqp.ServiceInfoFromCloud)
		status.Version = serviceInfo.Version

		// check installed service version
		if service, err := launcher.db.getService(serviceInfo.Id); err == nil && serviceInfo.Version <= service.version {
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

		if installDir, err := launcher.installService(imageFile, serviceInfo.Id, serviceInfo.Version); err != nil {
			os.RemoveAll(installDir)
			status.Err = err
			break
		}

	case ActionRemove:
		if err := launcher.removeService(status.Id); err != nil {
			status.Err = err
			break
		}
	}

	launcher.statusChannel <- status

	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	launcher.workQueue.Remove(item)

	for item := launcher.waitQueue.Front(); item != nil; item = item.Next() {
		if launcher.isIdInWorkQueue(item.Value.(serviceAction).id) {
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
	if err := unpackImage(image, installDir); err != nil {
		return installDir, err
	}

	configFile := path.Join(installDir, "config.json")

	// get service spec
	spec, err := getServiceSpec(configFile)
	if err != nil {
		return installDir, err
	}

	// update config.json
	if err := launcher.updateServiceSpec(&spec); err != nil {
		return installDir, err
	}

	// update config.json
	if err := writeServiceSpec(&spec, configFile); err != nil {
		return installDir, err
	}

	serviceName := "aos_" + id + ".service"

	serviceFile, err := launcher.createSystemdService(installDir, serviceName, id)
	if err != nil {
		return installDir, err
	}

	if err := launcher.startService(serviceFile, serviceName); err != nil {
		// TODO: try to restore old service
		return installDir, err
	}

	// check if service already installed
	service, err := launcher.db.getService(id)
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		return installDir, err
	}
	// remove if exists
	if err == nil {
		if err := launcher.db.removeService(service.id); err != nil {
			return installDir, err
		}
		if err := os.RemoveAll(service.path); err != nil {
			// indicate error, can continue
			log.WithField("path", service.path).Error("Can't remove service path")
		}
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
	service, err := launcher.db.getService(id)
	if err != nil {
		return err
	}
	log.WithFields(log.Fields{"id": service.id, "version": service.version}).Debug("Remove service")

	launcher.services.Delete(service.serviceName)

	if err := launcher.stopService(service.serviceName); err != nil {
		log.WithField("name", service.serviceName).Error("Can't stop service: ", err)
	}

	if err := launcher.db.removeService(service.id); err != nil {
		log.WithField("name", service.serviceName).Error("Can't remove service from db: ", err)
	}

	if err := os.RemoveAll(service.path); err != nil {
		log.WithField("path", service.path).Error("Can't remove service path")
	}

	return nil
}

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

					id, exist := launcher.services.Load(service.Name)

					if exist {
						if err := launcher.db.setServiceState(id.(string), state); err != nil {
							log.WithField("name", service.Name).Error("Can't set service state: ", err)
						}

						if err := launcher.db.setServiceStatus(id.(string), status); err != nil {
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

func (launcher *Launcher) updateServiceSpec(spec *specs.Spec) (err error) {
	// disable terminal
	spec.Process.Terminal = false

	mounts := []specs.Mount{
		specs.Mount{"/bin", "bind", "/bin", []string{"bind", "ro"}},
		specs.Mount{"/sbin", "bind", "/sbin", []string{"bind", "ro"}},
		specs.Mount{"/lib", "bind", "/lib", []string{"bind", "ro"}},
		specs.Mount{"/usr", "bind", "/usr", []string{"bind", "ro"}},
		// TODO: mount individual tmp
		// "destination": "/tmp",
		// "type": "tmpfs",
		// "source": "tmpfs",
		// "options": ["nosuid","strictatime","mode=755","size=65536k"]
		specs.Mount{"/tmp", "bind", "/tmp", []string{"bind", "rw"}}}
	spec.Mounts = append(spec.Mounts, mounts...)
	// add lib64 if exists
	if _, err := os.Stat("/lib64"); err == nil {
		spec.Mounts = append(spec.Mounts, specs.Mount{"/lib64", "bind", "/lib64", []string{"bind", "ro"}})
	}
	// add hosts
	hosts, _ := filepath.Abs(path.Join(launcher.workingDir, "etc", "hosts"))
	if _, err := os.Stat(hosts); err != nil {
		hosts = "/etc/hosts"
	}
	spec.Mounts = append(spec.Mounts, specs.Mount{path.Join("/etc", "hosts"), "bind", hosts, []string{"bind", "ro"}})
	// add resolv.conf
	resolvConf, _ := filepath.Abs(path.Join(launcher.workingDir, "etc", "resolv.conf"))
	if _, err := os.Stat(resolvConf); err != nil {
		resolvConf = "/etc/resolv.conf"
	}
	spec.Mounts = append(spec.Mounts, specs.Mount{path.Join("/etc", "resolv.conf"), "bind", resolvConf, []string{"bind", "ro"}})
	// add nsswitch.conf
	nsswitchConf, _ := filepath.Abs(path.Join(launcher.workingDir, "etc", "nsswitch.conf"))
	if _, err := os.Stat(nsswitchConf); err != nil {
		nsswitchConf = "/etc/nsswitch.conf"
	}
	spec.Mounts = append(spec.Mounts, specs.Mount{path.Join("/etc", "nsswitch.conf"), "bind", nsswitchConf, []string{"bind", "ro"}})

	// TODO: all services should have their own certificates
	// this mound for demo only and should be removed
	// mount /etc/ssl
	spec.Mounts = append(spec.Mounts, specs.Mount{path.Join("/etc", "ssl"), "bind", path.Join("/etc", "ssl"), []string{"bind", "ro"}})

	// add netns hook
	if spec.Hooks == nil {
		spec.Hooks = &specs.Hooks{}
	}
	spec.Hooks.Prestart = append(spec.Hooks.Prestart, specs.Hook{Path: launcher.netnsPath})

	return nil
}

func (launcher *Launcher) startService(serviceFile, serviceName string) (err error) {
	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	channel := make(chan string)

	launcher.services.Delete(serviceName)

	if _, err := launcher.systemd.StopUnit(serviceName, "replace", channel); err == nil {
		// stop already startes service
		log.WithField("name", serviceName).Debug("Already loaded. Stop it.")

		status := <-channel

		log.WithFields(log.Fields{"name": serviceName, "status": status}).Debug("Stop service")
	}

	if _, _, err := launcher.systemd.EnableUnitFiles([]string{serviceFile}, false, true); err != nil {
		return err
	}

	if err := launcher.systemd.Reload(); err != nil {
		return err
	}

	if _, err = launcher.systemd.StartUnit(serviceName, "replace", channel); err != nil {
		return err
	}
	status := <-channel

	log.WithFields(log.Fields{"name": serviceName, "status": status}).Debug("Start service")

	return nil
}

func (launcher *Launcher) stopService(serviceName string) (err error) {
	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

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
	execStopString := launcher.runcPath + " kill " + id + " SIGKILL"
	execStopPostString := launcher.runcPath + " delete -f " + id

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
