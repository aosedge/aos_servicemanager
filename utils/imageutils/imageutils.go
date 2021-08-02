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

package imageutils

import (
	"io"
	"os"
	"os/exec"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// UnpackTarImage extract tar image
func UnpackTarImage(source, destination string) (err error) {
	log.WithFields(log.Fields{"name": source, "destination": destination}).Debug("Unpack tar image")

	return aoserrors.Wrap(unTarFromFile(source, destination))
}

// CopyFile copies file content
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

/*******************************************************************************
 * Private
 ******************************************************************************/

func unTarFromFile(tarArchive string, destination string) (err error) {
	if _, err = os.Stat(tarArchive); err != nil {
		return aoserrors.Wrap(err)
	}

	if err = os.MkdirAll(destination, 0755); err != nil {
		return aoserrors.Wrap(err)
	}

	if output, err := exec.Command("tar", "xf", tarArchive, "-C", destination).CombinedOutput(); err != nil {
		log.Errorf("Failed to unpack archive: %s", string(output))

		return aoserrors.Wrap(err)
	}

	return nil
}
