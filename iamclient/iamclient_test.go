// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2020 Renesas Inc.
// Copyright 2020 EPAM Systems Inc.
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

package iamclient_test

import (
	"context"
	"io/ioutil"
	"net"
	"net/url"
	"os"
	"path"
	"reflect"
	"strconv"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/iamanager"
	"google.golang.org/grpc"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/iamclient"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const serverURL = "localhost:8089"

/*******************************************************************************
 * Types
 ******************************************************************************/

type testServer struct {
	grpcServer *grpc.Server
	csr        map[string]string
	certURL    map[string]string
	keyURL     map[string]string
}

type testSender struct {
	csr    map[string]string
	serial map[string]string
}

/*******************************************************************************
 * Vars
 ******************************************************************************/

var tmpDir string

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	var err error

	tmpDir, err = ioutil.TempDir("", "iam_")
	if err != nil {
		log.Fatalf("Error create temporary dir: %s", err)
	}

	ret := m.Run()

	if err := os.RemoveAll(tmpDir); err != nil {
		log.Fatalf("Error removing tmp dir: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestRenewCertificatesNotification(t *testing.T) {
	sender := &testSender{}

	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %s", err)
	}
	defer server.close()

	time.Sleep(5 * time.Second)

	server.csr = map[string]string{"online": "onlineCSR", "offline": "offlineCSR"}

	client, err := iamclient.New(&config.Config{IAMServerURL: serverURL}, sender, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %s", err)
	}
	defer client.Close()

	certInfo := []amqp.CertificateNotification{
		{Type: "online", Serial: "serail1", ValidTill: time.Now()},
		{Type: "offline", Serial: "serail2", ValidTill: time.Now()},
	}

	if err = client.RenewCertificatesNotification("testID", "pwd", certInfo); err != nil {
		t.Fatalf("Can't process renew certificate notification: %s", err)
	}

	if !reflect.DeepEqual(server.csr, sender.csr) {
		t.Errorf("Wrong sender CSR: %v", sender.csr)
	}
}

func TestInstallCertificates(t *testing.T) {
	sender := &testSender{}

	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %s", err)
	}
	defer server.close()

	// openssl req -newkey rsa:2048 -nodes -keyout online_key.pem -x509 -days 365 -out online_cert.pem -set_serial 1
	onlineCert := `
-----BEGIN CERTIFICATE-----
MIIDTjCCAjagAwIBAgIBATANBgkqhkiG9w0BAQsFADBAMQswCQYDVQQGEwJVQTET
MBEGA1UECAwKU29tZS1TdGF0ZTENMAsGA1UEBwwES3lpdjENMAsGA1UECgwERVBB
TTAeFw0yMDA5MTAxNDE1MzNaFw0yMTA5MTAxNDE1MzNaMEAxCzAJBgNVBAYTAlVB
MRMwEQYDVQQIDApTb21lLVN0YXRlMQ0wCwYDVQQHDARLeWl2MQ0wCwYDVQQKDARF
UEFNMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAvc1utrAEKlJ5rB9W
envNiEjGDW36NKmEc69nITCmI7wedg00oRSIOZTfZVdKp/Nsh0uuFnOYKt9unXso
fYGzCk4KxWL+t9HDsWMbpL7S/QNB1UF7P+rQFKp7gsj8wQmy+rpvRxwuTC7BfpJ9
az9WoreF4W43m3zDF4lIetnFDilx1NBYBfvW7/KW3e/iJcjs8WPSQCDVU3rOhFRd
8Y/qt4EOiJY5xTya0YFxgF37fKH/asy+ija54Wy7DhbLlkyE2JUqxp/SaomzUAQX
uf/kWYPh/s7VewjfW9xouT9aDLZEsLNQMEk6HRPY9DW2pCdYFjE8qdVYAs/f0V5y
QhPUaQIDAQABo1MwUTAdBgNVHQ4EFgQUCwq4ojzTld4lTka0POLVqgkyMJ4wHwYD
VR0jBBgwFoAUCwq4ojzTld4lTka0POLVqgkyMJ4wDwYDVR0TAQH/BAUwAwEB/zAN
BgkqhkiG9w0BAQsFAAOCAQEAIDdNqOeK4JyZtETgfwj5gqneZ9EH+euJ6HaOict8
7PZzyEt2p0QHZOWBm0s07kd+7SiS+UCLIYjJPCMWDAEN7V6zZqqp9oQL5bR1hSSe
7tDrOgVkEf8T1x4u4F2bpLJsz+GDX7t8H5lcvPGq0YkPDHluSASFJc//GN3TrSDV
yGuI5R8+7XalVAaCteo/Y2zhERMDsVm0KTGeTP2sblaBVFux7GTcWA1DHqVDYFnw
ExQScaNHy16y+a/nlDSpTraFIQdSG3twwlfyjR/Ua5SbkzzTEVG+Kll/3VvOagwV
8xTUZedoJTC7G5C2DGs+syl/B8WVrn7QPK+VSVU2QEG50Q==
-----END CERTIFICATE-----
`

	// openssl req -newkey rsa:2048 -nodes -keyout offline_key.pem -x509 -days 365 -out offline_cert.pem -set_serial 2
	offlineCert := `
-----BEGIN CERTIFICATE-----
MIIDTjCCAjagAwIBAgIBAjANBgkqhkiG9w0BAQsFADBAMQswCQYDVQQGEwJVQTET
MBEGA1UECAwKU29tZS1TdGF0ZTENMAsGA1UEBwwES3lpdjENMAsGA1UECgwERVBB
TTAeFw0yMDA5MTAxNDE1NTdaFw0yMTA5MTAxNDE1NTdaMEAxCzAJBgNVBAYTAlVB
MRMwEQYDVQQIDApTb21lLVN0YXRlMQ0wCwYDVQQHDARLeWl2MQ0wCwYDVQQKDARF
UEFNMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAoDW6XO7bHeTRnF10
5FZ6wFKnyp0v6aACJxuWrLdOGlOZeaO/7q1I1qD5ogxV2Fh6IHm6+4e+wo4lLAUX
F7Fi/ab/FZjHU6a2XNxNi6VnE7C7wp/UDt8/KfHV6ZzkyubHoraMl5Xi8g+3IN1N
kTc90OCZPL6aZHpMXaP+5dOXad/Pv2/6xHKB/Qx+VIQlo1c7oGrYxFc29LsUtlrP
C0tcDVAfMBBlRIhkw/dXoGcBSfqBIAnORYq/kGcbEewWDLdBiExr+TtJh837NgiE
q1+NIvWF4AS3vL+tm/rvXfz71v3Gy4JqL/8Eqn489VE2Vw/XJfLjte8fPpKc5NVN
Ah7bsQIDAQABo1MwUTAdBgNVHQ4EFgQUdjLMDiQH1r6a4ra3GX9hwdVzlWkwHwYD
VR0jBBgwFoAUdjLMDiQH1r6a4ra3GX9hwdVzlWkwDwYDVR0TAQH/BAUwAwEB/zAN
BgkqhkiG9w0BAQsFAAOCAQEAYCm82d7bMk3bROnNvIp/IcZdO6SItuhTy6iPQRd8
ZqIIPsOG/8uMTKimCvJZIhsb+P9rRj3Ubb4EAAHpftZJx6Y1yrGlFUVYhsDNSsQR
RqT4gg71B7R3Mgu0T9tV96nNa7P0w32wvNtc7B/it8DsFMz1wOcq/PPh3ufoR9Lm
onsMCZ7ep/quYwXutmvMrE3SLGApTc7lnqPqBtVq4ju29VS6wDsvLgEyuZQO9HlJ
KbjNq7kDX6ZbgJTVgVwEVY16464lTJ3j6/Osi3R3bUs5cg4onCFAS5KUTsfkbZ+G
KzpDMr/kcScwzmmNcN8aLp31TSRVee64QrK7yF3YJxL+rA==
-----END CERTIFICATE-----	
`

	if err = ioutil.WriteFile(path.Join(tmpDir, "online_cert.pem"), []byte(onlineCert), 0644); err != nil {
		log.Fatalf("Error create online cert: %s", err)
	}

	if err = ioutil.WriteFile(path.Join(tmpDir, "offline_cert.pem"), []byte(offlineCert), 0644); err != nil {
		log.Fatalf("Error create offline cert: %s", err)
	}

	onlineURL := url.URL{Scheme: "file", Path: path.Join(tmpDir, "online_cert.pem")}
	offlineURL := url.URL{Scheme: "file", Path: path.Join(tmpDir, "offline_cert.pem")}

	server.certURL = map[string]string{
		"online":  onlineURL.String(),
		"offline": offlineURL.String()}

	client, err := iamclient.New(&config.Config{IAMServerURL: serverURL}, sender, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %s", err)
	}
	defer client.Close()

	certInfo := []amqp.IssuedUnitCertificatesInfo{
		{Type: "online", CertificateChain: "onlineCert"},
		{Type: "offline", CertificateChain: "offlineCert"},
	}

	if err = client.InstallCertificates(certInfo); err != nil {
		t.Fatalf("Can't process install certificates request: %s", err)
	}

	serial := map[string]string{"online": "1", "offline": "2"}

	if !reflect.DeepEqual(serial, sender.serial) {
		t.Errorf("Wrong sender CSR: %v", sender.serial)
	}
}

func TestGetCertificates(t *testing.T) {
	sender := &testSender{}

	server, err := newTestServer(serverURL)
	if err != nil {
		t.Fatalf("Can't create test server: %s", err)
	}
	defer server.close()

	server.certURL = map[string]string{"online": "onlineCertURL", "offline": "offlineCertURL"}
	server.keyURL = map[string]string{"online": "onlineKeyURL", "offline": "offlineKeyURL"}

	client, err := iamclient.New(&config.Config{IAMServerURL: serverURL}, sender, true)
	if err != nil {
		t.Fatalf("Can't create IAM client: %s", err)
	}
	defer client.Close()

	for serial, certType := range []string{"online", "offline"} {
		certURL, keyURL, err := client.GetCertificate(certType, nil, strconv.Itoa(serial))
		if err != nil {
			t.Errorf("Can't get %s certificate: %s", certType, err)
			continue
		}

		if certURL != server.certURL[certType] {
			t.Errorf("Wrong %s cert URL: %s", certType, certURL)
		}

		if keyURL != server.keyURL[certType] {
			t.Errorf("Wrong %s key URL: %s", certType, keyURL)
		}
	}
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newTestServer(url string) (server *testServer, err error) {
	server = &testServer{}

	listener, err := net.Listen("tcp", url)
	if err != nil {
		return nil, err
	}
	server.grpcServer = grpc.NewServer()

	pb.RegisterIAManagerServer(server.grpcServer, server)

	go server.grpcServer.Serve(listener)

	return server, nil
}

func (server *testServer) close() (err error) {
	if server.grpcServer != nil {
		server.grpcServer.Stop()
	}

	return nil
}

func (server *testServer) CreateKeys(context context.Context, req *pb.CreateKeysReq) (rsp *pb.CreateKeysRsp, err error) {
	rsp = &pb.CreateKeysRsp{Type: req.Type}

	csr, ok := server.csr[req.Type]
	if !ok {
		rsp.Error = "not found"
	}

	rsp.Csr = csr

	return rsp, nil
}

func (server *testServer) ApplyCert(context context.Context, req *pb.ApplyCertReq) (rsp *pb.ApplyCertRsp, err error) {
	rsp = &pb.ApplyCertRsp{Type: req.Type}

	certURL, ok := server.certURL[req.Type]
	if !ok {
		rsp.Error = "not found"
	}

	rsp.CertUrl = certURL

	return rsp, nil
}

func (server *testServer) GetCert(context context.Context, req *pb.GetCertReq) (rsp *pb.GetCertRsp, err error) {
	rsp = &pb.GetCertRsp{Type: req.Type}

	certURL, ok := server.certURL[req.Type]
	if !ok {
		rsp.Error = "not found"
	}

	keyURL, ok := server.keyURL[req.Type]
	if !ok {
		rsp.Error = "not found"
	}

	rsp.CertUrl = certURL
	rsp.KeyUrl = keyURL

	return rsp, nil
}

func (sender *testSender) SendIssueUnitCertificatesRequest(requests []amqp.CertificateRequest) (err error) {
	sender.csr = make(map[string]string)

	for _, request := range requests {
		sender.csr[request.Type] = request.Csr
	}

	return nil
}

func (sender *testSender) SendInstallCertificatesConfirmation(confirmations []amqp.CertificateConfirmation) (err error) {
	sender.serial = make(map[string]string)

	for _, confirmation := range confirmations {
		sender.serial[confirmation.Type] = confirmation.Serial
	}

	return nil
}
