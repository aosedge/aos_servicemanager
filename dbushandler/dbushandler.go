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
	OBJECT_PATH    = "/com/aosservicemanager/vistoken"
	INTERFACE_NAME = "com.aosservicemanager.vistoken"
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
		log.Error("can't Create session connection %v", err)
		return dbusHandler, err
	}
	reply, err := conn.RequestName(INTERFACE_NAME, dbus.NameFlagDoNotQueue)
	if err != nil {
		log.Error("can't RequestName ", err)
		return dbusHandler, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Error("D-Bus name already taken")
		return dbusHandler, errors.New("D-Bus name already taken")
	}

	log.Info("Start D-BUS server")
	server := AosDbusInterface{dbusConn: conn}
	conn.Export(server, OBJECT_PATH, INTERFACE_NAME)

	dbusHandler = &server
	return dbusHandler, nil
}
func (dbusHandler *AosDbusInterface) StopServer() error {
	log.Info("Stop d-bus server")
	reply, err := dbusHandler.dbusConn.ReleaseName(INTERFACE_NAME)
	if err != nil {
		log.Error("ReleaseName error ", err)
		return err
	}
	if reply != dbus.ReleaseNameReplyReleased {
		log.Error("ReleaseName error ")
		return errors.New("ReleaseName error")
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
