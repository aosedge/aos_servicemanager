package launcher

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-systemd/dbus"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/monitoring"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

var (
	systemdSubscribeBuffers  = 32
	systemdSubscribeInterval = 500 * time.Millisecond
)

/*******************************************************************************
 * Service related API
 ******************************************************************************/

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

func (launcher *Launcher) restartService(id, serviceName string) (err error) {
	channel := make(chan string)
	if _, err = launcher.systemd.RestartUnit(serviceName, "replace", channel); err != nil {
		return err
	}
	status := <-channel

	log.WithFields(log.Fields{"name": serviceName, "status": status}).Debug("Restart service")

	if err = launcher.updateServiceState(id, stateRunning, statusOk); err != nil {
		log.WithField("id", id).Warnf("Can't update service state: %s", err)
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

	if err = launcher.updateServiceState(id, stateRunning, statusOk); err != nil {
		log.WithField("id", id).Warnf("Can't update service state: %s", err)
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

func (launcher *Launcher) stopService(id, serviceName string) (err error) {
	launcher.services.Delete(serviceName)

	channel := make(chan string)
	if _, err := launcher.systemd.StopUnit(serviceName, "replace", channel); err != nil {
		return err
	}
	status := <-channel

	log.WithFields(log.Fields{"name": serviceName, "status": status}).Debug("Stop service")

	if err = launcher.updateServiceState(id, stateStopped, statusOk); err != nil {
		log.WithField("id", id).Warnf("Can't update service state: %s", err)
	}

	return nil
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

func (launcher *Launcher) installService(serviceInfo amqp.ServiceInfoFromCloud) (installDir string, err error) {

	log.WithFields(log.Fields{"id": serviceInfo.ID, "version": serviceInfo.Version}).Debug("Install service")

	// download and unpack
	installDir, err = launcher.downloadAndUnpackImage(serviceInfo)
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

	if err = launcher.restartService(newService.ID, newService.ServiceName); err != nil {
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

	if err := launcher.deleteUser(service.UserName); err != nil {
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
ExecStartPost=-${SETNETLIMIT}

ExecStop=-${CLEARNETLIMIT}
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

		if err = launcher.monitor.StartMonitorService(service.ID, monitoring.ServiceMonitoringConfig{
			Pid:           pid,
			IPAddress:     ipAddress,
			WorkingDir:    service.Path,
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
		setCmd = launcher.wonderShaperPath + " -a netnsv0-${MAINPID}" + setCmd
		clearCmd = launcher.wonderShaperPath + " -c -a netnsv0-${MAINPID}"

		log.Debugf("Set net limit cmd: %s", setCmd)
		log.Debugf("Clear net limit cmd: %s", clearCmd)
	}

	return setCmd, clearCmd
}
