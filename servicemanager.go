package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/downloadmanager"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/launcher"
)

type appInfo struct {
	Name string
}

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

func sendInitalSetup(launcher *launcher.Launcher, handler *amqp.AmqpHandler) {
	initialList, err := launcher.GetServicesInfo()
	if err != nil {
		log.Error("Error getting initial list ", err)
		//TODO: return
	}
	if handler.SendInitialSetup(initialList) != nil {
		log.Error("Error send sendInitalSetup", err)
	}
}

func processAmqpReturn(data interface{}, handler *amqp.AmqpHandler, launcher *launcher.Launcher, output chan string) bool {
	switch data := data.(type) {
	case error:
		log.Warning("Received error from AMQP channel: ", data)
		handler.CloseAllConnections()
		return false
	case amqp.ServiceInfoFromCloud:
		version, err := launcher.GetServiceVersion(data.Id)
		if err != nil {
			log.Warning("error get version ", err)
			break
		}
		if data.Version > version {
			log.Debug("send download request url ", data.DownloadUrl)
			go downloadmanager.DownloadPkg(data, output)
		}

		return true
	case []amqp.ServiceInfoFromCloud:
		log.Info("recive array of services len ", len(data))
		currenList, err := launcher.GetServicesInfo()
		if err != nil {
			log.Warning("error get GetServicesInfo ", err)
			break
		}
		for iCur := len(currenList) - 1; iCur >= 0; iCur-- {
			for iDes := len(data) - 1; iDes >= 0; iDes-- {

				if data[iDes].Id == currenList[iCur].Id {
					if data[iDes].Version > currenList[iCur].Version {
						log.Info("Update ", data[iDes].Id, " from ", currenList[iCur].Version, " to ", data[iDes].Version)

						go downloadmanager.DownloadPkg(data[iDes], output)
					}
					data = append(data[:iDes], data[iDes+1:]...)
					currenList = append(currenList[:iCur], currenList[iCur+1:]...)
				}
			}
		}
		for _, deleteElemnt := range currenList {
			log.Info("delete ID ", deleteElemnt.Id)
			launcher.RemoveService(deleteElemnt.Id) //TODO ADD CHECK ERROR
		}

		for _, newElemnt := range data {
			log.Info("Download new serv Id ", newElemnt.Id, " version ", newElemnt.Version)
			go downloadmanager.DownloadPkg(newElemnt, output)
		}
		return true
	default:
		log.Info("receive some data amqp")

		return true
	}
	return true
}

func main() {
	log.Info("Start service manager")
	defer func() {
		log.Info("Stop service manager")
	}()

	out := make(chan string)

	//go downloadmanager.DownloadPkg("./", "https://kor.ill.in.ua/m/610x385/2122411.jpg", out)
	//go downloadmanager.DownloadPkg("./test/", "http://speedtest.tele2.net/100MB.zip", out)

	launcher, err := launcher.New("data")
	if err != nil {
		log.Fatal("Can't create launcher: ", err)
	}
	amqpHandler, err := amqp.New()
	if err != nil {
		log.Fatal("Can't amqpHandler: ", err)
	}

	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		launcher.Close()
		amqpHandler.CloseAllConnections()
		os.Exit(1)
	}()

	for {
		log.Debug("start connection")
		amqpChan, err := amqpHandler.InitAmqphandler("https://fusion-poc-2.cloudapp.net:9000")

		if err != nil {
			log.Error("Can't esablish connection ", err)
			time.Sleep(3 * time.Second)
			continue
		}
		connectionOK := true
		sendInitalSetup(launcher, amqpHandler)
		for connectionOK != false {
			log.Debug("start select ")
			select {
			case amqpReturn := <-amqpChan:
				stop := !processAmqpReturn(amqpReturn, amqpHandler, launcher, out)
				if stop == true {
					connectionOK = false
					break
				}
			case msg := <-out:
				if msg != "" {
					log.Debug("Save file here: ", msg)
					err = <-launcher.InstallService(msg)
					if err != nil {
						log.Error("Can't install service: ", err)
					}
					if err := os.Remove(msg); err != nil {
						log.Errorf("Can't remove file %s: %s", msg, err)
					}
				}
			}
		}
		log.Warning("StartReconect")
	}
}
