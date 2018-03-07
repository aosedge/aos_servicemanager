package launcher

import (
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

/*******************************************************************************
 * Types
 ******************************************************************************/

type database struct {
	name string
}

// serviceEntry describes entry structure
type serviceEntry struct {
	id      string
	version uint
	path    string
	state   ServiceState
	status  ServiceStatus
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// createDatabase creates database instance
func newDatabase(name string) (db *database, err error) {
	return &database{name: name}, nil
}

// addService adds new service entry
func (db *database) addService(entry serviceEntry) (err error) {
	return nil
}

// removeService removes existing service entry
func (db *database) removeService(id string) (err error) {
	return nil
}

// getService returns service entry
func (db *database) getService(id string) (entry serviceEntry, err error) {
	return serviceEntry{}, nil
}

// getServices returns all service entries
func (db *database) getServices() (entries []serviceEntry, err error) {
	return []serviceEntry{}, nil
}

// setServiceStatus sets service status
func (db *database) setServiceStatus(id string, status ServiceStatus) (err error) {
	return nil
}

// setServiceState sets service state
func (db *database) setServiceState(id string, state ServiceState) (err error) {
	return nil
}

// close closes database
func (db *database) close() {
}

/*******************************************************************************
 * Private
 ******************************************************************************/
