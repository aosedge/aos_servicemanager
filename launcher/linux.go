package launcher

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
 * Linux related API
 ******************************************************************************/

var userMutex sync.Mutex

func serviceIDToUser(serviceID string) (userName string) {
	// convert id to hashed u32 value
	hash := fnv.New64a()
	hash.Write([]byte(serviceID))

	return "AOS_" + strconv.FormatUint(hash.Sum64(), 16)
}

func isUserExist(serviceID string) (result bool) {
	userMutex.Lock()
	defer userMutex.Unlock()

	if _, err := user.Lookup(serviceIDToUser(serviceID)); err == nil {
		return true
	}

	return false
}

func createUser(serviceID string) (userName string, err error) {
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
		return "", fmt.Errorf("Error creating user: %s", err)
	}

	return userName, nil
}

func deleteUser(serviceID string) (err error) {
	userMutex.Lock()
	defer userMutex.Unlock()

	userName := serviceIDToUser(serviceID)

	log.WithField("user", userName).Debug("Delete user")

	if err = exec.Command("userdel", userName).Run(); err != nil {
		return err
	}

	return nil
}

func getUserUIDGID(id string) (uid, gid uint32, err error) {
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

func setUserFSQuota(path string, limit uint64, userName string) (err error) {
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
