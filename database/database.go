package database

import (
	"database/sql"
	"errors"

	_ "github.com/mattn/go-sqlite3" //ignore lint
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

//Database structure with database information
type Database struct {
	sql *sql.DB
}

//ServiceEntry describes entry structure
type ServiceEntry struct {
	ID          string // service id
	Version     uint   // service version
	Path        string // path to service bundle
	ServiceName string // systemd service name
	UserName    string // user used to run this service
	Permissions string // VIS permissions
	State       int    // service state
	Status      int    // service status
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new database handle
func New(name string) (db *Database, err error) {
	log.WithField("name", name).Debug("Open database")

	sqlite, err := sql.Open("sqlite3", name)
	if err != nil {
		return db, err
	}

	db = &Database{sqlite}

	exist, err := db.isTableExist(serviceTableName)
	if err != nil {
		return db, err
	}

	if !exist {
		log.Warning("Service table doesn't exist. Either it is first start or something bad happened.")
		if err := db.createServiceTable(); err != nil {
			return db, err
		}
	}

	return db, nil
}

// AddService adds new service entry
func (db *Database) AddService(entry ServiceEntry) (err error) {
	stmt, err := db.sql.Prepare("INSERT INTO services values(?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(entry.ID, entry.Version, entry.Path, entry.ServiceName,
		entry.UserName, entry.Permissions, entry.State, entry.Status)

	return err
}

// RemoveService removes existing service entry
func (db *Database) RemoveService(id string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM services WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(id)

	return err
}

// GetService returns service entry
func (db *Database) GetService(id string) (entry ServiceEntry, err error) {
	stmt, err := db.sql.Prepare("SELECT * FROM SERVICES WHERE id = ?")
	if err != nil {
		return entry, err
	}
	defer stmt.Close()

	err = stmt.QueryRow(id).Scan(&entry.ID, &entry.Version, &entry.Path, &entry.ServiceName,
		&entry.UserName, &entry.Permissions, &entry.State, &entry.Status)
	if err == sql.ErrNoRows {
		return entry, errors.New("Service does not exist")
	}
	if err != nil {
		return entry, err
	}

	return entry, nil
}

// GetServices returns all service entries
func (db *Database) GetServices() (entries []ServiceEntry, err error) {
	rows, err := db.sql.Query("SELECT * FROM services")
	if err != nil {
		return entries, err
	}
	defer rows.Close()

	entries = make([]ServiceEntry, 0)

	for rows.Next() {
		var entry ServiceEntry
		err = rows.Scan(&entry.ID, &entry.Version, &entry.Path, &entry.ServiceName,
			&entry.UserName, &entry.Permissions, &entry.State, &entry.Status)
		if err != nil {
			return entries, err
		}
		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

// SetServiceStatus sets service status
func (db *Database) SetServiceStatus(id string, status int) (err error) {
	stmt, err := db.sql.Prepare("UPDATE services SET status = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	result, err := stmt.Exec(status, id)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return errors.New("Service does not exist")
	}

	return err
}

// SetServiceState sets service state
func (db *Database) SetServiceState(id string, state int) (err error) {
	stmt, err := db.sql.Prepare("UPDATE services SET state = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	result, err := stmt.Exec(state, id)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return errors.New("Service does not exist")
	}

	return err
}

// Close closes database
func (db *Database) Close() {
	db.sql.Close()
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (db *Database) isTableExist(name string) (result bool, err error) {
	rows, err := db.sql.Query("SELECT * FROM sqlite_master WHERE name = ? and type='table'", name)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	result = rows.Next()

	return result, rows.Err()
}

func (db *Database) createServiceTable() (err error) {
	log.Info("Create service table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS services (id TEXT NOT NULL PRIMARY KEY,
															   version INTEGER,
															   path TEXT,
															   service TEXT,
															   user TEXT,
															   permissions TEXT,
															   state INTEGER,
															   status INTEGER);`)

	return err
}

func (db *Database) removeAllServices() (err error) {
	_, err = db.sql.Exec("DELETE FROM services;")

	return err
}
