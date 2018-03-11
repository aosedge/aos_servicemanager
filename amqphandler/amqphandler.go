package amqphandler

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

const DEFAULT_CONFIG_FILE = "/etc/demo-application/demo_config.json"

//NEW
type serviseDiscoveryRequest struct {
	Version int      `json:"versions"`
	VIN     string   `json:"VIN"`
	Users   []string `json:"users"`
}

type serviseDiscoveryResp struct {
	Version    int                   `json:"versions"`
	Connection reqbbitConnectioninfo `json:"connection"`
}
type reqbbitConnectioninfo struct {
	Exchange     string `json:"exchange"`
	ExchangeHost string `json:"exchangeHost"`
	Queue        string `json:"queue"`
	QueueHost    string `json:"queueHost"`
	SsessionId   string `json:"ssessionId"`
	ExchageParam exchangeParams
}

//todo change after review
type exchangeParams struct {
	Host         string
	ExchangeName string
	queue        amqpQueue
}

type consumeeParams struct {
	Host  string
	queue amqpQueue
}

type amqpQueue struct {
	Name             string `json:"name"`
	Durable          string `json:"durable"`
	DeleteWhenUnused string `json:"deleteWhenUnused"`
	Exclusive        string `json:"exclusive"`
	NoWait           string `json:"noWait"`
}

type amqpLocalConnectionInfo struct {
	conn  *amqp.Connection
	ch    *amqp.Channel
	valid bool
}

//OLD

type RequestStruct struct {
	User          string `json:"userName"`
	ApplianceId   string `json:"applianceId"`
	SendTimeStamp string `json:"SendTimeStamp"`
}

type PackageInfo struct {
	Name        string
	Version     string
	DownloadUrl string
}
type FileProperties struct {
	Name          string
	DownloadUrl   string
	Version       string
	TimeStamp     string
	ResponseCount int
}

type Configuration struct {
	Amqp_host   string
	Amqp_path   string
	Amqp_user   string
	Amqp_pass   string
	Root_cert   string
	Client_cert string
	Client_key  string
	Server_name string

	Ws_host          string
	Ws_path_download string
	Ws_path_update   string

	Rbt_device_queue_name string
	Rbt_auth_queue_name   string

	Usr_username     string
	Usr_appliance_id string
}

var configuration Configuration

//NEW

// channel for return packages
var amqpChan chan PackageInfo

// connection for sending data
var exchangeInfo amqpLocalConnectionInfo

// connection for receiving data
var consumerInfo amqpLocalConnectionInfo

func sendRequestDownload(body string) {

	//todo send to channel

}

func sendRequestUpdate(body string) {

	//todo send to chnnel

}

func get_list_TLS() {
	var err error

	cfg := new(tls.Config)

	// see at the top
	cfg.RootCAs = x509.NewCertPool()

	if ca, err := ioutil.ReadFile(configuration.Root_cert); err == nil {
		log.Printf("append sert %v", configuration.Root_cert)
		cfg.RootCAs.AppendCertsFromPEM(ca)
	} else {
		log.Println("Fail AppendCertsFromPEM ", err)
		return
	}

	// Move the client cert and key to a location specific to your application
	// and load them here.
	log.Printf("OK, load %v %v %v", configuration.Root_cert, configuration.Client_cert, configuration.Client_key)

	if cert, err := tls.LoadX509KeyPair(configuration.Client_cert, configuration.Client_key); err == nil {
		log.Printf("LoadX509KeyPair")
		cfg.Certificates = append(cfg.Certificates, cert)
	} else {
		log.Printf("Fail LoadX509KeyPair :", err)
		return
	}

	cfg.ServerName = configuration.Server_name

	// see a note about Common Name (CN) at the top

	useerINFO := url.UserPassword(configuration.Amqp_user, configuration.Amqp_pass)
	urlRabbitMQ := url.URL{Scheme: "amqps", User: useerINFO, Host: configuration.Amqp_host, Path: configuration.Amqp_path}

	log.Printf("urlRabbitMQ: %v", urlRabbitMQ)
	//conn, err := amqp.DialTLS("amqps://demo_1:FusionSecurePass123@23.97.205.32:5671/", cfg)
	//conn, err := amqp.DialTLS(urlRabbitMQ.String(), cfg)

	conn, err := amqp.DialConfig(urlRabbitMQ.String(), amqp.Config{
		Heartbeat:       60,
		TLSClientConfig: cfg,
		Locale:          "en_US",
	})
	if err != nil {
		log.Println("Fail DialTLS: ", err)
		return
	}
	defer conn.Close()

	go func() {
		log.Printf("closing: %s \n", <-conn.NotifyClose(make(chan *amqp.Error)))
	}()

	log.Printf("connection of %v", urlRabbitMQ.String())

	ch, err := conn.Channel()
	if err != nil {
		log.Println("Failed to open a channel: ", err)
		return
	}
	defer ch.Close()

	q, err := ch.QueueDeclare(
		configuration.Rbt_device_queue_name, // name
		true,  // durable
		true,  // delete when unused
		false, // exclusive
		false, // noWait
		nil,   // arguments
	)
	if err != nil {
		log.Println("Failed to declare a queue: ", err)
		return
	}

	msgs, err := ch.Consume(
		q.Name, // queue
		"",     // consumer
		true,   // auto-ack
		false,  // exclusive
		false,  // no-local
		false,  // no-wait
		nil,    // args
	)

	if err != nil {
		log.Println("Failed to register a consumer: ", err)
		return
	}

	corrId := "42"

	sendTime := time.Now().UTC()
	result := &RequestStruct{
		User:          configuration.Usr_username,
		ApplianceId:   configuration.Usr_appliance_id,
		SendTimeStamp: sendTime.Format(time.RFC3339), //"2017-12-11T07:59:34.437420", ,
	}
	body_JSON, _ := json.Marshal(result)
	log.Printf("send request %s", string(body_JSON))

	err = ch.Publish(
		"", // exchange
		configuration.Rbt_auth_queue_name, // routing key
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:   "application/json",
			DeliveryMode:  2,
			CorrelationId: corrId,
			ReplyTo:       q.Name,
			Body:          []byte(body_JSON),
		})

	if err != nil {
		log.Println("Failed to publish a message: ", err)
		return
	}

	for d := range msgs {
		log.Printf(" rseice message cor id %s \n", d.CorrelationId)
		log.Printf(string(d.Body))
		log.Printf("\n")
		if corrId == d.CorrelationId {
			log.Printf("sendRequestDownload \n")
			sendRequestDownload(string(d.Body))
		} else {

			log.Printf("sendRequestUpdate \n")
			sendRequestUpdate(string(d.Body))
		}

	}
	log.Printf(" \n END ")
}
func getAmqpConnInfo(request serviseDiscoveryRequest) (reqbbitConnectioninfo, error) {

	var jsonResp serviseDiscoveryResp

	reqJson, err := json.Marshal(request)
	if err != nil {
		log.Printf("erroe :%v", err)
	}

	log.Printf("request :%v", string(reqJson))

	caCert, err := ioutil.ReadFile("server.crt") //todo add path to cerificates
	if err != nil {
		log.Error(err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	cert, err := tls.LoadX509KeyPair("client.crt", "client.key")
	if err != nil {
		log.Error(err)
		return jsonResp.Connection, err
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      caCertPool,
				Certificates: []tls.Certificate{cert},
			},
		},
	}
	resp, err := client.Post("https://localhost:8443", "application/json", bytes.NewBuffer(reqJson)) //todo: define service descovery url
	if err != nil {
		log.Error(err)
		return jsonResp.Connection, err
	}

	htmlData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Error(err)
		return jsonResp.Connection, err
	}
	defer resp.Body.Close()

	err = json.Unmarshal(htmlData, &jsonResp) // todo add check
	if err != nil {
		log.Error(err)
		return jsonResp.Connection, err
	}

	log.Printf("Results: %v\n", jsonResp)

	return jsonResp.Connection, nil
}

func getamqpLocalConnectionInfo(params exchangeParams) (amqpLocalConnectionInfo, error) {

	var retData amqpLocalConnectionInfo
	conn, err := amqp.Dial("amqp://localhost:5672/")
	if err != nil {
		log.Warning("amqp.Dial to exchange ", err)
		return retData, err
	}

	ch, err := conn.Channel()
	if err != nil {
		log.Warning("Failed to open a channel", err)
		return retData, err
	}

	err = ch.ExchangeDeclare(
		"logs",   // name
		"fanout", // type
		true,     // durable
		false,    // auto-deleted
		false,    // internal
		false,    // no-wait
		nil,      // arguments
	)
	if err != nil {
		log.Warning("Failed to declare an exchangel", err)
		return retData, err
	}

	retData.conn = conn
	retData.ch = ch
	retData.valid = true
	log.Info("create excahnge OK\n")
	return retData, nil
}

/*func getConsumeInfo(param consumeeParams) (amqpLocalConnectionInfo, error) {

}*/

func publishMessage(data []byte, correlationId string) error {
	if exchangeInfo.valid != true {
		return errors.New("invalid connection")
	}

	err := exchangeInfo.ch.Publish(
		"logs", // exchange
		"",     // routing key
		false,  // mandatory
		false,  // immediate
		amqp.Publishing{
			ContentType:   "application/json",
			DeliveryMode:  2,
			CorrelationId: correlationId,
			Body:          data,
		})
	if err != nil {
		log.Warning("error publish", err)
	}
	return err
}

//todo add return error
func InitAmqphandler(outputchan chan PackageInfo) {

	amqpChan = outputchan

	//todo get VIn users form VIS
	servRequst := serviseDiscoveryRequest{
		Version: 1,
		VIN:     "12345ZXCVBNMA1234",
		Users:   []string{"user1", "vendor2"}}

	amqpConn, err2 := getAmqpConnInfo(servRequst)
	if err2 != nil {
		log.Error("NO connection info: ", err2)
		//todo add return
	}
	log.Printf("Results: %v\n", amqpConn)

	exchangeInfo, err := getamqpLocalConnectionInfo(amqpConn.ExchageParam)
	if err != nil {
		log.Error("error get exchage info ", err)
		//todo add return
	}

	log.Info("ex %v", exchangeInfo.valid)

	//main logic
	// getSendQueuq()
	// selsect{
	// 	1) receive data
	// 	2) onCloseExcahnge connection
	// 	3) onClose qiue
	// }

	//implment closeAll

	//log.Printf("strucrt %v", servRequst)

	// file2, e := ioutil.ReadFile("./test.json")
	// if e != nil {
	// 	log.Printf("!!!!!!!!!!!!File error: %v\n", e)
	// 	os.Exit(1)
	// }

	// var jsonResp serviseDiscoveryResp
	// json.Unmarshal(file2, &jsonResp)
	// log.Printf("Results: %v\n", jsonResp.Connection.Exchange)
	// log.Printf("Results: %v\n", jsonResp)
	config := DEFAULT_CONFIG_FILE

	file, err2 := os.Open(config)
	if err2 != nil {
		log.Println(" Config open  err: \n", err2)
		return
	}

	decoder := json.NewDecoder(file)
	err = decoder.Decode(&configuration)
	if err != nil {
		log.Println(" Decode error:", err)
		return
	}

	log.Printf(" ws host %v \n", configuration.Client_cert)
	log.Printf(" ws host %v \n", configuration.Root_cert)
	log.Printf(" ws host %v \n", configuration.Ws_path_download)

	//str := ("[{\"Name\":\"Demo application\",\"DownloadUrl\":\"https://fusionpoc1storage.blob.core.windows.net/images/Demo_application_1.2_demo_1.txt\",\"Version\":\"1.2\",\"TimeStamp\":\"2017-12-14T17:12:58.1443792Z\",\"ResponseCount\":0}]")
	//str2 := ("{\"Name\":\"Demo application\",\"DownloadUrl\":\"https://fusionpoc1storage.blob.core.windows.net/images/Demo_application_1.2_demo_1.txt\",\"Version\":\"1.2\",\"TimeStamp\":\"2017-12-14T17:12:58.1443792Z\",\"ResponseCount\":0}")
	//dec := json.NewDecoder(strings.NewReader(str))

	for {
		get_list_TLS()
		time.Sleep(5 * time.Second)
		log.Println("")
	}

	log.Printf(" [.] Got ")
}
