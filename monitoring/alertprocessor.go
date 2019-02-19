package monitoring

import (
	"time"

	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
)

type alertCallback func(time time.Time, value uint64)

// alertProcessor object for detection alerts
type alertProcessor struct {
	name              string
	source            *uint64
	callback          alertCallback
	rule              config.AlertRule
	thresholdTime     time.Time
	thresholdDetected bool
}

// createAlertProcessor creates alert processor based on configuration
func createAlertProcessor(name string, source *uint64,
	callback alertCallback, rule config.AlertRule) (alert *alertProcessor) {
	return &alertProcessor{name: name, source: source, callback: callback, rule: rule}
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

		alert.callback(currentTime, value)
	}
}
