package launcher

import (
	"time"

	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
)

/*******************************************************************************
 * Database related API
 ******************************************************************************/

func (launcher *Launcher) addServiceToDB(service database.ServiceEntry) (err error) {
	// add to database
	if err = launcher.db.AddService(service); err != nil {
		// TODO: delete linux user?
		// TODO: try to restore old
		return err
	}

	exist, err := launcher.db.IsUsersService(launcher.users, service.ID)
	if err != nil {
		return err
	}
	if !exist {
		if err = launcher.db.AddUsersService(launcher.users, service.ID); err != nil {
			return err
		}
	}

	return nil
}

func (launcher *Launcher) cleanServicesDB() (err error) {
	log.Debug("Clean services DB")

	startedServices, err := launcher.db.GetUsersServices(launcher.users)
	if err != nil {
		return err
	}

	allServices, err := launcher.db.GetServices()
	if err != nil {
		return err
	}

	now := time.Now()

	servicesToBeRemoved := 0
	statusChannel := make(chan error, len(allServices))

	for _, service := range allServices {
		// check if service just started
		justStarted := false

		for _, startedService := range startedServices {
			if service.ID == startedService.ID {
				justStarted = true
				break
			}
		}

		if justStarted {
			continue
		}

		if service.StartAt.Add(time.Hour*24*time.Duration(service.TTL)).Before(now) == true {
			servicesToBeRemoved++

			go func(id string) {
				err := launcher.removeService(id)
				if err != nil {
					log.WithField("id", id).Errorf("Can't remove service: %s", err)
				}
				statusChannel <- launcher.removeService(id)
			}(service.ID)
		}
	}

	// Wait all services are removed
	for i := 0; i < servicesToBeRemoved; i++ {
		<-statusChannel
	}

	return nil
}

func (launcher *Launcher) cleanUsersDB() (err error) {
	log.Debug("Clean users DB")

	usersList, err := launcher.db.GetUsersList()
	if err != nil {
		return err
	}

	for _, users := range usersList {
		services, err := launcher.db.GetUsersServices(users)
		if err != nil {
			log.WithField("users", users).Errorf("Can't get users services: %s", err)
		}
		if len(services) == 0 {
			log.WithField("users", users).Debug("Delete users from DB")
			err = launcher.db.DeleteUsers(users)
			if err != nil {
				log.WithField("users", users).Errorf("Can't delete users: %s", err)
			}
		}
	}

	return nil
}
