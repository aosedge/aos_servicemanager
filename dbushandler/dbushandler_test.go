package dbushandler_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/godbus/dbus"
	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
	dbusServer "gitpct.epam.com/epmd-aepr/aos_servicemanager/dbushandler"
)

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
	db.Close()

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

func TestGetPermission(t *testing.T) {
	if err := db.AddService(database.ServiceEntry{ID: "Service1",
		Permissions: `{"*": "rw", "123": "rw"}`}); err != nil {
		t.Fatalf("Can't add service: %s", err)
	}

	server, err := dbusServer.New(db)
	if err != nil {
		t.Fatalf("Can't create D-Bus server: %s", err)
	}
	defer server.Close()

	conn, err := dbus.SessionBus()
	if err != nil {
		t.Fatalf("Can't connect to session bus: %s", err)
	}

	var (
		permissionJson string
		status         string
		permissions    map[string]string
	)

	obj := conn.Object(dbusServer.InterfaceName, dbusServer.ObjectPath)

	err = obj.Call(dbusServer.InterfaceName+".GetPermission", 0, "Service1").Store(&permissionJson, &status)
	if err != nil {
		t.Fatalf("Can't make D-Bus call: %s", err)
	}

	if strings.ToUpper(status) != "OK" {
		t.Fatalf("Can't get permissions: %s", status)
	}

	err = json.Unmarshal([]byte(permissionJson), &permissions)
	if err != nil {
		t.Fatalf("Can't decode permissions: %s", err)
	}

	if len(permissions) != 2 {
		t.Fatal("Permission list length !=2")
	}

	if permissions["*"] != "rw" {
		t.Fatal("Incorrect permission")
	}
}
