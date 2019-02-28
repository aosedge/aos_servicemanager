package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3" //ignore lint
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	dbVersion = 2
)

/*******************************************************************************
 * Vars
 ******************************************************************************/

// ErrNotExist is returned when requested entry not exist in DB
var ErrNotExist = errors.New("Entry doesn't not exist")

// ErrVersionMismatch is returned when DB has unsupported DB version
var ErrVersionMismatch = errors.New("Version mismatch")

/*******************************************************************************
 * Types
 ******************************************************************************/

// Database structure with database information
type Database struct {
	sql *sql.DB
}

// ServiceEntry describes entry structure
type ServiceEntry struct {
	ID            string    // service id
	Version       uint64    // service version
	Path          string    // path to service bundle
	ServiceName   string    // systemd service name
	UserName      string    // user used to run this service
	Permissions   string    // VIS permissions
	State         int       // service state
	Status        int       // service status
	StartAt       time.Time // time at which service was started
	TTL           uint64    // expiration service duration in days
	AlertRules    string    // alert rules in json format
	UploadLimit   uint64    // upload traffic limit
	DownloadLimit uint64    // download traffic limit
	StorageLimit  uint64    // storage limit
	StateLimit    uint64    // state limit
}

// UsersEntry describes users entry structure
type UsersEntry struct {
	Users         []string
	ServiceID     string
	StorageFolder string
	StateChecksum []byte
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new database handle
func New(name string) (db *Database, err error) {
	log.WithField("name", name).Debug("Open database")

	// Check and create db path
	if _, err = os.Stat(filepath.Dir(name)); err != nil {
		if !os.IsNotExist(err) {
			return db, err
		}
		if err = os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return db, err
		}
	}

	sqlite, err := sql.Open("sqlite3", name)
	if err != nil {
		return db, err
	}

	db = &Database{sqlite}

	if err := db.createConfigTable(); err != nil {
		return db, err
	}
	if err := db.createServiceTable(); err != nil {
		return db, err
	}
	if err := db.createUsersTable(); err != nil {
		return db, err
	}
	if err := db.createTrafficMonitorTable(); err != nil {
		return db, err
	}

	version, err := db.getVersion()
	if err != nil {
		return db, err
	}

	if version != dbVersion {
		return db, ErrVersionMismatch
	}

	return db, nil
}

// AddService adds new service entry
func (db *Database) AddService(entry ServiceEntry) (err error) {
	stmt, err := db.sql.Prepare("INSERT INTO services values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(entry.ID, entry.Version, entry.Path, entry.ServiceName,
		entry.UserName, entry.Permissions, entry.State, entry.Status, entry.StartAt, entry.TTL,
		entry.AlertRules, entry.UploadLimit, entry.DownloadLimit, entry.StorageLimit, entry.StateLimit)

	return err
}

// UpdateService updates service entry
func (db *Database) UpdateService(entry ServiceEntry) (err error) {
	stmt, err := db.sql.Prepare(`UPDATE services
								 SET version = ?, path = ?, service = ?, user = ?,
								 permissions = ?, state = ?, status = ?, startat = ?,
								 ttl = ?, alertRules = ?, ulLimit = ?, dlLimit = ?,
								 storageLimit = ?, stateLimit = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	result, err := stmt.Exec(entry.Version, entry.Path, entry.ServiceName, entry.UserName, entry.Permissions,
		entry.State, entry.Status, entry.StartAt, entry.TTL, entry.AlertRules, entry.UploadLimit, entry.DownloadLimit,
		entry.StorageLimit, entry.StateLimit, entry.ID)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return ErrNotExist
	}

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
	stmt, err := db.sql.Prepare("SELECT * FROM services WHERE id = ?")
	if err != nil {
		return entry, err
	}
	defer stmt.Close()

	err = stmt.QueryRow(id).Scan(&entry.ID, &entry.Version, &entry.Path, &entry.ServiceName,
		&entry.UserName, &entry.Permissions, &entry.State, &entry.Status,
		&entry.StartAt, &entry.TTL, &entry.AlertRules, &entry.UploadLimit, &entry.DownloadLimit,
		&entry.StorageLimit, &entry.StateLimit)
	if err == sql.ErrNoRows {
		return entry, ErrNotExist
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
			&entry.UserName, &entry.Permissions, &entry.State, &entry.Status,
			&entry.StartAt, &entry.TTL, &entry.AlertRules, &entry.UploadLimit, &entry.DownloadLimit,
			&entry.StorageLimit, &entry.StateLimit)
		if err != nil {
			return entries, err
		}
		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

// GetServiceByServiceName returns service entry by service name
func (db *Database) GetServiceByServiceName(serviceName string) (entry ServiceEntry, err error) {
	stmt, err := db.sql.Prepare("SELECT * FROM services WHERE service = ?")
	if err != nil {
		return entry, err
	}
	defer stmt.Close()

	err = stmt.QueryRow(serviceName).Scan(&entry.ID, &entry.Version, &entry.Path, &entry.ServiceName,
		&entry.UserName, &entry.Permissions, &entry.State, &entry.Status,
		&entry.StartAt, &entry.TTL, &entry.AlertRules, &entry.UploadLimit, &entry.DownloadLimit,
		&entry.StorageLimit, &entry.StateLimit)
	if err == sql.ErrNoRows {
		return entry, ErrNotExist
	}
	if err != nil {
		return entry, err
	}

	return entry, nil
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
		return ErrNotExist
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
		return ErrNotExist
	}

	return err
}

// SetServiceStartTime sets service start time
func (db *Database) SetServiceStartTime(id string, time time.Time) (err error) {
	stmt, err := db.sql.Prepare("UPDATE services SET startat = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	result, err := stmt.Exec(time, id)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return ErrNotExist
	}

	return err
}

// AddUsersService adds service ID to users
func (db *Database) AddUsersService(users []string, serviceID string) (err error) {
	stmt, err := db.sql.Prepare("INSERT INTO users values(?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	usersJSON, err := json.Marshal(users)
	if err != nil {
		return err
	}

	_, err = stmt.Exec(usersJSON, serviceID, "", []byte{})

	return err
}

// RemoveUsersService removes service ID from users
func (db *Database) RemoveUsersService(users []string, serviceID string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM users WHERE users = ? AND serviceid = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	usersJSON, err := json.Marshal(users)
	if err != nil {
		return err
	}

	_, err = stmt.Exec(usersJSON, serviceID)

	return err
}

// SetUsersStorageFolder sets users storage folder
func (db *Database) SetUsersStorageFolder(users []string, serviceID string, storageFolder string) (err error) {
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return err
	}

	result, err := db.sql.Exec("UPDATE users SET storageFolder = ? WHERE users = ? AND serviceid = ?",
		storageFolder, usersJSON, serviceID)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return ErrNotExist
	}

	return nil
}

// SetUsersStateChecksum sets users state checksum
func (db *Database) SetUsersStateChecksum(users []string, serviceID string, checksum []byte) (err error) {
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return err
	}

	result, err := db.sql.Exec("UPDATE users SET stateCheckSum = ? WHERE users = ? AND serviceid = ?",
		checksum, usersJSON, serviceID)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return ErrNotExist
	}

	return nil
}

// GetUsersEntry returns users entry
func (db *Database) GetUsersEntry(users []string, serviceID string) (entry UsersEntry, err error) {
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return entry, err
	}

	rows, err := db.sql.Query("SELECT storageFolder, stateCheckSum FROM users WHERE users = ? AND serviceid = ?",
		usersJSON, serviceID)
	if err != nil {
		return entry, err
	}
	defer rows.Close()

	for rows.Next() {
		if err = rows.Scan(&entry.StorageFolder, &entry.StateChecksum); err != nil {
			return entry, err
		}

		entry.Users = users
		entry.ServiceID = serviceID

		return entry, nil
	}

	return entry, ErrNotExist
}

// GetUsersEntriesByServiceID returns users entry by service ID
func (db *Database) GetUsersEntriesByServiceID(serviceID string) (entries []UsersEntry, err error) {
	rows, err := db.sql.Query("SELECT users, storageFolder, stateCheckSum FROM users WHERE serviceid = ?", serviceID)
	if err != nil {
		return entries, err
	}
	defer rows.Close()

	entries = make([]UsersEntry, 0, 10)

	for rows.Next() {
		entry := UsersEntry{ServiceID: serviceID}
		usersJSON := []byte{}

		if err = rows.Scan(&usersJSON, &entry.StorageFolder, &entry.StateChecksum); err != nil {
			return entries, err
		}

		if err = json.Unmarshal(usersJSON, &entry.Users); err != nil {
			return entries, err
		}

		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

// GetUsersServices returns list of users service entries
func (db *Database) GetUsersServices(users []string) (entries []ServiceEntry, err error) {
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return nil, err
	}

	rows, err := db.sql.Query("SELECT * FROM services WHERE id IN (SELECT serviceid FROM users WHERE users = ?)", usersJSON)
	if err != nil {
		return entries, err
	}
	defer rows.Close()

	entries = make([]ServiceEntry, 0)

	for rows.Next() {
		var entry ServiceEntry
		err = rows.Scan(&entry.ID, &entry.Version, &entry.Path, &entry.ServiceName,
			&entry.UserName, &entry.Permissions, &entry.State, &entry.Status,
			&entry.StartAt, &entry.TTL, &entry.AlertRules, &entry.UploadLimit, &entry.DownloadLimit,
			&entry.StorageLimit, &entry.StateLimit)
		if err != nil {
			return entries, err
		}
		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

// IsUsersService returns true if service id belongs to current users
func (db *Database) IsUsersService(users []string, id string) (result bool, err error) {
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return result, err
	}

	rows, err := db.sql.Query("SELECT * FROM users WHERE users = ? AND serviceid = ?", usersJSON, id)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	if rows.Next() {
		return true, rows.Err()
	}

	return false, rows.Err()
}

// GetUsersList returns list of all users
func (db *Database) GetUsersList() (usersList [][]string, err error) {
	rows, err := db.sql.Query("SELECT DISTINCT users FROM users")
	if err != nil {
		return usersList, err
	}
	defer rows.Close()

	usersList = make([][]string, 0)

	for rows.Next() {
		var usersJSON []byte
		err = rows.Scan(&usersJSON)
		if err != nil {
			return usersList, err
		}

		var users []string

		if err = json.Unmarshal(usersJSON, &users); err != nil {
			return usersList, err
		}

		usersList = append(usersList, users)
	}

	return usersList, rows.Err()
}

// DeleteUsersByServiceID deletes users by service ID
func (db *Database) DeleteUsersByServiceID(serviceID string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM users WHERE serviceid = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(serviceID)

	return err
}

// SetTrafficMonitorData stores traffic monitor data
func (db *Database) SetTrafficMonitorData(chain string, timestamp time.Time, value uint64) (err error) {
	result, err := db.sql.Exec("UPDATE trafficmonitor SET time = ?, value = ? where chain = ?", timestamp, value, chain)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		if _, err = db.sql.Exec("INSERT INTO trafficmonitor VALUES(?, ?, ?)",
			chain, timestamp, value); err != nil {
			return err
		}
	}

	return nil
}

// GetTrafficMonitorData stores traffic monitor data
func (db *Database) GetTrafficMonitorData(chain string) (timestamp time.Time, value uint64, err error) {
	stmt, err := db.sql.Prepare("SELECT time, value FROM trafficmonitor WHERE chain = ?")
	if err != nil {
		return timestamp, value, err
	}
	defer stmt.Close()

	err = stmt.QueryRow(chain).Scan(&timestamp, &value)
	if err == sql.ErrNoRows {
		return timestamp, value, ErrNotExist
	}
	if err != nil {
		return timestamp, value, err
	}

	return timestamp, value, nil
}

// RemoveTrafficMonitorData removes existing traffic monitor entry
func (db *Database) RemoveTrafficMonitorData(chain string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM trafficmonitor WHERE chain = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(chain)

	return err
}

// SetJournalCursor stores system logger cursor
func (db *Database) SetJournalCursor(cursor string) (err error) {
	result, err := db.sql.Exec("UPDATE config SET cursor = ?", cursor)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return ErrNotExist
	}

	return nil
}

// GetJournalCursor retrieves logger cursor
func (db *Database) GetJournalCursor() (cursor string, err error) {
	stmt, err := db.sql.Prepare("SELECT cursor FROM config")
	if err != nil {
		return cursor, err
	}
	defer stmt.Close()

	err = stmt.QueryRow().Scan(&cursor)
	if err != nil {
		if err == sql.ErrNoRows {
			return cursor, ErrNotExist
		}

		return cursor, err
	}

	return cursor, nil
}

// Close closes database
func (db *Database) Close() {
	db.sql.Close()
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (db *Database) getVersion() (version uint64, err error) {
	stmt, err := db.sql.Prepare("SELECT version FROM config")
	if err != nil {
		return version, err
	}
	defer stmt.Close()

	err = stmt.QueryRow().Scan(&version)
	if err != nil {
		if err == sql.ErrNoRows {
			return version, ErrNotExist
		}

		return version, err
	}

	return version, nil
}

func (db *Database) setVersion(version uint64) (err error) {
	result, err := db.sql.Exec("UPDATE config SET version = ?", version)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return ErrNotExist
	}

	return nil
}

func (db *Database) isTableExist(name string) (result bool, err error) {
	rows, err := db.sql.Query("SELECT * FROM sqlite_master WHERE name = ? and type='table'", name)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	result = rows.Next()

	return result, rows.Err()
}

func (db *Database) createConfigTable() (err error) {
	log.Info("Create config table")

	exist, err := db.isTableExist("config")
	if err != nil {
		return err
	}

	if exist {
		return nil
	}

	if _, err = db.sql.Exec(`CREATE TABLE config (version INTEGER, cursor TEXT);`, dbVersion); err != nil {
		return err
	}

	if _, err = db.sql.Exec("INSERT INTO config (version, cursor) values(?, ?)", dbVersion, ""); err != nil {
		return err
	}

	return nil
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
															   status INTEGER,
															   startat TIMESTAMP,
															   ttl INTEGER,
															   alertRules TEXT,
															   ulLimit INTEGER,
															   dlLimit INTEGER,
															   storageLimit INTEGER,
															   stateLimit INTEGER)`)

	return err
}

func (db *Database) createUsersTable() (err error) {
	log.Info("Create users table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS users (users TEXT NOT NULL,
															serviceid TEXT NOT NULL,
															storageFolder TEXT,
															stateCheckSum BLOB,
															PRIMARY KEY(users, serviceid))`)

	return err
}

func (db *Database) createTrafficMonitorTable() (err error) {
	log.Info("Create traffic monitor table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS trafficmonitor (chain TEXT NOT NULL PRIMARY KEY,
																	 time TIMESTAMP,
																	 value INTEGER)`)

	return err
}

func (db *Database) removeAllServices() (err error) {
	_, err = db.sql.Exec("DELETE FROM services")

	return err
}

func (db *Database) removeAllUsers() (err error) {
	_, err = db.sql.Exec("DELETE FROM users")

	return err
}

func (db *Database) removeAllTrafficMonitor() (err error) {
	_, err = db.sql.Exec("DELETE FROM trafficmonitor")

	return err
}
