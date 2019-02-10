package logging

import (
	"bytes"
	"compress/gzip"

	log "github.com/sirupsen/logrus"
	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type archivator struct {
	zw          *gzip.Writer
	logBuffers  []bytes.Buffer
	logChannel  chan<- amqp.PushServiceLog
	partCount   uint64
	partSize    uint64
	maxPartSize uint64
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func newArchivator(logChannel chan<- amqp.PushServiceLog, maxPartSize uint64) (instance *archivator, err error) {
	instance = &archivator{logChannel: logChannel, maxPartSize: maxPartSize}

	instance.logBuffers = make([]bytes.Buffer, 1)

	if instance.zw, err = gzip.NewWriterLevel(
		&instance.logBuffers[0], gzip.BestCompression); err != nil {
		return nil, err
	}

	return instance, nil
}

func (instance *archivator) addLog(message string) (err error) {
	count, err := instance.zw.Write([]byte(message))
	if err != nil {
		return err
	}

	instance.partSize += uint64(count)

	log.WithFields(log.Fields{
		"partSize": instance.partSize,
		"message":  message}).Debug("Archivate log")

	if instance.partSize > instance.maxPartSize {
		if err = instance.zw.Close(); err != nil {
			return err
		}

		instance.logBuffers = append(instance.logBuffers, bytes.Buffer{})
		instance.partCount++
		instance.partSize = 0

		log.WithField("partCount", instance.partCount).Debug("Max part size reached")

		instance.zw.Reset(&instance.logBuffers[instance.partCount])
	}

	return nil
}

func (instance *archivator) sendLog(logID string) (err error) {
	if err = instance.zw.Close(); err != nil {
		return err
	}

	if instance.partSize > 0 {
		instance.partCount++
	}

	var i uint64

	for ; i < instance.partCount; i++ {
		data := instance.logBuffers[i].Bytes()
		part := i + 1

		log.WithFields(log.Fields{
			"part": part,
			"size": len(data)}).Debugf("Push log")

		instance.logChannel <- amqp.PushServiceLog{
			LogID:     logID,
			PartCount: &instance.partCount,
			Part:      &part,
			Data:      &data}
	}

	return nil
}
