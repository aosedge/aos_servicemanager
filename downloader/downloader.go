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

package downloader

import (
	"errors"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cavaliercoder/grab"
	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/image"

	"aos_servicemanager/alerts"
	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/fcrypt"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const downloadDirName = "download"
const decryptedDirName = "decrypt"

const updateDownloadsTime = 10 * time.Second
const statusAlertTickCount = 3

const startedFlagSuffix = ".started"
const interruptedFlagSuffix = ".interrupted"
const flagAccessRights = 0644
const interruptionReason = "Internet connection error"

const moduleID = "downloader"

/*******************************************************************************
 * Types
 ******************************************************************************/

// FcryptInterface api to work with aoscrypto engine
type fcryptInterface interface {
	ImportSessionKey(keyInfo fcrypt.CryptoSessionKeyInfo) (symetrContext fcrypt.SymmetricContextInterface, err error)
	CreateSignContext() (fcrypt.SignContextInterface, error)
}

// Downloader instance
type Downloader struct {
	crypt        fcryptInterface
	downloadDir  string
	decryptedDir string
	sender       alertSender
}

//alertSender provdes sender interface
type alertSender interface {
	// SendDownloadStartedStatusAlert sends download started status alert
	SendDownloadStartedStatusAlert(downloadStatus alerts.DownloadAlertStatus)

	// SendDownloadFinishedStatusAlert sends download finished status alert
	SendDownloadFinishedStatusAlert(downloadStatus alerts.DownloadAlertStatus, code int)

	// SendDownloadInterruptedStatusAlert sends download interrupted status alert
	SendDownloadInterruptedStatusAlert(downloadStatus alerts.DownloadAlertStatus, reason string)

	// SendDownloadResumedStatusAlert sends download resumed status alert
	SendDownloadResumedStatusAlert(downloadStatus alerts.DownloadAlertStatus, reason string)

	// SendDownloadStatusAlert sends download status alert
	SendDownloadStatusAlert(downloadStatus alerts.DownloadAlertStatus)
}

/*******************************************************************************
* Public
*******************************************************************************/

// New creates new downloader object
func New(config *config.Config, fcrypt fcryptInterface, sender alertSender) (downloader *Downloader, err error) {
	log.Debug("New downloader")

	if config == nil {
		return nil, errors.New("config is nil")
	}

	if err = os.MkdirAll(path.Join(config.WorkingDir, downloadDirName), 755); err != nil {
		return nil, err
	}

	if err = os.MkdirAll(path.Join(config.WorkingDir, decryptedDirName), 755); err != nil {
		return nil, err
	}

	downloader = &Downloader{
		crypt:        fcrypt,
		downloadDir:  path.Join(config.WorkingDir, downloadDirName),
		decryptedDir: path.Join(config.WorkingDir, decryptedDirName),
		sender:       sender,
	}

	return downloader, nil
}

// Close cleans up downloader stuff
func (downloader *Downloader) Close() {
	os.RemoveAll(downloader.decryptedDir)
}

// DownloadAndDecrypt download decrypt and validate blob
// if decryptDir = "" downloader will use own dir
func (downloader *Downloader) DownloadAndDecrypt(packageInfo amqp.DecryptDataStruct,
	chains []amqp.CertificateChain, certs []amqp.Certificate, decryptDir string) (resultFile string, err error) {
	var stat syscall.Statfs_t

	syscall.Statfs(downloader.downloadDir, &stat)

	if packageInfo.Size > stat.Bavail*uint64(stat.Bsize) {
		return resultFile, errors.New("not enough space")
	}

	fileName, err := downloader.processURLs(packageInfo.URLs)
	if err != nil {
		return "", err
	}
	defer os.Remove(fileName)

	if err = image.CheckFileInfo(fileName, image.FileInfo{
		Sha256: packageInfo.Sha256,
		Sha512: packageInfo.Sha512,
		Size:   packageInfo.Size}); err != nil {
		return "", err
	}

	if decryptDir == "" {
		decryptDir = downloader.decryptedDir
	}

	resultFile, err = downloader.decryptPackage(fileName, decryptDir, packageInfo.DecryptionInfo)
	if err != nil {
		return "", err
	}

	err = downloader.validateSigns(resultFile, packageInfo.Signs, chains, certs)
	if err != nil {
		os.RemoveAll(resultFile)
		return "", err
	}

	return resultFile, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (downloader *Downloader) processURLs(urls []string) (resultFile string, err error) {
	if len(urls) == 0 {
		return "", errors.New("file list URLs is empty")
	}

	fileDownloaded := false

	for _, rawURL := range urls {
		url, err := url.Parse(rawURL)
		if err != nil {
			return "", err
		}

		// skip already downloaded and decrypted files
		if !url.IsAbs() {
			break
		}

		if resultFile, err = downloader.download(rawURL); err != nil {
			log.WithField("url", rawURL).Warningf("Can't download file: %s", err)
			continue
		}

		fileDownloaded = true

		break
	}

	if !fileDownloaded {
		return resultFile, errors.New("can't download file from any source")
	}

	return resultFile, nil
}

func (downloader *Downloader) download(url string) (fileName string, err error) {
	log.WithField("url", url).Debug("Start downloading file")

	grabClient := grab.NewClient()

	timer := time.NewTicker(updateDownloadsTime)
	defer timer.Stop()

	req, err := grab.NewRequest(downloader.downloadDir, url)
	if err != nil {
		return "", err
	}

	//Create BeforeCopy hook
	req.BeforeCopy = downloader.beforeCopyHook

	//Create AfterCopy hook. Will be called only if copy was successfull
	req.AfterCopy = downloader.afterCopyHook

	resp := grabClient.Do(req)

	defer func() {
		if err = downloader.finalizeDownload(resp); err != nil {
			log.Warningf("Error finalizing download: %s", err)
		}
	}()

	counter := 0
	for {
		select {
		case <-timer.C:
			log.WithFields(log.Fields{"complete": resp.BytesComplete(), "total": resp.Size}).Debug("Download progress")

			counter++
			//Send status
			if downloader.sender != nil && counter >= statusAlertTickCount {
				counter = 0
				downloadStatus, err := getAlertStatusFromResponse(resp)
				if err != nil {
					return "", err
				}

				downloader.sender.SendDownloadStatusAlert(downloadStatus)
			}

		case <-resp.Done:
			if err := resp.Err(); err != nil {
				return "", err
			}

			log.WithFields(log.Fields{"url": url, "file": resp.Filename}).Debug("Download complete")

			return resp.Filename, nil
		}
	}
}

func (downloader *Downloader) beforeCopyHook(resp *grab.Response) (err error) {
	copyStartedFile := resp.Filename + startedFlagSuffix
	copyInterruptedFile := resp.Filename + interruptedFlagSuffix

	if !fileExists(copyStartedFile) {
		if err = createFile(copyStartedFile); err != nil {
			return err
		}

		if downloader.sender != nil {
			downloadStatus, err := getAlertStatusFromResponse(resp)
			if err != nil {
				return err
			}

			downloader.sender.SendDownloadStartedStatusAlert(downloadStatus)
		}
	} else {
		//If download was started and no interrupted file was created,
		//then no interrupted alert was sent. Sending interrupted alert
		if !fileExists(copyInterruptedFile) {
			if err = createFile(copyInterruptedFile); err != nil {
				return err
			}

			log.Debug("Send download interrupted alert")
			if downloader.sender != nil {
				downloadStatus, err := getAlertStatusFromResponse(resp)
				if err != nil {
					return err
				}

				//TODO: read reason from interruption flag file
				downloader.sender.SendDownloadInterruptedStatusAlert(downloadStatus, interruptionReason)
			}
		}
	}

	if resp.DidResume {
		log.Debug("Send download resumed alert")
		if downloader.sender != nil {
			downloadStatus, err := getAlertStatusFromResponse(resp)
			if err != nil {
				return err
			}

			//TODO: read reason from interruption flag file
			downloader.sender.SendDownloadResumedStatusAlert(downloadStatus, interruptionReason)
		}
	}

	return nil
}

func (downloader *Downloader) afterCopyHook(resp *grab.Response) (err error) {
	copyStartedFile := resp.Filename + startedFlagSuffix
	copyInterruptedFile := resp.Filename + interruptedFlagSuffix

	if fileExists(copyStartedFile) {
		log.WithField("file", copyStartedFile).Debug("Flag removed. Download finished successfully")
		if err = os.Remove(copyStartedFile); err != nil {
			return err
		}
	}

	if fileExists(copyInterruptedFile) {
		if err = os.Remove(copyInterruptedFile); err != nil {
			return err
		}
	}

	log.Debug("Send download finished alert")
	if downloader.sender != nil {
		downloadStatus, err := getAlertStatusFromResponse(resp)
		if err != nil {
			return err
		}

		downloader.sender.SendDownloadFinishedStatusAlert(downloadStatus, resp.HTTPResponse.StatusCode)
	}

	return nil
}

func (downloader *Downloader) finalizeDownload(resp *grab.Response) (err error) {
	copyStartedFile := resp.Filename + startedFlagSuffix
	copyInterruptedFile := resp.Filename + interruptedFlagSuffix

	if fileExists(copyStartedFile) {
		log.Debug("Send download interrupted alert")
		if downloader.sender != nil {
			downloadStatus, err := getAlertStatusFromResponse(resp)
			if err != nil {
				return err
			}

			//TODO: read reason from interruption flag file
			downloader.sender.SendDownloadInterruptedStatusAlert(downloadStatus, interruptionReason)
		}

		if err = createFile(copyInterruptedFile); err != nil {
			return err
		}
	}

	return nil
}

func (downloader *Downloader) decryptPackage(srcFileName string, decryptDir string,
	decryptionInfo *amqp.DecryptionInfo) (resultFile string, err error) {
	if decryptionInfo == nil {
		return "", errors.New("no decrypt image info")
	}

	if decryptionInfo.ReceiverInfo == nil {
		return "", errors.New("no receiver info")
	}

	context, err := downloader.crypt.ImportSessionKey(fcrypt.CryptoSessionKeyInfo{
		SymmetricAlgName:  decryptionInfo.BlockAlg,
		SessionKey:        decryptionInfo.BlockKey,
		SessionIV:         decryptionInfo.BlockIv,
		AsymmetricAlgName: decryptionInfo.AsymAlg,
		ReceiverInfo: fcrypt.ReceiverInfo{
			Issuer: decryptionInfo.ReceiverInfo.Issuer,
			Serial: decryptionInfo.ReceiverInfo.Serial},
	})
	if err != nil {
		return "", err
	}

	srcFile, err := os.Open(srcFileName)
	if err != nil {
		return "", err
	}
	defer srcFile.Close()

	dstFileName := path.Join(decryptDir, filepath.Base(srcFileName))

	dstFile, err := os.OpenFile(dstFileName, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return "", err
	}
	defer func() {
		dstFile.Close()

		if err != nil {
			os.RemoveAll(dstFileName)
		}
	}()

	log.WithFields(log.Fields{"srcFile": srcFile.Name(), "dstFile": dstFile.Name()}).Debug("Decrypt image")

	if err = context.DecryptFile(srcFile, dstFile); err != nil {
		return "", err
	}

	return dstFileName, err
}

func (downloader *Downloader) validateSigns(filePath string, signs *amqp.Signs,
	chains []amqp.CertificateChain, certs []amqp.Certificate) (err error) {
	context, err := downloader.crypt.CreateSignContext()
	if err != nil {
		return err
	}

	for _, cert := range certs {
		if err = context.AddCertificate(cert.Fingerprint, cert.Certificate); err != nil {
			return err
		}
	}

	for _, chain := range chains {
		if err = context.AddCertificateChain(chain.Name, chain.Fingerprints); err != nil {
			return err
		}
	}

	if signs == nil {
		return errors.New("package does not have signature")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	log.WithField("file", file.Name()).Debug("Check signature")

	if err = context.VerifySign(file, signs.ChainName, signs.Alg, signs.Value); err != nil {
		return err
	}

	return nil
}

func getAlertStatusFromResponse(resp *grab.Response) (status alerts.DownloadAlertStatus, err error) {
	if resp == nil {
		return alerts.DownloadAlertStatus{}, errors.New("invalid response")
	}

	return alerts.DownloadAlertStatus{Source: moduleID, URL: resp.Request.HTTPRequest.URL.String(),
		Progress: int(resp.Progress() * 100), DownloadedBytes: uint64(resp.BytesComplete()),
		TotalBytes: uint64(resp.Size)}, nil
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}

	return !info.IsDir()
}

func createFile(filename string) (err error) {
	return ioutil.WriteFile(filename, []byte{}, flagAccessRights)
}
