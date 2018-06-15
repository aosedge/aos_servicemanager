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
	// ObjectPath object path
	ObjectPath = "/com/epam/aos/vis"
	// IntrfaceName insterface name
	IntrfaceName = "com.epam.aos.vis"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// DBusHandler d-bus interface structure
type DBusHandler struct {
	// TODO: add reference to internal DB
	dbusConn *dbus.Conn
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates and launch d-bus server
func New() (dbusHandler *DBusHandler, err error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return dbusHandler, err
	}

	reply, err := conn.RequestName(IntrfaceName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return dbusHandler, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return dbusHandler, errors.New("D-Bus name already taken")
	}

	log.Debug("Start D-Bus server")

	server := DBusHandler{dbusConn: conn}

	// TODO: add introspect
	conn.Export(server, ObjectPath, IntrfaceName)

	dbusHandler = &server

	return dbusHandler, nil
}

// Close closes d-bus server
func (dbusHandler *DBusHandler) Close() (err error) {
	log.Debug("Close D-Bus server")

	reply, err := dbusHandler.dbusConn.ReleaseName(IntrfaceName)
	if err != nil {
		return err
	}
	if reply != dbus.ReleaseNameReplyReleased {
		return errors.New("Can't release D-Bus interface name")
	}

	return nil
}

/*******************************************************************************
 * D-BUS interface
 ******************************************************************************/

// GetPermission get permossion d-bus method
func (dbusHandler DBusHandler) GetPermission(token string) (result, status string, err *dbus.Error) {
	log.WithField("token", token).Debug("Get permissions")
	//TODO: make select from DB
	return `{"*": "rw", "123": "rw"}`, "OK", nil
}
