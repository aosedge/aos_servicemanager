package dbushandler

import (
	"errors"

	"github.com/godbus/dbus"
	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
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
	db       *database.Database
	dbusConn *dbus.Conn
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates and launch d-bus server
func New(db *database.Database) (dbusHandler *DBusHandler, err error) {
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

	server := DBusHandler{dbusConn: conn, db: db}

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
func (dbusHandler DBusHandler) GetPermission(token string) (result, status string, dbusErr *dbus.Error) {
	service, err := dbusHandler.db.GetService(token)
	if err != nil {
		return "", err.Error(), nil
	}

	log.WithFields(log.Fields{"token": token, "perm": service.Permissions}).Debug("Get permissions")

	return service.Permissions, "OK", nil
}
