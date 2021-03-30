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
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	log "github.com/sirupsen/logrus"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/iamanager"
	"gitpct.epam.com/epmd-aepr/aos_common/utils/cryptutils"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	amqp "aos_servicemanager/amqphandler"
	"aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	iamRequestTimeout   = 30 * time.Second
	iamReconnectTimeout = 10 * time.Second
)

const usersChangedChannelSize = 1

/*******************************************************************************
 * Types
 ******************************************************************************/

// Client IAM client instance
type Client struct {
	sync.Mutex

	sender Sender

	systemID string
	users    []string

	connection     *grpc.ClientConn
	pbclient       pb.IAManagerClient
	pbclientPublic pb.IAManagerPublicClient

	closeChannel        chan struct{}
	usersChangedChannel chan []string
}

// Sender provides API to send messages to the cloud
type Sender interface {
	SendIssueUnitCertificatesRequest(requests []amqp.CertificateRequest) (err error)
	SendInstallCertificatesConfirmation(confirmations []amqp.CertificateConfirmation) (err error)
}

// CertificateProvider provides certificate info
type CertificateProvider interface {
	GetCertSerial(certURL string) (serial string, err error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new IAM client
func New(config *config.Config, sender Sender, insecure bool) (client *Client, err error) {
	log.Debug("Connecting to IAM...")

	if sender == nil {
		return nil, errors.New("sender is nil")
	}

	client = &Client{
		sender:              sender,
		usersChangedChannel: make(chan []string, usersChangedChannelSize),
		closeChannel:        make(chan struct{}, 1)}
	defer func() {
		if err != nil {
			client.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	var secureOpt grpc.DialOption

	if insecure {
		secureOpt = grpc.WithInsecure()
	} else {
		tlsConfig, err := cryptutils.GetClientMutualTLSConfig(config.Crypt.CACert, config.CertStorage)
		if err != nil {
			return client, err
		}

		secureOpt = grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	}

	if client.connection, err = grpc.DialContext(ctx, config.IAMServerURL, secureOpt, grpc.WithBlock()); err != nil {
		return client, err
	}

	client.pbclient = pb.NewIAManagerClient(client.connection)
	client.pbclientPublic = pb.NewIAManagerPublicClient(client.connection)

	log.Debug("Connected to IAM")

	if client.systemID, err = client.getSystemID(); err != nil {
		return client, err
	}

	if client.users, err = client.getUsers(); err != nil {
		return client, err
	}

	go client.handleUsersChanged()

	return client, nil
}

// GetSystemID returns system ID
func (client *Client) GetSystemID() (systemID string) {
	return client.systemID
}

// GetUsers returns current users
func (client *Client) GetUsers() (users []string) {
	client.Lock()
	defer client.Unlock()

	return client.users
}

// UsersChangedChannel returns users changed channel
func (client *Client) UsersChangedChannel() (channel <-chan []string) {
	return client.usersChangedChannel
}

// RenewCertificatesNotification renew certificates notification
func (client *Client) RenewCertificatesNotification(pwd string, certInfo []amqp.CertificateNotification) (err error) {
	var newCerts []amqp.CertificateRequest

	for _, cert := range certInfo {
		log.WithFields(log.Fields{"type": cert.Type, "serial": cert.Serial, "validTill": cert.ValidTill}).Debug("Renew certificate")

		ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
		defer cancel()

		request := &pb.CreateKeyReq{Type: cert.Type, Password: pwd}

		response, err := client.pbclient.CreateKey(ctx, request)
		if err != nil {
			return err
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
func (client *Client) InstallCertificates(certInfo []amqp.IssuedUnitCertificatesInfo,
	certProvider CertificateProvider) (err error) {
	var confirmations []amqp.CertificateConfirmation

	for _, cert := range certInfo {
		log.WithFields(log.Fields{"type": cert.Type}).Debug("Install certificate")

		ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
		defer cancel()

		request := &pb.ApplyCertReq{Type: cert.Type, Cert: cert.CertificateChain}
		certConfirmation := amqp.CertificateConfirmation{Type: cert.Type}

		response, err := client.pbclient.ApplyCert(ctx, request)
		if err == nil {
			certConfirmation.Serial, err = certProvider.GetCertSerial(response.CertUrl)
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

// RegisterService registers new service with permissions and create secret
func (client *Client) RegisterService(serviceID string, permissions map[string]map[string]string) (secret string, err error) {
	log.WithField("serviceID", serviceID).Debug("Register service")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	reqPermissions := make(map[string]*pb.Permissions)
	for key, value := range permissions {
		reqPermissions[key] = &pb.Permissions{Permissions: value}
	}

	req := &pb.RegisterServiceReq{ServiceId: serviceID, Permissions: reqPermissions}

	response, err := client.pbclient.RegisterService(ctx, req)
	if err != nil {
		return "", err
	}

	return response.Secret, nil
}

// UnregisterService unregisters service
func (client *Client) UnregisterService(serviceID string) (err error) {
	log.WithField("serviceID", serviceID).Debug("Unregister service")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	req := &pb.UnregisterServiceReq{ServiceId: serviceID}

	_, err = client.pbclient.UnregisterService(ctx, req)
	if err != nil {
		return err
	}

	return nil
}

// GetPermissions gets permissions by secret and functional server ID
func (client *Client) GetPermissions(secret, funcServerID string) (serviceID string, permissions map[string]string, err error) {
	log.WithField("funcServerID", funcServerID).Debug("Get permissions")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	req := &pb.GetPermissionsReq{Secret: secret, FunctionalServerId: funcServerID}

	response, err := client.pbclientPublic.GetPermissions(ctx, req)
	if err != nil {
		return "", nil, err
	}

	return response.ServiceId, response.Permissions.Permissions, nil
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

	log.WithFields(log.Fields{"certURL": response.CertUrl, "keyURL": response.KeyUrl}).Debug("Certificate info")

	return response.CertUrl, response.KeyUrl, nil
}

// Close closes IAM client
func (client *Client) Close() (err error) {
	if client.connection != nil {
		client.closeChannel <- struct{}{}
		client.connection.Close()
	}

	log.Debug("Disconnected from IAM")

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (client *Client) getSystemID() (systemID string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	request := &empty.Empty{}

	response, err := client.pbclient.GetSystemInfo(ctx, request)
	if err != nil {
		return "", err
	}

	log.WithFields(log.Fields{"systemID": response.SystemId}).Debug("Get system ID")

	return response.SystemId, nil
}

func (client *Client) getUsers() (users []string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	request := &empty.Empty{}

	response, err := client.pbclient.GetUsers(ctx, request)
	if err != nil {
		return nil, err
	}

	log.WithFields(log.Fields{"users": response.Users}).Debug("Get users")

	return response.Users, nil
}

func (client *Client) handleUsersChanged() {
	err := client.subscribeUsersChanged()

	for {
		if err != nil && len(client.closeChannel) == 0 {
			log.Errorf("Error subscribe users changed: %s", err)
			log.Debugf("Reconnect to IAM in %v...", iamReconnectTimeout)
		}

		select {
		case <-client.closeChannel:
			return

		case <-time.After(iamReconnectTimeout):
			err = client.subscribeUsersChanged()
		}
	}
}

func (client *Client) subscribeUsersChanged() (err error) {
	log.Debug("Subscribe to users changed notification")

	request := &empty.Empty{}

	stream, err := client.pbclient.SubscribeUsersChanged(context.Background(), request)
	if err != nil {
		return err
	}

	users, err := client.getUsers()
	if err != nil {
		return err
	}

	if !isUsersEqual(users, client.users) {
		client.Lock()
		client.users = users
		client.Unlock()

		client.usersChangedChannel <- client.users
	}

	for {
		notification, err := stream.Recv()
		if err != nil {
			return err
		}

		log.WithFields(log.Fields{"users": notification.Users}).Debug("Users changed notification")

		if !isUsersEqual(notification.Users, client.users) {
			client.Lock()
			client.users = notification.Users
			client.Unlock()

			client.usersChangedChannel <- client.users
		}
	}
}

func isUsersEqual(users1, users2 []string) (result bool) {
	if len(users1) != len(users2) {
		return false
	}

	for i, user := range users1 {
		if user != users2[i] {
			return false
		}
	}

	return true
}
