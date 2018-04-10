package launcher_test

import (
	"container/list"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/launcher"
)

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

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	if err := generateImage("tmp/tmp_image"); err != nil {
		return err
	}

	specFile := path.Join("tmp/tmp_image", "config.json")

	spec, err := launcher.GetServiceSpec(specFile)
	if err != nil {
		return err
	}

	if spec.Annotations == nil {
		spec.Annotations = make(map[string]string)
	}

	for i := 1; i <= 5; i++ {
		serviceName := fmt.Sprintf("service%d", i)

		spec.Process.Args = []string{"python3", "/home/service.py", serviceName}

		spec.Annotations["id"] = serviceName
		spec.Annotations["version"] = fmt.Sprintf("%d", i)

		if err := launcher.WriteServiceSpec(&spec, specFile); err != nil {
			return err
		}

		err = launcher.PackImage("tmp/tmp_image", fmt.Sprintf("tmp/image%d.tgz", i))
		if err != nil {
			return err
		}
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
import sys

i = 0
serviceName = sys.argv[1]

print(">>>> Start", serviceName)
while True:
	print(">>>> aos", serviceName, "count", i)
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
	launcher, err := launcher.New("tmp")
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	result := list.New()

	result.PushBack(launcher.InstallService("tmp/image1.tgz", "service1", 1))
	result.PushBack(launcher.InstallService("tmp/image2.tgz", "service2", 1))
	result.PushBack(launcher.InstallService("tmp/image3.tgz", "service3", 1))

	for r := result.Front(); r != nil; r = r.Next() {
		status, ok := r.Value.(<-chan error)
		if !ok {
			t.Error("Invalid interface")
		}
		if err = <-status; err != nil {
			t.Fatalf("Can't install/remove service: %s", err)
		}
	}

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}

	if len(services) != 3 {
		t.Error("Wrong service number")
	}

	for _, service := range services {
		if service.Id != "service1" && service.Id != "service2" && service.Id != "service3" {
			t.Error("Wrong services installed")
		}
		if service.Status != "OK" {
			t.Errorf("Bad service status: %s", service.Status)
		}
	}

	result.Init()

	result.PushBack(launcher.RemoveService("service1"))
	result.PushBack(launcher.InstallService("tmp/image4.tgz", "service4", 1))
	result.PushBack(launcher.RemoveService("service2"))
	result.PushBack(launcher.InstallService("tmp/image5.tgz", "service5", 1))
	result.PushBack(launcher.RemoveService("service3"))

	for r := result.Front(); r != nil; r = r.Next() {
		status, ok := r.Value.(<-chan error)
		if !ok {
			t.Error("Invalid interface")
		}
		if err = <-status; err != nil {
			t.Errorf("Can't install/remove service: %s", err)
		}
	}

	services, err = launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}

	if len(services) != 2 {
		t.Errorf("Wrong service number")
	}

	for _, service := range services {
		if service.Id != "service4" && service.Id != "service5" {
			t.Errorf("Wrong services installed")
		}
		if service.Status != "OK" {
			t.Errorf("Bad service status: %s", service.Status)
		}
	}

	result.Init()

	result.PushBack(launcher.RemoveService("service4"))
	result.PushBack(launcher.RemoveService("service5"))

	for r := result.Front(); r != nil; r = r.Next() {
		status, ok := r.Value.(<-chan error)
		if !ok {
			t.Error("Invalid interface")
		}
		if err = <-status; err != nil {
			t.Errorf("Can't install/remove service: %s", err)
		}
	}

	result.Init()

	services, err = launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}
	if len(services) != 0 {
		t.Errorf("Wrong service number")
	}
}

func installAllServices() (err error) {
	launcher, err := launcher.New("tmp")
	if err != nil {
		return err
	}
	defer launcher.Close()

	result := list.New()

	result.PushBack(launcher.InstallService("tmp/image1.tgz", "service1", 1))
	result.PushBack(launcher.InstallService("tmp/image2.tgz", "service2", 1))
	result.PushBack(launcher.InstallService("tmp/image3.tgz", "service3", 1))
	result.PushBack(launcher.InstallService("tmp/image4.tgz", "service4", 1))
	result.PushBack(launcher.InstallService("tmp/image5.tgz", "service5", 1))

	for r := result.Front(); r != nil; r = r.Next() {
		status, ok := r.Value.(<-chan error)
		if !ok {
			return errors.New("Invalid interface")
		}
		if err = <-status; err != nil {
			return err
		}
	}

	return nil
}

func TestAutoStart(t *testing.T) {
	if err := installAllServices(); err != nil {
		t.Fatalf("Can't install services: %s", err)
	}

	launcher, err := launcher.New("tmp")
	if err != nil {
		t.Fatalf("Can't create launcher: %s", err)
	}
	defer launcher.Close()

	time.Sleep(time.Second * 3)

	services, err := launcher.GetServicesInfo()
	if err != nil {
		t.Errorf("Can't get services info: %s", err)
	}

	if len(services) != 5 {
		t.Error("Wrong service number")
	}

	for _, service := range services {
		if service.Id != "service1" && service.Id != "service2" && service.Id != "service3" &&
			service.Id != "service4" && service.Id != "service5" {
			t.Error("Wrong services installed")
		}
		if service.Status != "OK" {
			t.Errorf("Bad service status: %s", service.Status)
		}
	}

	result := list.New()
	result.PushBack(launcher.RemoveService("service1"))
	result.PushBack(launcher.RemoveService("service2"))
	result.PushBack(launcher.RemoveService("service3"))
	result.PushBack(launcher.RemoveService("service4"))
	result.PushBack(launcher.RemoveService("service5"))

	for r := result.Front(); r != nil; r = r.Next() {
		status, ok := r.Value.(<-chan error)
		if !ok {
			t.Error("Invalid interface")
		}
		if err = <-status; err != nil {
			t.Errorf("Can't install/remove service: %s", err)
		}
	}

}
