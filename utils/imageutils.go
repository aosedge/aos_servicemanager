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
	"os"
	"os/exec"

	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

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
