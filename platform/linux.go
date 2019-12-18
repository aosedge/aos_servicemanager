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

package platform

import (
	"fmt"
	"hash/fnv"
	"os/exec"
	"os/user"
	"strconv"
	"sync"

	"github.com/anexia-it/fsquota"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Variables
 ******************************************************************************/

var userMutex sync.Mutex

/*******************************************************************************
 * Public
 ******************************************************************************/

// IsUserExist checks if user exists in the system
func IsUserExist(serviceID string) (result bool) {
	userMutex.Lock()
	defer userMutex.Unlock()

	if _, err := user.Lookup(serviceIDToUser(serviceID)); err == nil {
		return true
	}

	return false
}

// CreateUser creates system user
func CreateUser(serviceID string) (userName string, err error) {
	userMutex.Lock()
	defer userMutex.Unlock()

	// create user
	userName = serviceIDToUser(serviceID)
	// if user exists
	if _, err = user.Lookup(userName); err == nil {
		log.WithField("user", userName).Debug("User already exists")

		return userName, nil
	}

	log.WithField("user", userName).Debug("Create user")

	if err = exec.Command("useradd", "-M", userName).Run(); err != nil {
		return "", fmt.Errorf("error creating user: %s", err)
	}

	return userName, nil
}

// DeleteUser deletes system user
func DeleteUser(serviceID string) (err error) {
	userMutex.Lock()
	defer userMutex.Unlock()

	userName := serviceIDToUser(serviceID)

	log.WithField("user", userName).Debug("Delete user")

	if err = exec.Command("userdel", userName).Run(); err != nil {
		return err
	}

	return nil
}

// GetUserUIDGID returns UID/GID by user name
func GetUserUIDGID(id string) (uid, gid uint32, err error) {
	user, err := user.Lookup(id)
	if err != nil {
		return 0, 0, err
	}

	uid64, err := strconv.ParseUint(user.Uid, 10, 32)
	if err != nil {
		return 0, 0, err
	}

	gid64, err := strconv.ParseUint(user.Gid, 10, 32)
	if err != nil {
		return 0, 0, err
	}

	return uint32(uid64), uint32(gid64), nil
}

// SetUserFSQuota sets file system quota for user
func SetUserFSQuota(path string, limit uint64, userName string) (err error) {
	supported, _ := fsquota.UserQuotasSupported(path)

	if limit == 0 && !supported {
		return nil
	}

	user, err := user.Lookup(userName)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{"user": userName, "limit": limit}).Debug("Set user FS quota")

	limits := fsquota.Limits{}

	limits.Bytes.SetHard(limit)

	if _, err := fsquota.SetUserQuota(path, user, limits); err != nil {
		return err
	}

	return nil
}

// GetUserFSQuotaUsage gets file system user usage
func GetUserFSQuotaUsage(path string, userName string) (byteUsed uint64, err error) {
	if supported, _ := fsquota.UserQuotasSupported(path); !supported {
		return byteUsed, nil
	}

	user, err := user.Lookup(userName)
	if err != nil {
		return byteUsed, err
	}

	info, err := fsquota.GetUserInfo(path, user)
	if err != nil {
		return byteUsed, err
	}

	return info.BytesUsed, nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func serviceIDToUser(serviceID string) (userName string) {
	// convert id to hashed u32 value
	hash := fnv.New64a()
	hash.Write([]byte(serviceID))

	return "AOS_" + strconv.FormatUint(hash.Sum64(), 16)
}
