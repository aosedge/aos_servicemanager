package main

import (
	"fmt"
	amqp "gitpct.epam.com/empd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/empd-aepr/aos_servicemanager/downloadmanager"
)

type appInfo struct {
	Name string
}

func main() {

	out := make(chan string)

	amqpChan := make(chan amqp.PackageInfo, 100)
	//go downloadmanager.DownloadPkg("./", "https://kor.ill.in.ua/m/610x385/2122411.jpg", out)
	//go downloadmanager.DownloadPkg("./test/", "http://speedtest.tele2.net/100MB.zip", out)

	go amqp.InitAmqphandler(amqpChan)

	for {
		select {
		case pacghInfo := <-amqpChan:
			fmt.Printf("Receive package info: %v\n", pacghInfo)
			//todo verify via containerlib if ok
			go downloadmanager.DownloadPkg("./", pacghInfo.DownloadUrl, out)

		case msg := <-out:
			fmt.Printf("Save file here: %v\n", msg)

		}
	}
	fmt.Printf("end\n")
}
