package dbushandler_test

import (
	"encoding/json"
	"testing"

	"github.com/godbus/dbus"

	dbusServer "gitpct.epam.com/epmd-aepr/aos_servicemanager/dbushandler"
)

func TestGetPermission(t *testing.T) {
	server, err := dbusServer.New()
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

	obj := conn.Object(dbusServer.IntrfaceName, dbusServer.ObjectPath)

	err = obj.Call(dbusServer.IntrfaceName+".GetPermission", 0, "Service1").Store(&permissionJson, &status)
	if err != nil {
		t.Fatalf("Can't make D-Bus call: %s", err)
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
