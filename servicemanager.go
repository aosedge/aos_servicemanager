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

package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/journal"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"

	"aos_servicemanager/alerts"
	"aos_servicemanager/config"
	"aos_servicemanager/database"
	"aos_servicemanager/iamclient"
	"aos_servicemanager/launcher"
	"aos_servicemanager/layermanager"
	"aos_servicemanager/logging"
	"aos_servicemanager/monitoring"
	"aos_servicemanager/networkmanager"
	resource "aos_servicemanager/resourcemanager"
	"aos_servicemanager/smserver"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const reconnectTimeout = 10 * time.Second

const dbFileName = "servicemanager.db"

/*******************************************************************************
 * Types
 ******************************************************************************/

type serviceManager struct {
	alerts          *alerts.Alerts
	smServer        *smserver.SMServer
	cfg             *config.Config
	db              *database.Database
	launcher        *launcher.Launcher
	resourcemanager *resource.ResourceManager
	logging         *logging.Logging
	monitor         *monitoring.Monitor
	network         *networkmanager.NetworkManager
	iam             *iamclient.Client
	layerMgr        *layermanager.LayerManager
}

type journalHook struct {
	severityMap map[log.Level]journal.Priority
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

// GitSummary provided by govvv at compile-time
var GitSummary = "Unknown"

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * ServiceManager
 ******************************************************************************/

func cleanup(cfg *config.Config, dbFile string) {
	log.Info("System cleanup")

	if err := launcher.Cleanup(cfg); err != nil {
		log.Errorf("Can't cleanup launcher: %s", err)
	}

	log.WithField("file", dbFile).Debug("Delete DB file")

	if err := os.RemoveAll(dbFile); err != nil {
		log.Errorf("Can't cleanup database: %s", err)
	}

	log.Debug("Delete networks")

	network, err := networkmanager.New(cfg, nil)
	if err != nil {
		log.Errorf("Can't create network: %s", err)
	}

	if err = network.DeleteAllNetworks(); err != nil {
		log.Errorf("Can't delete networks: %s", err)
	}
}

func newServiceManager(cfg *config.Config) (sm *serviceManager, err error) {
	defer func() {
		if err != nil {
			sm.close()
			sm = nil
		}
	}()

	sm = &serviceManager{cfg: cfg}

	// Create DB
	dbFile := path.Join(cfg.WorkingDir, dbFileName)

	sm.db, err = database.New(dbFile, cfg.Migration.MigrationPath, cfg.Migration.MergedMigrationPath)
	if err == database.ErrMigrationFailed {
		cleanup(cfg, dbFile)

		if sm.db, err = database.New(dbFile, cfg.Migration.MigrationPath,
			cfg.Migration.MergedMigrationPath); err != nil {
			return sm, aoserrors.Wrap(err)
		}
	} else if err != nil {
		return sm, aoserrors.Wrap(err)
	}

	// Check operation version

	version, err := sm.db.GetOperationVersion()
	if err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if launcher.OperationVersion != version {
		log.Warning("Unsupported operation version")

		sm.db.Close()

		cleanup(cfg, dbFile)

		if sm.db, err = database.New(dbFile, cfg.Migration.MigrationPath,
			cfg.Migration.MergedMigrationPath); err != nil {
			return sm, aoserrors.Wrap(err)
		}
	}

	if sm.layerMgr, err = layermanager.New(cfg, sm.db); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	// Create IAM client
	if sm.iam, err = iamclient.New(cfg, false); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	// Create alerts
	if sm.alerts, err = alerts.New(cfg, sm.db, sm.db); err != nil {
		if err == alerts.ErrDisabled {
			log.Warn(err)
		} else {
			return sm, aoserrors.Wrap(err)
		}
	}

	// Create network
	if sm.network, err = networkmanager.New(cfg, sm.db); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	// Create monitor
	if sm.monitor, err = monitoring.New(cfg, sm.alerts, sm.network); err != nil {
		if err == monitoring.ErrDisabled {
			log.Warn(err)
		} else {
			return sm, aoserrors.Wrap(err)
		}
	}

	// Create resourcemanager
	if sm.resourcemanager, err = resource.New(cfg.BoardConfigFile, sm.alerts); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	// Create launcher
	if sm.launcher, err = launcher.New(cfg, sm.db, sm.layerMgr, sm.monitor,
		sm.network, sm.resourcemanager, sm.iam); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	// Create logging
	if sm.logging, err = logging.New(cfg, sm.db); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if sm.smServer, err = smserver.New(cfg, sm.launcher, sm.layerMgr,
		sm.alerts, sm.monitor, sm.resourcemanager, sm.logging, false); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if err = sm.checkConsistency(); err != nil {
		log.Errorf("Consistency error: %s. Cleanup...", err)

		sm.launcher.Close()
		sm.launcher = nil

		if launcherErr := launcher.Cleanup(sm.cfg); err != nil {
			log.Errorf("Can't cleanup launcher: %s", launcherErr)
		}

		if layerErr := sm.layerMgr.Cleanup(); err != nil {
			log.Errorf("Can't cleanup layermanager: %s", layerErr)
		}

		return sm, aoserrors.Wrap(err)
	}

	return sm, nil
}

func (sm *serviceManager) handleChannels(ctx context.Context) (err error) {
	for {
		select {
		case users := <-sm.iam.GetUsersChangedChannel():
			sm.launcher.SetUsers(users)

		case <-ctx.Done():
			return
		}
	}
}

func (sm *serviceManager) close() {
	// Close logging
	if sm.logging != nil {
		sm.logging.Close()
	}

	// Close grpc server
	if sm.smServer != nil {
		sm.smServer.Stop()
	}

	// Close launcher
	if sm.launcher != nil {
		sm.launcher.Close()
	}

	// Close monitor
	if sm.monitor != nil {
		sm.monitor.Close()
	}

	// Close network
	if sm.network != nil {
		sm.network.Close()
	}

	// Close alerts
	if sm.alerts != nil {
		sm.alerts.Close()
	}

	// Close DB
	if sm.db != nil {
		sm.db.Close()
	}
}

func (sm *serviceManager) checkConsistency() (err error) {
	if err = sm.launcher.CheckServicesConsistency(); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = sm.layerMgr.CheckLayersConsistency(); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func newJournalHook() (hook *journalHook) {
	hook = &journalHook{
		severityMap: map[log.Level]journal.Priority{
			log.DebugLevel: journal.PriDebug,
			log.InfoLevel:  journal.PriInfo,
			log.WarnLevel:  journal.PriWarning,
			log.ErrorLevel: journal.PriErr,
			log.FatalLevel: journal.PriCrit,
			log.PanicLevel: journal.PriEmerg,
		}}

	return hook
}

func (hook *journalHook) Fire(entry *log.Entry) (err error) {
	if entry == nil {
		return aoserrors.New("log entry is nil")
	}

	logMessage, err := entry.String()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	err = journal.Print(hook.severityMap[entry.Level], logMessage)

	return aoserrors.Wrap(err)
}

func (hook *journalHook) Levels() []log.Level {
	return []log.Level{
		log.PanicLevel,
		log.FatalLevel,
		log.ErrorLevel,
		log.WarnLevel,
		log.InfoLevel,
		log.DebugLevel,
	}
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func main() {
	// Initialize command line flags
	configFile := flag.String("c", "aos_servicemanager.cfg", "path to config file")
	strLogLevel := flag.String("v", "info", `log level: "debug", "info", "warn", "error", "fatal", "panic"`)
	doCleanup := flag.Bool("reset", false, `Removes all services, wipes services and storages and remove DB`)
	showVersion := flag.Bool("version", false, `Show service manager version`)
	useJournal := flag.Bool("j", false, "output logs to systemd journal")

	flag.Parse()

	// Show version
	if *showVersion {
		fmt.Printf("Version: %s\n", GitSummary)
		return
	}

	// Set log output
	if *useJournal {
		log.AddHook(newJournalHook())
		log.SetOutput(ioutil.Discard)
	} else {
		log.SetOutput(os.Stdout)
	}

	// Set log level
	logLevel, err := log.ParseLevel(*strLogLevel)
	if err != nil {
		log.Fatalf("Error: %s", err)
	}
	log.SetLevel(logLevel)

	log.WithFields(log.Fields{"configFile": *configFile, "version": GitSummary}).Info("Start service manager")

	cfg, err := config.New(*configFile)
	if err != nil {
		log.Fatalf("Can't create config: %s", err)
	}

	if *doCleanup {
		cleanup(cfg, path.Join(cfg.WorkingDir, dbFileName))
		return
	}

	sm, err := newServiceManager(cfg)
	if err != nil {
		log.Fatalf("Can't create service manager: %s", err)
	}
	defer sm.close()

	if err = sm.launcher.SetUsers(sm.iam.GetUsers()); err != nil {
		log.Errorf("Can't set users: %s", err)
	}

	ctx, fnCancel := context.WithCancel(context.Background())

	go sm.handleChannels(ctx)

	// Handle SIGTERM
	terminateChannel := make(chan os.Signal, 1)
	signal.Notify(terminateChannel, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-terminateChannel

		fnCancel()

		sm.close()

		os.Exit(0)
	}()

	if err = sm.smServer.Start(); err != nil {
		os.Exit(1)
	}
}
