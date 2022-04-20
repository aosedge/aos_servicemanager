// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2021 Renesas Electronics Corporation.
// Copyright (C) 2021 EPAM Systems, Inc.
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

package imageutils

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/aoscloud/aos_common/aoserrors"
	log "github.com/sirupsen/logrus"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	dirSymlinkSize  = 4 * 1024
	contentTypeSize = 64
)

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// UnpackTarImage extract tar image.
func UnpackTarImage(source, destination string) (err error) {
	log.WithFields(log.Fields{"name": source, "destination": destination}).Debug("Unpack tar image")

	return aoserrors.Wrap(unTarFromFile(source, destination))
}

// CopyFile copies file content.
func CopyFile(source, destination string) (err error) {
	sourceFile, err := os.Open(source)
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer sourceFile.Close()

	desFile, err := os.Create(destination)
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer desFile.Close()

	_, err = io.Copy(desFile, sourceFile)

	return aoserrors.Wrap(err)
}

func GetUncompressedTarContentSize(path string) (size int64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, aoserrors.Wrap(err)
	}
	defer file.Close()

	bReader := bufio.NewReader(file)

	testBytes, err := bReader.Peek(contentTypeSize)
	if err != nil {
		return 0, aoserrors.Wrap(err)
	}

	reader := io.Reader(bReader)

	if strings.Contains(http.DetectContentType(testBytes), "x-gzip") {
		gzipReader, err := gzip.NewReader(reader)
		if err != nil {
			return 0, aoserrors.Wrap(err)
		}
		defer gzipReader.Close()

		reader = gzipReader
	}

	tarReader := tar.NewReader(reader)

	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return size, nil
		}

		if err != nil {
			return 0, aoserrors.Wrap(err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			size += dirSymlinkSize

		case tar.TypeSymlink:
			size += dirSymlinkSize

		case tar.TypeReg:
			size += header.Size

		}
	}
}

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func unTarFromFile(tarArchive string, destination string) (err error) {
	if _, err = os.Stat(tarArchive); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = os.MkdirAll(destination, 0o755); err != nil {
		return aoserrors.Wrap(err)
	}

	if output, err := exec.Command("tar", "xf", tarArchive, "-C", destination).CombinedOutput(); err != nil {
		log.Errorf("Failed to unpack archive: %s", string(output))

		return aoserrors.Wrap(err)
	}

	return nil
}
