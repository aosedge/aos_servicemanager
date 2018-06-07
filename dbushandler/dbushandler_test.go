package dbushandler_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/godbus/dbus"
	log "github.com/sirupsen/logrus"

	dbusServer "gitpct.epam.com/epmd-aepr/aos_servicemanager/dbushandler"
)

const (
	OBJECT_PATH    = "/com/aosservicemanager/vistoken"
	INTERFACE_NAME = "com.aosservicemanager.vistoken"
	TEST_TOKEN     = "APPUID"
)

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

func TestGetPermission(t *testing.T) {
	log.Debug("[TEST] TestGetPermission")

	server, err := dbusServer.New()
	if server == nil {
		t.Fatalf("Can't create D-BUS server")
		return
	}
	if err != nil {
		t.Fatalf("Can't create D-BUS server %v", err)
		return
	}

	defer server.StopServer()

	var permissionJson string
	var dbusErr string
	var permissions map[string]string

	conn, err := dbus.SessionBus()
	if err != nil {
		t.Fatalf("No session bus conn %v", err)
		return
	}

	obj := conn.Object(INTERFACE_NAME, OBJECT_PATH)

	err = obj.Call(INTERFACE_NAME+".GetPermission", 0, TEST_TOKEN).Store(&permissionJson, &dbusErr)
	if err != nil {
		t.Fatalf("Can't make d-bus call %v", err)
		return
	}

	err = json.Unmarshal([]byte(permissionJson), &permissions)
	if err != nil {
		t.Fatalf("Error Unmarshal  %v", err)
		return
	}

	log.Info("[TEST]: ", permissionJson)

	if len(permissions) != 2 {
		t.Fatalf("Permission list length !=2 ")
		return
	}

	if permissions["*"] != "rw" {
		t.Fatalf("Incorrect permission")
		return
	}
}
