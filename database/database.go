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
	"strings"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/aostypes"
	"github.com/aoscloud/aos_common/api/cloudprotocol"
	"github.com/aoscloud/aos_common/migration"
	_ "github.com/mattn/go-sqlite3" // ignore lint
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/launcher"
	"github.com/aoscloud/aos_servicemanager/layermanager"
	"github.com/aoscloud/aos_servicemanager/networkmanager"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	busyTimeout = 60000
	journalMode = "WAL"
	syncMode    = "NORMAL"
)

const dbVersion = 6

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

// errNotExist is returned when requested entry not exist in DB.
var errNotExist = errors.New("entry does not exist")

// ErrMigrationFailed is returned if migration was failed and db returned to the previous state.
var ErrMigrationFailed = errors.New("database migration failed")

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// Database structure with database information.
type Database struct {
	sql *sql.DB
}

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new database handle.
func New(name string, migrationPath string, mergedMigrationPath string) (db *Database, err error) {
	if db, err = newDatabase(name, migrationPath, mergedMigrationPath, dbVersion); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return db, nil
}

// GetOperationVersion returns operation version.
func (db *Database) GetOperationVersion() (version uint64, err error) {
	if err = db.getDataFromQuery("SELECT operationVersion FROM config", &version); err != nil {
		return version, err
	}

	return version, nil
}

// SetOperationVersion sets operation version.
func (db *Database) SetOperationVersion(version uint64) error {
	return db.executeQuery("UPDATE config SET operationVersion = ?", version)
}

// AddService adds new service.
func (db *Database) AddService(service servicemanager.ServiceInfo) (err error) {
	return db.executeQuery("INSERT INTO services values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		service.ServiceID, service.AosVersion, service.ServiceProvider, service.Description, service.ImagePath,
		service.ManifestDigest, service.Cached, service.Timestamp, service.Size, service.GID)
}

// RemoveService removes existing service.
func (db *Database) RemoveService(serviceID string, aosVersion uint64) (err error) {
	if err = db.executeQuery("DELETE FROM services WHERE id = ? AND aosVersion = ?",
		serviceID, aosVersion); errors.Is(err, errNotExist) {
		return nil
	}

	return err
}

// GetServices returns all services.
func (db *Database) GetServices() (services []servicemanager.ServiceInfo, err error) {
	return getFromQuery(
		db,
		"SELECT * FROM services",
		func(service *servicemanager.ServiceInfo) []any {
			return []any{
				&service.ServiceID, &service.AosVersion, &service.ServiceProvider, &service.Description,
				&service.ImagePath, &service.ManifestDigest, &service.Cached, &service.Timestamp,
				&service.Size, &service.GID,
			}
		})
}

// GetAllServiceVersions returns all service version by service ID.
func (db *Database) GetAllServiceVersions(id string) (services []servicemanager.ServiceInfo, err error) {
	if services, err = getFromQuery(
		db,
		"SELECT * FROM services WHERE id = ? ORDER BY aosVersion",
		func(service *servicemanager.ServiceInfo) []any {
			return []any{
				&service.ServiceID, &service.AosVersion, &service.ServiceProvider, &service.Description,
				&service.ImagePath, &service.ManifestDigest, &service.Cached, &service.Timestamp,
				&service.Size, &service.GID,
			}
		}, id); err != nil {
		return nil, err
	}

	if len(services) == 0 {
		return nil, servicemanager.ErrNotExist
	}

	return services, nil
}

// SetServiceCached sets cached status for the service.
func (db *Database) SetServiceCached(serviceID string, aosVersion uint64, cached bool) (err error) {
	if err = db.executeQuery("UPDATE services SET cached = ? WHERE id = ? AND aosVersion = ?",
		cached, serviceID, aosVersion); errors.Is(err, errNotExist) {
		return servicemanager.ErrNotExist
	}

	return err
}

// SetTrafficMonitorData stores traffic monitor data.
func (db *Database) SetTrafficMonitorData(chain string, timestamp time.Time, value uint64) (err error) {
	if err = db.executeQuery("UPDATE trafficmonitor SET time = ?, value = ? where chain = ?",
		timestamp, value, chain); errors.Is(err, errNotExist) {
		if _, err := db.sql.Exec("INSERT INTO trafficmonitor VALUES(?, ?, ?)", chain, timestamp, value); err != nil {
			return aoserrors.Wrap(err)
		}

		return nil
	}

	return err
}

// GetTrafficMonitorData stores traffic monitor data.
func (db *Database) GetTrafficMonitorData(chain string) (timestamp time.Time, value uint64, err error) {
	if err = db.getDataFromQuery(fmt.Sprintf("SELECT time, value FROM trafficmonitor WHERE chain = \"%s\"", chain),
		&timestamp, &value); err != nil {
		if errors.Is(err, errNotExist) {
			return timestamp, value, networkmanager.ErrEntryNotExist
		}

		return timestamp, value, err
	}

	return timestamp, value, nil
}

// RemoveTrafficMonitorData removes existing traffic monitor entry.
func (db *Database) RemoveTrafficMonitorData(chain string) (err error) {
	if err = db.executeQuery("DELETE FROM trafficmonitor WHERE chain = ?", chain); errors.Is(err, errNotExist) {
		return nil
	}

	return err
}

// SetJournalCursor stores system logger cursor.
func (db *Database) SetJournalCursor(cursor string) error {
	return db.executeQuery("UPDATE config SET cursor = ?", cursor)
}

// GetJournalCursor retrieves logger cursor.
func (db *Database) GetJournalCursor() (cursor string, err error) {
	if err = db.getDataFromQuery("SELECT cursor FROM config", &cursor); err != nil {
		return cursor, err
	}

	return cursor, nil
}

// GetOverrideEnvVars returns override env vars.
func (db *Database) GetOverrideEnvVars() (vars []cloudprotocol.EnvVarsInstanceInfo, err error) {
	var textEnvVars string

	if err = db.getDataFromQuery("SELECT envvars FROM config", &textEnvVars); err != nil {
		return vars, err
	}

	if textEnvVars == "" {
		return vars, nil
	}

	if err = json.Unmarshal([]byte(textEnvVars), &vars); err != nil {
		return vars, aoserrors.Wrap(err)
	}

	return vars, nil
}

// SetOverrideEnvVars updates override env vars.
func (db *Database) SetOverrideEnvVars(envVarsInfo []cloudprotocol.EnvVarsInstanceInfo) error {
	rowVars, err := json.Marshal(envVarsInfo)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	return db.executeQuery("UPDATE config SET envvars = ?", string(rowVars))
}

// GetOnlineTime returns previously stored online time.
func (db *Database) GetOnlineTime() (onlineTime time.Time, err error) {
	if err = db.getDataFromQuery("SELECT onlineTime FROM config", &onlineTime); err != nil {
		return time.Time{}, err
	}

	return onlineTime, nil
}

// SetOnlineTime sets online time.
func (db *Database) SetOnlineTime(onlineTime time.Time) error {
	return db.executeQuery("UPDATE config SET onlineTime = ?", onlineTime)
}

// AddLayer add layer to layers table.
func (db *Database) AddLayer(layer layermanager.LayerInfo) (err error) {
	return db.executeQuery("INSERT INTO layers values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		layer.Digest, layer.LayerID, layer.Path, layer.OSVersion, layer.VendorVersion,
		layer.Description, layer.AosVersion, layer.Timestamp, layer.Cached, layer.Size)
}

// DeleteLayerByDigest remove layer from DB by digest.
func (db *Database) DeleteLayerByDigest(digest string) (err error) {
	if err = db.executeQuery("DELETE FROM layers WHERE digest = ?", digest); errors.Is(err, errNotExist) {
		return nil
	}

	return err
}

// GetLayersInfo get all installed layers.
func (db *Database) GetLayersInfo() (layersList []layermanager.LayerInfo, err error) {
	return getFromQuery(
		db,
		"SELECT * FROM layers",
		func(layer *layermanager.LayerInfo) []any {
			return []any{
				&layer.Digest, &layer.LayerID, &layer.Path, &layer.OSVersion,
				&layer.VendorVersion, &layer.Description, &layer.AosVersion, &layer.Timestamp,
				&layer.Cached, &layer.Size,
			}
		})
}

// GetLayerInfoByDigest returns layers information by layer digest.
func (db *Database) GetLayerInfoByDigest(digest string) (layer layermanager.LayerInfo, err error) {
	if err = db.getDataFromQuery(fmt.Sprintf("SELECT * FROM layers WHERE digest = \"%s\"", digest),
		&layer.Digest, &layer.LayerID, &layer.Path, &layer.OSVersion,
		&layer.VendorVersion, &layer.Description,
		&layer.AosVersion, &layer.Timestamp, &layer.Cached, &layer.Size); err != nil {
		if errors.Is(err, errNotExist) {
			return layer, layermanager.ErrNotExist
		}

		return layer, err
	}

	return layer, nil
}

// SetLayerTimestamp sets timestamp for the layer.
func (db *Database) SetLayerTimestamp(digest string, timestamp time.Time) error {
	if err := db.executeQuery("UPDATE layers SET timestamp = ? WHERE digest = ?",
		timestamp, digest); errors.Is(err, errNotExist) {
		return layermanager.ErrNotExist
	}

	return nil
}

// SetLayerCached sets cached status for the layer.
func (db *Database) SetLayerCached(digest string, cached bool) (err error) {
	if err = db.executeQuery("UPDATE layers SET cached = ? WHERE digest = ?",
		cached, digest); errors.Is(err, errNotExist) {
		return layermanager.ErrNotExist
	}

	return err
}

// AddInstance adds instance information to db.
func (db *Database) AddInstance(instance launcher.InstanceInfo) error {
	network, err := json.Marshal(instance.NetworkParameters)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	return db.executeQuery("INSERT INTO instances values(?, ?, ?, ?, ?, ?, ?, ?, ?)",
		instance.InstanceID, instance.ServiceID, instance.SubjectID, instance.Instance, instance.UID,
		instance.Priority, instance.StoragePath, instance.StatePath, network)
}

// UpdateInstance updates instance information in db.
func (db *Database) UpdateInstance(instance launcher.InstanceInfo) (err error) {
	network, err := json.Marshal(instance.NetworkParameters)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if err = db.executeQuery(
		`UPDATE instances SET serviceID = ?, subjectID = ?, instance = ?, uid = ?, priority = ?, storagePath = ?,
		statePath = ?, network = ? WHERE instanceID = ?`,
		instance.ServiceID, instance.SubjectID, instance.Instance, instance.UID, instance.Priority,
		instance.StoragePath, instance.StatePath, network, instance.InstanceID); errors.Is(err, errNotExist) {
		return aoserrors.Wrap(launcher.ErrNotExist)
	}

	return err
}

// RemoveInstance removes instance information from db.
func (db *Database) RemoveInstance(instanceID string) (err error) {
	if err = db.executeQuery(
		"DELETE FROM instances WHERE instanceID = ?", instanceID); errors.Is(err, errNotExist) {
		return nil
	}

	return err
}

// GetInstanceInfoByID returns instance ident and service aos version by instanceID.
func (db *Database) GetInstanceInfoByID(
	instanceID string,
) (ident aostypes.InstanceIdent, aosVersion uint64, err error) {
	instance, err := db.getInstanceInfoFromQuery("SELECT * FROM instances WHERE instanceID = ?", instanceID)
	if err != nil {
		return ident, 0, aoserrors.Wrap(err)
	}

	stmt, err := db.sql.Prepare(
		`SELECT aosVersion FROM services WHERE aosVersion = (SELECT MAX(aosVersion)
		FROM services WHERE id = ?) AND id = ?`)
	if err != nil {
		return ident, 0, aoserrors.Wrap(err)
	}
	defer stmt.Close()

	err = stmt.QueryRow(instance.ServiceID, instance.ServiceID).Scan(&aosVersion)

	if errors.Is(err, sql.ErrNoRows) {
		return ident, 0, servicemanager.ErrNotExist
	}

	if err != nil {
		return ident, 0, aoserrors.Wrap(err)
	}

	return instance.InstanceIdent, aosVersion, nil
}

// GetAllInstances returns all instance.
func (db *Database) GetAllInstances() (instances []launcher.InstanceInfo, err error) {
	return db.getInstancesFromQuery("SELECT * FROM instances")
}

// GetInstanceIDs returns instance ids by filter.
func (db *Database) GetInstanceIDs(filter cloudprotocol.InstanceFilter) (instances []string, err error) {
	var (
		conditionList []string
		whereOperator string
	)

	if filter.ServiceID != nil {
		conditionList = append(conditionList, fmt.Sprintf("serviceID = \"%s\"", *filter.ServiceID))
	}

	if filter.SubjectID != nil {
		conditionList = append(conditionList, fmt.Sprintf("subjectID = \"%s\"", *filter.SubjectID))
	}

	if filter.Instance != nil {
		conditionList = append(conditionList, fmt.Sprintf("instance = %d", *filter.Instance))
	}

	if len(conditionList) > 0 {
		whereOperator = "WHERE"
	}

	instanceInfos, err := db.getInstancesFromQuery(fmt.Sprintf("SELECT * FROM instances %s %s", whereOperator,
		strings.Join(conditionList, " AND ")))
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	for _, value := range instanceInfos {
		instances = append(instances, value.InstanceID)
	}

	return instances, nil
}

// AddNetworkInfo adds network information to db.
func (db *Database) AddNetworkInfo(networkInfo networkmanager.NetworkParameters) error {
	return db.executeQuery("INSERT INTO network values(?, ?, ?, ?, ?)",
		networkInfo.NetworkID, networkInfo.IP, networkInfo.Subnet, networkInfo.VlanID, networkInfo.VlanIfName)
}

// RemoveNetworkInfo removes network information from db.
func (db *Database) RemoveNetworkInfo(networkID string) (err error) {
	if err = db.executeQuery("DELETE FROM network WHERE networkID = ?", networkID); errors.Is(err, errNotExist) {
		return nil
	}

	return err
}

// GetNetworkInfo returns networks information.
func (db *Database) GetNetworksInfo() ([]networkmanager.NetworkParameters, error) {
	rows, err := db.sql.Query("SELECT * FROM network")
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}
	defer rows.Close()

	if rows.Err() != nil {
		return nil, aoserrors.Wrap(rows.Err())
	}

	var networks []networkmanager.NetworkParameters

	for rows.Next() {
		var networkInfo networkmanager.NetworkParameters

		if err = rows.Scan(
			&networkInfo.NetworkID, &networkInfo.IP, &networkInfo.Subnet,
			&networkInfo.VlanID, &networkInfo.VlanIfName); err != nil {
			return nil, aoserrors.Wrap(err)
		}

		networks = append(networks, networkInfo)
	}

	return networks, nil
}

// Close closes database.
func (db *Database) Close() {
	db.sql.Close()
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func newDatabase(name string, migrationPath string, mergedMigrationPath string, version uint) (*Database, error) {
	log.WithField("name", name).Debug("Open database")

	// Check and create db path
	if _, err := os.Stat(filepath.Dir(name)); err != nil {
		if !os.IsNotExist(err) {
			return nil, aoserrors.Wrap(err)
		}

		if err = os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return nil, aoserrors.Wrap(err)
		}
	}

	sqlite, err := sql.Open("sqlite3", fmt.Sprintf("%s?_busy_timeout=%d&_journal_mode=%s&_sync=%s",
		name, busyTimeout, journalMode, syncMode))
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	db := &Database{sqlite}

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

	if err := db.createNetworkTable(); err != nil {
		return db, aoserrors.Wrap(err)
	}

	if err := db.createConfigTable(); err != nil {
		return db, aoserrors.Wrap(err)
	}

	if err := db.createServiceTable(); err != nil {
		return db, aoserrors.Wrap(err)
	}

	if err := db.createTrafficMonitorTable(); err != nil {
		return db, aoserrors.Wrap(err)
	}

	if err := db.createLayersTable(); err != nil {
		return db, aoserrors.Wrap(err)
	}

	if err := db.createInstancesTable(); err != nil {
		return db, err
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

func (db *Database) createNetworkTable() (err error) {
	log.Info("Create network table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS network (networkID TEXT NOT NULL PRIMARY KEY,
                                                              ip TEXT,
                                                              subnet TEXT,
                                                              vlanID INTEGER,
                                                              vlanIfName TEXT)`)

	return aoserrors.Wrap(err)
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
			cursor TEXT,
			envvars TEXT,
			onlineTime TIMESTAMP)`); err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err = db.sql.Exec(
		`INSERT INTO config (
			operationVersion,
			cursor,
			envvars,
			onlineTime) values(?, ?, ?, ?)`, launcher.OperationVersion, "", "", time.Now()); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func (db *Database) createServiceTable() (err error) {
	log.Info("Create service table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS services (id TEXT NOT NULL ,
															   aosVersion INTEGER,
															   providerID TEXT,
															   description TEXT,
															   imagePath TEXT,
															   manifestDigest BLOB,
															   cached INTEGER,
															   timestamp TIMESTAMP,
															   size INTEGER,
															   GID INTEGER,
															   PRIMARY KEY(id, aosVersion))`)

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
															 aosVersion INTEGER,
															 timestamp TIMESTAMP,
															 cached INTEGER,
															 size INTEGER)`)

	return aoserrors.Wrap(err)
}

func (db *Database) createInstancesTable() (err error) {
	log.Info("Create instances table")

	_, err = db.sql.Exec(`CREATE TABLE IF NOT EXISTS instances (instanceID TEXT NOT NULL PRIMARY KEY,
																serviceID TEXT,
																subjectID TEXT,
																instance INTEGER,
																uid INTEGER,
																priority INTEGER,
																storagePath TEXT,
																statePath TEXT,
																network BLOB)`)

	return aoserrors.Wrap(err)
}

func (db *Database) removeAllServices() (err error) {
	_, err = db.sql.Exec("DELETE FROM services")

	return aoserrors.Wrap(err)
}

func (db *Database) removeAllTrafficMonitor() (err error) {
	_, err = db.sql.Exec("DELETE FROM trafficmonitor")

	return aoserrors.Wrap(err)
}

func (db *Database) getInstancesFromQuery(
	query string, args ...interface{},
) (instances []launcher.InstanceInfo, err error) {
	rows, err := db.sql.Query(query, args...)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}
	defer rows.Close()

	if rows.Err() != nil {
		return nil, aoserrors.Wrap(rows.Err())
	}

	for rows.Next() {
		var (
			instance launcher.InstanceInfo
			network  []byte
		)

		if err = rows.Scan(&instance.InstanceID, &instance.ServiceID, &instance.SubjectID, &instance.Instance, &instance.UID,
			&instance.Priority, &instance.StoragePath, &instance.StatePath, &network); err != nil {
			return nil, aoserrors.Wrap(err)
		}

		if err = json.Unmarshal(network, &instance.NetworkParameters); err != nil {
			return nil, aoserrors.Wrap(err)
		}

		instances = append(instances, instance)
	}

	return instances, nil
}

func (db *Database) executeQuery(query string, args ...interface{}) error {
	stmt, err := db.sql.Prepare(query)
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	result, err := stmt.Exec(args...)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if count == 0 {
		return aoserrors.Wrap(errNotExist)
	}

	return nil
}

func (db *Database) getInstanceInfoFromQuery(
	query string, args ...interface{},
) (instance launcher.InstanceInfo, err error) {
	stmt, err := db.sql.Prepare(query)
	if err != nil {
		return instance, aoserrors.Wrap(err)
	}
	defer stmt.Close()

	var network []byte

	if err := stmt.QueryRow(args...).Scan(
		&instance.InstanceID, &instance.ServiceID, &instance.SubjectID, &instance.Instance, &instance.UID,
		&instance.Priority, &instance.StoragePath, &instance.StatePath, &network); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return instance, aoserrors.Wrap(launcher.ErrNotExist)
		}

		return instance, aoserrors.Wrap(err)
	}

	if err = json.Unmarshal(network, &instance.NetworkParameters); err != nil {
		return instance, aoserrors.Wrap(err)
	}

	return instance, nil
}

func getFromQuery[T any](db *Database, query string, binder func(*T) []any, args ...interface{}) (res []T, err error) {
	rows, err := db.sql.Query(query, args...)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	if rows.Err() != nil {
		return nil, aoserrors.Wrap(rows.Err())
	}
	defer rows.Close()

	for rows.Next() {
		var data T

		cols := binder(&data)

		err := rows.Scan(cols...)
		if err != nil {
			return res, aoserrors.Wrap(err)
		}

		res = append(res, data)
	}

	return res, nil
}

func (db *Database) getDataFromQuery(query string, result ...interface{}) error {
	stmt, err := db.sql.Prepare(query)
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer stmt.Close()

	if err = stmt.QueryRow().Scan(result...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errNotExist
		}

		return aoserrors.Wrap(err)
	}

	return nil
}
