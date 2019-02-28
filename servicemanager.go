package main

import (
	"errors"
	"flag"
	"os"
	"os/signal"
	"path"
	"reflect"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/alerts"
	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/dbushandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/fcrypt"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/launcher"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/logging"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/monitoring"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/visclient"
)

const (
	reconnectTimeout = 3 * time.Second
)

// GitSummary provided by govvv at compile-time
var GitSummary string
var errQuit = errors.New("Close application")

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetOutput(os.Stdout)
}

func sendInitialSetup(amqpHandler *amqp.AmqpHandler, launcherHandler *launcher.Launcher) (err error) {
	initialList, err := launcherHandler.GetServicesInfo()
	if err != nil {
		log.Fatalf("Can't get services: %s", err)
	}

	if err = amqpHandler.SendInitialSetup(initialList); err != nil {
		return err
	}

	return nil
}

func processAmqpMessage(
	message amqp.Message,
	amqpHandler *amqp.AmqpHandler,
	launcherHandler *launcher.Launcher,
	loggingHandler *logging.Logging) (err error) {
	switch data := message.Data.(type) {
	case []amqp.ServiceInfoFromCloud:
		log.WithField("len", len(data)).Info("Receive services info")

		currentList, err := launcherHandler.GetServicesInfo()
		if err != nil {
			log.Errorf("Error getting services info: %s", err)
			return err
		}

		type serviceDesc struct {
			serviceInfo          *amqp.ServiceInfo
			serviceInfoFromCloud *amqp.ServiceInfoFromCloud
		}

		servicesMap := make(map[string]*serviceDesc)

		for _, item := range currentList {
			serviceInfo := item

			servicesMap[serviceInfo.ID] = &serviceDesc{serviceInfo: &serviceInfo}
		}

		for _, item := range data {
			serviceInfoFromCloud := item

			if _, ok := servicesMap[serviceInfoFromCloud.ID]; !ok {
				servicesMap[serviceInfoFromCloud.ID] = &serviceDesc{}
			}

			servicesMap[serviceInfoFromCloud.ID].serviceInfoFromCloud = &serviceInfoFromCloud
		}

		for _, service := range servicesMap {
			if service.serviceInfoFromCloud != nil && service.serviceInfo != nil {
				// Update
				if service.serviceInfoFromCloud.Version > service.serviceInfo.Version {
					log.WithFields(log.Fields{
						"id":   service.serviceInfo.ID,
						"from": service.serviceInfo.Version,
						"to":   service.serviceInfoFromCloud.Version}).Info("Update service")

					launcherHandler.InstallService(*service.serviceInfoFromCloud)
				}
			} else if service.serviceInfoFromCloud != nil {
				// Install
				log.WithFields(log.Fields{
					"id":      service.serviceInfoFromCloud.ID,
					"version": service.serviceInfoFromCloud.Version}).Info("Install service")

				launcherHandler.InstallService(*service.serviceInfoFromCloud)
			} else if service.serviceInfo != nil {
				// Remove
				log.WithFields(log.Fields{
					"id":      service.serviceInfo.ID,
					"version": service.serviceInfo.Version}).Info("Remove service")

				launcherHandler.UninstallService(service.serviceInfo.ID)
			}
		}

	case *amqp.StateAcceptance:
		log.WithFields(log.Fields{
			"serviceID": data.ServiceID,
			"result":    data.Result}).Info("Receive state acceptance")

		launcherHandler.StateAcceptance(*data, message.CorrelationID)

	case *amqp.UpdateState:
		log.WithFields(log.Fields{
			"serviceID": data.ServiceID,
			"checksum":  data.Checksum}).Info("Receive update state")

		launcherHandler.UpdateState(*data)

	case *amqp.RequestServiceLog:
		log.WithFields(log.Fields{
			"serviceID": data.ServiceID,
			"from":      data.From,
			"till":      data.Till}).Info("Receive request service log")

		loggingHandler.GetServiceLog(*data)

	case *amqp.RequestServiceCrashLog:
		log.WithFields(log.Fields{
			"serviceID": data.ServiceID}).Info("Receive request service crash log")

		loggingHandler.GetServiceCrashLog(*data)

	default:
		log.Warnf("Receive unsupported amqp message: %s", reflect.TypeOf(data))
	}

	return nil
}

func sendServiceStatus(amqpHandler *amqp.AmqpHandler, status launcher.ActionStatus) (err error) {
	info := amqp.ServiceInfo{ID: status.ID, Version: status.Version, StateChecksum: status.StateChecksum}

	switch status.Action {
	case launcher.ActionInstall:
		if status.Err != nil {
			info.Status = "error"
			info.Error = "Can't install service"

			log.WithFields(log.Fields{
				"id":      status.ID,
				"version": status.Version}).Errorf("Can't install service: %s", status.Err)
		} else {
			info.Status = "installed"

			log.WithFields(log.Fields{
				"id":      status.ID,
				"version": status.Version}).Info("Service successfully installed")
		}

	case launcher.ActionUninstall:
		if status.Err != nil {
			info.Status = "error"
			info.Error = "Can't remove service"

			log.WithFields(log.Fields{
				"id":      status.ID,
				"version": status.Version}).Errorf("Can't remove service: %s", status.Err)
		} else {
			info.Status = "removed"

			log.WithFields(log.Fields{
				"id":      status.ID,
				"version": status.Version}).Info("Service successfully removed")
		}
	}

	if err = amqpHandler.SendServiceStatus(info); err != nil {
		return err
	}

	return nil
}

func run(
	amqpHandler *amqp.AmqpHandler,
	launcherHandler *launcher.Launcher,
	visHandler *visclient.VisClient,
	monitorHandler *monitoring.Monitor,
	loggingHandler *logging.Logging,
	terminateChannel chan os.Signal) (err error) {

	var monitorDataChannel chan amqp.MonitoringData

	if monitorHandler != nil {
		monitorDataChannel = monitorHandler.DataChannel
	}

	for {
		select {
		case <-terminateChannel:
			return errQuit

		case amqpMessage := <-amqpHandler.MessageChannel:
			if err, ok := amqpMessage.Data.(error); ok {
				log.Errorf("Receive amqp error: %s", err)
				return err
			}

			if err := processAmqpMessage(amqpMessage, amqpHandler, launcherHandler, loggingHandler); err != nil {
				log.Errorf("Error processing amqp result: %s", err)
			}

		case status := <-launcherHandler.StatusChannel:
			if err := sendServiceStatus(amqpHandler, status); err != nil {
				log.Errorf("Error send service status message: %s", err)
			}

		case newState := <-launcherHandler.NewStateChannel:
			if err := amqpHandler.SendNewState(newState.ServiceID, newState.State,
				newState.Checksum, newState.CorrelationID); err != nil {
				log.Errorf("Error send new state message: %s", err)
			}

		case stateRequest := <-launcherHandler.StateRequestChannel:
			if err := amqpHandler.SendStateRequest(stateRequest.ServiceID, stateRequest.Default); err != nil {
				log.Errorf("Error send new state message: %s", err)
			}

		case data := <-monitorDataChannel:
			if err := amqpHandler.SendMonitoringData(data); err != nil {
				log.Errorf("Error send monitoring data: %s", err)
			}

		case logData := <-loggingHandler.LogChannel:
			if err := amqpHandler.SendServiceLog(logData); err != nil {
				log.Errorf("Error send service log: %s", err)
			}

		case users := <-visHandler.UsersChangedChannel:
			log.WithField("users", users).Info("Users changed")
			return nil

		case err := <-visHandler.ErrorChannel:
			return err
		}
	}
}

func cleanup(workingDir, dbFile string) {
	log.Debug("System cleanup")

	if err := launcher.Cleanup(workingDir); err != nil {
		log.Fatalf("Can't cleanup launcher: %s", err)
	}

	log.WithField("file", dbFile).Debug("Delete DB file")
	if err := os.RemoveAll(dbFile); err != nil {
		log.Fatalf("Can't cleanup database: %s", err)
	}
}

func main() {
	// Initialize command line flags
	configFile := flag.String("c", "aos_servicemanager.cfg", "path to config file")
	strLogLevel := flag.String("v", "info", `log level: "debug", "info", "warn", "error", "fatal", "panic"`)
	doCleanup := flag.Bool("reset", false, `Removes all services, wipes services and storages and remove DB`)

	flag.Parse()

	// Set log level
	logLevel, err := log.ParseLevel(*strLogLevel)
	if err != nil {
		log.Fatalf("Error: %s", err)
	}
	log.SetLevel(logLevel)

	log.WithFields(log.Fields{"configFile": *configFile, "version": GitSummary}).Info("Start service manager")

	// Create config
	config, err := config.New(*configFile)
	if err != nil {
		log.Fatalf("Error while opening configuration file: %s", err)
	}

	dbFile := path.Join(config.WorkingDir, "servicemanager.db")

	if *doCleanup {
		cleanup(config.WorkingDir, dbFile)
		return
	}

	// Initialize fcrypt
	fcrypt.Init(config.Crypt)

	// Create DB
	db, err := database.New(dbFile)
	if err != nil {
		if err == database.ErrVersionMismatch {
			log.Warning("Unsupported database version")

			cleanup(config.WorkingDir, dbFile)

			db, err = database.New(dbFile)
		}

		if err != nil {
			log.Fatalf("Can't open database: %s", err)
		}
	}
	defer db.Close()

	// Create alerts
	alerts, err := alerts.New(config, db, db)
	if err != nil {
		log.Fatalf("Can't create alerts: %s", err)
	}
	defer alerts.Close()

	// Create monitor
	monitor, err := monitoring.New(config, db, alerts)
	if err != nil {
		if err == monitoring.ErrDisabled {
			log.Warn(err)
		} else {
			log.Fatalf("Can't create monitor: %s", err)
		}
	}

	// Create launcher
	launcherHandler, err := launcher.New(config, db, monitor)
	if err != nil {
		log.Fatalf("Can't create launcher: %s", err)
	}
	defer launcherHandler.Close()

	// Create D-Bus server
	dbusServer, err := dbushandler.New(db)
	if err != nil {
		log.Fatalf("Can't create D-BUS server: %s", err)
	}
	defer dbusServer.Close()

	// Create VIS client
	vis, err := visclient.New()
	if err != nil {
		log.Fatalf("Can't connect to VIS: %s", err)
	}
	defer vis.Close()

	amqpHandler, err := amqp.New()
	if err != nil {
		log.Fatalf("Can't create amqp: %s", err)
	}
	defer amqpHandler.Close()

	// Create logging
	logging, err := logging.New(config, db)
	if err != nil {
		log.Fatalf("Can't create logging: %s", err)
	}
	defer logging.Close()

	// Handle SIGTERM
	terminateChannel := make(chan os.Signal, 1)
	signal.Notify(terminateChannel, os.Interrupt, syscall.SIGTERM)

	for {
		var (
			users []string
			vin   string
		)

		if !vis.IsConnected() {
			if err = vis.Connect(config.VISServerURL); err != nil {
				log.Errorf("Can't connect to VIS: %s", err)
				goto reconnect
			}
		}

		// Get vin code
		if vin, err = vis.GetVIN(); err != nil {
			log.Errorf("Can't get VIN: %s", err)
			vis.Disconnect()
			goto reconnect
		}

		// Get users
		if users, err = vis.GetUsers(); err != nil {
			log.Errorf("Can't get users: %s", err)
			vis.Disconnect()
			goto reconnect
		}

		err = launcherHandler.SetUsers(users)
		if err != nil {
			log.Fatalf("Can't set users: %s", err)
		}

		// Connect
		if err = amqpHandler.Connect(config.ServiceDiscoveryURL, vin, users); err != nil {
			log.Errorf("Can't establish connection: %s", err)
			goto reconnect
		}

		if err = sendInitialSetup(amqpHandler, launcherHandler); err != nil {
			log.Errorf("Can't send initial setup: %s", err)
			goto reconnect
		}

		if err = run(amqpHandler, launcherHandler, vis,
			monitor, logging, terminateChannel); err != nil {
			if err == errQuit {
				return
			}
			log.Errorf("Runtime error: %s", err)
		}

	reconnect:

		amqpHandler.Disconnect()

		select {
		case <-terminateChannel:
			// Close application
			return

		case <-time.After(reconnectTimeout):
		}

		log.Debug("Reconnecting...")
	}
}
