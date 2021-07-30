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

// Package launcher provides set of API to controls services lifecycle

package downloader_test

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"syscall"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/image"
	"gitpct.epam.com/epmd-aepr/aos_common/utils/testtools"

	"aos_servicemanager/alerts"
	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/downloader"
	"aos_servicemanager/fcrypt"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type fakeFcrypt struct {
}

type fakeSymmetricContext struct {
}

type fakeSignContext struct {
}

type fakeLayerSender struct {
}

//TestSender instance
type TestSender struct {
}

type alertsCounter struct {
	alertStarted     int
	alertFinished    int
	alertInterrupted int
	alertResumed     int
	alertStatus      int
}

/*******************************************************************************
 * Consts
 ******************************************************************************/

const pythonServerScript = "start.py"
const decryptedDirName = "decrypt"

/*******************************************************************************
 * Vars
 ******************************************************************************/

var downloaderObj *downloader.Downloader
var serverDir string
var downloadDir string
var testSender *TestSender
var alertsCnt alertsCounter

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

	if err = setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	conf := config.Config{DownloadDir: downloadDir}

	downloaderObj, err = downloader.New(&conf, new(fakeFcrypt), testSender)
	if err != nil {
		log.Fatalf("Error creation downloader: %s", err)
	}

	ret := m.Run()

	if err = cleanup(); err != nil {
		log.Fatalf("Error cleaning up: %s", err)
	}

	os.Exit(ret)
}

func TestDownload(t *testing.T) {
	//Define alertsCnt with 0
	alertsCnt = alertsCounter{}
	sentData := []byte("Hello downloader\n")
	fileName := "package.txt"

	err := ioutil.WriteFile(path.Join(serverDir, fileName), sentData, 0644)
	if err != nil {
		log.Fatalf("Can't create package file: %s", err)
	}
	packageInfo := preparePackageInfo(fileName)
	chains := []amqp.CertificateChain{}
	certs := []amqp.Certificate{}

	_, err = downloaderObj.DownloadAndDecrypt(packageInfo, chains, certs, path.Join(downloadDir, decryptedDirName))
	if err != nil {
		t.Errorf("Can't DownloadAndDecrypt %s", err)
	}

	if alertsCnt.alertStarted != 1 {
		t.Error("DownloadStartedAlert was not received")
	}

	if alertsCnt.alertFinished != 1 {
		t.Error("DownloadFinishedAlert was not received")
	}
}

func TestInterruptResumeDownload(t *testing.T) {
	//Define alertsCnt with 0
	alertsCnt = alertsCounter{}
	fileName := "bigPackage.txt"
	filePath := path.Join(serverDir, fileName)
	// Generate file with size 1Mb
	if err := generateBigPackage(filePath, "1", "1M"); err != nil {
		t.Errorf("Can't generate big file")
	}
	defer os.RemoveAll(filePath)

	//Kill connection after 32 secs to receive status alert
	go func() {
		time.Sleep(32 * time.Second)
		log.Debug("Kill connection")

		if _, err := exec.Command("ss", "-K", "src", "127.0.0.1", "dport", "=", "8001").CombinedOutput(); err != nil {
			t.Errorf("Can't stop http server: %s", err)
		}
	}()

	packageInfo := preparePackageInfo(fileName)
	chains := []amqp.CertificateChain{}
	certs := []amqp.Certificate{}

	_, err := downloaderObj.DownloadAndDecrypt(packageInfo, chains, certs, path.Join(downloadDir, decryptedDirName))
	if err == nil {
		t.Errorf("Error was expected DownloadAndDecrypt %s", err)
	}

	if alertsCnt.alertStarted != 1 {
		t.Error("DownloadStartedAlert was not received")
	}

	if alertsCnt.alertStatus == 0 {
		t.Error("DownloadStatusAlert was not received")
	}

	if alertsCnt.alertInterrupted != 1 {
		t.Error("DownloadInterruptedAlert was not received")
	}

	time.Sleep(1 * time.Second)

	_, err = downloaderObj.DownloadAndDecrypt(packageInfo, chains, certs, path.Join(downloadDir, decryptedDirName))
	if err != nil {
		t.Errorf("Can't DownloadAndDecrypt %s", err)
	}

	if alertsCnt.alertResumed != 1 {
		t.Error("DownloadResumedAlert was not received")
	}

	if alertsCnt.alertFinished != 1 {
		t.Error("DownloadFinishedAlert was not received")
	}
}

// The test checks if available disk size is counted properly after resuming
// download and takes into account files that already have been partially downloaded
func TestAvailableSize(t *testing.T) {
	mountDir, imgFile, err := createTmpDisk(8)
	if err != nil {
		t.Fatalf("Can't create temporary disk: %s", err)
	}

	defer func() {
		if err = clearTmpDisk(mountDir, imgFile); err != nil {
			t.Errorf("Can't remove temporary disk: %s", err)
		}
	}()

	conf := config.Config{DownloadDir: mountDir}

	secondDownloader, err := downloader.New(&conf, new(fakeFcrypt), testSender)
	if err != nil {
		log.Fatalf("Error creation downloader: %s", err)
	}

	fileName := "bigPackage.txt"
	filePath := path.Join(serverDir, fileName)
	// Generate file with size 1Mb
	if err := generateBigPackage(filePath, "1", "1M"); err != nil {
		t.Errorf("Can't generate big file: %s", err)
	}

	var stat syscall.Statfs_t
	syscall.Statfs(mountDir, &stat)

	// Fill-up the available space to fit only one package and safe 100KB for metadata files and etc
	payloadSize := stat.Bavail*uint64(stat.Bsize) - 2*1024*1024 - 100*1024

	if err := generateBigPackage(path.Join(mountDir, "payload"), fmt.Sprintf("%d", payloadSize), "1"); err != nil {
		t.Errorf("Can't generate payload file: %s", err)
	}

	// Download more than half of the package in 1 minute and kill the connection
	go func() {
		time.Sleep(time.Minute)
		log.Debug("Kill connection")

		if _, err := exec.Command("ss", "-K", "src", "127.0.0.1", "dport", "=", "8001").CombinedOutput(); err != nil {
			t.Errorf("Can't stop http server: %s", err)
		}
	}()

	packageInfo := preparePackageInfo(fileName)
	chains := []amqp.CertificateChain{}
	certs := []amqp.Certificate{}

	if _, err = secondDownloader.DownloadAndDecrypt(packageInfo, chains, certs, path.Join(mountDir, decryptedDirName)); err == nil {
		t.Error("Error was expected")
	}

	time.Sleep(1 * time.Second)

	if _, err = secondDownloader.DownloadAndDecrypt(packageInfo, chains, certs, path.Join(mountDir, decryptedDirName)); err != nil {
		t.Errorf("Can't download and decrypt package: %s", err)
	}
}

/*******************************************************************************
 * Interfaces
 ******************************************************************************/

func (crypt fakeFcrypt) ImportSessionKey(keyInfo fcrypt.CryptoSessionKeyInfo) (fcrypt.SymmetricContextInterface, error) {
	return new(fakeSymmetricContext), nil
}

func (crypt fakeFcrypt) CreateSignContext() (fcrypt.SignContextInterface, error) {
	return new(fakeSignContext), nil
}

func (symCont fakeSymmetricContext) DecryptFile(encryptedFile, clearFile *os.File) (err error) {
	data, err := ioutil.ReadFile(encryptedFile.Name())
	if err != nil {
		return err
	}

	if _, err = clearFile.Write(data); err != nil {
		return err
	}

	return nil
}

func (sigCont fakeSignContext) AddCertificate(fingerprint string, asn1Bytes []byte) (err error) {
	return nil
}

func (sigCont fakeSignContext) AddCertificateChain(name string, fingerprints []string) (err error) {
	return nil
}

func (sigCont fakeSignContext) VerifySign(f *os.File, chainName string, algName string, signValue []byte) (err error) {
	return nil
}

func (instance *TestSender) SendDownloadStartedStatusAlert(downloadStatus alerts.DownloadAlertStatus) {
	log.Debug("DownloadStartedAlert")
	printDownloadAlertStatus(downloadStatus)
	alertsCnt.alertStarted++
}

func (instance *TestSender) SendDownloadFinishedStatusAlert(downloadStatus alerts.DownloadAlertStatus, code int) {
	log.Debugf("DownloadFinishedStatusAlert. code = %d", code)
	printDownloadAlertStatus(downloadStatus)
	alertsCnt.alertFinished++
}

func (instance *TestSender) SendDownloadInterruptedStatusAlert(downloadStatus alerts.DownloadAlertStatus, reason string) {
	log.Debugf("DowloadInterruptedStatusAlert. reason = %s", reason)
	printDownloadAlertStatus(downloadStatus)
	alertsCnt.alertInterrupted++
}

func (instance *TestSender) SendDownloadResumedStatusAlert(downloadStatus alerts.DownloadAlertStatus, reason string) {
	log.Debugf("DownloadResumedStatusAlert. reason = %s", reason)
	printDownloadAlertStatus(downloadStatus)
	alertsCnt.alertResumed++
}

func (instance *TestSender) SendDownloadStatusAlert(downloadStatus alerts.DownloadAlertStatus) {
	log.Debug("DownloadStatusAlert")
	printDownloadAlertStatus(downloadStatus)
	alertsCnt.alertStatus++
}

func printDownloadAlertStatus(downloadStatus alerts.DownloadAlertStatus) {
	log.Debugf("DownloadAlertStatus: source = %s, url = %s, progress = %d, downloadedBytes = %d, totalBytes = %d",
		downloadStatus.Source, downloadStatus.URL, downloadStatus.Progress, downloadStatus.DownloadedBytes,
		downloadStatus.TotalBytes)
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func setup() (err error) {
	serverDir, err = ioutil.TempDir("", "server_")
	if err != nil {
		return err
	}

	downloadDir, err = ioutil.TempDir("", "wd_")
	if err != nil {
		return err
	}

	if err = setWondershaperLimit("lo", "128"); err != nil {
		return err
	}

	go func() {
		log.Fatal(http.ListenAndServe(":8001", http.FileServer(http.Dir(serverDir))))
	}()

	time.Sleep(time.Second)

	return nil
}

func cleanup() (err error) {
	clearWondershaperLimit("lo")
	os.RemoveAll(downloadDir)
	os.RemoveAll(serverDir)

	return nil
}

func preparePackageInfo(fileName string) (packageInfo amqp.DecryptDataStruct) {
	packageInfo.URLs = []string{"http://localhost:8001/" + fileName}

	filePath := path.Join(serverDir, fileName)
	imageFileInfo, err := image.CreateFileInfo(filePath)
	if err != nil {
		log.Error("error CreateFileInfo", err)
		return packageInfo
	}

	recInfo := struct {
		Serial string `json:"serial"`
		Issuer []byte `json:"issuer"`
	}{
		Serial: "string",
		Issuer: []byte("issuer"),
	}

	packageInfo.Sha256 = imageFileInfo.Sha256
	packageInfo.Sha512 = imageFileInfo.Sha512
	packageInfo.Size = imageFileInfo.Size
	packageInfo.DecryptionInfo = &amqp.DecryptionInfo{
		BlockAlg:     "AES256/CBC/pkcs7",
		BlockIv:      []byte{},
		BlockKey:     []byte{},
		AsymAlg:      "RSA/PKCS1v1_5",
		ReceiverInfo: &recInfo,
	}
	packageInfo.Signs = new(amqp.Signs)

	return packageInfo
}

func generateBigPackage(fileName string, count, bs string) (err error) {
	if output, err := exec.Command("dd", "if=/dev/zero", "of="+fileName, "bs="+bs,
		"count="+count).CombinedOutput(); err != nil {
		return fmt.Errorf("%s (%s)", err, (string(output)))
	}

	return nil
}

//Set traffic limit for interface
func setWondershaperLimit(iface string, limit string) (err error) {
	if output, err := exec.Command("wondershaper", "-a", iface, "-d", limit).CombinedOutput(); err != nil {
		return fmt.Errorf("%s (%s)", err, (string(output)))
	}

	return nil
}

func clearWondershaperLimit(iface string) (err error) {
	if output, err := exec.Command("wondershaper", "-ca", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("%s (%s)", err, (string(output)))
	}

	return nil
}

func createTmpDisk(sizeMb uint64) (mountDir, imgFileName string, err error) {
	imgFile, err := ioutil.TempFile("", "um_")
	if err != nil {
		return "", "", err
	}

	imgFileName = imgFile.Name()

	defer func() {
		if err != nil {
			os.Remove(imgFileName)
		}
	}()

	if mountDir, err = ioutil.TempDir("", "um_"); err != nil {
		return "", "", err
	}

	defer func() {
		if err != nil {
			os.RemoveAll(mountDir)
		}
	}()

	if err = testtools.CreateFilePartition(imgFileName, "ext3", sizeMb, nil, false); err != nil {
		return "", "", err
	}

	if output, err := exec.Command("mount", imgFileName, mountDir).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("%s (%s)", err, (string(output)))
	}

	return mountDir, imgFileName, nil
}

func clearTmpDisk(mountDir, imgFile string) (err error) {
	if output, err := exec.Command("umount", mountDir).CombinedOutput(); err != nil {
		return fmt.Errorf("%s (%s)", err, (string(output)))
	}

	if err = os.RemoveAll(mountDir); err != nil {
		return err
	}

	if err = os.Remove(imgFile); err != nil {
		return err
	}

	return nil
}
