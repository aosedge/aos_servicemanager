// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3" //ignore lint
	log "github.com/sirupsen/logrus"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/launcher"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	busyTimeout = 60000
	journalMode = "WAL"
	syncMode    = "NORMAL"
)

const installedStatus = "installed"

/*******************************************************************************
 * Vars
 ******************************************************************************/

// ErrNotExist is returned when requested entry not exist in DB
var ErrNotExist = errors.New("entry does not exist")

/*******************************************************************************
 * Types
 ******************************************************************************/

// Database structure with database information
type Database struct {
	sql *sql.DB
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

	sqlite, err := sql.Open("sqlite3", fmt.Sprintf("%s?_busy_timeout=%d&_journal_mode=%s&_sync=%s",
		name, busyTimeout, journalMode, syncMode))
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
	if err := db.createLayersTable(); err != nil {
		return db, err
	}

	return db, nil
}

// GetOperationVersion returns operation version
func (db *Database) GetOperationVersion() (version uint64, err error) {
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

// SetOperationVersion sets operation version
func (db *Database) SetOperationVersion(version uint64) (err error) {
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

// AddService adds new service
func (db *Database) AddService(service launcher.Service) (err error) {
	stmt, err := db.sql.Prepare("INSERT INTO services values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(service.ID, service.Version, service.Path, service.UnitName,
		service.UserName, service.Permissions, service.State, service.Status, service.StartAt, service.TTL,
		service.AlertRules, service.UploadLimit, service.DownloadLimit, service.StorageLimit, service.StateLimit)

	return err
}

// UpdateService updates service
func (db *Database) UpdateService(service launcher.Service) (err error) {
	stmt, err := db.sql.Prepare(`UPDATE services
								 SET version = ?, path = ?, unit = ?, user = ?,
								 permissions = ?, state = ?, status = ?, startat = ?,
								 ttl = ?, alertRules = ?, ulLimit = ?, dlLimit = ?,
								 storageLimit = ?, stateLimit = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	result, err := stmt.Exec(service.Version, service.Path, service.UnitName, service.UserName, service.Permissions,
		service.State, service.Status, service.StartAt, service.TTL, service.AlertRules, service.UploadLimit, service.DownloadLimit,
		service.StorageLimit, service.StateLimit, service.ID)
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

// RemoveService removes existing service
func (db *Database) RemoveService(serviceID string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM services WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(serviceID)

	return err
}

// GetService returns service by service ID
func (db *Database) GetService(serviceID string) (service launcher.Service, err error) {
	stmt, err := db.sql.Prepare("SELECT * FROM services WHERE id = ?")
	if err != nil {
		return service, err
	}
	defer stmt.Close()

	err = stmt.QueryRow(serviceID).Scan(&service.ID, &service.Version, &service.Path, &service.UnitName,
		&service.UserName, &service.Permissions, &service.State, &service.Status,
		&service.StartAt, &service.TTL, &service.AlertRules, &service.UploadLimit, &service.DownloadLimit,
		&service.StorageLimit, &service.StateLimit)
	if err == sql.ErrNoRows {
		return service, ErrNotExist
	}
	if err != nil {
		return service, err
	}

	return service, nil
}

// GetServices returns all services
func (db *Database) GetServices() (services []launcher.Service, err error) {
	rows, err := db.sql.Query("SELECT * FROM services")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var service launcher.Service

		err = rows.Scan(&service.ID, &service.Version, &service.Path, &service.UnitName,
			&service.UserName, &service.Permissions, &service.State, &service.Status,
			&service.StartAt, &service.TTL, &service.AlertRules, &service.UploadLimit, &service.DownloadLimit,
			&service.StorageLimit, &service.StateLimit)
		if err != nil {
			return services, err
		}

		services = append(services, service)
	}

	return services, rows.Err()
}

// GetServiceByUnitName returns service by systemd unit name
func (db *Database) GetServiceByUnitName(unitName string) (service launcher.Service, err error) {
	stmt, err := db.sql.Prepare("SELECT * FROM services WHERE unit = ?")
	if err != nil {
		return service, err
	}
	defer stmt.Close()

	err = stmt.QueryRow(unitName).Scan(&service.ID, &service.Version, &service.Path, &service.UnitName,
		&service.UserName, &service.Permissions, &service.State, &service.Status,
		&service.StartAt, &service.TTL, &service.AlertRules, &service.UploadLimit, &service.DownloadLimit,
		&service.StorageLimit, &service.StateLimit)
	if err == sql.ErrNoRows {
		return service, ErrNotExist
	}
	if err != nil {
		return service, err
	}

	return service, nil
}

// SetServiceStatus sets service status
func (db *Database) SetServiceStatus(serviceID string, status launcher.ServiceStatus) (err error) {
	stmt, err := db.sql.Prepare("UPDATE services SET status = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	result, err := stmt.Exec(status, serviceID)
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
func (db *Database) SetServiceState(serviceID string, state launcher.ServiceState) (err error) {
	stmt, err := db.sql.Prepare("UPDATE services SET state = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	result, err := stmt.Exec(state, serviceID)
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
func (db *Database) SetServiceStartTime(serviceID string, time time.Time) (err error) {
	stmt, err := db.sql.Prepare("UPDATE services SET startat = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	result, err := stmt.Exec(time, serviceID)
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

// AddServiceToUsers adds service ID to users
func (db *Database) AddServiceToUsers(users []string, serviceID string) (err error) {
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

// RemoveServiceFromUsers removes service ID from users
func (db *Database) RemoveServiceFromUsers(users []string, serviceID string) (err error) {
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

// GetUsersService returns users service
func (db *Database) GetUsersService(users []string, serviceID string) (usersService launcher.UsersService, err error) {
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return usersService, err
	}

	rows, err := db.sql.Query("SELECT storageFolder, stateCheckSum FROM users WHERE users = ? AND serviceid = ?",
		usersJSON, serviceID)
	if err != nil {
		return usersService, err
	}
	defer rows.Close()

	for rows.Next() {
		if err = rows.Scan(&usersService.StorageFolder, &usersService.StateChecksum); err != nil {
			return usersService, err
		}

		usersService.Users = users
		usersService.ServiceID = serviceID

		return usersService, nil
	}

	return usersService, ErrNotExist
}

// GetUsersServicesByServiceID returns users services by service ID
func (db *Database) GetUsersServicesByServiceID(serviceID string) (usersServices []launcher.UsersService, err error) {
	rows, err := db.sql.Query("SELECT users, storageFolder, stateCheckSum FROM users WHERE serviceid = ?", serviceID)
	if err != nil {
		return usersServices, err
	}
	defer rows.Close()

	for rows.Next() {
		usersService := launcher.UsersService{ServiceID: serviceID}
		usersJSON := []byte{}

		if err = rows.Scan(&usersJSON, &usersService.StorageFolder, &usersService.StateChecksum); err != nil {
			return usersServices, err
		}

		if err = json.Unmarshal(usersJSON, &usersService.Users); err != nil {
			return usersServices, err
		}

		usersServices = append(usersServices, usersService)
	}

	return usersServices, rows.Err()
}

// GetUsersServices returns list of users services
func (db *Database) GetUsersServices(users []string) (usersServices []launcher.Service, err error) {
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return nil, err
	}

	rows, err := db.sql.Query("SELECT * FROM services WHERE id IN (SELECT serviceid FROM users WHERE users = ?)", usersJSON)
	if err != nil {
		return usersServices, err
	}
	defer rows.Close()

	for rows.Next() {
		var service launcher.Service

		err = rows.Scan(&service.ID, &service.Version, &service.Path, &service.UnitName,
			&service.UserName, &service.Permissions, &service.State, &service.Status,
			&service.StartAt, &service.TTL, &service.AlertRules, &service.UploadLimit, &service.DownloadLimit,
			&service.StorageLimit, &service.StateLimit)
		if err != nil {
			return usersServices, err
		}

		usersServices = append(usersServices, service)
	}

	return usersServices, rows.Err()
}

// RemoveServiceFromAllUsers removes service from all users
func (db *Database) RemoveServiceFromAllUsers(serviceID string) (err error) {
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

// SetUpgradeState stores upgrade state
func (db *Database) SetUpgradeState(state int) (err error) {
	result, err := db.sql.Exec("UPDATE config SET upgradeState = ?", state)
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

// GetUpgradeState returns upgrade state
func (db *Database) GetUpgradeState() (state int, err error) {
	stmt, err := db.sql.Prepare("SELECT upgradeState FROM config")
	if err != nil {
		return state, err
	}
	defer stmt.Close()

	err = stmt.QueryRow().Scan(&state)
	if err != nil {
		if err == sql.ErrNoRows {
			return state, ErrNotExist
		}

		return state, err
	}

	return state, nil
}

// SetUpgradeData stores upgrade data
func (db *Database) SetUpgradeData(data amqp.SystemUpgrade) (err error) {
	dataJSON, err := json.Marshal(&data)
	if err != nil {
		return err
	}

	result, err := db.sql.Exec("UPDATE config SET upgradeData = ?", dataJSON)
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

// GetUpgradeData returns upgrade data
func (db *Database) GetUpgradeData() (data amqp.SystemUpgrade, err error) {
	stmt, err := db.sql.Prepare("SELECT upgradeData FROM config")
	if err != nil {
		return data, err
	}
	defer stmt.Close()

	var dataJSON []byte

	if err = stmt.QueryRow().Scan(&dataJSON); err != nil {
		if err == sql.ErrNoRows {
			return data, ErrNotExist
		}

		return data, err
	}

	if dataJSON == nil {
		return data, nil
	}

	if err = json.Unmarshal(dataJSON, &data); err != nil {
		return data, err
	}

	return data, nil
}

// SetUpgradeVersion stores upgrade version
func (db *Database) SetUpgradeVersion(version uint64) (err error) {
	result, err := db.sql.Exec("UPDATE config SET upgradeVersion = ?", version)
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

// GetUpgradeVersion returns upgrade version
func (db *Database) GetUpgradeVersion() (version uint64, err error) {
	stmt, err := db.sql.Prepare("SELECT upgradeVersion FROM config")
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	if err = stmt.QueryRow().Scan(&version); err != nil {
		if err == sql.ErrNoRows {
			return 0, ErrNotExist
		}

		return 0, err
	}

	return version, nil
}

//AddLayer add layer to layers table
func (db *Database) AddLayer(digest, layerID, path, osVersion string) (err error) {
	stmt, err := db.sql.Prepare("INSERT INTO layers values(?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(digest, layerID, path, osVersion)

	return err
}

//DeleteLayerByDigest remove layer from DB by digest
func (db *Database) DeleteLayerByDigest(digest string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM layers WHERE digest = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(digest)

	return err
}

//GetLayerPathByDigest return layer installation path by digest
func (db *Database) GetLayerPathByDigest(digest string) (path string, err error) {
	stmt, err := db.sql.Prepare("SELECT path FROM layers WHERE digest = ?")
	if err != nil {
		return path, err
	}
	defer stmt.Close()

	err = stmt.QueryRow(digest).Scan(&path)
	if err == sql.ErrNoRows {
		return path, ErrNotExist
	}
	if err != nil {
		return path, err
	}

	return path, nil
}

//GetLayersInfo get all installed layers
func (db *Database) GetLayersInfo() (layersList []amqp.LayerInfo, err error) {
	rows, err := db.sql.Query("SELECT digest, layerId FROM layers ")
	if err != nil {
		return layersList, err
	}
	defer rows.Close()

	for rows.Next() {
		layer := amqp.LayerInfo{Status: installedStatus}

		if err = rows.Scan(&layer.Digest, &layer.LayerID); err != nil {
			return layersList, err
		}

		layersList = append(layersList, layer)
	}

	return layersList, rows.Err()
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

func (db *Database) createConfigTable() (err error) {
	log.Info("Create config table")

	exist, err := db.isTableExist("config")
	if err != nil {
		return err
	}

	if exist {
		return nil
	}

	if _, err = db.sql.Exec(
		`CREATE TABLE config (
			version INTEGER,
			cursor TEXT,
			upgradeState INTEGER,
			upgradeData BLOB,
			upgradeVersion INTEGER)`); err != nil {
		return err
	}

	if _, err = db.sql.Exec(
		`INSERT INTO config (
			version,
			cursor,
			upgradeState,
			upgradeData,
			upgradeVersion) values(?, ?, ?, ?, ?)`, 0, "", 0, []byte{}, 3); err != nil {
		return err
	}

	return nil
}

func (db *Database) createServiceTable() (err error) {
	log.Info("Create service table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS services (id TEXT NOT NULL PRIMARY KEY,
															   version INTEGER,
															   path TEXT,
															   unit TEXT,
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

func (db *Database) createLayersTable() (err error) {
	log.Info("Create layers table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS layers (digest TEXT NOT NULL PRIMARY KEY,
															 layerId TEXT,
															 path TEXT,
															 osVersion TEXT	)`)

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
