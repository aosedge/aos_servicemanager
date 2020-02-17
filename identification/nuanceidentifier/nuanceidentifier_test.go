package nuanceidentifier_test

import (
	"os"
	"testing"

	log "github.com/sirupsen/logrus"

	"aos_servicemanager/identification/nuanceidentifier"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

/*******************************************************************************
 * Types
 ******************************************************************************/

/*******************************************************************************
 * Vars
 ******************************************************************************/

var nuance *nuanceidentifier.Instance

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
	if nuance, err = nuanceidentifier.New([]byte(`{"systemID":"123456", "users":["user1"]}`)); err != nil {
		return err
	}
	defer nuance.Close()

	return nil
}

func cleanup() (err error) {
	return nil
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Setup error: %s", err)
	}

	ret := m.Run()

	if err := cleanup(); err != nil {
		log.Fatalf("Cleanup error: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestGetSystemID(t *testing.T) {
	systemID, err := nuance.GetSystemID()
	if err != nil {
		t.Fatalf("Error getting system ID: %s", err)
	}

	if systemID != "123456" {
		t.Fatalf("Wrong system ID value: %s", systemID)
	}
}

func TestGetUsers(t *testing.T) {
	users, err := nuance.GetUsers()
	if err != nil {
		t.Fatalf("Error getting users: %s", err)
	}

	if users[0] != "user1" {
		t.Fatalf("Wrong users value: %s", users)
	}
}
