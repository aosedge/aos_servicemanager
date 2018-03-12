package main

import (
	"os"
	"path"

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

func main() {
	log.Info("Start service manager")
	defer func() {
		log.Info("Stop service manager")
	}()

	out := make(chan string)

	amqpChan := make(chan amqp.PackageInfo, 100)
	//go downloadmanager.DownloadPkg("./", "https://kor.ill.in.ua/m/610x385/2122411.jpg", out)
	//go downloadmanager.DownloadPkg("./test/", "http://speedtest.tele2.net/100MB.zip", out)

	go amqp.InitAmqphandler(amqpChan)

	launcher, err := launcher.New(path.Join(os.Getenv("GOPATH"), "aos"))
	if err != nil {
		log.Fatal("Can't create launcher")
	}
	defer launcher.Close()

	for {
		select {
		case pacghInfo := <-amqpChan:
			log.Debug("Receive package info: %v", pacghInfo)
			//todo verify via containerlib if ok
			go downloadmanager.DownloadPkg("./", pacghInfo.DownloadUrl, out)

		case msg := <-out:
			log.Debug("Save file here: %v", msg)

		}
	}
}
