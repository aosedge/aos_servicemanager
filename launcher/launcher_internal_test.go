package launcher

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type TestLauncher struct {
	*Launcher
}

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

func newTestLauncher() (testLauncher *TestLauncher, statusChannel <-chan ActionStatus, err error) {
	instance, statusChannel, err := New("tmp")

	testLauncher = &TestLauncher{instance}
	testLauncher.downloader = testLauncher

	return testLauncher, statusChannel, err
}

func (launcher *TestLauncher) downloadService(serviceInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		log.Error("Can't create image dir : ", err)
		return outputFile, err
	}
	defer os.RemoveAll(imageDir)

	if err := generateImage(imageDir); err != nil {
		return outputFile, err
	}

	specFile := path.Join(imageDir, "config.json")

	spec, err := getServiceSpec(specFile)
	if err != nil {
		return outputFile, err
	}

	spec.Process.Args = []string{"python3", "/home/service.py", serviceInfo.Id, fmt.Sprintf("%d", serviceInfo.Version)}

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

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	return nil
}

func cleanup() (err error) {
	if err := os.RemoveAll("tmp"); err != nil {
		return err
	}

	return nil
}

func generateImage(imagePath string) (err error) {
	// create dir
	if err := os.MkdirAll(path.Join(imagePath, "rootfs", "home"), 0755); err != nil {
		return err
	}

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
	launcher, statusChan, err := newTestLauncher()
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	numInstallServices := 30
	numRemoveServices := 10

	// install services
	for i := 0; i < numInstallServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{Id: fmt.Sprintf("service%d", i)})
	}
	// remove services
	for i := 0; i < numRemoveServices; i++ {
		launcher.RemoveService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numInstallServices+numRemoveServices; i++ {
		if status := <-statusChan; status.Err != nil {
			if status.Action == ActionInstall {
				t.Error("Can't install service: ", status.Err)
			} else {
				t.Error("Can't remove service: ", status.Err)
			}
		}
	}

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Error("Can't get services info: ", err)
	}
	if len(services) != numInstallServices-numRemoveServices {
		t.Errorf("Wrong service quantity")
	}
	for _, service := range services {
		if service.Status != "OK" {
			t.Errorf("Service %s error status: %s", service.Id, service.Status)
		}
	}

	time.Sleep(time.Second * 5)

	// remove remaining services
	for i := numRemoveServices; i < numInstallServices; i++ {
		launcher.RemoveService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numInstallServices-numRemoveServices; i++ {
		if status := <-statusChan; status.Err != nil {
			t.Error("Can't remove service: ", status.Err)
		}
	}

	services, err = launcher.GetServicesInfo()
	if err != nil {
		t.Error("Can't get services info: ", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service quantity")
	}
}

func TestAutoStart(t *testing.T) {
	launcher, statusChan, err := newTestLauncher()
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}

	numServices := 10

	// install services
	for i := 0; i < numServices; i++ {
		launcher.InstallService(amqp.ServiceInfoFromCloud{Id: fmt.Sprintf("service%d", i)})
	}

	for i := 0; i < numServices; i++ {
		if status := <-statusChan; status.Err != nil {
			t.Error("Can't install service: ", status.Err)
		}
	}

	launcher.Close()

	time.Sleep(time.Second * 5)

	launcher, statusChan, err = newTestLauncher()
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	time.Sleep(time.Second * 5)

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Error("Can't get services info: ", err)
	}
	if len(services) != numServices {
		t.Errorf("Wrong service quantity")
	}
	for _, service := range services {
		if service.Status != "OK" {
			t.Errorf("Service %s error status: %s", service.Id, service.Status)
		}
	}

	// remove services
	for i := 0; i < numServices; i++ {
		launcher.RemoveService(fmt.Sprintf("service%d", i))
	}

	for i := 0; i < numServices; i++ {
		if status := <-statusChan; status.Err != nil {
			t.Error("Can't remove service: ", status.Err)
		}
	}

	services, err = launcher.GetServicesInfo()
	if err != nil {
		t.Error("Can't get services info: ", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service quantity")
	}
}

func TestErrors(t *testing.T) {
	launcher, statusChan, err := newTestLauncher()
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	// test version mistmatch

	launcher.InstallService(amqp.ServiceInfoFromCloud{Id: "service0", Version: 5})
	launcher.InstallService(amqp.ServiceInfoFromCloud{Id: "service0", Version: 4})
	launcher.InstallService(amqp.ServiceInfoFromCloud{Id: "service0", Version: 6})

	for i := 0; i < 3; i++ {
		status := <-statusChan
		switch {
		case status.Version == 5 && status.Err != nil:
			t.Errorf("Can't install service version %d: %s", status.Version, status.Err)
		case status.Version == 4 && status.Err == nil:
			t.Errorf("Service version %d should not be installed", status.Version)
		case status.Version == 6 && status.Err != nil:
			t.Errorf("Can't install service version %d: %s", status.Version, status.Err)
		}
	}

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Error("Can't get services info: ", err)
	}
	if len(services) != 1 {
		t.Errorf("Wrong service quantity")
	} else if services[0].Version != 6 {
		t.Errorf("Wrong service version")
	}

	launcher.RemoveService("service0")

	if status := <-statusChan; status.Err != nil {
		t.Error("Can't remove service: ", status.Err)
	}
}

func TestUpdate(t *testing.T) {
	launcher, statusChan, err := newTestLauncher()
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	serverAddr, err := net.ResolveUDPAddr("udp", ":10001")
	if err != nil {
		t.Fatalf("Can't create resolve UDP address: %s", err)
	}

	serverConn, err := net.ListenUDP("udp", serverAddr)
	if err != nil {
		t.Fatalf("Can't listen UDP: %s", err)
	}
	defer serverConn.Close()

	launcher.InstallService(amqp.ServiceInfoFromCloud{Id: "service0", Version: 0})

	if status := <-statusChan; status.Err != nil {
		t.Fatalf("Can't install service: %s", status.Err)
	}

	if err := serverConn.SetReadDeadline(time.Now().Add(time.Second * 30)); err != nil {
		t.Fatalf("Can't set read dead line: %s", err)
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

	launcher.InstallService(amqp.ServiceInfoFromCloud{Id: "service0", Version: 1})

	if status := <-statusChan; status.Err != nil {
		t.Fatalf("Can't install service: %s", status.Err)
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

	launcher.RemoveService("service0")

	if status := <-statusChan; status.Err != nil {
		t.Errorf("Can't remove service: %s", status.Err)
	}
}
