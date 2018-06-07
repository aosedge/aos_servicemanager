package main

import (
	"os"
	"os/signal"
	"reflect"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/dbushandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/launcher"
)

const (
	aosReconnectTimeSec = 3
)

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

func sendInitalSetup(launcher *launcher.Launcher, handler *amqp.AmqpHandler) (err error) {
	initialList, err := launcher.GetServicesInfo()
	if err != nil {
		log.Error("Error getting initial list: ", err)
		return err
	}

	if handler.SendInitialSetup(initialList) != nil {
		log.Error("Error sending initial setup: ", err)
		return err
	}

	return nil
}

func processAmqpMessage(data interface{}, handler *amqp.AmqpHandler, launcher *launcher.Launcher) (err error) {
	switch data := data.(type) {
	case []amqp.ServiceInfoFromCloud:
		log.WithField("len", len(data)).Info("Recive services info")

		currenList, err := launcher.GetServicesInfo()
		if err != nil {
			log.Error("Error getting services info: ", err)
			return err
		}

		for iCur := len(currenList) - 1; iCur >= 0; iCur-- {
			for iDes := len(data) - 1; iDes >= 0; iDes-- {
				if data[iDes].ID == currenList[iCur].ID {
					if data[iDes].Version > currenList[iCur].Version {
						log.Info("Update ", data[iDes].ID, " from ", currenList[iCur].Version, " to ", data[iDes].Version)

						go launcher.InstallService(data[iDes])
					}

					data = append(data[:iDes], data[iDes+1:]...)
					currenList = append(currenList[:iCur], currenList[iCur+1:]...)
				}
			}
		}

		for _, deleteElemnt := range currenList {
			go launcher.RemoveService(deleteElemnt.ID)
		}

		for _, newElement := range data {
			go launcher.InstallService(newElement)
		}

		return nil

	default:
		log.Warn("Receive unsupported amqp message: ", reflect.TypeOf(data))
		return nil
	}
}

func main() {
	log.Info("Start service manager")

	db, err := database.New("data/servicemanager.db")
	if err != nil {
		log.Fatal("Can't open database: ", err)
	}
	defer db.Close()

	launcherHandler, launcherChan, err := launcher.New("data", db)
	if err != nil {
		log.Fatal("Can't create launcher: ", err)
	}
	defer launcherHandler.Close()

	amqpHandler, err := amqp.New()
	if err != nil {
		log.Fatal("Can't create amqpHandler: ", err)
	}
	defer amqpHandler.CloseAllConnections()

	dbusServer, err := dbushandler.New()

	if err != nil {
		log.Fatal("Can't create D-BUS server %v", err)
	}
	if dbusServer == nil {
		log.Fatal("Can't create D-BUS server")
	}

	// handle SIGTERM
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		launcherHandler.Close()
		amqpHandler.CloseAllConnections()
		dbusServer.StopServer()
		os.Exit(1)
	}()

	for {
		log.Debug("Start connection")

		amqpChan, err := amqpHandler.InitAmqphandler("https://fusion-poc-2.cloudapp.net:9000")
		if err != nil {
			log.Error("Can't esablish connection: ", err)
			log.Debug("Reconnecting...")
			time.Sleep(time.Second * aosReconnectTimeSec)
			continue
		}

		sendInitalSetup(launcherHandler, amqpHandler)

		for {
			select {
			case amqpMessage := <-amqpChan:
				// check for error
				if err, ok := amqpMessage.(error); ok {
					log.Error("Receive amqp error: ", err)
					log.Debug("Reconnecting...")
					break
				}

				if err := processAmqpMessage(amqpMessage, amqpHandler, launcherHandler); err != nil {
					log.Error("Error processing amqp result: ", err)
				}
			case serviceStatus := <-launcherChan:
				switch serviceStatus.Action {
				case launcher.ActionInstall:
					info := amqp.ServiceInfo{ID: serviceStatus.ID, Version: serviceStatus.Version}
					if serviceStatus.Err != nil {
						info.Status = "error"
						errorMsg := amqp.ServiceError{ID: -1, Message: "Can't install service"}
						info.Error = &errorMsg
						log.WithFields(log.Fields{"id": serviceStatus.ID, "version": serviceStatus.Version}).Error("Can't install service: ", serviceStatus.Err)
					} else {
						info.Status = "installed"
						log.WithFields(log.Fields{"id": serviceStatus.ID, "version": serviceStatus.Version}).Info("Service successfully installed")
					}
					err := amqpHandler.SendServiceStatusMsg(info)
					if err != nil {
						log.Error("Error send service status message: ", err)
					}
				case launcher.ActionRemove:
					info := amqp.ServiceInfo{ID: serviceStatus.ID, Version: serviceStatus.Version}
					if serviceStatus.Err != nil {
						info.Status = "error"
						errorMsg := amqp.ServiceError{ID: -1, Message: "Can't remove service"}
						info.Error = &errorMsg
						log.WithFields(log.Fields{"id": serviceStatus.ID, "version": serviceStatus.Version}).Error("Can't remove service: ", serviceStatus.Err)
					} else {
						info.Status = "removed"
						log.WithFields(log.Fields{"id": serviceStatus.ID, "version": serviceStatus.Version}).Info("Service successfully removed")
					}
					err := amqpHandler.SendServiceStatusMsg(info)
					if err != nil {
						log.Error("Error send service status message: ", err)
					}
				}
			}
		}
	}
}
