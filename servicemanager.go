package main

import (
	"./downloadmanager"
	"fmt"
)

func main() {

	out := make(chan string)
	go downloadmanager.MyDownloadFile("./", "http://speedtest.tele2.net/100MB.zip", out)
	go downloadmanager.MyDownloadFile("./test/", "http://speedtest.tele2.net/100MB.zip", out)

	for i := range out {
		fmt.Printf("Save file here: %v\n", i)
	}
	fmt.Printf("end\n")
}
