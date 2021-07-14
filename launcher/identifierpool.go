// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2021 Renesas Inc.
// Copyright 2021 EPAM Systems Inc.
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
	"fmt"
	"os/user"
	"sync"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	idsRangeBegin uint32 = 5000
	idsRangeEnd   uint32 = 10000
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type identifierPool struct {
	sync.Mutex
	lockedUIDs []uint32
	lockedGIDs []uint32
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (pool *identifierPool) add(uid, gid uint32) error {
	pool.Lock()
	defer pool.Unlock()

	if isInPool(pool.lockedUIDs, uid) {
		return aoserrors.New("service with given UID already exist in pool")
	}

	if isInPool(pool.lockedGIDs, gid) {
		return aoserrors.New("service with given GID already exist in pool")
	}

	pool.lockedUIDs = append(pool.lockedUIDs, uid)
	pool.lockedGIDs = append(pool.lockedGIDs, gid)

	return nil
}

func (pool *identifierPool) getFree() (uid, gid uint32, err error) {
	pool.Lock()
	defer pool.Unlock()

	uid, err = getFreeID(pool.lockedUIDs, func(uid uint32) bool {
		if user, err := user.LookupId(fmt.Sprint(uid)); err == nil || user != nil {
			log.Warningf("UID %d is occupied by system", uid)
			return false
		}

		return true
	})
	if err != nil {
		return 0, 0, err
	}

	gid, err = getFreeID(pool.lockedGIDs, func(gid uint32) bool {
		if group, err := user.LookupGroupId(fmt.Sprint(gid)); err == nil || group != nil {
			log.Warningf("GID %d is occupied by system", gid)
			return false
		}

		return true
	})
	if err != nil {
		return 0, 0, err
	}

	pool.lockedUIDs = append(pool.lockedUIDs, uid)
	pool.lockedGIDs = append(pool.lockedGIDs, gid)

	return uid, gid, nil
}

func (pool *identifierPool) remove(uid, gid uint32) (err error) {
	pool.Lock()
	defer pool.Unlock()

	if err = removeID(&pool.lockedUIDs, uid); err != nil {
		return err
	}

	if err = removeID(&pool.lockedGIDs, gid); err != nil {
		return err
	}

	return nil
}

func isInPool(pool []uint32, id uint32) (exist bool) {
	for _, value := range pool {
		if id == value {
			return true
		}
	}

	return false
}

func getFreeID(pool []uint32, systemAvailability func(uint32) bool) (id uint32, err error) {
	for i := idsRangeBegin; i <= idsRangeEnd; i++ {
		if isInPool(pool, i) {
			continue
		}

		if !systemAvailability(i) {
			continue
		}

		return i, nil
	}

	return 0, aoserrors.New("can't get free id")
}

func removeID(pool *[]uint32, id uint32) (err error) {
	for i, value := range *pool {
		if value == id {
			*pool = append((*pool)[:i], (*pool)[i+1:]...)
			return nil
		}
	}

	return aoserrors.New("can't remove ID from pool: UID/GID is not found")
}
