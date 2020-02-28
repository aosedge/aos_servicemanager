package nuanceidentifier_test

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"reflect"
	"testing"

	log "github.com/sirupsen/logrus"

	"aos_servicemanager/identification/nuanceidentifier"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	tmpDir       = "/tmp/aos"
	systemIDFile = "systemid.data"
	usersFile    = "user_claims.json"
	systemID     = "11111111-222222-3333333"
	usersJson    = `{"claim": ["user1","user2"]}`
)

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
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		log.Fatalf("Can't crate tmp dir: %s", err)
	}

	if err := ioutil.WriteFile(path.Join(tmpDir, systemIDFile), []byte(systemID), 0644); err != nil {
		log.Fatalf("Error create a new file: %s", err)
	}

	if err := ioutil.WriteFile(path.Join(tmpDir, usersFile), []byte(usersJson), 0644); err != nil {
		log.Fatalf("Error create a new file: %s", err)
	}

	jsonString := fmt.Sprintf(`{"SystemIDFile": "%s", "UsersFile": "%s"}`,
		path.Join(tmpDir, systemIDFile), path.Join(tmpDir, usersFile))
	if nuance, err = nuanceidentifier.New([]byte(jsonString)); err != nil {
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
	var err error

	if err = setup(); err != nil {
		log.Fatalf("Setup error: %s", err)
	}

	ret := m.Run()

	if err = cleanup(); err != nil {
		log.Fatalf("Cleanup error: %s", err)
	}

	if err = os.RemoveAll(tmpDir); err != nil {
		log.Fatalf("Error removing tmp dir: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestGetSystemID(t *testing.T) {
	sysID, err := nuance.GetSystemID()
	if err != nil {
		t.Fatalf("Error getting system ID: %s", err)
	}

	if sysID != systemID {
		t.Fatalf("Wrong system ID value: %s, expect: %s", sysID, systemID)
	}
}

func TestGetUsers(t *testing.T) {
	users, err := nuance.GetUsers()
	if err != nil {
		t.Fatalf("Error getting users: %s", err)
	}

	var usersList struct {
		Claim []string
	}

	err = json.Unmarshal([]byte(usersJson), &usersList)
	if err != nil {
		t.Fatalf("Unmarshal JSON: %v", err)
	}

	if !reflect.DeepEqual(usersList.Claim, users) {
		t.Fatalf("User claims mismatch")
	}
}
