// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"encoding/hex"
	"encoding/json"
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
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
	"golang.org/x/sys/unix"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/monitoring"
	"aos_servicemanager/networkmanager"
	"aos_servicemanager/platform"
	"aos_servicemanager/resourcemanager"
	"aos_servicemanager/utils/action"
	"aos_servicemanager/utils/imageutils"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

// OperationVersion defines current operation version
// IMPORTANT: if new functionality doesn't allow existing services to work
// properly, this value should be increased. It will force to remove all
// services and their storages before first start.
const OperationVersion = 6

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

const (
	serviceDir = "services" // services directory

	aosSecretEnv = "SERVICE_SECRET"

	ociRuntimeConfigFile = "config.json"
	ociImageConfigFile   = "image.json"
	aosServiceConfigFile = "service.json"
)

const (
	stateChannelSize = 32
)

const serviceTemplate = `# This is template file used to launch AOS services
# Known variables:
# * ${ID}            - service id
# * ${SERVICEPATH}   - path to service dir
# * ${RUNNER}        - path to runner
[Unit]
Description=AOS Service
After=network.target

[Service]
Type=forking
Restart=always
RestartSec=1
ExecStartPre=${RUNNER} delete -f ${ID}
ExecStart=${RUNNER} run -d --pid-file ${SERVICEPATH}/.pid -b ${SERVICEPATH} ${ID}

ExecStop=${RUNNER} kill ${ID} SIGKILL
ExecStopPost=${RUNNER} delete -f ${ID}
PIDFile=${SERVICEPATH}/.pid
SuccessExitStatus=SIGKILL

[Install]
WantedBy=multi-user.target
`

const serviceTemplateFile = "template.service"

const decryptedDirName = "decrypt"

const (
	hostfsWiteoutsDir = "hostfs/whiteouts"
)

const (
	serviceMergedDir      = "merged"
	serviceRootfsDir      = "rootfs"
	serviceMountPointsDir = "mounts"
)

const defaultServiceProvider = "default"

const errNotLoaded = "not loaded"

const ttlValidatePeriod = 1 * time.Minute

/*******************************************************************************
 * Vars
 ******************************************************************************/

var defaultHostfsBinds = []string{"bin", "sbin", "lib", "lib64", "usr"}

/*******************************************************************************
 * Types
 ******************************************************************************/

// Launcher instance
type Launcher struct {
	// NewStateChannel used to notify about new service state
	NewStateChannel chan NewState

	sender           Sender
	serviceProvider  ServiceProvider
	monitor          ServiceMonitor
	network          NetworkProvider
	serviceRegistrar ServiceRegistrar
	devicemanager    DeviceManagement
	systemd          *dbus.Conn
	config           *config.Config
	layerProvider    layerProvider

	envVarsProvider *envVarsProvider
	ttlStopChannel  chan bool

	actionHandler  *action.Handler
	storageHandler *storageHandler
	idsPool        *identifierPool

	ttlTicker *time.Ticker

	downloader downloader
	decryptDir string

	users []string

	services map[string]string

	serviceTemplate string
	runnerPath      string

	sync.Mutex
}

// Service describes service structure
type Service struct {
	ID              string        // service id
	AosVersion      uint64        // service aosVersion
	VendorVersion   string        // service vendorVersion
	ServiceProvider string        // service provider
	Path            string        // path to service bundle
	UnitName        string        // systemd unit name
	UID             uint32        // service userID
	GID             uint32        // service gid
	State           ServiceState  // service state
	Status          ServiceStatus // service status
	StartAt         time.Time     // time at which service was started
	AlertRules      string        // alert rules in json format
	Description     string        // service description
	ManifestDigest  []byte        // sha256 of service manifest
}

// UsersService describes users service structure
type UsersService struct {
	Users         []string // user claims
	ServiceID     string   // service id
	StorageFolder string   // service storage folder
	StateChecksum []byte   // service state checksum
}

// ServiceProvider provides API to create, remove or access services DB
type ServiceProvider interface {
	AddService(service Service) (err error)
	UpdateService(service Service) (err error)
	RemoveService(serviceID string) (err error)
	GetService(serviceID string) (service Service, err error)
	GetServices() (services []Service, err error)
	GetServiceProviderServices(serviceProvider string) (services []Service, err error)
	GetServiceByUnitName(unitName string) (service Service, err error)
	SetServiceStatus(serviceID string, status ServiceStatus) (err error)
	SetServiceState(serviceID string, state ServiceState) (err error)
	SetServiceStartTime(serviceID string, time time.Time) (err error)
	AddServiceToUsers(users []string, serviceID string) (err error)
	RemoveServiceFromUsers(users []string, serviceID string) (err error)
	GetUsersServices(users []string) (services []Service, err error)
	RemoveServiceFromAllUsers(serviceID string) (err error)
	GetUsersService(users []string, serviceID string) (userService UsersService, err error)
	GetUsersServicesByServiceID(serviceID string) (userServices []UsersService, err error)
	SetUsersStorageFolder(users []string, serviceID string, storageFolder string) (err error)
	SetUsersStateChecksum(users []string, serviceID string, checksum []byte) (err error)
	GetAllOverrideEnvVars() (vars []amqp.OverrideEnvsFromCloud, err error)
	UpdateOverrideEnvVars(subjects []string, serviceID string, vars []amqp.EnvVarInfo) (err error)
}

// ServiceRegistrar provides API to register/unregister service
type ServiceRegistrar interface {
	RegisterService(serviceID string, permissions map[string]map[string]string) (secret string, err error)
	UnregisterService(serviceID string) (err error)
}

// ServiceMonitor provides API to start/stop service monitoring
type ServiceMonitor interface {
	StartMonitorService(serviceID string, monitoringConfig monitoring.ServiceMonitoringConfig) (err error)
	StopMonitorService(serviceID string) (err error)
}

// Sender provides API to send messages to the cloud
type Sender interface {
	SendStateRequest(serviceID string, defaultState bool) (err error)
	SendOverrideEnvVarsStatus(envs []amqp.EnvVarInfoStatus) (err error)
}

// NetworkProvider provides network interface
type NetworkProvider interface {
	AddServiceToNetwork(serviceID, spID string, params networkmanager.NetworkParams) (err error)
	RemoveServiceFromNetwork(serviceID, spID string) (err error)
	IsServiceInNetwork(serviceID, spID string) (err error)
	GetServiceIP(serviceID, spID string) (ip string, err error)
	DeleteNetwork(spID string) (err error)
}

// DeviceManagement provides API to validate, request and release devices
type DeviceManagement interface {
	RequestDeviceResourceByName(name string) (deviceResource resourcemanager.DeviceResource, err error)
	RequestDevice(device string, serviceID string) (err error)
	ReleaseDevice(device string, serviceID string) (err error)
	RequestBoardResourceByName(name string) (boardResource resourcemanager.BoardResource, err error)
}

// NewState new state message
type NewState struct {
	CorrelationID string
	ServiceID     string
	State         string
	Checksum      string
}

// ServiceStatus service status
type ServiceStatus int

// ServiceState service state
type ServiceState int

type actionType int

type downloader interface {
	DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
		chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error)
}

type stateAcceptance struct {
	correlationID string
	acceptance    amqp.StateAcceptance
}

type layerProvider interface {
	GetLayerPathByDigest(layerDigest string) (layerPath string, err error)
}

type installServiceInfo struct {
	serviceDetails amqp.ServiceInfoFromCloud
	chains         []amqp.CertificateChain
	certs          []amqp.Certificate
	statusSender   statusSender
}

type statusSender chan amqp.ServiceInfo

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new launcher object
func New(config *config.Config, downloader downloader, sender Sender, serviceProvider ServiceProvider,
	layerProvider layerProvider, monitor ServiceMonitor, network NetworkProvider, devicemanager DeviceManagement, serviceRegistrar ServiceRegistrar) (launcher *Launcher, err error) {
	log.WithField("runner", config.Runner).Debug("New launcher")

	launcher = &Launcher{
		config:           config,
		downloader:       downloader,
		sender:           sender,
		serviceProvider:  serviceProvider,
		layerProvider:    layerProvider,
		monitor:          monitor,
		network:          network,
		devicemanager:    devicemanager,
		services:         make(map[string]string),
		serviceRegistrar: serviceRegistrar,
		idsPool:          &identifierPool{},
		decryptDir:       path.Join(config.WorkingDir, decryptedDirName),
	}

	launcher.NewStateChannel = make(chan NewState, stateChannelSize)

	launcher.ttlStopChannel = make(chan bool, 1)

	if launcher.actionHandler, err = action.New(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if launcher.storageHandler, err = newStorageHandler(config.StorageDir, serviceProvider,
		launcher.NewStateChannel, sender); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	// Check and create service dir
	dir := path.Join(config.WorkingDir, serviceDir)
	if _, err = os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return nil, aoserrors.Wrap(err)
		}
		if err = os.MkdirAll(dir, 0755); err != nil {
			return nil, aoserrors.Wrap(err)
		}
	}

	// Create systemd connection
	launcher.systemd, err = dbus.NewSystemConnection()
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	// Get systemd service template
	launcher.serviceTemplate, err = getSystemdServiceTemplate(config.WorkingDir)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	// Retrieve runner abs path
	launcher.runnerPath, err = exec.LookPath(config.Runner)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if launcher.envVarsProvider, err = createEnvVarsProvider(launcher.serviceProvider, launcher.sender); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if err = launcher.prepareHostfsDir(); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	// Create storage dir
	if err = os.MkdirAll(launcher.config.StorageDir, 0755); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	services, err := launcher.serviceProvider.GetServices()
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	for _, service := range services {
		if err = launcher.idsPool.add(service.UID, service.GID); err != nil {
			log.Errorf("Can't add service UID/GID to pool: %s", err)
		}
	}

	os.RemoveAll(launcher.decryptDir)

	return launcher, nil
}

// Close closes launcher
func (launcher *Launcher) Close() {
	log.Debug("Close launcher")

	launcher.StopServices()

	launcher.systemd.Close()

	launcher.storageHandler.Close()

	close(launcher.ttlStopChannel)
}

// GetServiceVersion returns installed version of requested service
func (launcher *Launcher) GetServiceVersion(id string) (version uint64, err error) {
	log.WithField("id", id).Debug("Get service version")

	service, err := launcher.serviceProvider.GetService(id)
	if err != nil {
		return version, aoserrors.Wrap(err)
	}

	version = service.AosVersion

	return version, nil
}

// InstallService installs and runs service
func (launcher *Launcher) InstallService(serviceInfo amqp.ServiceInfoFromCloud,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (statusChannel <-chan amqp.ServiceInfo) {
	statusSender := make(statusSender, 1)

	info := installServiceInfo{
		serviceDetails: serviceInfo,
		chains:         chains,
		certs:          certs,
		statusSender:   statusSender,
	}

	info.statusSender.sendStatus(info.serviceDetails.ID, info.serviceDetails.AosVersion, amqp.PendingStatus, "", "")

	launcher.actionHandler.PutInQueue(serviceInfo.ID, info, launcher.doActionInstall)

	return statusSender
}

// UninstallService stops and removes service
func (launcher *Launcher) UninstallService(id string) (statusChannel <-chan amqp.ServiceInfo) {
	statusSender := make(statusSender, 1)

	launcher.actionHandler.PutInQueue(id, statusSender, launcher.doActionUninstall)

	return statusSender
}

// CheckServicesConsistency checks if service folders exist
func (launcher *Launcher) CheckServicesConsistency() (err error) {
	//Check for storage folder
	if _, err = os.Stat(launcher.config.StorageDir); err != nil {
		log.Error("Can't find storagedir")
		return aoserrors.Wrap(err)
	}

	services, err := launcher.serviceProvider.GetServices()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, service := range services {
		if err := launcher.isServiceValid(service); err != nil {
			log.WithField("id", service.ID).Errorf("Service is invalid: %s", err.Error())

			//try to remove only corrupted service
			if err := launcher.removeService(service); err != nil {
				return aoserrors.Wrap(err)
			}
		}
	}

	return nil
}

// GetServicesInfo returns information about all installed services
func (launcher *Launcher) GetServicesInfo() (info []amqp.ServiceInfo, err error) {
	log.Debug("Get services info")

	services, err := launcher.serviceProvider.GetUsersServices(launcher.users)
	if err != nil {
		return info, aoserrors.Wrap(err)
	}

	info = make([]amqp.ServiceInfo, len(services))

	for i, service := range services {
		info[i] = amqp.ServiceInfo{ID: service.ID, AosVersion: service.AosVersion, Status: service.Status.String()}

		userService, err := launcher.serviceProvider.GetUsersService(launcher.users, service.ID)
		if err != nil {
			return info, aoserrors.Wrap(err)
		}

		aosConfig, err := getAosServiceConfig(path.Join(service.Path, aosServiceConfigFile))
		if err != nil {
			return info, aoserrors.Wrap(err)
		}

		if aosConfig.GetStateLimit() != 0 {
			info[i].StateChecksum = hex.EncodeToString(userService.StateChecksum)
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

	if err = launcher.addUserServicesToSystemd(users); err != nil {
		return aoserrors.Wrap(err)
	}

	launcher.StopServices()

	launcher.users = users

	launcher.StartServices()

	if err = launcher.cleanServicesDB(); err != nil {
		log.Errorf("Error cleaning DB: %s", err)
	}

	go launcher.validateTTLs()

	return nil
}

// RemoveAllServices removing all services
func (launcher *Launcher) RemoveAllServices() (err error) {
	services, err := launcher.serviceProvider.GetServices()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	statusChannel := make(chan error, len(services))

	for _, service := range services {
		launcher.actionHandler.PutInQueue(service.ID, service,
			func(id string, data interface{}) {
				service, ok := data.(Service)
				if !ok {
					statusChannel <- aoserrors.New("wrong data type")
					return
				}

				if err = launcher.removeService(service); err != nil {
					log.Errorf("Can't remove service %s: %s", service.ID, err)
				}

				statusChannel <- err
			})
	}

	// Wait all services are deleted
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}

	err = launcher.systemd.Reload()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	services, err = launcher.serviceProvider.GetServices()
	if err != nil {
		return aoserrors.Wrap(err)
	}
	if len(services) != 0 {
		return aoserrors.New("can't remove all services")
	}

	return aoserrors.Wrap(err)
}

// StateAcceptance notifies launcher about new state acceptance
func (launcher *Launcher) StateAcceptance(acceptance amqp.StateAcceptance, correlationID string) {
	launcher.actionHandler.PutInQueue(acceptance.ServiceID,
		stateAcceptance{correlationID, acceptance}, launcher.doStateAcceptance)
}

// UpdateState updates service state
func (launcher *Launcher) UpdateState(state amqp.UpdateState) {
	launcher.actionHandler.PutInQueue(state.ServiceID, state, launcher.doUpdateState)
}

// Cleanup deletes all AOS services, their storages and states
func Cleanup(cfg *config.Config) (err error) {
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

					if _, err := systemd.DisableUnitFiles([]string{serviceName}, true); err != nil {
						log.WithField("name", serviceName).Error("Can't disable unit: ", err)
					}
				}
			}
		}

		if err := systemd.Reload(); err != nil {
			log.Errorf("Can't reload systemd: %s", err)
		}
	}

	serviceDir := path.Join(cfg.WorkingDir, serviceDir)

	log.WithField("dir", serviceDir).Debug("Remove service dir")

	if err := os.RemoveAll(serviceDir); err != nil {
		log.Fatalf("Can't remove service folder: %s", err)
	}

	log.WithField("dir", cfg.StorageDir).Debug("Remove storage dir")

	if err := os.RemoveAll(cfg.StorageDir); err != nil {
		log.Fatalf("Can't remove storage folder: %s", err)
	}

	if err := os.RemoveAll(path.Join(cfg.WorkingDir, serviceTemplateFile)); err != nil {
		log.Fatalf("Can't remove service template file: %s", err)
	}

	if err := os.RemoveAll(cfg.LayersDir); err != nil {
		log.Errorf("Can't cleanup layers: %s", err)
	}

	return nil
}

// StartServices starts current users services
func (launcher *Launcher) StartServices() {
	log.WithField("users", launcher.users).Debug("Start user services")

	services, err := launcher.serviceProvider.GetUsersServices(launcher.users)
	if err != nil {
		log.Errorf("Can't start services: %s", err)
		return
	}

	launcher.startServices(services)
}

// StopServices stops current users services
func (launcher *Launcher) StopServices() {
	log.WithField("users", launcher.users).Debug("Stop user services")

	var services []Service
	var err error

	if launcher.users == nil {
		services, err = launcher.serviceProvider.GetServices()
		if err != nil {
			log.Errorf("Can't stop services: %s", err)
			return
		}
	} else {
		services, err = launcher.serviceProvider.GetUsersServices(launcher.users)
		if err != nil {
			log.Errorf("Can't stop services: %s", err)
			return
		}
	}

	launcher.stopServices(services)
}

// GetServicePermissions returns service permissions
func (launcher *Launcher) GetServicePermissions(serviceID string) (permission string, err error) {
	service, err := launcher.serviceProvider.GetService(serviceID)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	aosConfig, err := getAosServiceConfig(path.Join(service.Path, aosServiceConfigFile))
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	// TODO: delete this functionality after adding a functional vis server
	jsonPermissions, err := json.Marshal(aosConfig.Permissions)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	return string(jsonPermissions), nil
}

// ProcessDesiredEnvVarsList override env vars fore services
func (launcher *Launcher) ProcessDesiredEnvVarsList(envVars amqp.DecodedOverrideEnvVars) (err error) {
	subjectServiceToRestart, err := launcher.envVarsProvider.processOverrideEnvVars(envVars.OverrideEnvVars)
	if err != nil {
		return err
	}

	launcher.restartServicesBySubjectServiceID(subjectServiceToRestart)

	return nil
}

func (state ServiceState) String() string {
	return [...]string{"Init", "Running", "Stopped"}[state]
}

func (status ServiceStatus) String() string {
	return [...]string{"installed", "Error"}[status]
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (launcher *Launcher) startServices(services []Service) {
	var err error
	statusChannel := make(chan error, len(services))

	// Start all services in parallel
	for _, service := range services {
		launcher.actionHandler.PutInQueue(service.ID, service,
			func(id string, data interface{}) {
				service, ok := data.(Service)
				if !ok {
					statusChannel <- aoserrors.New("wrong data type")
					return
				}

				if err = launcher.startService(service); err != nil {
					log.Errorf("Can't start service %s: %s", service.ID, err)
				}

				statusChannel <- err
			})
	}

	// Wait all services are started
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}
}

func (launcher *Launcher) stopServices(services []Service) {
	var err error
	statusChannel := make(chan error, len(services))

	// Stop all services in parallel
	for _, service := range services {
		launcher.actionHandler.PutInQueue(service.ID, service,
			func(id string, data interface{}) {
				service, ok := data.(Service)
				if !ok {
					statusChannel <- aoserrors.New("wrong data type")
					return
				}

				if err = launcher.stopService(service); err != nil {
					log.Errorf("Can't stop service %s: %s", service.ID, err)
				}

				statusChannel <- err
			})
	}

	// Wait all services are stopped
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}
}

func (launcher *Launcher) restartServicesBySubjectServiceID(subjectServiceToRestart []subjectServicePair) {
	servicesToRestart := []Service{}

	for _, value := range subjectServiceToRestart {
		if launcher.isSubjectActive(value.subjectID) {
			service, err := launcher.serviceProvider.GetService(value.serviseID)
			if err != nil {
				log.Errorf("Service %s doesn't present in the system, err: %s", value.serviseID, err.Error())
				continue
			}

			servicesToRestart = append(servicesToRestart, service)
		}
	}

	if len(servicesToRestart) == 0 {
		return
	}

	launcher.stopServices(servicesToRestart)
	launcher.startServices(servicesToRestart)
}

func (launcher *Launcher) addUserServicesToSystemd(users []string) (err error) {
	services, err := launcher.serviceProvider.GetUsersServices(users)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, service := range services {
		fileName, err := filepath.Abs(path.Join(service.Path, service.UnitName))
		if err != nil {
			return aoserrors.Wrap(err)
		}

		if _, err = launcher.systemd.LinkUnitFiles([]string{fileName}, true, true); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if err = launcher.systemd.Reload(); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) prepareHostfsDir() (err error) {
	witeoutsDir := path.Join(launcher.config.WorkingDir, hostfsWiteoutsDir)

	if err = os.MkdirAll(witeoutsDir, 0755); err != nil {
		return aoserrors.Wrap(err)
	}

	allowedDirs := defaultHostfsBinds

	if len(launcher.config.HostBinds) > 0 {
		allowedDirs = launcher.config.HostBinds
	}

	rootContent, err := ioutil.ReadDir("/")
	if err != nil {
		return aoserrors.Wrap(err)
	}

	for _, item := range rootContent {
		itemPath := path.Join(witeoutsDir, item.Name())

		if _, err = os.Stat(itemPath); err == nil {
			// skip already exists items
			continue
		}

		if !os.IsNotExist(err) {
			return aoserrors.Wrap(err)
		}

		allowed := false

		for _, allowedItem := range allowedDirs {
			if item.Name() == allowedItem {
				allowed = true

				break
			}
		}

		if allowed {
			continue
		}

		// Create whiteout for not allowed items
		if err = syscall.Mknod(itemPath, syscall.S_IFCHR, int(unix.Mkdev(0, 0))); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
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

func (sender *statusSender) sendStatus(id string, aosVersion uint64, status, errStr, stateCheckSum string) {
	*sender <- amqp.ServiceInfo{
		ID:            id,
		AosVersion:    aosVersion,
		Status:        status,
		Error:         errStr,
		StateChecksum: stateCheckSum,
	}
}

func (launcher *Launcher) doActionInstall(id string, data interface{}) {
	var err error

	installInfo := data.(installServiceInfo)

	log.WithFields(log.Fields{
		"id":         installInfo.serviceDetails.ID,
		"aosVersion": installInfo.serviceDetails.AosVersion}).Debug("Install service")

	defer func() {
		if err != nil {
			log.WithFields(log.Fields{
				"id":         installInfo.serviceDetails.ID,
				"aosVersion": installInfo.serviceDetails.AosVersion}).Errorf("Can't install service: %s", err)

			installInfo.statusSender.sendStatus(installInfo.serviceDetails.ID, installInfo.serviceDetails.AosVersion,
				amqp.ErrorStatus, err.Error(), "")
		}

		close(installInfo.statusSender)
	}()

	if err = launcher.installService(installInfo); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	var userService UsersService

	if userService, err = launcher.serviceProvider.GetUsersService(launcher.users, id); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	installInfo.statusSender.sendStatus(installInfo.serviceDetails.ID, installInfo.serviceDetails.AosVersion,
		amqp.InstalledStatus, "", hex.EncodeToString(userService.StateChecksum))

	log.WithFields(log.Fields{
		"id":         installInfo.serviceDetails.ID,
		"aosVersion": installInfo.serviceDetails.AosVersion}).Info("Service successfully installed")
}

func (launcher *Launcher) doActionUninstall(id string, data interface{}) {
	var (
		err     error
		service Service
	)

	statusSender := data.(statusSender)

	log.WithFields(log.Fields{"id": id}).Debug("Uninstall service")

	defer func() {
		if err != nil {
			log.WithFields(log.Fields{"id": id}).Errorf("Can't uninstall service: %s", err)

			statusSender.sendStatus(id, service.AosVersion, amqp.ErrorStatus, err.Error(), "")
		}

		close(statusSender)
	}()

	if service, err = launcher.serviceProvider.GetService(id); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	statusSender.sendStatus(id, service.AosVersion, amqp.RemovingStatus, "", "")

	if err = launcher.uninstallService(service); err != nil {
		err = aoserrors.Wrap(err)
		return
	}

	statusSender.sendStatus(id, service.AosVersion, amqp.RemovedStatus, "", "")

	log.WithFields(log.Fields{"id": id}).Info("Service successfully uninstalled")
}

func (launcher *Launcher) installService(installInfo installServiceInfo) (err error) {
	if launcher.users == nil {
		return aoserrors.New("users are not set")
	}

	service, err := launcher.serviceProvider.GetService(installInfo.serviceDetails.ID)
	if err != nil && !strings.Contains(err.Error(), "not exist") {
		return aoserrors.Wrap(err)
	}
	serviceExists := err == nil

	// Skip incorrect version
	if serviceExists && installInfo.serviceDetails.AosVersion < service.AosVersion {
		return aoserrors.New("version mistmatch")
	}

	// If same service version exists, just start the service
	if serviceExists && installInfo.serviceDetails.AosVersion == service.AosVersion {
		if err = launcher.addServiceToCurrentUsers(installInfo.serviceDetails.ID); err != nil {
			return aoserrors.Wrap(err)
		}

		if err = launcher.startService(service); err != nil {
			return aoserrors.Wrap(err)
		}

		return nil
	}

	installInfo.statusSender.sendStatus(installInfo.serviceDetails.ID, installInfo.serviceDetails.AosVersion,
		amqp.DownloadingStatus, "", "")

	unpackDir, err := ioutil.TempDir("", "aos_")
	defer os.RemoveAll(unpackDir)

	decryptData := amqp.DecryptDataStruct{URLs: installInfo.serviceDetails.URLs,
		Sha256:         installInfo.serviceDetails.Sha256,
		Sha512:         installInfo.serviceDetails.Sha512,
		Size:           installInfo.serviceDetails.Size,
		DecryptionInfo: installInfo.serviceDetails.DecryptionInfo,
		Signs:          installInfo.serviceDetails.Signs}

	// download and unpack
	image, err := launcher.downloader.DownloadAndDecrypt(decryptData, installInfo.chains, installInfo.certs, launcher.decryptDir)
	if image != "" {
		defer os.Remove(image)
	}
	if err != nil {
		return aoserrors.Wrap(err)
	}

	installInfo.statusSender.sendStatus(installInfo.serviceDetails.ID, installInfo.serviceDetails.AosVersion,
		amqp.InstallingStatus, "", "")

	if err = imageutils.UnpackTarImage(image, unpackDir); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = validateUnpackedImage(unpackDir); err != nil {
		return aoserrors.Wrap(err)
	}

	servicePath := path.Join(launcher.config.WorkingDir, serviceDir)
	// Create services dir if needed
	if err = os.MkdirAll(servicePath, 0755); err != nil {
		return aoserrors.Wrap(err)
	}

	// We need to install or update the service

	// create install dir
	installDir, err := ioutil.TempDir(servicePath, "")
	if err != nil {
		return aoserrors.Wrap(err)
	}

	defer func() {
		if err != nil {
			// Remove install dir if exists
			if _, err := os.Stat(installDir); err == nil {
				if err := os.RemoveAll(installDir); err != nil {
					log.WithField("serviceID", installInfo.serviceDetails.ID).Errorf("Can't remove service dir: %s", err)
				}
			}
		}
	}()

	log.WithFields(log.Fields{"dir": installDir, "serviceID": installInfo.serviceDetails.ID}).Debug("Create install dir")

	newService, err := launcher.prepareService(unpackDir, installDir, installInfo.serviceDetails, serviceExists, service)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if !serviceExists {
		if err = launcher.addService(newService); err != nil {
			return aoserrors.Wrap(err)
		}
	} else {
		if err = launcher.updateService(service, newService, installInfo.statusSender); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (launcher *Launcher) uninstallService(service Service) (err error) {
	if launcher.users == nil {
		return aoserrors.New("users are not set")
	}

	if err := launcher.stopService(service); err != nil {
		return aoserrors.Wrap(err)
	}

	userService, err := launcher.serviceProvider.GetUsersService(launcher.users, service.ID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if userService.StorageFolder != "" {
		log.WithFields(log.Fields{
			"folder":    userService.StorageFolder,
			"serviceID": service.ID}).Debug("Remove storage folder")

		if err = os.RemoveAll(userService.StorageFolder); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if err = launcher.serviceProvider.RemoveServiceFromUsers(launcher.users, service.ID); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
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

	aosConfig, err := getAosServiceConfig(path.Join(service.Path, aosServiceConfigFile))
	if err != nil {
		log.Errorf("Can't get aos service config: %s", err)
		return
	}

	if err = launcher.storageHandler.UpdateState(launcher.users, service, state.State, state.Checksum,
		aosConfig.GetStateLimit()); err != nil {
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

func (launcher *Launcher) updateServiceState(id string, state ServiceState, status ServiceStatus) (err error) {
	service, err := launcher.serviceProvider.GetService(id)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if service.State != state {
		log.WithField("id", id).Debugf("Set service state: %s", state)

		if err = launcher.serviceProvider.SetServiceState(id, state); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if service.Status != status {
		log.WithField("id", id).Debugf("Set service status: %s", status)

		if err = launcher.serviceProvider.SetServiceStatus(id, status); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (launcher *Launcher) mountRootfs(service Service, storageFolder string, layers []string) (err error) {
	mergedDir := path.Join(service.Path, serviceMergedDir)

	// create merged dir
	if err = os.MkdirAll(mergedDir, 0755); err != nil {
		return aoserrors.Wrap(err)
	}

	upperDir, workDir := "", ""

	if storageFolder != "" {
		upperDir = path.Join(storageFolder, upperDirName)
		workDir = path.Join(storageFolder, workDirName)
	}

	log.WithFields(log.Fields{"path": mergedDir, "id": service.ID}).Debug("Mount service rootfs")

	layerDirs := []string{path.Join(service.Path, serviceMountPointsDir), path.Join(service.Path, serviceRootfsDir)}
	layerDirs = append(layerDirs, layers...)
	layerDirs = append(layerDirs, path.Join(launcher.config.WorkingDir, hostfsWiteoutsDir))
	layerDirs = append(layerDirs, string("/"))

	if err = overlayMount(mergedDir, layerDirs, workDir, upperDir); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) umountRootfs(service Service) (err error) {
	mergedDir := path.Join(service.Path, serviceMergedDir)

	log.WithFields(log.Fields{"path": mergedDir, "id": service.ID}).Debug("Unmount service rootfs")

	if err = umountWithRetry(mergedDir, 0); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) applyDevicesAndResources(spec *serviceSpec, service Service,
	aosSrvConf *aosServiceConfig) (err error) {
	// Update Devices in spec
	if err = launcher.setDevices(spec, aosSrvConf.Devices); err != nil {
		return aoserrors.Wrap(err)
	}

	// Update Resources in spec
	if err = launcher.setServiceResources(spec, aosSrvConf.Resources); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) prepareServiceRootfs(spec *serviceSpec, service Service,
	aosSrvConf *aosServiceConfig) (err error) {
	if err = spec.bindHostDirs(launcher.config.WorkingDir); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = spec.setRootfs(serviceMergedDir); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.createMountPoints(service.Path, spec); err != nil {
		return aoserrors.Wrap(err)
	}

	storageFolder, err := launcher.storageHandler.PrepareStorageFolder(launcher.users, service,
		aosSrvConf.GetStorageLimit(), aosSrvConf.GetStateLimit())
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if aosSrvConf.GetStateLimit() > 0 {
		if err = spec.addBindMount(path.Join(storageFolder, stateFile), path.Join("/", stateFile), "rw"); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	imageParts, err := getImageParts(service.Path)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	layers := make([]string, 0, len(imageParts.layersDigest))

	for _, layerDigest := range imageParts.layersDigest {
		layerPath, err := launcher.layerProvider.GetLayerPathByDigest(layerDigest)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		layers = append(layers, layerPath)
	}

	if err = launcher.mountRootfs(service, storageFolder, layers); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) applyNetworkSettings(spec *serviceSpec, service Service,
	aosSrvConf *aosServiceConfig, imageSpec *imagespec.Image) (err error) {
	networkFiles := []string{"/etc/hosts", "/etc/resolv.conf"}

	if netNsPath := networkmanager.GetNetNsPathByName(service.ID); netNsPath != "" {
		for i, ns := range spec.ocSpec.Linux.Namespaces {
			switch ns.Type {
			case runtimespec.NetworkNamespace:
				spec.ocSpec.Linux.Namespaces[i].Path = netNsPath
			}
		}
	}

	params := networkmanager.NetworkParams{
		HostsFilePath:      path.Join(service.Path, serviceMountPointsDir, networkFiles[0]),
		ResolvConfFilePath: path.Join(service.Path, serviceMountPointsDir, networkFiles[1]),
	}

	if aosSrvConf.Quotas.DownloadSpeed != nil {
		params.IngressKbit = *aosSrvConf.Quotas.DownloadSpeed
	}

	if aosSrvConf.Quotas.UploadSpeed != nil {
		params.EgressKbit = *aosSrvConf.Quotas.UploadSpeed
	}

	if aosSrvConf.Quotas.UploadLimit != nil {
		params.UploadLimit = *aosSrvConf.Quotas.UploadLimit
	}

	if aosSrvConf.Quotas.DownloadLimit != nil {
		params.DownloadLimit = *aosSrvConf.Quotas.DownloadLimit
	}

	if aosSrvConf.Hostname != nil {
		params.Hostname = *aosSrvConf.Hostname
	}

	params.ExposedPorts = make([]string, 0, len(imageSpec.Config.ExposedPorts))
	for key := range imageSpec.Config.ExposedPorts {
		params.ExposedPorts = append(params.ExposedPorts, key)
	}

	params.AllowedConnections = make([]string, 0, len(aosSrvConf.AllowedConnections))
	for key := range aosSrvConf.AllowedConnections {
		params.AllowedConnections = append(params.AllowedConnections, key)
	}

	if aosSrvConf.Hostname != nil {
		params.Hostname = *aosSrvConf.Hostname
	}

	if params.Hosts, err = launcher.getHostsFromResources(aosSrvConf.Resources); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.network.AddServiceToNetwork(service.ID, service.ServiceProvider, params); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) registerService(spec *serviceSpec, service Service,
	aosSrvConf *aosServiceConfig) (err error) {
	if aosSrvConf.Permissions == nil {
		return nil
	}

	secret, err := launcher.serviceRegistrar.RegisterService(service.ID, aosSrvConf.Permissions)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	spec.mergeEnv([]string{aosSecretEnv + "=" + secret})

	return nil
}

func (launcher *Launcher) unregisterService(service Service, aosSrvConf *aosServiceConfig) (err error) {
	if aosSrvConf.Permissions == nil {
		return nil
	}

	if err := launcher.serviceRegistrar.UnregisterService(service.ID); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) overrideEnvVars(spec *serviceSpec, service Service) (err error) {
	currentSubject := launcher.users[0] //TODO: currently supported only one user

	vars, err := launcher.envVarsProvider.getEnvVars(subjectServicePair{subjectID: currentSubject,
		serviseID: service.ID})
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if len(vars) == 0 {
		return nil
	}

	envVars := []string{}
	for _, oneVar := range vars {
		envVars = append(envVars, oneVar.Variable)
	}

	spec.mergeEnv(envVars)

	return nil
}

func (launcher *Launcher) prestartService(service Service, aosConfig *aosServiceConfig) (err error) {
	err = validateImageManifest(service)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	imageSpec, err := getImageSpecFromImageConfig(path.Join(service.Path, ociImageConfigFile))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	// generate config.json
	spec, err := generateRuntimeSpec(imageSpec, path.Join(service.Path, ociRuntimeConfigFile))
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer func() {
		if specErr := spec.save(); specErr != nil {
			if err == nil {
				err = specErr
			}
		}
	}()

	spec.setUserUIDGID(service.UID, service.GID)

	if err = spec.applyAosServiceConfig(aosConfig); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := launcher.applyDevicesAndResources(spec, service, aosConfig); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := launcher.registerService(spec, service, aosConfig); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := launcher.overrideEnvVars(spec, service); err != nil {
		return aoserrors.Wrap(err)
	}

	if err := launcher.prepareServiceRootfs(spec, service, aosConfig); err != nil {
		return aoserrors.Wrap(err)
	}

	if launcher.network != nil {
		if err = launcher.applyNetworkSettings(spec, service, aosConfig, &imageSpec); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	if err = launcher.requestDeviceResources(service, aosConfig.Devices); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) addServiceToSystemd(service Service) (err error) {
	fileName, err := filepath.Abs(path.Join(service.Path, service.UnitName))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = launcher.systemd.LinkUnitFiles([]string{fileName}, true, true); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.systemd.Reload(); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) requestDeviceResources(service Service, devices []Device) (err error) {
	for _, device := range devices {
		log.Debugf("Request device %s, for %s service", device.Name, service.ID)

		if err = launcher.devicemanager.RequestDevice(device.Name, service.ID); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (launcher *Launcher) startService(service Service) (err error) {
	if _, ok := launcher.services[service.ID]; ok {
		log.WithFields(log.Fields{"name": service.UnitName}).Warn("Service already started")

		return nil
	}

	aosConfig, err := getAosServiceConfig(path.Join(service.Path, aosServiceConfigFile))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.prestartService(service, &aosConfig); err != nil {
		return aoserrors.Wrap(err)
	}

	channel := make(chan string)
	if _, err = launcher.systemd.StartUnit(service.UnitName, "replace", channel); err != nil {
		return aoserrors.Wrap(err)
	}
	status := <-channel

	log.WithFields(log.Fields{"name": service.UnitName, "status": status}).Debug("Start service")

	if launcher.monitor != nil && !reflect.ValueOf(launcher.monitor).IsNil() {
		if err = launcher.updateMonitoring(service, stateRunning, &aosConfig); err != nil {
			log.WithField("id", service.ID).Error("Can't update monitoring: ", err)
		}
	}

	if err = launcher.updateServiceState(service.ID, stateRunning, statusOk); err != nil {
		log.WithField("id", service.ID).Warnf("Can't update service state: %s", err)
	}

	if err = launcher.serviceProvider.SetServiceStartTime(service.ID, time.Now()); err != nil {
		log.WithField("id", service.ID).Warnf("Can't set service start time: %s", err)
	}

	launcher.services[service.ID] = service.UnitName

	return nil
}

func (launcher *Launcher) releaseDeviceResources(service Service, devices []Device) (err error) {
	for _, device := range devices {
		log.Debugf("Release device %s, for %s service", device.Name, service.ID)

		if err = launcher.devicemanager.ReleaseDevice(device.Name, service.ID); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (launcher *Launcher) poststopService(service Service, aosConfig *aosServiceConfig) (retErr error) {
	if err := launcher.umountRootfs(service); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't umount rootfs: %s", err)
			retErr = err
		}
	}

	if err := launcher.storageHandler.StopStateWatching(launcher.users, service, aosConfig.GetStateLimit()); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't stop state watching: %s", err)
			retErr = err
		}
	}

	if err := launcher.releaseDeviceResources(service, aosConfig.Devices); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't release devices: %s", err)
			retErr = err
		}
	}

	if err := launcher.unregisterService(service, aosConfig); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't unregister service: %s", err)
			retErr = err
		}
	}

	if launcher.network != nil {
		if err := launcher.network.IsServiceInNetwork(service.ID, service.ServiceProvider); err == nil {
			if err := launcher.network.RemoveServiceFromNetwork(
				service.ID, service.ServiceProvider); err != nil && !strings.Contains(err.Error(), "not found") {
				if retErr == nil {
					log.WithField("id", service.ID).Errorf("Can't remove service from network: %s", err)
					retErr = err
				}
			}
		}
	}

	return aoserrors.Wrap(retErr)
}

func (launcher *Launcher) stopService(service Service) (retErr error) {
	aosConfig, err := getAosServiceConfig(path.Join(service.Path, aosServiceConfigFile))
	if err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't get service config: %s", err)
			retErr = err
		}
	}

	channel := make(chan string)
	if _, err := launcher.systemd.StopUnit(service.UnitName, "replace", channel); err != nil {
		if strings.Contains(err.Error(), errNotLoaded) {
			log.WithField("id", service.ID).Warn("Service not loaded")
		} else {
			log.WithField("id", service.ID).Errorf("Can't stop systemd unit: %s", err)
			retErr = err
		}
	} else {
		status := <-channel
		log.WithFields(log.Fields{"id": service.ID, "status": status}).Debug("Stop service")
	}

	if err := launcher.poststopService(service, &aosConfig); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't perform post stop: %s", err)
			retErr = err
		}
	}

	if launcher.monitor != nil && !reflect.ValueOf(launcher.monitor).IsNil() {
		if err = launcher.updateMonitoring(service, stateStopped, &aosConfig); err != nil {
			log.WithField("id", service.ID).Error("Can't update monitoring: ", err)
		}
	}

	if err := launcher.updateServiceState(service.ID, stateStopped, statusOk); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't update service state: %s", err)
			retErr = err
		}
	}

	delete(launcher.services, service.ID)

	return aoserrors.Wrap(retErr)
}

func (launcher *Launcher) restoreService(service Service) (retErr error) {
	log.WithField("id", service.ID).Warn("Restore previous service version")

	if err := launcher.serviceProvider.UpdateService(service); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't update service in DB: %s", err)
			retErr = err
		}
	}

	aosConfig, err := getAosServiceConfig(path.Join(service.Path, aosServiceConfigFile))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err := platform.SetUserFSQuota(launcher.config.StorageDir, aosConfig.GetStorageLimit(),
		service.UID, service.GID); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't set user FS quoate: %s", err)
			retErr = err
		}
	}

	if err := launcher.addServiceToSystemd(service); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't add service to systemd: %s", err)
			retErr = err
		}
	}

	if err := launcher.startService(service); err != nil {
		if retErr == nil {
			log.WithField("id", service.ID).Errorf("Can't install service: %s", err)
			retErr = err
		}
	}

	return aoserrors.Wrap(retErr)
}

func (launcher *Launcher) createMountPoints(serviceDir string, spec *serviceSpec) (err error) {
	mountPointsDir := path.Join(serviceDir, serviceMountPointsDir)

	if err = os.MkdirAll(mountPointsDir, 0755); err != nil {
		return aoserrors.Wrap(err)
	}

	for _, mount := range spec.ocSpec.Mounts {

		var permissions uint64

		for _, option := range mount.Options {
			nameValue := strings.Split(strings.TrimSpace(option), "=")

			if len(nameValue) > 1 && nameValue[0] == "mode" {
				if permissions, err = strconv.ParseUint(nameValue[1], 8, 32); err != nil {
					return aoserrors.Wrap(err)
				}
			}
		}

		itemPath := path.Join(mountPointsDir, mount.Destination)

		switch mount.Type {
		case "proc", "tmpfs", "sysfs":
			if err = os.MkdirAll(itemPath, 0755); err != nil {
				return aoserrors.Wrap(err)
			}

			if permissions != 0 {
				if err = os.Chmod(itemPath, os.FileMode(permissions)); err != nil {
					return aoserrors.Wrap(err)
				}
			}

		case "bind":
			stat, err := os.Stat(mount.Source)
			if err != nil {
				return aoserrors.Wrap(err)
			}

			if stat.IsDir() {
				if err = os.MkdirAll(itemPath, 0755); err != nil {
					return aoserrors.Wrap(err)
				}
			} else {
				if err = os.MkdirAll(filepath.Dir(itemPath), 0755); err != nil {
					return aoserrors.Wrap(err)
				}

				file, err := os.OpenFile(itemPath, os.O_CREATE, 0644)
				if err != nil {
					return aoserrors.Wrap(err)
				}
				file.Close()
			}

			if permissions != 0 {
				if err = os.Chmod(itemPath, os.FileMode(permissions)); err != nil {
					return aoserrors.Wrap(err)
				}
			}
		}
	}

	return nil
}

func (launcher *Launcher) setDevices(spec *serviceSpec, devices []Device) (err error) {
	// get devices from aos service configuration
	// and get all resource information for device from device manager
	// and add groups and host devices for class device

	for _, device := range devices {
		deviceResource, err := launcher.devicemanager.RequestDeviceResourceByName(device.Name)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		for _, hostDevice := range deviceResource.HostDevices {
			// use absolute path from host devices and permissions from aos configuration
			if err = spec.addHostDevice(Device{hostDevice, device.Permissions}); err != nil {
				return aoserrors.Wrap(err)
			}
		}

		for _, group := range deviceResource.Groups {
			if err = spec.addAdditionalGroup(group); err != nil {
				return aoserrors.Wrap(err)
			}
		}

	}

	return nil
}

func (launcher *Launcher) setServiceResources(spec *serviceSpec, resources []string) (err error) {
	for _, resource := range resources {
		boardResource, err := launcher.devicemanager.RequestBoardResourceByName(resource)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		for _, group := range boardResource.Groups {
			if err = spec.addAdditionalGroup(group); err != nil {
				return aoserrors.Wrap(err)
			}
		}

		for _, mount := range boardResource.Mounts {
			if err = spec.addMount(runtimespec.Mount{Destination: mount.Destination,
				Source:  mount.Source,
				Type:    mount.Type,
				Options: mount.Options}); err != nil {
				return aoserrors.Wrap(err)
			}
		}

		spec.mergeEnv(boardResource.Env)
	}

	return nil
}

func (launcher *Launcher) getHostsFromResources(resources []string) (hosts []config.Host, err error) {
	for _, resource := range resources {
		boardResource, err := launcher.devicemanager.RequestBoardResourceByName(resource)
		if err != nil {
			return hosts, aoserrors.Wrap(err)
		}

		hosts = append(hosts, boardResource.Hosts...)
	}

	return hosts, nil
}

func (launcher *Launcher) prepareService(unpackDir, installDir string,
	serviceInfo amqp.ServiceInfoFromCloud, update bool, oldService Service) (service Service, err error) {
	var uid, gid uint32

	if update {
		uid = oldService.UID
		gid = oldService.GID
	} else {
		uid, gid, err = launcher.idsPool.getFree()
		if err != nil {
			return service, aoserrors.Wrap(err)
		}
	}

	imageParts, err := getImageParts(unpackDir)
	if err != nil {
		return service, aoserrors.Wrap(err)
	}

	if err := imageutils.CopyFile(path.Join(unpackDir, manifestFileName), path.Join(installDir, manifestFileName)); err != nil {
		return service, aoserrors.Wrap(err)
	}

	if err := imageutils.CopyFile(imageParts.imageConfigPath, path.Join(installDir, ociImageConfigFile)); err != nil {
		return service, aoserrors.Wrap(err)
	}

	if err := imageutils.CopyFile(imageParts.aosSrvConfigPath, path.Join(installDir, aosServiceConfigFile)); err != nil {
		if !os.IsNotExist(err) {
			return service, aoserrors.Wrap(err)
		}

		log.Debug("Service without aos service configuration")
	}

	rootfsDir := path.Join(installDir, serviceRootfsDir)

	// unpack rootfs layer
	if err = imageutils.UnpackTarImage(imageParts.serviceFSLayerPath, rootfsDir); err != nil {
		return service, aoserrors.Wrap(err)
	}

	if err = filepath.Walk(rootfsDir, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return aoserrors.Wrap(err)
		}

		return os.Chown(name, int(uid), int(gid))
	}); err != nil {
		return service, aoserrors.Wrap(err)
	}

	serviceName := "aos_" + serviceInfo.ID + ".service"

	if err = launcher.createSystemdService(installDir, serviceName, serviceInfo.ID); err != nil {
		return service, aoserrors.Wrap(err)
	}

	alertRules, err := json.Marshal(serviceInfo.AlertRules)
	if err != nil {
		return service, aoserrors.Wrap(err)
	}

	service = Service{
		ID:              serviceInfo.ID,
		AosVersion:      serviceInfo.AosVersion,
		VendorVersion:   serviceInfo.VendorVersion,
		Description:     serviceInfo.Description,
		ServiceProvider: serviceInfo.ProviderID,
		Path:            installDir,
		UnitName:        serviceName,
		UID:             uid,
		GID:             gid,
		State:           stateInit,
		Status:          statusOk,
		AlertRules:      string(alertRules),
	}

	if service.ServiceProvider == "" {
		service.ServiceProvider = defaultServiceProvider
	}

	service.ManifestDigest, err = getManifestChecksum(service.Path)
	if err != nil {
		return service, aoserrors.Wrap(err)
	}

	return service, nil
}

func (launcher *Launcher) addService(service Service) (err error) {
	// We can't remove service if it is not in serviceProvider. Just return error and rollback will be
	// handled by parent function

	aosConfig, err := getAosServiceConfig(path.Join(service.Path, aosServiceConfigFile))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = platform.SetUserFSQuota(launcher.config.StorageDir,
		aosConfig.GetStorageLimit()+aosConfig.GetStateLimit(), service.UID, service.GID); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.serviceProvider.AddService(service); err != nil {
		return aoserrors.Wrap(err)
	}

	defer func() {
		if err != nil {
			log.WithField("id", service.ID).Errorf("Error adding service: %s", err)

			launcher.removeService(service)
		}
	}()

	if err = launcher.addServiceToCurrentUsers(service.ID); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.addServiceToSystemd(service); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.startService(service); err != nil {
		return aoserrors.Wrap(err)
	}

	return aoserrors.Wrap(err)
}

func (launcher *Launcher) updateService(oldService, newService Service, statusSender statusSender) (err error) {
	defer func() {
		if err != nil {
			log.WithField("id", newService.ID).Errorf("Update service error: %s", err)

			if err = launcher.stopService(newService); err != nil {
				log.WithField("id", newService.ID).Errorf("Can't stop service: %s", err)
			}

			if err = os.RemoveAll(newService.Path); err != nil {
				log.WithField("id", newService.ID).Errorf("Can't remove new service dir: %s", err)
			}

			if err := launcher.restoreService(oldService); err != nil {
				statusSender.sendStatus(oldService.ID, oldService.AosVersion, amqp.RemovingStatus, "", "")
				launcher.removeService(oldService)
				statusSender.sendStatus(oldService.ID, oldService.AosVersion, amqp.RemovedStatus, "", "")
			} else {
				statusSender.sendStatus(oldService.ID, oldService.AosVersion, amqp.RemovedStatus, "", "")
			}
		}
	}()

	statusSender.sendStatus(oldService.ID, oldService.AosVersion, amqp.RemovingStatus, "", "")

	newAosConfig, err := getAosServiceConfig(path.Join(newService.Path, aosServiceConfigFile))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.updateServiceState(oldService.ID, stateStopped, statusOk); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.addServiceToCurrentUsers(newService.ID); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = platform.SetUserFSQuota(launcher.config.StorageDir, newAosConfig.GetStorageLimit(),
		newService.UID, newService.GID); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.stopService(oldService); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = os.RemoveAll(oldService.Path); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.addServiceToSystemd(newService); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.startService(newService); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.serviceProvider.UpdateService(newService); err != nil {
		return aoserrors.Wrap(err)
	}

	statusSender.sendStatus(oldService.ID, oldService.AosVersion, amqp.RemovedStatus, "", "")

	return nil
}

func (launcher *Launcher) removeService(service Service) (retErr error) {
	log.WithFields(log.Fields{"id": service.ID, "aosVersion": service.AosVersion}).Debug("Remove service")

	if err := launcher.stopService(service); err != nil {
		if retErr == nil {
			retErr = err
		}
	}

	if _, err := launcher.systemd.DisableUnitFiles([]string{service.UnitName}, true); err != nil {
		if retErr == nil {
			log.WithField("name", service.ID).Errorf("Can't disable systemd unit: %s", err)
			retErr = err
		}
	}

	if err := launcher.systemd.Reload(); err != nil {
		log.Errorf("Can't reload systemd: %s", err)
	}

	usersServices, err := launcher.serviceProvider.GetUsersServicesByServiceID(service.ID)
	if err != nil {
		if retErr == nil {
			log.WithField("name", service.ID).Errorf("Can't get users services: %s", err)
			retErr = err
		}
	}

	for _, userService := range usersServices {
		if userService.StorageFolder != "" {
			log.WithFields(log.Fields{
				"folder":    userService.StorageFolder,
				"serviceID": service.ID}).Debug("Remove storage folder")

			if err := os.RemoveAll(userService.StorageFolder); err != nil {
				if retErr == nil {
					log.WithField("name", service.ID).Errorf("Can't remove storage folder: %s", err)
					retErr = err
				}
			}
		}
	}

	if err := launcher.serviceProvider.RemoveServiceFromAllUsers(service.ID); err != nil {
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

	if launcher.network != nil {
		spServices, err := launcher.serviceProvider.GetServiceProviderServices(service.ServiceProvider)
		if err != nil {
			if retErr == nil {
				log.WithField("name", service.ID).Errorf("Can't get service provider services: %s", err)
				retErr = err
			}
		} else {
			if len(spServices) == 0 {
				if err := launcher.network.DeleteNetwork(service.ServiceProvider); err != nil {
					if retErr == nil {
						log.WithField("name", service.ID).Errorf("Can't remove network: %s", err)
						retErr = err
					}
				}
			}
		}
	}

	if err := launcher.idsPool.remove(service.UID, service.GID); err != nil {
		if retErr == nil {
			log.WithField("name", service.ID).Errorf("Can't remove service UID/GID from pool: %s", err)
			retErr = err
		}
	}

	if err := os.RemoveAll(service.Path); err != nil {
		if retErr == nil {
			log.WithField("name", service.ID).Errorf("Can't remove service folder: %s", err)
			retErr = err
		}
	}

	return aoserrors.Wrap(retErr)
}

func getSystemdServiceTemplate(workingDir string) (template string, err error) {
	fileName := path.Join(workingDir, serviceTemplateFile)
	fileContent, err := ioutil.ReadFile(fileName)
	if err != nil {
		if !os.IsNotExist(err) {
			return template, aoserrors.Wrap(err)
		}

		log.Warnf("Service template file does not exist. Creating %s", fileName)

		if err = ioutil.WriteFile(fileName, []byte(serviceTemplate), 0644); err != nil {
			return template, aoserrors.Wrap(err)
		}

		return serviceTemplate, nil
	}

	return string(fileContent), nil
}

func (launcher *Launcher) createSystemdService(installDir, serviceName, id string) (err error) {
	f, err := os.Create(path.Join(installDir, serviceName))
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer f.Close()

	absServicePath, err := filepath.Abs(installDir)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	lines := strings.SplitAfter(launcher.serviceTemplate, "\n")
	for _, line := range lines {
		// skip comments
		if strings.HasPrefix(line, "#") {
			continue
		}

		// replaces variables with values
		line = strings.Replace(line, "${RUNNER}", launcher.runnerPath, -1)
		line = strings.Replace(line, "${ID}", id, -1)
		line = strings.Replace(line, "${SERVICEPATH}", absServicePath, -1)

		fmt.Fprint(f, line)
	}

	return aoserrors.Wrap(err)
}

func (launcher *Launcher) updateMonitoring(service Service, state ServiceState, aosConfig *aosServiceConfig) (err error) {
	switch state {
	case stateRunning:
		var rules amqp.ServiceAlertRules

		if err := json.Unmarshal([]byte(service.AlertRules), &rules); err != nil {
			return aoserrors.Wrap(err)
		}

		var ipAddress string

		if launcher.network != nil {
			if ipAddress, err = launcher.network.GetServiceIP(service.ID, service.ServiceProvider); err != nil {
				return err
			}
		}

		monitoringConfig := monitoring.ServiceMonitoringConfig{
			ServiceDir:   service.Path,
			IPAddress:    ipAddress,
			UID:          service.UID,
			GID:          service.GID,
			ServiceRules: &rules}

		if err = launcher.monitor.StartMonitorService(service.ID, monitoringConfig); err != nil {
			return aoserrors.Wrap(err)
		}

	case stateStopped:
		if err = launcher.monitor.StopMonitorService(service.ID); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

func (launcher *Launcher) addServiceToCurrentUsers(serviceID string) (err error) {
	_, err = launcher.serviceProvider.GetUsersService(launcher.users, serviceID)
	if err == nil {
		return nil
	}

	if !strings.Contains(err.Error(), "not exist") {
		return aoserrors.Wrap(err)
	}

	if err = launcher.serviceProvider.AddServiceToUsers(launcher.users, serviceID); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = launcher.envVarsProvider.syncEnvVarsWithStorage(); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (launcher *Launcher) cleanServicesDB() (err error) {
	log.Debug("Clean services DB")

	startedServices, err := launcher.serviceProvider.GetUsersServices(launcher.users)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	allServices, err := launcher.serviceProvider.GetServices()
	if err != nil {
		return aoserrors.Wrap(err)
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

		aosConfig, err := getAosServiceConfig(path.Join(service.Path, aosServiceConfigFile))
		if err != nil {
			return aoserrors.Wrap(err)
		}

		ttl := launcher.config.DefaultServiceTTLDays

		if aosConfig.ServiceTTL != nil {
			ttl = *aosConfig.ServiceTTL
		}

		if service.StartAt.Add(time.Hour*24*time.Duration(ttl)).Before(now) == true {
			servicesToBeRemoved++

			go func(service Service) {
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

func (launcher *Launcher) isServiceValid(service Service) (err error) {
	//check service folder
	if fi, err := os.Stat(service.Path); os.IsNotExist(err) || !fi.Mode().IsDir() {
		return aoserrors.Errorf("service folder %s doesn't exist", service.Path)
	}

	//check image manifest
	if _, err = os.Stat(path.Join(service.Path, manifestFileName)); os.IsNotExist(err) {
		return aoserrors.Errorf("image manifest file %s doesn't exist", path.Join(service.Path, manifestFileName))
	}

	//check image specification
	if _, err = os.Stat(path.Join(service.Path, ociImageConfigFile)); os.IsNotExist(err) {
		return aoserrors.Errorf("image specification file %s doesn't exist", path.Join(service.Path, ociImageConfigFile))
	}

	//check service file
	if _, err = os.Stat(path.Join(service.Path, service.UnitName)); os.IsNotExist(err) {
		return aoserrors.Errorf("service file %s doesn't exist", path.Join(service.Path, service.UnitName))
	}

	return nil
}

func (launcher *Launcher) isSubjectActive(subjectID string) bool {
	for _, curSubject := range launcher.users {
		if subjectID == curSubject {
			return true
		}
	}

	return false
}

func (launcher *Launcher) validateTTLs() {
	launcher.Lock()

	if launcher.ttlTicker != nil {
		launcher.Unlock()
		return
	}

	launcher.ttlTicker = time.NewTicker(ttlValidatePeriod)
	defer launcher.ttlTicker.Stop()

	launcher.Unlock()

	for {
		select {
		case <-launcher.ttlTicker.C:
			subjectServiceToRestart, err := launcher.envVarsProvider.validateEnvVarsTTL()
			if err != nil {
				log.Error("Validate env var ttl error: ", err)
				continue
			}

			launcher.restartServicesBySubjectServiceID(subjectServiceToRestart)

		case <-launcher.ttlStopChannel:
			return
		}
	}
}
