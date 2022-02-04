// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2021 Renesas Electronics Corporation.
// Copyright (C) 2021 EPAM Systems, Inc.
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

	"github.com/aoscloud/aos_common/aoserrors"
	pb "github.com/aoscloud/aos_common/api/servicemanager/v1"
	"github.com/aoscloud/aos_common/migration"
	_ "github.com/mattn/go-sqlite3" // ignore lint
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/launcher"
	"github.com/aoscloud/aos_servicemanager/layermanager"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	busyTimeout = 60000
	journalMode = "WAL"
	syncMode    = "NORMAL"
)

const dbVersion = 5

/*******************************************************************************
 * Vars
 ******************************************************************************/

// ErrNotExist is returned when requested entry not exist in DB.
var ErrNotExist = errors.New("entry does not exist")

// ErrMigrationFailed is returned if migration was failed and db returned to the previous state.
var ErrMigrationFailed = errors.New("database migration failed")

/*******************************************************************************
 * Types
 ******************************************************************************/

// Database structure with database information.
type Database struct {
	sql *sql.DB
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new database handle.
func New(name string, migrationPath string, mergedMigrationPath string) (db *Database, err error) {
	if db, err = newDatabase(name, migrationPath, mergedMigrationPath, dbVersion); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return db, nil
}

// GetOperationVersion returns operation version.
func (db *Database) GetOperationVersion() (version uint64, err error) {
	stmt, err := db.sql.Prepare("SELECT operationVersion FROM config")
	if err != nil {
		return version, aoserrors.Wrap(err)
	}
	defer stmt.Close()

	err = stmt.QueryRow().Scan(&version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return version, ErrNotExist
		}

		return version, aoserrors.Wrap(err)
	}

	return version, nil
}

// SetOperationVersion sets operation version.
func (db *Database) SetOperationVersion(version uint64) (err error) {
	result, err := db.sql.Exec("UPDATE config SET operationVersion = ?", version)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if count == 0 {
		return ErrNotExist
	}

	return nil
}

// AddService adds new service.
func (db *Database) AddService(service servicemanager.ServiceInfo) (err error) {
	stmt, err := db.sql.Prepare("INSERT INTO services values(?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(
		service.ServiceID, service.AosVersion, service.ServiceProvider, service.Description, service.ImagePath,
		service.GID, service.ManifestDigest, service.IsActive)

	return aoserrors.Wrap(err)
}

// RemoveService removes existing service.
func (db *Database) RemoveService(service servicemanager.ServiceInfo) error {
	stmt, err := db.sql.Prepare("DELETE FROM services WHERE id = ? AND aosVersion = ?")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(service.ServiceID, service.AosVersion)

	return aoserrors.Wrap(err)
}

// GetService returns service by service ID.
func (db *Database) GetService(serviceID string) (service servicemanager.ServiceInfo, err error) {
	stmt, err := db.sql.Prepare(
		"SELECT * FROM services WHERE aosVersion = (SELECT MAX(aosVersion) FROM services WHERE id = ?)")
	if err != nil {
		return service, aoserrors.Wrap(err)
	}
	defer stmt.Close()

	err = stmt.QueryRow(serviceID).Scan(
		&service.ServiceID, &service.AosVersion, &service.ServiceProvider, &service.Description,
		&service.ImagePath, &service.GID, &service.ManifestDigest, &service.IsActive)

	if errors.Is(err, sql.ErrNoRows) {
		return service, servicemanager.ErrNotExist
	}

	if err != nil {
		return service, aoserrors.Wrap(err)
	}

	return service, aoserrors.Wrap(err)
}

// GetAllServices returns all services.
func (db *Database) GetAllServices() (services []servicemanager.ServiceInfo, err error) {
	rows, err := db.sql.Query("SELECT * FROM services")
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}
	defer rows.Close()

	for rows.Next() {
		var service servicemanager.ServiceInfo

		if err = rows.Scan(
			&service.ServiceID, &service.AosVersion, &service.ServiceProvider, &service.Description,
			&service.ImagePath, &service.GID, &service.ManifestDigest, &service.IsActive); err != nil {
			return services, aoserrors.Wrap(err)
		}

		services = append(services, service)
	}

	return services, aoserrors.Wrap(rows.Err())
}

// GetAllServiceVersions returns all service versions.
func (db *Database) GetAllServiceVersions(id string) (services []servicemanager.ServiceInfo, err error) {
	rows, err := db.sql.Query("SELECT * FROM services WHERE id = ?", id)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}
	defer rows.Close()

	for rows.Next() {
		var service servicemanager.ServiceInfo

		if err = rows.Scan(
			&service.ServiceID, &service.AosVersion, &service.ServiceProvider, &service.Description,
			&service.ImagePath, &service.GID, &service.ManifestDigest, &service.IsActive); err != nil {
			return services, aoserrors.Wrap(err)
		}

		services = append(services, service)
	}

	return services, aoserrors.Wrap(rows.Err())
}

// ActivateService sets isActive to true for the service.
func (db *Database) ActivateService(service servicemanager.ServiceInfo) error {
	stmt, err := db.sql.Prepare("UPDATE services SET isActive = 1 WHERE id = ? AND aosVersion = ?")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	result, err := stmt.Exec(service.ServiceID, service.AosVersion)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if count == 0 {
		return ErrNotExist
	}

	return aoserrors.Wrap(err)
}

// AddServiceToUsers adds service ID to users.
func (db *Database) AddServiceToUsers(users []string, serviceID string) (err error) {
	stmt, err := db.sql.Prepare("INSERT INTO users values(?, ?, ?, ?, ?)")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	usersJSON, err := json.Marshal(users)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	_, err = stmt.Exec(usersJSON, serviceID, "", []byte{}, "")

	return aoserrors.Wrap(err)
}

// RemoveServiceFromUsers removes service ID from users.
func (db *Database) RemoveServiceFromUsers(users []string, serviceID string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM users WHERE users = ? AND serviceid = ?")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	usersJSON, err := json.Marshal(users)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	_, err = stmt.Exec(usersJSON, serviceID)

	return aoserrors.Wrap(err)
}

// SetUsersStorageFolder sets users storage folder.
func (db *Database) SetUsersStorageFolder(users []string, serviceID string, storageFolder string) (err error) {
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	result, err := db.sql.Exec("UPDATE users SET storageFolder = ? WHERE users = ? AND serviceid = ?",
		storageFolder, usersJSON, serviceID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if count == 0 {
		return ErrNotExist
	}

	return nil
}

// SetUsersStateChecksum sets users state checksum.
func (db *Database) SetUsersStateChecksum(users []string, serviceID string, checksum []byte) (err error) {
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	result, err := db.sql.Exec("UPDATE users SET stateCheckSum = ? WHERE users = ? AND serviceid = ?",
		checksum, usersJSON, serviceID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if count == 0 {
		return ErrNotExist
	}

	return nil
}

// GetUsersService returns users service.
func (db *Database) GetUsersService(users []string, serviceID string) (usersService launcher.UsersService, err error) {
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return usersService, aoserrors.Wrap(err)
	}

	rows, err := db.sql.Query("SELECT storageFolder, stateCheckSum FROM users WHERE users = ? AND serviceid = ?",
		usersJSON, serviceID)
	if err != nil {
		return usersService, aoserrors.Wrap(err)
	}
	defer rows.Close()

	if rows.Err() != nil {
		return usersService, aoserrors.Wrap(rows.Err())
	}

	if !rows.Next() {
		return usersService, ErrNotExist
	}

	if err = rows.Scan(&usersService.StorageFolder, &usersService.StateChecksum); err != nil {
		return usersService, aoserrors.Wrap(err)
	}

	usersService.Users = users
	usersService.ServiceID = serviceID

	return usersService, nil
}

// GetUsersServicesByServiceID returns users services by service ID.
func (db *Database) GetUsersServicesByServiceID(serviceID string) (usersServices []launcher.UsersService, err error) {
	rows, err := db.sql.Query("SELECT users, storageFolder, stateCheckSum FROM users WHERE serviceid = ?", serviceID)
	if err != nil {
		return usersServices, aoserrors.Wrap(err)
	}
	defer rows.Close()

	for rows.Next() {
		usersService := launcher.UsersService{ServiceID: serviceID}
		usersJSON := []byte{}

		if err = rows.Scan(&usersJSON, &usersService.StorageFolder, &usersService.StateChecksum); err != nil {
			return usersServices, aoserrors.Wrap(err)
		}

		if err = json.Unmarshal(usersJSON, &usersService.Users); err != nil {
			return usersServices, aoserrors.Wrap(err)
		}

		usersServices = append(usersServices, usersService)
	}

	return usersServices, aoserrors.Wrap(rows.Err())
}

// RemoveServiceFromAllUsers removes service from all users.
func (db *Database) RemoveServiceFromAllUsers(serviceID string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM users WHERE serviceid = ?")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(serviceID)

	return aoserrors.Wrap(err)
}

// UpdateOverrideEnvVars add/update/remove overrides env vars.
func (db *Database) UpdateOverrideEnvVars(users []string, serviceID string, vars []*pb.EnvVarInfo) (err error) {
	// TODO: Currunt version support only one user. Should be re-implemented
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	varsText := ""

	if len(vars) > 0 {
		varsJSON, err := json.Marshal(vars)
		if err != nil {
			return aoserrors.Wrap(err)
		}

		varsText = string(varsJSON)
	}

	result, err := db.sql.Exec("UPDATE users SET overrideEnvVars = ? WHERE users = ? AND serviceid = ?",
		varsText, usersJSON, serviceID)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if count == 0 {
		return ErrNotExist
	}

	return nil
}

// GetAllOverrideEnvVars returns list of env vars for services.
func (db *Database) GetAllOverrideEnvVars() (vars []pb.OverrideEnvVar, err error) {
	rows, err := db.sql.Query("SELECT users, serviceid, overrideEnvVars FROM users")
	if err != nil {
		return vars, aoserrors.Wrap(err)
	}
	defer rows.Close()

	if rows.Err() != nil {
		return vars, aoserrors.Wrap(rows.Err())
	}

	for rows.Next() {
		varsText := ""
		usersText := ""
		users := []string{}

		var envVar pb.OverrideEnvVar

		err = rows.Scan(&usersText, &envVar.ServiceId, &varsText)
		if err != nil {
			return vars, aoserrors.Wrap(err)
		}

		if err = json.Unmarshal([]byte(usersText), &users); err != nil {
			return vars, aoserrors.Wrap(err)
		}

		if len(users) < 1 {
			return vars, aoserrors.Wrap(ErrNotExist)
		}

		envVar.SubjectId = users[0] // TODO: currently support only one user

		if varsText != "" {
			if err = json.Unmarshal([]byte(varsText), &envVar.Vars); err != nil {
				return vars, aoserrors.Wrap(err)
			}
		}

		vars = append(vars, envVar) // nolint
	}

	return vars, nil
}

// SetTrafficMonitorData stores traffic monitor data.
func (db *Database) SetTrafficMonitorData(chain string, timestamp time.Time, value uint64) (err error) {
	result, err := db.sql.Exec("UPDATE trafficmonitor SET time = ?, value = ? where chain = ?", timestamp, value, chain)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if count == 0 {
		if _, err = db.sql.Exec("INSERT INTO trafficmonitor VALUES(?, ?, ?)",
			chain, timestamp, value); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	return nil
}

// GetTrafficMonitorData stores traffic monitor data.
func (db *Database) GetTrafficMonitorData(chain string) (timestamp time.Time, value uint64, err error) {
	stmt, err := db.sql.Prepare("SELECT time, value FROM trafficmonitor WHERE chain = ?")
	if err != nil {
		return timestamp, value, aoserrors.Wrap(err)
	}
	defer stmt.Close()

	err = stmt.QueryRow(chain).Scan(&timestamp, &value)
	if errors.Is(err, sql.ErrNoRows) {
		return timestamp, value, ErrNotExist
	}

	if err != nil {
		return timestamp, value, aoserrors.Wrap(err)
	}

	return timestamp, value, nil
}

// RemoveTrafficMonitorData removes existing traffic monitor entry.
func (db *Database) RemoveTrafficMonitorData(chain string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM trafficmonitor WHERE chain = ?")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(chain)

	return aoserrors.Wrap(err)
}

// SetJournalCursor stores system logger cursor.
func (db *Database) SetJournalCursor(cursor string) (err error) {
	result, err := db.sql.Exec("UPDATE config SET cursor = ?", cursor)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if count == 0 {
		return ErrNotExist
	}

	return nil
}

// GetJournalCursor retrieves logger cursor.
func (db *Database) GetJournalCursor() (cursor string, err error) {
	stmt, err := db.sql.Prepare("SELECT cursor FROM config")
	if err != nil {
		return cursor, aoserrors.Wrap(err)
	}
	defer stmt.Close()

	err = stmt.QueryRow().Scan(&cursor)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cursor, ErrNotExist
		}

		return cursor, aoserrors.Wrap(err)
	}

	return cursor, nil
}

// AddLayer add layer to layers table.
func (db *Database) AddLayer(layer layermanager.LayerInfo) (err error) {
	stmt, err := db.sql.Prepare("INSERT INTO layers values(?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(layer.Digest, layer.LayerID, layer.Path, layer.OSVersion, layer.VendorVersion,
		layer.Description, layer.AosVersion)

	return aoserrors.Wrap(err)
}

// DeleteLayerByDigest remove layer from DB by digest.
func (db *Database) DeleteLayerByDigest(digest string) (err error) {
	stmt, err := db.sql.Prepare("DELETE FROM layers WHERE digest = ?")
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(digest)

	return aoserrors.Wrap(err)
}

// GetLayersInfo get all installed layers.
func (db *Database) GetLayersInfo() (layersList []layermanager.LayerInfo, err error) {
	rows, err := db.sql.Query("SELECT * FROM layers ")
	if err != nil {
		return layersList, aoserrors.Wrap(err)
	}
	defer rows.Close()

	for rows.Next() {
		layer := layermanager.LayerInfo{}

		if err = rows.Scan(&layer.Digest, &layer.LayerID, &layer.Path, &layer.OSVersion,
			&layer.VendorVersion, &layer.Description, &layer.AosVersion); err != nil {
			return layersList, aoserrors.Wrap(err)
		}

		layersList = append(layersList, layer)
	}

	return layersList, aoserrors.Wrap(rows.Err())
}

// GetLayerInfoByDigest returns layers information by layer digest.
func (db *Database) GetLayerInfoByDigest(digest string) (layer layermanager.LayerInfo, err error) {
	stmt, err := db.sql.Prepare("SELECT * FROM layers WHERE digest = ?")
	if err != nil {
		return layer, aoserrors.Wrap(err)
	}
	defer stmt.Close()

	err = stmt.QueryRow(digest).Scan(&layer.Digest, &layer.LayerID, &layer.Path, &layer.OSVersion,
		&layer.VendorVersion, &layer.Description, &layer.AosVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return layer, layermanager.ErrNotExist
	}

	if err != nil {
		return layer, aoserrors.Wrap(err)
	}

	layer.Digest = digest

	return layer, nil
}

// Close closes database.
func (db *Database) Close() {
	db.sql.Close()
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newDatabase(name string, migrationPath string, mergedMigrationPath string, version uint) (db *Database,
	err error) {
	log.WithField("name", name).Debug("Open database")

	// Check and create db path
	if _, err = os.Stat(filepath.Dir(name)); err != nil {
		if !os.IsNotExist(err) {
			return db, aoserrors.Wrap(err)
		}

		if err = os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return db, aoserrors.Wrap(err)
		}
	}

	sqlite, err := sql.Open("sqlite3", fmt.Sprintf("%s?_busy_timeout=%d&_journal_mode=%s&_sync=%s",
		name, busyTimeout, journalMode, syncMode))
	if err != nil {
		return db, aoserrors.Wrap(err)
	}

	db = &Database{sqlite}

	defer func() {
		if err != nil {
			db.Close()
		}
	}()

	if err = migration.MergeMigrationFiles(migrationPath, mergedMigrationPath); err != nil {
		return db, aoserrors.Wrap(err)
	}

	exists, err := db.isTableExist("config")
	if err != nil {
		return db, aoserrors.Wrap(err)
	}

	if !exists {
		// Set database version if database not exist
		if err = migration.SetDatabaseVersion(sqlite, migrationPath, version); err != nil {
			log.Errorf("Error forcing database version. Err: %s", err)

			return db, aoserrors.Wrap(ErrMigrationFailed)
		}
	} else {
		if err = migration.DoMigrate(db.sql, mergedMigrationPath, version); err != nil {
			log.Errorf("Error during database migration. Err: %s", err)

			return db, aoserrors.Wrap(ErrMigrationFailed)
		}
	}

	if err := db.createConfigTable(); err != nil {
		return db, aoserrors.Wrap(err)
	}

	if err := db.createServiceTable(); err != nil {
		return db, aoserrors.Wrap(err)
	}

	if err := db.createUsersTable(); err != nil {
		return db, aoserrors.Wrap(err)
	}

	if err := db.createTrafficMonitorTable(); err != nil {
		return db, aoserrors.Wrap(err)
	}

	if err := db.createLayersTable(); err != nil {
		return db, aoserrors.Wrap(err)
	}

	return db, nil
}

func (db *Database) isTableExist(name string) (result bool, err error) {
	rows, err := db.sql.Query("SELECT * FROM sqlite_master WHERE name = ? and type='table'", name)
	if err != nil {
		return false, aoserrors.Wrap(err)
	}
	defer rows.Close()

	result = rows.Next()

	return result, aoserrors.Wrap(rows.Err())
}

func (db *Database) createConfigTable() (err error) {
	log.Info("Create config table")

	exist, err := db.isTableExist("config")
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if exist {
		return nil
	}

	if _, err = db.sql.Exec(
		`CREATE TABLE config (
			operationVersion INTEGER,
			cursor TEXT)`); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = db.sql.Exec(
		`INSERT INTO config (
			operationVersion,
			cursor) values(?, ?)`, launcher.OperationVersion, ""); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (db *Database) createServiceTable() (err error) {
	log.Info("Create service table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS services (id TEXT NOT NULL ,
															   aosVersion INTEGER,
															   serviceProvider TEXT,
															   description TEXT,
															   imagePath TEXT,
															   gid INTEGER,
															   manifestDigest BLOB,
															   isActive INTEGER,
															   PRIMARY KEY(id, aosVersion))`)

	return aoserrors.Wrap(err)
}

func (db *Database) createUsersTable() (err error) {
	log.Info("Create users table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS users (users TEXT NOT NULL,
															serviceid TEXT NOT NULL,
															storageFolder TEXT,
															stateCheckSum BLOB,
															overrideEnvVars TEXT,
															PRIMARY KEY(users, serviceid))`)

	return aoserrors.Wrap(err)
}

func (db *Database) createTrafficMonitorTable() (err error) {
	log.Info("Create traffic monitor table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS trafficmonitor (chain TEXT NOT NULL PRIMARY KEY,
																	 time TIMESTAMP,
																	 value INTEGER)`)

	return aoserrors.Wrap(err)
}

func (db *Database) createLayersTable() (err error) {
	log.Info("Create layers table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS layers (digest TEXT NOT NULL PRIMARY KEY,
															 layerId TEXT,
															 path TEXT,
															 osVersion TEXT,
															 vendorVersion TEXT,
															 description TEXT,
															 aosVersion INTEGER)`)

	return aoserrors.Wrap(err)
}

func (db *Database) removeAllServices() (err error) {
	_, err = db.sql.Exec("DELETE FROM services")

	return aoserrors.Wrap(err)
}

func (db *Database) removeAllUsers() (err error) {
	_, err = db.sql.Exec("DELETE FROM users")

	return aoserrors.Wrap(err)
}

func (db *Database) removeAllTrafficMonitor() (err error) {
	_, err = db.sql.Exec("DELETE FROM trafficmonitor")

	return aoserrors.Wrap(err)
}
