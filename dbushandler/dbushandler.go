package dbushandler

import (
	"errors"

	"github.com/godbus/dbus"
	"github.com/godbus/dbus/introspect"
	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	// ObjectPath object path
	ObjectPath = "/com/epam/aos/vis"
	// InterfaceName interface name
	InterfaceName = "com.epam.aos.vis"
)

const intro = `
<node>
	<interface name="com.epam.aos.vis">
		<method name="GetPermissions">
			<arg name="token" direction="in" type="s">
				<doc:doc><doc:summary>VIS client token (service id)</doc:summary></doc:doc>
			</arg>
			<arg name="permissions" direction="out" type="s">
				<doc:doc><doc:summary>VIS client permissions</doc:summary></doc:doc>
			</arg>
			<arg name="status" direction="out" type="s">
				<doc:doc><doc:summary>Status of getting VIS permissions: OK or error</doc:summary></doc:doc>
			</arg>
			<doc:doc>
				<doc:description>
				<doc:para>
					Returns VIS client permission
				</doc:para>
				</doc:description>
			</doc:doc>
		</method>
	</interface>` + introspect.IntrospectDataString + `</node> `

/*******************************************************************************
 * Types
 ******************************************************************************/

// ServiceProvider provides service entry
type ServiceProvider interface {
	GetService(id string) (entry database.ServiceEntry, err error)
}

// DBusHandler d-bus interface structure
type DBusHandler struct {
	serviceProvider ServiceProvider
	dbusConn        *dbus.Conn
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates and launch d-bus server
func New(serviceProvider ServiceProvider) (dbusHandler *DBusHandler, err error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return dbusHandler, err
	}

	reply, err := conn.RequestName(InterfaceName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return dbusHandler, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return dbusHandler, errors.New("D-Bus name already taken")
	}

	log.Debug("Start D-Bus server")

	server := DBusHandler{dbusConn: conn, serviceProvider: serviceProvider}

	conn.Export(server, ObjectPath, InterfaceName)
	conn.Export(introspect.Introspectable(intro), ObjectPath,
		"org.freedesktop.DBus.Introspectable")

	dbusHandler = &server

	return dbusHandler, nil
}

// Close closes d-bus server
func (dbusHandler *DBusHandler) Close() (err error) {
	log.Debug("Close D-Bus server")

	reply, err := dbusHandler.dbusConn.ReleaseName(InterfaceName)
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
	service, err := dbusHandler.serviceProvider.GetService(token)
	if err != nil {
		return "", err.Error(), nil
	}

	log.WithFields(log.Fields{"token": token, "perm": service.Permissions}).Debug("Get permissions")

	return service.Permissions, "OK", nil
}
