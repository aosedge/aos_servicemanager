package monitoring

import (
	"time"

	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
)

// alertProcessor object for detection alerts
type alertProcessor struct {
	name              string
	source            *uint64
	destination       *[]amqp.AlertData
	rule              config.AlertRule
	thresholdTime     time.Time
	thresholdDetected bool
}

// createAlertProcessor creates alert processor based on configuration
func createAlertProcessor(name string, source *uint64,
	destination *[]amqp.AlertData, rule config.AlertRule) (alert *alertProcessor) {
	return &alertProcessor{name: name, source: source, destination: destination, rule: rule}
}

// checkAlertDetection checks if alert was detected
func (alert *alertProcessor) checkAlertDetection(currentTime time.Time) {
	value := *alert.source

	if value >= alert.rule.MaxThreshold && alert.thresholdTime.IsZero() {
		alert.thresholdTime = currentTime
	}

	if value < alert.rule.MinThreshold && !alert.thresholdTime.IsZero() {
		alert.thresholdTime = time.Time{}
		alert.thresholdDetected = false
	}

	if !alert.thresholdTime.IsZero() &&
		currentTime.Sub(alert.thresholdTime) >= alert.rule.MinTimeout.Duration &&
		!alert.thresholdDetected {

		log.WithFields(log.Fields{
			"value": value,
			"time":  currentTime.Format("Jan 2 15:04:05")}).Debugf("%s alert", alert.name)

		alert.thresholdDetected = true
		if len(*alert.destination) < cap(*alert.destination) {
			*alert.destination = append(*alert.destination,
				amqp.AlertData{Value: value, Timestamp: currentTime})
		} else {
			log.WithField("limit", cap(*alert.destination)).Warnf("Alert limit exceeds")
		}
	}
}
