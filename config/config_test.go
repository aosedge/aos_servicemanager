package config_test

import (
	"io/ioutil"
	"log"
	"os"
	"path"
	"testing"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
)

/*******************************************************************************
 * Private
 ******************************************************************************/

func createConfigFile() (err error) {
	configContent := `{
	"fcrypt" : {
		"CACert" : "CACert",
		"ClientCert" : "ClientCert",
		"ClientKey" : "ClientKey",
		"OfflinePrivKey" : "OfflinePrivKey",
		"OfflineCert" : "OfflineCert"	
	},
	"serviceDiscovery" : "www.aos.com",
	"workingDir" : "workingDir"
}`

	if err := ioutil.WriteFile(path.Join("tmp", "aos_servicemanager.cfg"), []byte(configContent), 0644); err != nil {
		return err
	}

	return nil
}

func setup() (err error) {
	if err := os.MkdirAll("tmp", 0755); err != nil {
		return err
	}

	if err = createConfigFile(); err != nil {
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

func TestGetCrypt(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.Crypt.CACert != "CACert" {
		t.Errorf("Wrong CACert value: %s", config.Crypt.CACert)
	}

	if config.Crypt.ClientCert != "ClientCert" {
		t.Errorf("Wrong ClientCert value: %s", config.Crypt.ClientCert)
	}

	if config.Crypt.ClientKey != "ClientKey" {
		t.Errorf("Wrong ClientKey value: %s", config.Crypt.ClientKey)
	}

	if config.Crypt.OfflinePrivKey != "OfflinePrivKey" {
		t.Errorf("Wrong OfflinePrivKey value: %s", config.Crypt.OfflinePrivKey)
	}

	if config.Crypt.OfflineCert != "OfflineCert" {
		t.Errorf("Wrong OfflineCert value: %s", config.Crypt.OfflineCert)
	}
}

func TestGetServerURL(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.ServiceDiscoveryURL != "www.aos.com" {
		t.Errorf("Wrong server URL value: %s", config.ServiceDiscoveryURL)
	}
}

func TestGetWorkingDir(t *testing.T) {
	config, err := config.New("tmp/aos_servicemanager.cfg")
	if err != nil {
		t.Fatalf("Error opening config file: %s", err)
	}

	if config.WorkingDir != "workingDir" {
		t.Errorf("Wrong workingDir value: %s", config.WorkingDir)
	}
}
