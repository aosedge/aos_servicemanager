package launcher

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os/exec"
	"os/user"
	"strconv"

	"github.com/anexia-it/fsquota"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Linux related API
 ******************************************************************************/

func (launcher *Launcher) createUser(id string) (userName string, err error) {
	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	// convert id to hashed u32 value
	hash := fnv.New64a()
	hash.Write([]byte(id))

	// create user
	userName = "AOS_" + strconv.FormatUint(hash.Sum64(), 16)
	// if user exists
	if _, err = user.Lookup(userName); err == nil {
		return userName, errors.New("User already exists")
	}

	log.WithField("user", userName).Debug("Create user")

	if err = exec.Command("useradd", "-M", userName).Run(); err != nil {
		return userName, fmt.Errorf("Error creating user: %s", err)
	}

	return userName, nil
}

func (launcher *Launcher) deleteUser(id string) (err error) {
	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	log.WithField("user", id).Debug("Delete user")

	if err = exec.Command("userdel", id).Run(); err != nil {
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

	limits := fsquota.Limits{}

	limits.Bytes.SetHard(limit)

	if _, err := fsquota.SetUserQuota(path, user, limits); err != nil {
		return err
	}

	return nil
}
