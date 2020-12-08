// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2020 Renesas Inc.
// Copyright 2020 EPAM Systems Inc.
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

package iamclient

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"time"

	log "github.com/sirupsen/logrus"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/iamanager"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
	"aos_servicemanager/fcrypt"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	iamRequestTimeout = 30 * time.Second
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Client IAM client instance
type Client struct {
	sender     Sender
	connection *grpc.ClientConn
	pbclient   pb.IAManagerClient
}

// Sender provides API to send messages to the cloud
type Sender interface {
	SendIssueUnitCertificatesRequest(requests []amqp.CertificateRequest) (err error)
	SendInstallCertificatesConfirmation(confirmations []amqp.CertificateConfirmation) (err error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new IAM client
func New(config *config.Config, sender Sender, insecure bool) (client *Client, err error) {
	if sender == nil {
		return nil, errors.New("sender is nil")
	}

	client = &Client{sender: sender}

	log.Debug("Connecting to IAM...")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	var secureOpt grpc.DialOption

	if insecure {
		secureOpt = grpc.WithInsecure()
	} else {
		secureOpt = grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: false}))
	}

	if client.connection, err = grpc.DialContext(ctx, config.IAMServerURL, secureOpt, grpc.WithBlock()); err != nil {
		return nil, err
	}

	client.pbclient = pb.NewIAManagerClient(client.connection)

	log.Debug("Connected to IAM")

	return client, nil
}

// RenewCertificatesNotification renew certificates notification
func (client *Client) RenewCertificatesNotification(systemID, pwd string, certInfo []amqp.CertificateNotification) (err error) {
	var newCerts []amqp.CertificateRequest

	for _, cert := range certInfo {
		log.WithFields(log.Fields{"type": cert.Type, "serial": cert.Serial, "validTill": cert.ValidTill}).Debug("Renew certificate")

		ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
		defer cancel()

		request := &pb.CreateKeysReq{Type: cert.Type, SystemId: systemID, Password: pwd}

		response, err := client.pbclient.CreateKeys(ctx, request)
		if err != nil {
			return err
		}

		if response.Error != "" {
			return errors.New(response.Error)
		}

		newCerts = append(newCerts, amqp.CertificateRequest{Type: response.Type, Csr: response.Csr})
	}

	if len(newCerts) == 0 {
		return nil
	}

	if err := client.sender.SendIssueUnitCertificatesRequest(newCerts); err != nil {
		return err
	}

	return nil
}

// InstallCertificates applies new issued certificates
func (client *Client) InstallCertificates(certInfo []amqp.IssuedUnitCertificatesInfo) (err error) {
	var confirmations []amqp.CertificateConfirmation

	for _, cert := range certInfo {
		log.WithFields(log.Fields{"type": cert.Type}).Debug("Install certificate")

		ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
		defer cancel()

		request := &pb.ApplyCertReq{Type: cert.Type, Cert: cert.CertificateChain}
		certConfirmation := amqp.CertificateConfirmation{Type: cert.Type}

		response, err := client.pbclient.ApplyCert(ctx, request)

		if response.GetError() != "" {
			err = errors.New(response.Error)
		}

		if err == nil {
			certConfirmation.Serial, err = fcrypt.GetCrtSerialByURL(response.CertUrl)
		}

		if err == nil {
			certConfirmation.Status = "installed"
		} else if err != nil {
			certConfirmation.Status = "not installed"
			certConfirmation.Description = err.Error()

			log.WithFields(log.Fields{"type": cert.Type}).Errorf("Can't install certificate: %s", err)
		}

		confirmations = append(confirmations, certConfirmation)
	}

	if len(confirmations) == 0 {
		return nil
	}

	if err = client.sender.SendInstallCertificatesConfirmation(confirmations); err != nil {
		return err
	}

	return nil
}

// GetCertificate gets certificate by issuer
func (client *Client) GetCertificate(certType string, issuer []byte, serial string) (certURL, keyURL string, err error) {
	log.WithFields(log.Fields{
		"type":   certType,
		"issuer": base64.StdEncoding.EncodeToString(issuer),
		"serial": serial}).Debug("Get certificate")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	request := &pb.GetCertReq{Type: certType, Issuer: issuer, Serial: serial}

	response, err := client.pbclient.GetCert(ctx, request)
	if err != nil {
		return "", "", err
	}

	if response.Error != "" {
		return "", "", errors.New(response.Error)
	}

	log.WithFields(log.Fields{"certURL": response.CertUrl, "keyURL": response.KeyUrl}).Debug("Certificate info")

	return response.CertUrl, response.KeyUrl, nil
}

// Close closes IAM client
func (client *Client) Close() (err error) {
	log.Debug("Disconnected from IAM")

	if client.connection != nil {
		client.connection.Close()
	}

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/
