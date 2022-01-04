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

package platform

import (
	"fmt"
	"os/user"

	"github.com/anexia-it/fsquota"
	"github.com/aoscloud/aos_common/aoserrors"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Variables
 ******************************************************************************/

/*******************************************************************************
 * Public
 ******************************************************************************/

// SetUserFSQuota sets file system quota for user
func SetUserFSQuota(path string, limit uint64, uid, gid uint32) (err error) {
	supported, _ := fsquota.UserQuotasSupported(path)

	if limit == 0 && !supported {
		return nil
	}

	user := user.User{Uid: fmt.Sprint(uid), Gid: fmt.Sprint(gid)}

	log.WithFields(log.Fields{"uid": uid, "gid": uid, "limit": limit}).Debug("Set user FS quota")

	limits := fsquota.Limits{}

	limits.Bytes.SetHard(limit)

	if _, err := fsquota.SetUserQuota(path, &user, limits); err != nil { // nolint
		return aoserrors.Wrap(err)
	}

	return nil
}

// GetUserFSQuotaUsage gets file system user usage
func GetUserFSQuotaUsage(path string, uid, gid uint32) (byteUsed uint64, err error) {
	if supported, _ := fsquota.UserQuotasSupported(path); !supported {
		return byteUsed, nil
	}

	user := user.User{Uid: fmt.Sprint(uid), Gid: fmt.Sprint(gid)}

	info, err := fsquota.GetUserInfo(path, &user)
	if err != nil {
		return byteUsed, aoserrors.Wrap(err)
	}

	return info.BytesUsed, nil
}
