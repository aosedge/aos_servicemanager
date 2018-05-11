package dbushandler

import (
	"errors"
	"github.com/godbus/dbus"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/
const (
	ObjectPath   = "/com/aosservicemanager/vistoken"
	IntrfaceName = "com.aosservicemanager.vistoken"
)

/*******************************************************************************
 * Types
 ******************************************************************************/
type AosDbusInterface struct {
	//TODO: add reference to internal DB
	dbusConn *dbus.Conn
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates and launch d-bus server
func New() (dbusHandler *AosDbusInterface, err error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		log.Error("Can't create session dbus connection %v", err)
		return dbusHandler, err
	}
	reply, err := conn.RequestName(IntrfaceName, dbus.NameFlagDoNotQueue)
	if err != nil {
		log.Error("Can't reques dbus inerface name ", err)
		return dbusHandler, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Error("D-Bus name already taken")
		return dbusHandler, errors.New("D-Bus name already taken")
	}

	log.Info("Start D-BUS server")
	server := AosDbusInterface{dbusConn: conn}
	conn.Export(server, ObjectPath, IntrfaceName)

	dbusHandler = &server
	return dbusHandler, nil
}

func (dbusHandler *AosDbusInterface) StopServer() error {
	log.Info("Stop d-bus server")
	reply, err := dbusHandler.dbusConn.ReleaseName(IntrfaceName)
	if err != nil {
		log.Error("Error release dbus interface name", err)
		return err
	}
	if reply != dbus.ReleaseNameReplyReleased {
		log.Error("Error release dbus interface name ")
		return errors.New("Error release dbus interface name")
	}
	return nil
}

/*******************************************************************************
 * D-BUS interface
 ******************************************************************************/

func (server AosDbusInterface) GetPermission(token string) (string, string, *dbus.Error) {
	log.Info("GetPermission token: ", token)
	//TODO: make select from DB
	return `{"*": "rw", "123": "rw"}`, "OK", nil
}
