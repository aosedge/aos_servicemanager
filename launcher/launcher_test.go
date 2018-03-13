package launcher_test

import (
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"testing"

	"github.com/opencontainers/runtime-spec/specs-go"
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

	spec, err := readSpec("tmp/tmp_image")
	if err != nil {
		return err
	}

	mounts := []specs.Mount{
		specs.Mount{"/bin", "bind", "/bin", []string{"bind", "ro"}},
		specs.Mount{"/lib", "bind", "/lib", []string{"bind", "ro"}},
		specs.Mount{"/lib64", "bind", "/lib64", []string{"bind", "ro"}}}

	spec.Mounts = append(spec.Mounts, mounts...)

	if spec.Annotations == nil {
		spec.Annotations = make(map[string]string)
	}

	for i := 1; i <= 5; i++ {
		spec.Annotations["packageName"] = fmt.Sprintf("service%d", i)
		spec.Annotations["version"] = fmt.Sprintf("%d", i)

		if err := writeSpec("tmp/tmp_image", spec); err != nil {
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

func readSpec(imagePath string) (spec specs.Spec, err error) {
	raw, err := ioutil.ReadFile(path.Join(imagePath, "config.json"))
	if err != nil {
		return spec, err
	}

	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, err
	}

	return spec, nil
}

func writeSpec(imagePath string, spec specs.Spec) (err error) {
	f, err := os.Create(path.Join(imagePath, "config.json"))
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "\t")

	if err := encoder.Encode(spec); err != nil {
		return err
	}

	return nil
}

func generateImage(imagePath string) (err error) {
	// create dir
	if err := os.MkdirAll(path.Join(imagePath, "rootfs"), 0755); err != nil {
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

	result.PushBack(launcher.InstallService("tmp/image1.tgz"))
	result.PushBack(launcher.InstallService("tmp/image2.tgz"))
	result.PushBack(launcher.InstallService("tmp/image3.tgz"))

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
	result.PushBack(launcher.InstallService("tmp/image4.tgz"))
	result.PushBack(launcher.RemoveService("service2"))
	result.PushBack(launcher.InstallService("tmp/image5.tgz"))
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

	result.PushBack(launcher.InstallService("tmp/image1.tgz"))
	result.PushBack(launcher.InstallService("tmp/image2.tgz"))
	result.PushBack(launcher.InstallService("tmp/image3.tgz"))
	result.PushBack(launcher.InstallService("tmp/image4.tgz"))
	result.PushBack(launcher.InstallService("tmp/image5.tgz"))

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
}
