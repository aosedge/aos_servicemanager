package launcher

import (
	"database/sql"
	"errors"

	_ "github.com/mattn/go-sqlite3"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	serviceTableName = "services"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type database struct {
	name string
	sql  *sql.DB
}

// serviceEntry describes entry structure
type serviceEntry struct {
	id          string        // service id
	version     uint          // service version
	path        string        // path to service bundle
	serviceName string        // systemd service name
	state       serviceState  // service state
	status      serviceStatus // service status
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// createDatabase creates database instance
func newDatabase(name string) (db *database, err error) {
	log.WithField("name", name).Debug("Open database")

	sqlite, err := sql.Open("sqlite3", name)
	if err != nil {
		return nil, err
	}

	db = &database{name, sqlite}

	exist, err := db.isTableExist(serviceTableName)
	if err != nil {
		return nil, err
	}

	if !exist {
		log.Warning("Service table doesn't exist. Either it is first start or something bad happened.")
		if err := db.createServiceTable(); err != nil {
			return nil, err
		}
	}

	return db, nil
}

// addService adds new service entry
func (db *database) addService(entry serviceEntry) (err error) {
	stmt, err := db.sql.Prepare("INSERT INTO services(id, version, path, service, state, status) values(?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(entry.id, entry.version, entry.path, entry.serviceName, entry.state, entry.status)

	return err
}

// removeService removes existing service entry
func (db *database) removeService(id string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM services WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(id)

	return err
}

// getService returns service entry
func (db *database) getService(id string) (entry serviceEntry, err error) {
	stmt, err := db.sql.Prepare("SELECT * FROM SERVICES WHERE id = ?")
	if err != nil {
		return entry, err
	}
	defer stmt.Close()

	err = stmt.QueryRow(id).Scan(&entry.id, &entry.version, &entry.path, &entry.serviceName, &entry.state, &entry.status)
	if err == sql.ErrNoRows {
		return entry, errors.New("Service does not exist")
	}
	if err != nil {
		return entry, err
	}

	return entry, nil
}

// getServices returns all service entries
func (db *database) getServices() (entries []serviceEntry, err error) {
	rows, err := db.sql.Query("SELECT * FROM services")
	if err != nil {
		return entries, err
	}
	defer rows.Close()

	entries = make([]serviceEntry, 0)

	for rows.Next() {
		var entry serviceEntry
		err = rows.Scan(&entry.id, &entry.version, &entry.path, &entry.serviceName, &entry.state, &entry.status)
		if err != nil {
			return entries, err
		}
		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

// setServiceStatus sets service status
func (db *database) setServiceStatus(id string, status serviceStatus) (err error) {
	stmt, err := db.sql.Prepare("UPDATE services SET status = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(status, id)

	return err
}

// setServiceState sets service state
func (db *database) setServiceState(id string, state serviceState) (err error) {
	stmt, err := db.sql.Prepare("UPDATE services SET state = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(state, id)

	return nil
}

// close closes database
func (db *database) close() {
	db.sql.Close()
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (db *database) isTableExist(name string) (result bool, err error) {
	rows, err := db.sql.Query("SELECT * FROM sqlite_master WHERE name = ? and type='table'", name)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	result = rows.Next()

	return result, rows.Err()
}

func (db *database) createServiceTable() (err error) {
	log.Info("Create service table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS services (id TEXT NOT NULL PRIMARY KEY,
															   version INTEGER,
															   path TEXT,
															   service TEXT,
															   state INTEGER,
															   status INTEGER);`)

	return err
}

func (db *database) removeAllServices() (err error) {
	_, err = db.sql.Exec("DELETE FROM services;")

	return err
}
