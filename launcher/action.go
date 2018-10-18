package launcher

import (
	"container/list"
	"errors"
	"os"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
)

/*******************************************************************************
 * Action related API
 ******************************************************************************/

func (launcher *Launcher) isIDInWorkQueue(id string) (result bool) {
	for item := launcher.workQueue.Front(); item != nil; item = item.Next() {
		if item.Value.(serviceAction).id == id {
			return true
		}
	}

	return false
}

func (launcher *Launcher) putInQueue(action serviceAction) {
	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	if launcher.isIDInWorkQueue(action.id) {
		launcher.waitQueue.PushBack(action)
		return
	}

	if launcher.workQueue.Len() >= maxExecutedActions {
		launcher.waitQueue.PushBack(action)
		return
	}

	go launcher.processAction(launcher.workQueue.PushBack(action))
}

func (launcher *Launcher) processAction(item *list.Element) {
	action := item.Value.(serviceAction)
	status := ActionStatus{Action: action.action, ID: action.id}

	switch action.action {
	case ActionInstall:
		serviceInfo := action.data.(amqp.ServiceInfoFromCloud)
		status.Version = serviceInfo.Version

		status.Err = launcher.doActionInstall(serviceInfo)

	case ActionRemove:
		status.Version, status.Err = launcher.doActionRemove(status.ID)
	}

	launcher.StatusChannel <- status

	launcher.mutex.Lock()
	defer launcher.mutex.Unlock()

	launcher.workQueue.Remove(item)

	for item := launcher.waitQueue.Front(); item != nil; item = item.Next() {
		if launcher.isIDInWorkQueue(item.Value.(serviceAction).id) {
			continue
		}

		go launcher.processAction(launcher.workQueue.PushBack(launcher.waitQueue.Remove(item)))
		break
	}
}

func (launcher *Launcher) doActionInstall(serviceInfo amqp.ServiceInfoFromCloud) (err error) {
	if launcher.users == nil {
		return errors.New("Users are not set")
	}

	service, err := launcher.db.GetService(serviceInfo.ID)
	if err != nil && err != database.ErrNotExist {
		return err
	}

	// Skip incorrect version
	if err == nil && serviceInfo.Version < service.Version {
		return errors.New("Version mistmatch")
	}

	installed := false

	// Check if we need to install
	if err != nil || serviceInfo.Version > service.Version {
		if installDir, err := launcher.installService(serviceInfo); err != nil {
			if installDir != "" {
				os.RemoveAll(installDir)
			}
			return err
		}
		installed = true
	}

	// Update users DB
	exist, err := launcher.db.IsUsersService(launcher.users, serviceInfo.ID)
	if err != nil {
		return err
	}

	if !exist {
		if err := launcher.db.AddUsersService(launcher.users, serviceInfo.ID); err != nil {
			return err
		}
	}

	// Start service
	if !installed {
		if err = launcher.startService(service.ID, service.ServiceName); err != nil {
			return err
		}
	}

	return nil
}

func (launcher *Launcher) doActionRemove(id string) (version uint64, err error) {
	service, err := launcher.db.GetService(id)
	if err != nil {
		return 0, err
	}

	version = service.Version

	if launcher.users == nil {
		return version, errors.New("Users are not set")
	}

	if err := launcher.stopService(service.ID, service.ServiceName); err != nil {
		return version, err
	}

	if err = launcher.db.RemoveUsersService(launcher.users, service.ID); err != nil {
		return version, err
	}

	return version, nil
}
