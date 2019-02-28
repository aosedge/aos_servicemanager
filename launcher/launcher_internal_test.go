package launcher

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jlaffaye/ftp"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/sha3"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/monitoring"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Generates test image with python script
type pythonImage struct {
}

// Generates test image with iperf server
type iperfImage struct {
}

// Generates test image with ftp server
type ftpImage struct {
	storageLimit uint64
	stateLimit   uint64
}

// Test monitor info
type testMonitorInfo struct {
	serviceID string
	config    monitoring.ServiceMonitoringConfig
}

// Test monitor
type testMonitor struct {
	startChannel chan *testMonitorInfo
	stopChannel  chan string
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var db *database.Database

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newTestLauncher(downloader downloader, monitor ServiceMonitor) (launcher *Launcher, err error) {
	launcher, err = New(&config.Config{WorkingDir: "tmp", DefaultServiceTTL: 30}, db, monitor)
	if err != nil {
		return launcher, err
	}

	launcher.downloader = downloader

	return launcher, err
}

func (downloader pythonImage) downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		log.Error("Can't create image dir : ", err)
		return outputFile, err
	}
	defer os.RemoveAll(imageDir)

	// create dir
	if err := os.MkdirAll(path.Join(imageDir, "rootfs", "home"), 0755); err != nil {
		return outputFile, err
	}

	if err := generatePythonContent(imageDir); err != nil {
		return outputFile, err
	}

	if err := generateConfig(imageDir); err != nil {
		return outputFile, err
	}

	specFile := path.Join(imageDir, "config.json")

	spec, err := getServiceSpec(specFile)
	if err != nil {
		return outputFile, err
	}

	spec.Process.Args = []string{"python3", "/home/service.py", serviceInfo.ID, fmt.Sprintf("%d", serviceInfo.Version)}

	if spec.Annotations == nil {
		spec.Annotations = make(map[string]string)
	}
	spec.Annotations[aosProductPrefix+"vis.permissions"] = `{"*": "rw", "123": "rw"}`

	if err := writeServiceSpec(&spec, specFile); err != nil {
		return outputFile, err
	}

	imageFile, err := ioutil.TempFile("", "aos_")
	if err != nil {
		return outputFile, err
	}
	outputFile = imageFile.Name()
	imageFile.Close()

	if err = packImage(imageDir, outputFile); err != nil {
		return outputFile, err
	}

	return outputFile, nil
}

func (downloader iperfImage) downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		log.Error("Can't create image dir : ", err)
		return outputFile, err
	}
	defer os.RemoveAll(imageDir)

	// create dir
	if err := os.MkdirAll(path.Join(imageDir, "rootfs", "home"), 0755); err != nil {
		return outputFile, err
	}

	if err := generateConfig(imageDir); err != nil {
		return outputFile, err
	}

	specFile := path.Join(imageDir, "config.json")

	spec, err := getServiceSpec(specFile)
	if err != nil {
		return outputFile, err
	}

	spec.Process.Args = []string{"iperf", "-s"}

	if spec.Annotations == nil {
		spec.Annotations = make(map[string]string)
	}
	spec.Annotations[aosProductPrefix+"network.uploadSpeed"] = "4096"
	spec.Annotations[aosProductPrefix+"network.downloadSpeed"] = "8192"

	if err := writeServiceSpec(&spec, specFile); err != nil {
		return outputFile, err
	}

	imageFile, err := ioutil.TempFile("", "aos_")
	if err != nil {
		return outputFile, err
	}
	outputFile = imageFile.Name()
	imageFile.Close()

	if err = packImage(imageDir, outputFile); err != nil {
		return outputFile, err
	}

	return outputFile, nil
}

func (downloader ftpImage) downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		log.Error("Can't create image dir : ", err)
		return outputFile, err
	}
	defer os.RemoveAll(imageDir)

	// create dir
	if err := os.MkdirAll(path.Join(imageDir, "rootfs", "home"), 0755); err != nil {
		return outputFile, err
	}

	if err := generateFtpContent(imageDir); err != nil {
		return outputFile, err
	}

	if err := generateConfig(imageDir); err != nil {
		return outputFile, err
	}

	specFile := path.Join(imageDir, "config.json")

	spec, err := getServiceSpec(specFile)
	if err != nil {
		return outputFile, err
	}

	spec.Process.Args = []string{"python3", "/home/service.py", serviceInfo.ID, fmt.Sprintf("%d", serviceInfo.Version)}

	if spec.Annotations == nil {
		spec.Annotations = make(map[string]string)
	}
	spec.Annotations[aosProductPrefix+"storage.limit"] = strconv.FormatUint(downloader.storageLimit, 10)
	spec.Annotations[aosProductPrefix+"state.limit"] = strconv.FormatUint(downloader.stateLimit, 10)

	if err := writeServiceSpec(&spec, specFile); err != nil {
		return outputFile, err
	}

	imageFile, err := ioutil.TempFile("", "aos_")
	if err != nil {
		return outputFile, err
	}
	outputFile = imageFile.Name()
	imageFile.Close()

	if err = packImage(imageDir, outputFile); err != nil {
		return outputFile, err
	}

	return outputFile, nil
}
func newTestMonitor() (monitor *testMonitor, err error) {
	monitor = &testMonitor{}

	monitor.startChannel = make(chan *testMonitorInfo, 100)
	monitor.stopChannel = make(chan string, 100)

	return monitor, nil
}

func (monitor *testMonitor) StartMonitorService(serviceID string, monitorConfig monitoring.ServiceMonitoringConfig) (err error) {

	monitor.startChannel <- &testMonitorInfo{serviceID, monitorConfig}

	return nil
}

func (monitor *testMonitor) StopMonitorService(serviceID string) (err error) {
	monitor.stopChannel <- serviceID

	return nil
}

func (launcher *Launcher) removeAllServices() (err error) {
	services, err := launcher.serviceProvider.GetServices()
	if err != nil {
		return err
	}

	statusChannel := make(chan error, len(services))

	for _, service := range services {
		go func(service database.ServiceEntry) {
			err := launcher.removeService(service)
			if err != nil {
				log.Errorf("Can't remove service %s: %s", service.ID, err)
			}
			statusChannel <- err
		}(service)
	}

	// Wait all services are deleted
	for i := 0; i < len(services); i++ {
		<-statusChannel
	}

	err = launcher.systemd.Reload()
	if err != nil {
		return err
	}

	services, err = launcher.serviceProvider.GetServices()
	if err != nil {
		return err
	}
	if len(services) != 0 {
		return errors.New("Can't remove all services")
	}

	usersList, err := launcher.serviceProvider.GetUsersList()
	if err != nil {
		return err
	}
	if len(usersList) != 0 {
		return errors.New("Can't remove all users")
	}

	return err
}

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	db, err = database.New("tmp/servicemanager.db")
	if err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	defer db.Close()

	launcher, err := newTestLauncher(new(pythonImage), nil)
	if err != nil {
		return err
	}
	defer launcher.Close()

	if err := launcher.removeAllServices(); err != nil {
		return err
	}

	if err := os.RemoveAll("tmp"); err != nil {
		return err
	}

	return nil
}

func generatePythonContent(imagePath string) (err error) {
	serviceContent := `#!/usr/bin/python

import time
import socket
import sys

i = 0
serviceName = sys.argv[1]
serviceVersion = sys.argv[2]

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM) 
message = serviceName + ", version: " + serviceVersion
sock.sendto(str.encode(message), ("172.19.0.1", 10001))
sock.close()

print(">>>> Start", serviceName, "version", serviceVersion)
while True:
	print(">>>> aos", serviceName, "version", serviceVersion, "count", i)
	i = i + 1
	time.sleep(5)`

	if err := ioutil.WriteFile(path.Join(imagePath, "rootfs", "home", "service.py"), []byte(serviceContent), 0644); err != nil {
		return err
	}

	return nil
}

func generateFtpContent(imagePath string) (err error) {
	serviceContent := `#!/usr/bin/python

from pyftpdlib.authorizers import DummyAuthorizer
from pyftpdlib.handlers import FTPHandler
from pyftpdlib.servers import FTPServer

authorizer = DummyAuthorizer()
authorizer.add_anonymous("/home/service/storage", perm="elradfmw")

handler = FTPHandler
handler.authorizer = authorizer

server = FTPServer(("", 21), handler)
server.serve_forever()`

	if err := ioutil.WriteFile(path.Join(imagePath, "rootfs", "home", "service.py"), []byte(serviceContent), 0644); err != nil {
		return err
	}

	return nil
}

func generateConfig(imagePath string) (err error) {
	// remove json
	if err := os.Remove(path.Join(imagePath, "config.json")); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}

	// generate config spec
	out, err := exec.Command("runc", "spec", "-b", imagePath).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}

	return nil
}

func (launcher *Launcher) connectToFtp(serviceID string) (ftpConnection *ftp.ServerConn, err error) {
	service, err := launcher.serviceProvider.GetService(serviceID)
	if err != nil {
		return nil, err
	}

	ip, err := monitoring.GetServiceIPAddress(service.Path)
	if err != nil {
		return nil, err
	}

	ftpConnection, err = ftp.DialTimeout(ip+":21", 5*time.Second)
	if err != nil {
		return nil, err
	}

	if err = ftpConnection.Login("anonymous", "anonymous"); err != nil {
		ftpConnection.Quit()
		return nil, err
	}

	return ftpConnection, nil
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {

	if err := setup(); err != nil {
		log.Fatalf("Error creating service images: %s", err)
	}

	ret := m.Run()

	if err := cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestInstallRemove(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage), nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	numInstallServices := 10
	numRemoveServices := 5

	// install services
	for i := 0; i < numInstallServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}
	// remove services
	for i := 0; i < numRemoveServices; i++ {
		launcher.RemoveService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numInstallServices+numRemoveServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			if status.Action == ActionInstall {
				t.Errorf("Can't install service %s: %s", status.ID, status.Err)
			} else {
				t.Errorf("Can't remove service %s: %s", status.ID, status.Err)
			}
		}
	}

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != numInstallServices-numRemoveServices {
		t.Errorf("Wrong service quantity")
	}
	for _, service := range services {
		if service.Status != "OK" {
			t.Errorf("Service %s error status: %s", service.ID, service.Status)
		}
	}

	time.Sleep(time.Second * 2)

	// remove remaining services
	for i := numRemoveServices; i < numInstallServices; i++ {
		launcher.RemoveService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numInstallServices-numRemoveServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			t.Errorf("Can't remove service %s: %s", status.ID, status.Err)
		}
	}

	services, err = launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service quantity")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestAutoStart(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage), nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	numServices := 5

	// install services
	for i := 0; i < numServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}

	for i := 0; i < numServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			t.Errorf("Can't install service %s: %s", status.ID, status.Err)
		}
	}

	launcher.Close()

	time.Sleep(time.Second * 2)

	launcher, err = newTestLauncher(new(pythonImage), nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	time.Sleep(time.Second * 2)

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != numServices {
		t.Errorf("Wrong service quantity")
	}
	for _, service := range services {
		if service.Status != "OK" {
			t.Errorf("Service %s error status: %s", service.ID, service.Status)
		}
	}

	// remove services
	for i := 0; i < numServices; i++ {
		launcher.RemoveService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			t.Errorf("Can't remove service %s: %s", status.ID, status.Err)
		}
	}

	services, err = launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service quantity")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestErrors(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage), nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	// test version mistmatch

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 5})
	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 4})
	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 6})

	for i := 0; i < 3; i++ {
		status := <-launcher.StatusChannel
		switch {
		case status.Version == 5 && status.Err != nil:
			t.Errorf("Can't install service %s version %d: %s", status.ID, status.Version, status.Err)
		case status.Version == 4 && status.Err == nil:
			t.Errorf("Service %s version %d should not be installed", status.ID, status.Version)
		case status.Version == 6 && status.Err != nil:
			t.Errorf("Can't install service %s version %d: %s", status.ID, status.Version, status.Err)
		}
	}

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != 1 {
		t.Errorf("Wrong service quantity: %d", len(services))
	} else if services[0].Version != 6 {
		t.Errorf("Wrong service version")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestUpdate(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage), nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	serverAddr, err := net.ResolveUDPAddr("udp", ":10001")
	if err != nil {
		t.Fatalf("Can't create resolve UDP address: %s", err)
	}

	serverConn, err := net.ListenUDP("udp", serverAddr)
	if err != nil {
		t.Fatalf("Can't listen UDP: %s", err)
	}
	defer serverConn.Close()

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 0})

	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Fatalf("Can't install %s service: %s", status.ID, status.Err)
	}

	if err := serverConn.SetReadDeadline(time.Now().Add(time.Second * 30)); err != nil {
		t.Fatalf("Can't set read deadline: %s", err)
	}

	buf := make([]byte, 1024)

	n, _, err := serverConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("Can't read from UDP: %s", err)
	} else {
		message := string(buf[:n])

		if message != "service0, version: 0" {
			t.Fatalf("Wrong service content: %s", message)
		}
	}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 1})

	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Fatalf("Can't install %s service: %s", status.ID, status.Err)
	}

	n, _, err = serverConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("Can't read from UDP: %s", err)
	} else {
		message := string(buf[:n])

		if message != "service0, version: 1" {
			t.Fatalf("Wrong service content: %s", message)
		}
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestNetworkSpeed(t *testing.T) {
	launcher, err := newTestLauncher(new(iperfImage), nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	numServices := 2

	for i := 0; i < numServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}

	for i := 0; i < numServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			t.Errorf("Can't install service %s: %s", status.ID, status.Err)
		}
	}

	for i := 0; i < numServices; i++ {
		serviceID := fmt.Sprintf("service%d", i)

		service, err := launcher.serviceProvider.GetService(serviceID)
		if err != nil {
			t.Errorf("Can't get service: %s", err)
			continue
		}

		addr, err := monitoring.GetServiceIPAddress(service.Path)
		if err != nil {
			t.Errorf("Can't get ip address: %s", err)
			continue
		}
		output, err := exec.Command("iperf", "-c"+addr, "-d", "-r", "-t2", "-yc").Output()
		if err != nil {
			t.Errorf("Iperf failed: %s", err)
			continue
		}

		ulSpeed := -1
		dlSpeed := -1

		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			result := strings.Split(line, ",")
			if len(result) >= 9 {
				if result[4] == "5001" {
					value, err := strconv.ParseInt(result[8], 10, 64)
					if err != nil {
						t.Errorf("Can't parse ul speed: %s", err)
						continue
					}
					ulSpeed = int(value) / 1000
				} else {
					value, err := strconv.ParseUint(result[8], 10, 64)
					if err != nil {
						t.Errorf("Can't parse ul speed: %s", err)
						continue
					}
					dlSpeed = int(value) / 1000
				}
			}
		}

		if ulSpeed == -1 || dlSpeed == -1 {
			t.Error("Can't determine ul/dl speed")
		}

		if ulSpeed > 4096*1.5 || dlSpeed > 8192*1.5 {
			t.Errorf("Speed limit exceeds: dl %d, ul %d", dlSpeed, ulSpeed)
		}
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestVisPermissions(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage), nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 0})

	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Fatalf("Can't install %s service: %s", status.ID, status.Err)
	}

	service, err := db.GetService("service0")
	if err != nil {
		t.Fatalf("Can't get service: %s", err)
	}

	if service.Permissions != `{"*": "rw", "123": "rw"}` {
		t.Fatalf("Permissions mismatch")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestUsersServices(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage), nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	numUsers := 3
	numServices := 3

	for i := 0; i < numUsers; i++ {
		users := []string{fmt.Sprintf("user%d", i)}

		if err = launcher.SetUsers(users); err != nil {
			t.Fatalf("Can't set users: %s", err)
		}

		services, err := launcher.serviceProvider.GetUsersServices(users)
		if err != nil {
			t.Fatalf("Can't get users services: %s", err)
		}
		if len(services) != 0 {
			t.Fatalf("Wrong service quantity")
		}

		// install services
		for j := 0; j < numServices; j++ {
			launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("user%d_service%d", i, j)})
		}
		for i := 0; i < numServices; i++ {
			if status := <-launcher.StatusChannel; status.Err != nil {
				t.Errorf("Can't install service %s: %s", status.ID, status.Err)
			}
		}

		time.Sleep(time.Second * 2)

		services, err = launcher.serviceProvider.GetServices()
		if err != nil {
			t.Fatalf("Can't get services: %s", err)
		}

		count := 0
		for _, service := range services {
			if service.State == stateRunning {
				exist, err := launcher.serviceProvider.IsUsersService(users, service.ID)
				if err != nil {
					t.Errorf("Can't check users service: %s", err)
				}
				if !exist {
					t.Errorf("Service doesn't belong to users: %s", service.ID)
				}
				count++
			}
		}

		if count != numServices {
			t.Fatalf("Wrong running services count")
		}
	}

	for i := 0; i < numUsers; i++ {
		users := []string{fmt.Sprintf("user%d", i)}

		if err = launcher.SetUsers(users); err != nil {
			t.Fatalf("Can't set users: %s", err)
		}

		time.Sleep(time.Second * 2)

		services, err := launcher.serviceProvider.GetServices()
		if err != nil {
			t.Fatalf("Can't get services: %s", err)
		}

		count := 0
		for _, service := range services {
			if service.State == stateRunning {
				exist, err := launcher.serviceProvider.IsUsersService(users, service.ID)
				if err != nil {
					t.Errorf("Can't check users service: %s", err)
				}
				if !exist {
					t.Errorf("Service doesn't belong to users: %s", service.ID)
				}
				count++
			}
		}

		if count != numServices {
			t.Fatalf("Wrong running services count")
		}
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestServiceTTL(t *testing.T) {
	launcher, err := newTestLauncher(new(pythonImage), nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	numServices := 3

	if err = launcher.SetUsers([]string{"user0"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	// install services
	for i := 0; i < numServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{ID: fmt.Sprintf("service%d", i)})
	}
	for i := 0; i < numServices; i++ {
		if status := <-launcher.StatusChannel; status.Err != nil {
			t.Errorf("Can't install service %s: %s", status.ID, status.Err)
		}
	}

	services, err := launcher.serviceProvider.GetServices()
	if err != nil {
		t.Fatalf("Can't get services: %s", err)
	}

	for _, service := range services {
		if err = launcher.serviceProvider.SetServiceStartTime(service.ID, service.StartAt.Add(-time.Hour*24*30)); err != nil {
			t.Errorf("Can't set service start time: %s", err)
		}
	}

	if err = launcher.SetUsers([]string{"user1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	services, err = launcher.serviceProvider.GetServices()
	if err != nil {
		t.Fatalf("Can't get services: %s", err)
	}

	if len(services) != 0 {
		t.Fatal("Wrong service quantity")
	}

	usersList, err := launcher.serviceProvider.GetUsersList()
	if err != nil {
		t.Fatalf("Can't get users list: %s", err)
	}

	if len(usersList) != 0 {
		t.Fatal("Wrong users quantity", usersList)
	}
}

func TestServiceMonitoring(t *testing.T) {
	monitor, err := newTestMonitor()
	if err != nil {
		t.Fatalf("Can't create monitor: %s", err)
	}

	launcher, err := newTestLauncher(new(pythonImage), monitor)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"user0"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	serviceAlerts := amqp.ServiceAlertRules{
		RAM: &config.AlertRule{
			MinTimeout:   config.Duration{Duration: 30 * time.Second},
			MinThreshold: 0,
			MaxThreshold: 80},
		CPU: &config.AlertRule{
			MinTimeout:   config.Duration{Duration: 1 * time.Minute},
			MinThreshold: 0,
			MaxThreshold: 20},
		UsedDisk: &config.AlertRule{
			MinTimeout:   config.Duration{Duration: 5 * time.Minute},
			MinThreshold: 0,
			MaxThreshold: 20}}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "Service1", ServiceMonitoring: &serviceAlerts})
	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Errorf("Can't install service %s: %s", status.ID, status.Err)
	}

	select {
	case info := <-monitor.startChannel:
		if info.serviceID != "Service1" {
			t.Fatalf("Wrong service ID: %s", info.serviceID)
		}

		if !reflect.DeepEqual(info.config.ServiceRules, &serviceAlerts) {
			t.Fatalf("Wrong service alert rules")
		}

	case <-time.After(1000 * time.Millisecond):
		t.Errorf("Waiting for service monitor timeout")
	}

	launcher.RemoveService("Service1")
	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Errorf("Can't remove service %s: %s", status.ID, status.Err)
	}

	select {
	case serviceID := <-monitor.stopChannel:
		if serviceID != "Service1" {
			t.Fatalf("Wrong service ID: %s", serviceID)
		}

	case <-time.After(2000 * time.Millisecond):
		t.Errorf("Waiting for service monitor timeout")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestServiceStorage(t *testing.T) {
	launcher, err := newTestLauncher(&ftpImage{1024 * 12, 0}, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 0})
	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Fatalf("Can't install %s service: %s", status.ID, status.Err)
	}

	ftp, err := launcher.connectToFtp("service0")
	if err != nil {
		t.Fatalf("Can't connect to ftp: %s", err)
	}
	defer ftp.Quit()

	testData := make([]byte, 8192)

	if err := ftp.Stor("test1.dat", bytes.NewReader(testData)); err != nil {
		t.Errorf("Can't write file: %s", err)
	}

	if err := ftp.Stor("test2.dat", bytes.NewReader(testData)); err == nil {
		t.Errorf("Unexpected nil error")
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}

func TestServiceState(t *testing.T) {
	launcher, err := newTestLauncher(&ftpImage{1024 * 12, 256}, nil)
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	if err = launcher.SetUsers([]string{"User1"}); err != nil {
		t.Fatalf("Can't set users: %s", err)
	}

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 0})
	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Fatalf("Can't install %s service: %s", status.ID, status.Err)
	}

	ftp, err := launcher.connectToFtp("service0")
	if err != nil {
		t.Fatalf("Can't connect to ftp: %s", err)
	}
	defer ftp.Quit()

	// Check new state accept

	stateData := ""

	if err := ftp.Stor("state.dat", bytes.NewReader([]byte(stateData))); err != nil {
		t.Errorf("Can't write file: %s", err)
	}

	time.Sleep(500 * time.Millisecond)

	stateData = "Hello"

	if err := ftp.Stor("state.dat", bytes.NewReader([]byte(stateData))); err != nil {
		t.Errorf("Can't write file: %s", err)
	}

	select {
	case newState := <-launcher.NewStateChannel:
		if newState.State != stateData {
			t.Errorf("Wrong state: %s", newState.State)
		}

		if err = launcher.StateAcceptance(amqp.StateAcceptance{
			Result: "accepted"}, newState.CorrelationID); err != nil {
			t.Errorf("Can't accept state: %s", err)
		}

	case <-time.After(2 * time.Second):
		t.Error("No new state event")
	}

	// Check new state reject

	stateData = "Hello again"

	if err := ftp.Stor("state.dat", bytes.NewReader([]byte(stateData))); err != nil {
		t.Errorf("Can't write file: %s", err)
	}

	select {
	case newState := <-launcher.NewStateChannel:
		if newState.State != stateData {
			t.Errorf("Wrong state: %s", newState.State)
		}

		if err = launcher.StateAcceptance(amqp.StateAcceptance{
			Result: "rejected",
			Reason: "just because"}, newState.CorrelationID); err != nil {
			t.Errorf("Can't reject state: %s", err)
		}

	case <-time.After(2 * time.Second):
		t.Error("No new state event")
	}

	select {
	case <-launcher.StateRequestChannel:
		stateData = "Hello"
		calcSum := sha3.Sum224([]byte(stateData))

		launcher.UpdateState(amqp.UpdateState{
			ServiceID: "service0",
			State:     stateData,
			Checksum:  hex.EncodeToString(calcSum[:])})

		time.Sleep(1 * time.Second)

	case <-time.After(2 * time.Second):
		t.Error("No state request event")
	}

	if ftp, err = launcher.connectToFtp("service0"); err != nil {
		t.Fatalf("Can't connect to ftp: %s", err)
	}
	defer ftp.Quit()

	response, err := ftp.Retr("state.dat")
	if err != nil {
		t.Errorf("Can't retrieve state file: %s", err)
	} else {
		serviceState, err := ioutil.ReadAll(response)
		if err != nil {
			t.Errorf("Can't retrieve state file: %s", err)
		}

		if string(serviceState) != stateData {
			t.Errorf("Wrong state: %s", serviceState)
		}

		response.Close()
	}

	// Check state after update

	launcher.InstallService(amqp.ServiceInfoFromCloud{ID: "service0", Version: 1})
	if status := <-launcher.StatusChannel; status.Err != nil {
		t.Fatalf("Can't install %s service: %s", status.ID, status.Err)
	}

	if ftp, err = launcher.connectToFtp("service0"); err != nil {
		t.Fatalf("Can't connect to ftp: %s", err)
	}
	defer ftp.Quit()

	if response, err = ftp.Retr("state.dat"); err != nil {
		t.Errorf("Can't retrieve state file: %s", err)
	} else {
		serviceState, err := ioutil.ReadAll(response)
		if err != nil {
			t.Errorf("Can't retrieve state file: %s", err)
		}

		if string(serviceState) != stateData {
			t.Errorf("Wrong state: %s", serviceState)
		}

		response.Close()
	}

	if err := launcher.removeAllServices(); err != nil {
		t.Errorf("Can't cleanup all services: %s", err)
	}
}
