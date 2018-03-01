// Copyright 2015 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build ignore
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"

	"github.com/gorilla/websocket"
	"strings"
	"time"
)

type FileProperties struct {
	Name          string
	DownloadUrl   string
	Version       string
	TimeStamp     string
	ResponseCount int
}

type installRet struct {
	Added string
}

type afmAppInfo struct {
	Description  string
	Nname        string
	Shortname    string
	Id           string
	Version      string
	Author       string
	Author_email string
	Width        string
	Height       string
}

type afmPsInfo struct {
	Runid int
	Pids  []int
	State string
	ID    string
}

var addr = flag.String("addr", "localhost:8080", "http service address")

var wschan chan appInfo

var upgrader = websocket.Upgrader{} // use default options

func failOnError(err error, msg string) {
	if err != nil {
		log.Printf("%s: %s", msg, err)
	}
}

func subStr(input string, separator string) string {
	lastBin := strings.LastIndex(input, separator)
	return input[0:lastBin]
}

func runWgt(wgtID string) {
	log.Printf("wiil run %v", wgtID)
	cmd := exec.Command("afm-util", "run", wgtID)
	cmd.Stdin = strings.NewReader("some input")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	failOnError(err, "fail to run app")
	log.Printf("RUNNNNNN")
	log.Printf("run output %v", out.String())
}

func getAllInstalledWgt() []afmAppInfo {

	var list []afmAppInfo
	cmd := exec.Command("afm-util", "list")
	cmd.Stdin = strings.NewReader("some input")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()

	if err != nil {
		log.Fatal(err)
	} else {
		err = json.Unmarshal(out.Bytes(), &list)
		failOnError(err, "fail parse appList")
	}
	return list
}

func stopAppByWgtID(wgtId string) {
	/*var psList []afmPsInfo
	cmd := exec.Command("afm-util", "ps")
	cmd.Stdin = strings.NewReader("some input")
	var out bytes.Buffer
	cmd.Stdout = &out

	err := cmd.Run()
	if err != nil {
		log.Printf("", err)
	} else {
		err = json.Unmarshal(out.Bytes(), &psList)
		failOnError(err, "fail parse appList")
		if len(psList) > 0 {
			for _, psInfo := range psList {
				if psInfo.ID == wgtId {*/
	//cmd := exec.Command("afm-util", "stop", strconv.Itoa(psInfo.Runid))
	cmd := exec.Command("afm-util", "stop", wgtId)
	cmd.Stdin = strings.NewReader("some input")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	log.Printf("stop app ID %v out %v", wgtId, out.String(), err) // psInfo.Runid, err)
	//	break
	//}
	//}
	//}
	//}
}

func getInstaledAppInfoByWgtName(wgtName string) []afmAppInfo {

	var appIDs []afmAppInfo
	// appName := subStr(wgtName, ".")

	applist := getAllInstalledWgt()

	if len(applist) > 0 {
		for _, app := range applist {
			if wgtName == subStr(app.Id, "@") {
				appIDs = append(appIDs, app)
			}
		}
	}
	return appIDs
}

func uninstallWgtById(wgtId string) {
	time.Sleep(5 * time.Second)
	cmd := exec.Command("afm-util", "uninstall", wgtId)
	cmd.Stdin = strings.NewReader("some input")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	failOnError(err, "error uninstall app")
	log.Printf("remove ID %v %v \n", wgtId, out.String())
}

func downloadFile(filepath string, url string) (err error) {

	out, err := os.Create(filepath)
	if err != nil {
		fmt.Println("error create file")
		return err
	}
	defer out.Close()

	// Get the data
	resp, err := http.Get(url)
	if err != nil {
		fmt.Println("Can't download file")
		log.Println(err)
		return err
	}
	defer resp.Body.Close()

	// Writer the body to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

func parseFileNameFromUrl(url string) string {

	r, _ := http.NewRequest("GET", url, nil)
	return path.Base(r.URL.Path)
}

func parseListOfMessages(message []byte) []FileProperties {
	var files []FileProperties
	json.Unmarshal(message, &files)

	fmt.Println("%#v", files)
	return files
}

func install_app(path string) (error, string) {
	var ret_err error = nil
	var appID installRet

	cmd := exec.Command("afm-util", "install", path)
	cmd.Stdin = strings.NewReader("some input")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		log.Fatal(err)
		ret_err = err
	} else {
		fmt.Printf("Install OK %v", path)
		err = json.Unmarshal(out.Bytes(), &appID)
		failOnError(err, "fail parse install")
		ret_err = err

	}
	return ret_err, appID.Added
}

func perfromActionFormackage(wgtPropert FileProperties) {

	//compare version if not the same -> download, stop, install, run
	fileNmae := parseFileNameFromUrl(wgtPropert.DownloadUrl)
	list := getInstaledAppInfoByWgtName(wgtPropert.Name) //tmp solution
	var needToinstall bool
	if len(list) > 0 {
		if list[0].Version != wgtPropert.Version {
			downloadFile("/tmp/"+fileNmae, wgtPropert.DownloadUrl)
			needToinstall = true
			stopAppByWgtID(list[0].Id)
			uninstallWgtById(list[0].Id)
		}
	} else {
		downloadFile("/tmp/"+fileNmae, wgtPropert.DownloadUrl)
		needToinstall = true
	}

	fmt.Printf("needToinstall %v \n", needToinstall)
	if needToinstall == true {
		log.Printf("install end run %v", ("/tmp/" + fileNmae))
		err, wgtID := install_app("/tmp/" + fileNmae)
		failOnError(err, "fail to istall")
		time.Sleep(2 * time.Second)
		runWgt(wgtID)
	}

}

func download_list_of_image(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	defer c.Close()
	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			log.Println("read:", err)
			break
		}
		log.Printf("recv: %s", message)

		var files []FileProperties
		files = parseListOfMessages(message)

		for _, file := range files {
			perfromActionFormackage(file)
		}
	}
}

func update(w http.ResponseWriter, r *http.Request) {
	/*c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	defer c.Close()

	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			log.Println("read:", err)
			break
		}
		log.Printf("recv: %s", message)

		var file FileProperties
		json.Unmarshal(message, &file)

		perfromActionFormackage(file)
	}*/
	var infore appInfo
	infore.Name = "somename"
	wschan <- infore
}

func home(w http.ResponseWriter, r *http.Request) {
	log.Printf("INIT\n")
	homeTemplate.Execute(w, "ws://"+r.Host+"/download")
}

func Initwshandler(outch chan appInfo) {

	wschan = outch
	flag.Parse()
	log.SetFlags(0)

	// log.Printf("run all available apps")
	// allAppList := getAllInstalledWgt()
	// for _, appInfo := range allAppList {
	// 	runWgt(appInfo.Id)
	// }

	/*var wgtPr FileProperties
	wgtPr.DownloadUrl = "https://fusionpoc1storage.blob.core.windows.net/images/runc-system-monitor.wgt"
	wgtPr.Version = "0.1"
	wgtPr.AppID = "runc-system-monitor@0.1"
	wgtPr.Name = "runc-system-monitor"

	//2017/12/22 17:36:19 [{"Name":"Demo application","DownloadUrl":"https://fusionpoc1storage.blob.core.windows.net/images/runc-system-monitor.wgt","Version":"1.41"
	perfromActionFormackage(wgtPr)
	*/
	log.Printf("INIT\n")
	http.HandleFunc("/download", download_list_of_image)
	http.HandleFunc("/update", update)
	http.HandleFunc("/", home)
	log.Fatal(http.ListenAndServe(*addr, nil))

}

var homeTemplate = template.Must(template.New("").Parse(`
<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<script>  
window.addEventListener("load", function(evt) {

    var output = document.getElementById("output");
    var input = document.getElementById("input");
    var ws;

    var print = function(message) {
        var d = document.createElement("div");
        d.innerHTML = message;
        output.appendChild(d);
    };

    document.getElementById("open").onclick = function(evt) {
        if (ws) {0
            return false;
        }
        ws = new WebSocket("{{.}}");
        ws.onopen = function(evt) {
            print("OPEN");
        }
        ws.onclose = function(evt) {
            print("CLOSE");
            ws = null;
        }
        ws.onmessage = function(evt) {
            print("RESPONSE: " + evt.data);
        }
        ws.onerror = function(evt) {
            print("ERROR: " + evt.data);
        }
        return false;
    };

    document.getElementById("send").onclick = function(evt) {
        if (!ws) {
            return false;
        }
        print("SEND: " + input.value);
        ws.send(input.value);
        return false;
    };

    document.getElementById("close").onclick = function(evt) {
        if (!ws) {
            return false;
        }
        ws.close();
        return false;
    };

});
</script>
</head>
<body>
<table>
<tr><td valign="top" width="50%">
<p>Click "Open" to create a connection to the server, 
"Send" to send a message to the server and "Close" to close the connection. 
You can change the message and send multiple times.
<p>
<form>
<button id="open">Open</button>
<button id="close">Close</button>
<p><input id="input" type="text" value="Hello world!">
<button id="send">Send</button>
</form>
</td><td valign="top" width="50%">
<div id="output"></div>
</td></tr></table>
</body>
</html>
`))
