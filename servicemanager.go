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

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"syscall"

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/journalalerts"
	"github.com/aosedge/aos_common/resourcemonitor"
	"github.com/aosedge/aos_common/utils/cryptutils"
	"github.com/aosedge/aos_common/utils/iamclient"
	"github.com/coreos/go-systemd/daemon"
	"github.com/coreos/go-systemd/journal"
	log "github.com/sirupsen/logrus"
	"golang.org/x/exp/slices"

	"github.com/aosedge/aos_servicemanager/alerts"
	"github.com/aosedge/aos_servicemanager/config"
	"github.com/aosedge/aos_servicemanager/database"
	"github.com/aosedge/aos_servicemanager/launcher"
	"github.com/aosedge/aos_servicemanager/layermanager"
	"github.com/aosedge/aos_servicemanager/logging"
	"github.com/aosedge/aos_servicemanager/networkmanager"
	resource "github.com/aosedge/aos_servicemanager/resourcemanager"
	"github.com/aosedge/aos_servicemanager/runner"
	"github.com/aosedge/aos_servicemanager/servicemanager"
	"github.com/aosedge/aos_servicemanager/smclient"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const dbFileName = "servicemanager.db"

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

type serviceManager struct {
	cryptoContext   *cryptutils.CryptoContext
	journalAlerts   *journalalerts.JournalAlerts
	alerts          *alerts.Alerts
	cfg             *config.Config
	db              *database.Database
	launcher        *launcher.Launcher
	resourcemanager *resource.ResourceManager
	logging         *logging.Logging
	monitor         *resourcemonitor.ResourceMonitor
	network         *networkmanager.NetworkManager
	iam             *iamclient.Client
	client          *smclient.SMClient
	layerMgr        *layermanager.LayerManager
	serviceMgr      *servicemanager.ServiceManager
	runner          *runner.Runner
}

type journalHook struct {
	severityMap map[log.Level]journal.Priority
}

/***********************************************************************************************************************
 * Vars
 **********************************************************************************************************************/

// GitSummary provided by govvv at compile-time.
var GitSummary = "Unknown" //nolint:gochecknoglobals

/***********************************************************************************************************************
 * Init
 **********************************************************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetOutput(os.Stdout)
}

/***********************************************************************************************************************
 * ServiceManager
 **********************************************************************************************************************/

func cleanup(cfg *config.Config, dbFile string) {
	log.Info("System cleanup")

	if err := os.RemoveAll(cfg.ServicesDir); err != nil {
		log.Errorf("Can't remove services dir: %v", err)
	}

	if err := os.RemoveAll(cfg.LayersDir); err != nil {
		log.Errorf("Can't remove services dir: %v", err)
	}

	log.WithField("file", dbFile).Debug("Delete DB file")

	if err := os.RemoveAll(dbFile); err != nil {
		log.Errorf("Can't cleanup database: %s", err)
	}

	if err := os.RemoveAll(cfg.NodeConfigFile); err != nil {
		log.Errorf("Can't remove node config file: %v", err)
	}
}

//nolint:funlen
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
	if errors.Is(err, database.ErrMigrationFailed) {
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

	if sm.serviceMgr, err = servicemanager.New(cfg, sm.db); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if sm.cryptoContext, err = cryptutils.NewCryptoContext(cfg.CACert); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if sm.iam, err = iamclient.New(cfg.IAMPublicServerURL, cfg.IAMProtectedServerURL, cfg.CertStorage,
		nil, sm.cryptoContext, false); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if sm.alerts, err = alerts.New(); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if sm.journalAlerts, err = journalalerts.New(sm.cfg.JournalAlerts, sm.db, sm.db, sm.alerts); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	nodeInfo, err := sm.iam.GetCurrentNodeInfo()
	if err != nil {
		return sm, aoserrors.Wrap(err)
	}

	runners, err := nodeInfo.GetNodeRunners()
	if err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if !slices.Contains(runners, "runx") {
		if sm.network, err = networkmanager.New(cfg, sm.db); err != nil {
			return sm, aoserrors.Wrap(err)
		}
	}

	if sm.resourcemanager, err = resource.New(cfg.NodeConfigFile, sm.alerts); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if sm.monitor, err = resourcemonitor.New(
		cfg.Monitoring, sm.iam, sm.resourcemanager, sm.network, sm.alerts); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if sm.runner, err = runner.New(); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if sm.launcher, err = launcher.New(cfg, sm.iam, sm.db, sm.serviceMgr, sm.layerMgr, sm.runner, sm.resourcemanager,
		sm.network, sm.iam, sm.monitor, sm.alerts); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if sm.logging, err = logging.New(cfg, sm.db); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	if sm.client, err = smclient.New(cfg, sm.iam, sm.iam, sm.serviceMgr, sm.layerMgr, sm.launcher, sm.resourcemanager,
		sm.alerts, sm.monitor, sm.logging, sm.network, sm.cryptoContext, false); err != nil {
		return sm, aoserrors.Wrap(err)
	}

	return sm, nil
}

func (sm *serviceManager) close() {
	if sm.serviceMgr != nil {
		sm.serviceMgr.Close()
	}

	if sm.layerMgr != nil {
		sm.layerMgr.Close()
	}

	if sm.logging != nil {
		sm.logging.Close()
	}

	if sm.launcher != nil {
		sm.launcher.Close()
	}

	if sm.runner != nil {
		sm.runner.Close()
	}

	if sm.monitor != nil {
		sm.monitor.Close()
	}

	if sm.network != nil {
		sm.network.Close()
	}

	if sm.journalAlerts != nil {
		sm.journalAlerts.Close()
	}

	if sm.iam != nil {
		sm.iam.Close()
	}

	if sm.db != nil {
		sm.db.Close()
	}

	if sm.cryptoContext != nil {
		sm.cryptoContext.Close()
	}
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
		},
	}

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

	err = journal.Print(hook.severityMap[entry.Level], "%s", logMessage)

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

/***********************************************************************************************************************
 * Main
 **********************************************************************************************************************/

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
		fmt.Printf("Version: %s\n", GitSummary) //nolint:forbidigo // logs aren't initialized
		return
	}

	// Set log output
	if *useJournal {
		log.AddHook(newJournalHook())
		log.SetOutput(io.Discard)
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

	// Notify systemd
	if _, err = daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		log.Errorf("Can't notify systemd: %s", err)
	}

	// Handle SIGTERM
	terminateChannel := make(chan os.Signal, 1)
	signal.Notify(terminateChannel, os.Interrupt, syscall.SIGTERM)

	<-terminateChannel
}
