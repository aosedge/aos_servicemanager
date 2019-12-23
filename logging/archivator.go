// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2019 Renesas Inc.
// Copyright 2019 EPAM Systems Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logging

import (
	"bytes"
	"compress/gzip"
	"errors"

	log "github.com/sirupsen/logrus"

	amqp "aos_servicemanager/amqphandler"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

type archivator struct {
	zw           *gzip.Writer
	logBuffers   []bytes.Buffer
	logChannel   chan<- amqp.PushServiceLog
	partCount    uint64
	partSize     uint64
	maxPartSize  uint64
	maxPartCount uint64
}

/*******************************************************************************
 * Variables
 ******************************************************************************/

var errMaxPartCount = errors.New("max part count reached")

/*******************************************************************************
 * Private
 ******************************************************************************/

func newArchivator(logChannel chan<- amqp.PushServiceLog, maxPartSize, maxPartCount uint64) (instance *archivator, err error) {
	instance = &archivator{logChannel: logChannel, maxPartSize: maxPartSize, maxPartCount: maxPartCount}

	instance.logBuffers = make([]bytes.Buffer, 1)

	if instance.zw, err = gzip.NewWriterLevel(
		&instance.logBuffers[0], gzip.BestCompression); err != nil {
		return nil, err
	}

	return instance, nil
}

func (instance *archivator) addLog(message string) (err error) {
	log.WithFields(log.Fields{
		"partSize": instance.partSize + uint64(len(message)),
		"message":  message}).Debug("Archivate log")

	if instance.partCount >= instance.maxPartCount {
		return errMaxPartCount
	}

	count, err := instance.zw.Write([]byte(message))
	if err != nil {
		return err
	}

	instance.partSize += uint64(count)

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

	if instance.partCount == 0 {
		var part uint64 = 1

		instance.logChannel <- amqp.PushServiceLog{
			LogID:     logID,
			PartCount: &part,
			Part:      &part,
			Data:      &[]byte{}}

		log.WithFields(log.Fields{
			"part": part,
			"size": 0}).Debugf("Push log")

		return nil
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
