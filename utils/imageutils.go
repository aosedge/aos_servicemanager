// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
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

package utils

import (
	"bytes"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"syscall"

	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_common/image"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/fcrypt"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// FcryptInterface api to work with aoscrypto engine
type FcryptInterface interface {
	ImportSessionKey(keyInfo fcrypt.CryptoSessionKeyInfo) (symetrContext fcrypt.SymmetricContextInterface, err error)
	CreateSignContext() (fcrypt.SignContextInterface, error)
}

// DownloadImage download encrypted image
func DownloadImage(data amqp.DecryptDataStruct, downloadDir string) (fileName string, err error) {

	image, err := image.New()
	if err != nil {
		return fileName, errors.New("can't create image instance")
	}

	if len(data.URLs) == 0 {
		return fileName, errors.New("upgrade file list URLs is empty")
	}

	fileDownloaded := false

	for _, rawURL := range data.URLs {
		url, err := url.Parse(rawURL)
		if err != nil {
			return "", err
		}

		// skip already downloaded and decrypted files
		if !url.IsAbs() {
			break
		}

		var stat syscall.Statfs_t

		syscall.Statfs(downloadDir, &stat)

		if data.Size > stat.Bavail*uint64(stat.Bsize) {
			return fileName, errors.New("not enough space")
		}

		if fileName, err = image.Download(downloadDir, rawURL); err != nil {
			log.WithField("url", rawURL).Warningf("Can't download file: %s", err)
			continue
		}

		fileDownloaded = true

		break
	}

	if !fileDownloaded {
		return fileName, errors.New("can't download file from any source")
	}

	return fileName, nil
}

// CheckFile check file checksums
func CheckFile(fileName string, data amqp.DecryptDataStruct) (err error) {
	if err = image.CheckFileInfo(fileName, image.FileInfo{
		Sha256: data.Sha256,
		Sha512: data.Sha512,
		Size:   data.Size}); err != nil {
		return err
	}

	return nil
}

// DecryptImage decrypt already downloaded image
func DecryptImage(srcFileName, dstFileName string, crypt FcryptInterface, decryptionInfo *amqp.DecryptionInfo) (err error) {
	if decryptionInfo == nil {
		return errors.New("image decryption info = nil")
	}

	if crypt == nil {
		return errors.New("FcryptInterface = nil")
	}

	if decryptionInfo.ReceiverInfo == nil {
		return errors.New("ReceiverInfo dinecryptionInfo = nil")
	}

	context, err := crypt.ImportSessionKey(fcrypt.CryptoSessionKeyInfo{
		SymmetricAlgName:  decryptionInfo.BlockAlg,
		SessionKey:        decryptionInfo.BlockKey,
		SessionIV:         decryptionInfo.BlockIv,
		AsymmetricAlgName: decryptionInfo.AsymAlg,
		ReceiverInfo: fcrypt.ReceiverInfo{
			Issuer: decryptionInfo.ReceiverInfo.Issuer,
			Serial: decryptionInfo.ReceiverInfo.Serial},
	})
	if err != nil {
		return err
	}

	srcFile, err := os.Open(srcFileName)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dstFileName, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	log.WithFields(log.Fields{"srcFile": srcFile.Name(), "dstFile": dstFile.Name()}).Debug("Decrypt image")

	if err = context.DecryptFile(srcFile, dstFile); err != nil {
		return err
	}

	return nil
}

// CheckSigns check image signature
func CheckSigns(filePath string, crypt FcryptInterface,
	signs *amqp.Signs, chains []amqp.CertificateChain, certs []amqp.Certificate) (err error) {
	context, err := crypt.CreateSignContext()
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
		return errors.New("upgradeData does not have signature")
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

// UnpackTarImage extract tar image
func UnpackTarImage(source, destination string) (err error) {
	log.WithFields(log.Fields{"name": source, "destination": destination}).Debug("Unpack tar image")

	return unTarFromFile(source, destination)
}

/*******************************************************************************
 * Private
 ******************************************************************************/
func unTarFromFile(tarArchieve string, destination string) (err error) {
	if _, err = os.Stat(tarArchieve); err != nil {
		log.Error("Can't find tar arcieve")
		return err
	}

	if err = os.MkdirAll(destination, 0755); err != nil {
		return errors.New("can't create tar destination path")
	}

	cmd := exec.Command("tar", "xf", tarArchieve, "-C", destination)

	var out bytes.Buffer
	cmd.Stdout = &out

	err = cmd.Run()
	if err != nil {
		log.Errorf("Failed to untar archieve. Output is: %s", out.String())
	}

	return err
}
