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

// Package launcher provides set of API to controls services lifecycle

package launcher

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const umountRetry = 3
const umountDelay = 1 * time.Second

/*******************************************************************************
 * Types
 ******************************************************************************/

type mountInfo struct {
	source       string
	destintation string
	mountType    string
	options      string
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func isOverlayMount(mountPoint string) (mounted bool, err error) {
	var buf syscall.Statfs_t

	if err = syscall.Statfs(mountPoint, &buf); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, err
	}

	if buf.Type == unix.OVERLAYFS_SUPER_MAGIC {
		return true, nil
	}

	return false, nil
}

func overlayMount(mountPoint string, lowerDirs []string, workDir, upperDir string) (err error) {
	isMounted, err := isOverlayMount(mountPoint)
	if err != nil {
		return err
	}

	if isMounted {
		log.WithField("path", mountPoint).Warnf("Path is mounted. Skipped.")

		return nil
	}

	opts := "lowerdir=" + strings.Join(lowerDirs, ":")

	if upperDir != "" {
		if workDir == "" {
			return errors.New("working dir path should be set")
		}

		if err = os.RemoveAll(workDir); err != nil {
			return err
		}

		if err = os.MkdirAll(workDir, 0755); err != nil {
			return err
		}

		opts = opts + ",workdir=" + workDir + ",upperdir=" + upperDir
	}

	if err = syscall.Mount("overlay", mountPoint, "overlay", 0, opts); err != nil {
		return err
	}

	return nil
}

func umountWithRetry(mountPoint string, flags int) (err error) {
	isMounted, err := isOverlayMount(mountPoint)
	if err != nil {
		log.Errorf("Can't check overlay mount: %s", err)

		isMounted = true
	}

	if !isMounted {
		return nil
	}

	for i := 0; i < umountRetry; i++ {
		if err = syscall.Unmount(mountPoint, flags); err == nil {
			return nil
		}

		log.Warnf("Can't umount %s: %s. Retry...", mountPoint, err)

		time.Sleep(umountDelay)

		syscall.Sync()
	}

	return err
}
