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

package image

import (
	"errors"
	"io"
	"os"
	"reflect"
	"time"

	"github.com/cavaliercoder/grab"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/sha3"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	updateDownloadsTime = 10 * time.Second
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Handler image handler instance
type Handler struct {
	grab *grab.Client
}

// FileInfo file info
type FileInfo struct {
	Sha256 []byte
	Sha512 []byte
	Size   uint64
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new image handler
func New() (image *Handler, err error) {
	image = &Handler{grab: grab.NewClient()}

	return image, nil
}

// Download downloads the file by url
func (image *Handler) Download(destination, url string) (fileName string, err error) {
	log.WithField("url", url).Debug("Start downloading file")

	timer := time.NewTicker(updateDownloadsTime)
	defer timer.Stop()

	req, err := grab.NewRequest(destination, url)
	if err != nil {
		return "", err
	}

	resp := image.grab.Do(req)

	for {
		select {
		case <-timer.C:
			log.WithFields(log.Fields{"complete": resp.BytesComplete(), "total": resp.Size}).Debug("Download progress")

		case <-resp.Done:
			if err := resp.Err(); err != nil {
				return "", err
			}

			log.WithFields(log.Fields{"url": url, "file": resp.Filename}).Debug("Download complete")

			return resp.Filename, nil
		}
	}
}

// CheckFileInfo checks if file matches FileInfo
func CheckFileInfo(fileName string, fileInfo FileInfo) (err error) {
	file, err := os.Open(fileName)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}

	if uint64(stat.Size()) != fileInfo.Size {
		return errors.New("file size mistmatch")
	}

	hash256 := sha3.New256()

	if _, err := io.Copy(hash256, file); err != nil {
		return err
	}

	if !reflect.DeepEqual(hash256.Sum(nil), fileInfo.Sha256) {
		return errors.New("checksum sha256 mistmatch")
	}

	hash512 := sha3.New512()

	if _, err = file.Seek(0, 0); err != nil {
		return err
	}

	if _, err := io.Copy(hash512, file); err != nil {
		return err
	}

	if !reflect.DeepEqual(hash512.Sum(nil), fileInfo.Sha512) {
		return errors.New("checksum sha512 mistmatch")
	}

	return nil
}

// CreateFileInfo creates FileInfo from existing file
func CreateFileInfo(fileName string) (fileInfo FileInfo, err error) {
	file, err := os.Open(fileName)
	if err != nil {
		return fileInfo, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fileInfo, err
	}

	fileInfo.Size = uint64(stat.Size())

	hash256 := sha3.New256()

	if _, err := io.Copy(hash256, file); err != nil {
		return fileInfo, err
	}

	fileInfo.Sha256 = hash256.Sum(nil)

	hash512 := sha3.New512()

	if _, err = file.Seek(0, 0); err != nil {
		return fileInfo, err
	}

	if _, err := io.Copy(hash512, file); err != nil {
		return fileInfo, err
	}

	fileInfo.Sha512 = hash512.Sum(nil)

	return fileInfo, nil
}