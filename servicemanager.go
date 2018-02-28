package main

import (
	"./downloadmanager"
	//"./wshandler"
	"fmt"
)

type appInfo struct {
	Name string
}

func main() {

	out := make(chan string)
	wsout := make(chan appInfo)

	//go downloadmanager.MyDownloadFile("./", "https://kor.ill.in.ua/m/610x385/2122411.jpg", out)
	//go downloadmanager.MyDownloadFile("./test/", "http://speedtest.tele2.net/100MB.zip", out)

	go Initwshandler(wsout)

	for {
		select {
		case msg := <-out:
			fmt.Printf("Save file here: %v\n", msg)
		case msg2 := <-wsout:
			fmt.Printf(" update from WS %v\n", msg2.Name)
			go downloadmanager.MyDownloadFile("./", "https://kor.ill.in.ua/m/610x385/2122411.jpg", out)
		}
	}
	fmt.Printf("end\n")
}
