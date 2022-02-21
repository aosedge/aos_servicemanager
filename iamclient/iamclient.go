// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2021 Renesas Electronics Corporation.
// Copyright (C) 2021 EPAM Systems, Inc.
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
	"sync"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	pb "github.com/aoscloud/aos_common/api/iamanager/v2"
	"github.com/aoscloud/aos_common/utils/cryptutils"
	"github.com/golang/protobuf/ptypes/empty"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/aoscloud/aos_servicemanager/config"
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

// Client IAM client instance.
type Client struct {
	sync.Mutex

	users []string

	publicConnection    *grpc.ClientConn
	protectedConnection *grpc.ClientConn
	pbclientPublic      pb.IAMPublicServiceClient
	pbclientProtected   pb.IAMProtectedServiceClient

	closeChannel        chan struct{}
	usersChangedChannel chan []string
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new IAM client.
func New(config *config.Config, cryptcoxontext *cryptutils.CryptoContext, insecure bool) (client *Client, err error) {
	log.Debug("Connecting to IAM...")

	client = &Client{
		closeChannel:        make(chan struct{}, 1),
		usersChangedChannel: make(chan []string, usersChangedChannelSize),
	}

	defer func() {
		if err != nil {
			client.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	securePublicOpt := grpc.WithInsecure()
	secureProtectedOpt := grpc.WithInsecure()

	if !insecure {
		tlsConfig, err := cryptcoxontext.GetClientTLSConfig()
		if err != nil {
			return client, aoserrors.Wrap(err)
		}

		securePublicOpt = grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	}

	if client.publicConnection, err = grpc.DialContext(ctx, config.IAMPublicServerURL, securePublicOpt, grpc.WithBlock()); err != nil {
		return client, aoserrors.Wrap(err)
	}

	client.pbclientPublic = pb.NewIAMPublicServiceClient(client.publicConnection)

	if !insecure {
		certURL, keyURL, err := client.GetCertKeyURL(config.CertStorage)
		if err != nil {
			return client, err
		}

		tlsConfig, err := cryptcoxontext.GetClientMutualTLSConfig(certURL, keyURL)
		if err != nil {
			return client, aoserrors.Wrap(err)
		}

		secureProtectedOpt = grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	}

	if client.protectedConnection, err = grpc.DialContext(ctx, config.IAMServerURL, secureProtectedOpt, grpc.WithBlock()); err != nil {
		return client, aoserrors.Wrap(err)
	}

	client.pbclientProtected = pb.NewIAMProtectedServiceClient(client.protectedConnection)

	log.Debug("Connected to IAM")

	if client.users, err = client.getUsers(); err != nil {
		return client, aoserrors.Wrap(err)
	}

	go client.handleUsersChanged()

	return client, nil
}

// GetUsers returns current users.
func (client *Client) GetUsers() (users []string) {
	client.Lock()
	defer client.Unlock()

	return client.users
}

// GetUsersChangedChannel returns users changed channel.
func (client *Client) GetUsersChangedChannel() (channel <-chan []string) {
	return client.usersChangedChannel
}

// RegisterService registers new service with permissions and create secret.
func (client *Client) RegisterService(serviceID string, permissions map[string]map[string]string) (secret string,
	err error) {
	log.WithField("serviceID", serviceID).Debug("Register service")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	reqPermissions := make(map[string]*pb.Permissions)
	for key, value := range permissions {
		reqPermissions[key] = &pb.Permissions{Permissions: value}
	}

	req := &pb.RegisterServiceRequest{ServiceId: serviceID, Permissions: reqPermissions}

	response, err := client.pbclientProtected.RegisterService(ctx, req)
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	return response.Secret, nil
}

// UnregisterService unregisters service.
func (client *Client) UnregisterService(serviceID string) (err error) {
	log.WithField("serviceID", serviceID).Debug("Unregister service")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	req := &pb.UnregisterServiceRequest{ServiceId: serviceID}

	_, err = client.pbclientProtected.UnregisterService(ctx, req)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

// GetPermissions gets permissions by secret and functional server ID.
func (client *Client) GetPermissions(secret, funcServerID string) (serviceID string,
	permissions map[string]string, err error) {
	log.WithField("funcServerID", funcServerID).Debug("Get permissions")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	req := &pb.PermissionsRequest{Secret: secret, FunctionalServerId: funcServerID}

	response, err := client.pbclientPublic.GetPermissions(ctx, req)
	if err != nil {
		return "", nil, aoserrors.Wrap(err)
	}

	return response.ServiceId, response.Permissions.Permissions, nil
}

// GetCertKeyURL gets cerificate and key url from IAM
func (client *Client) GetCertKeyURL(keyType string) (certURL, keyURL string, err error) {
	response, err := client.pbclientPublic.GetCert(context.Background(), &pb.GetCertRequest{Type: keyType})
	if err != nil {
		return "", "", aoserrors.Wrap(err)
	}

	return response.CertUrl, response.KeyUrl, nil
}

// Close closes IAM client.
func (client *Client) Close() (err error) {
	if client.publicConnection != nil {
		client.closeChannel <- struct{}{}
		client.publicConnection.Close()
	}

	if client.protectedConnection != nil {
		client.protectedConnection.Close()
	}

	log.Debug("Disconnected from IAM")

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (client *Client) getUsers() (users []string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	request := &empty.Empty{}

	response, err := client.pbclientPublic.GetUsers(ctx, request)
	if err != nil {
		return nil, aoserrors.Wrap(err)
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

	stream, err := client.pbclientPublic.SubscribeUsersChanged(context.Background(), request)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	users, err := client.getUsers()
	if err != nil {
		return aoserrors.Wrap(err)
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
			return aoserrors.Wrap(err)
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
