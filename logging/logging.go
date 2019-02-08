// Package logging provides set of API to retrieve system and services log
package logging

import (
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/database"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	logChannelSize = 32
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Logging instance
type Logging struct {
	LogChannel chan amqp.PushServiceLog

	db database.ServiceItf
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new logging object
func New(config *config.Config, db database.ServiceItf) (instance *Logging, err error) {
	log.Debug("New logging")

	instance = &Logging{db: db}

	instance.LogChannel = make(chan amqp.PushServiceLog, logChannelSize)

	return instance, nil
}

// Close closes logging
func (instance *Logging) Close() {
	log.Debug("Close logging")
}

// GetServiceLog returns service log
func (instance *Logging) GetServiceLog(request amqp.RequestServiceLog) {
	log.WithFields(log.Fields{
		"serviceID": request.ServiceID,
		"logID":     request.LogID,
		"dateFrom":  request.From,
		"dateTill":  request.Till}).Debug("Get service log")
}

// GetServiceCrashLog returns service log
func (instance *Logging) GetServiceCrashLog(request amqp.RequestServiceCrashLog) {
	log.WithFields(log.Fields{
		"serviceID": request.ServiceID,
		"logID":     request.LogID}).Debug("Get service crash log")
}

/*******************************************************************************
 * Private
 ******************************************************************************/
