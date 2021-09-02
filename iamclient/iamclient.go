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
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_common/aoserrors"
	pb "gitpct.epam.com/epmd-aepr/aos_common/api/iamanager"
	"gitpct.epam.com/epmd-aepr/aos_common/utils/cryptutils"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	iamRequestTimeout   = 30 * time.Second
	iamReconnectTimeout = 10 * time.Second
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// Client IAM client instance
type Client struct {
	sync.Mutex

	connection     *grpc.ClientConn
	pbclient       pb.IAManagerClient
	pbclientPublic pb.IAManagerPublicClient

	closeChannel        chan struct{}
	usersChangedChannel chan []string
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new IAM client
func New(config *config.Config, insecure bool) (client *Client, err error) {
	log.Debug("Connecting to IAM...")

	client = &Client{closeChannel: make(chan struct{}, 1)}
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
			return client, aoserrors.Wrap(err)
		}

		secureOpt = grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	}

	if client.connection, err = grpc.DialContext(ctx, config.IAMServerURL, secureOpt, grpc.WithBlock()); err != nil {
		return client, aoserrors.Wrap(err)
	}

	client.pbclient = pb.NewIAManagerClient(client.connection)
	client.pbclientPublic = pb.NewIAManagerPublicClient(client.connection)

	log.Debug("Connected to IAM")

	return client, nil
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
		return "", aoserrors.Wrap(err)
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
		return aoserrors.Wrap(err)
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
		return "", nil, aoserrors.Wrap(err)
	}

	return response.ServiceId, response.Permissions.Permissions, nil
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
