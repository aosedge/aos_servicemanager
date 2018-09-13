// Package monitoring AOS Core Monitoring Component
package monitoring

import (
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	dataChannelSize = 32
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// ServiceMonitoringItf provides API to start/stop service monitoring
type ServiceMonitoringItf interface {
	StartMonitorService(serviceID string, monitoringConfig ServiceMonitoringConfig) (err error)
	StopMonitorService(serviceID string) (err error)
}

// Monitor instance
type Monitor struct {
	DataChannel chan amqp.MonitoringData
}

// ServiceMonitoringConfig contains info about service and rules for monitoring alerts
type ServiceMonitoringConfig struct {
	Pid          int32
	WorkingDir   string
	ServiceRules *amqp.ServiceAlertRules
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new monitor instance
func New(config *config.Config) (monitor *Monitor, err error) {
	log.Debug("Create monitor")

	monitor = &Monitor{}

	monitor.DataChannel = make(chan amqp.MonitoringData, dataChannelSize)

	return monitor, nil
}

// Close closes monitor instance
func (monitor *Monitor) Close() {
	log.Debug("Close monitor")
}

// StartMonitorService starts monitoring service
func (monitor *Monitor) StartMonitorService(serviceID string, monitoringConfig ServiceMonitoringConfig) (err error) {
	log.WithField("id", serviceID).Debug("Start service monitoring")

	return nil
}

// StopMonitorService stops monitoring service
func (monitor *Monitor) StopMonitorService(serviceID string) (err error) {
	log.WithField("id", serviceID).Debug("Stop service monitoring")

	return nil
}
