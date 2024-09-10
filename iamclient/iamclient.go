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

	"github.com/aosedge/aos_common/aoserrors"
	"github.com/aosedge/aos_common/aostypes"
	"github.com/aosedge/aos_common/api/cloudprotocol"
	pb "github.com/aosedge/aos_common/api/iamanager"
	"github.com/aosedge/aos_common/utils/cryptutils"
	"github.com/aosedge/aos_common/utils/pbconvert"
	"github.com/golang/protobuf/ptypes/empty"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aosedge/aos_servicemanager/config"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const iamRequestTimeout = 30 * time.Second

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// Client IAM client instance.
type Client struct {
	sync.Mutex

	nodeInfo cloudprotocol.NodeInfo

	publicConnection         *grpc.ClientConn
	protectedConnection      *grpc.ClientConn
	publicService            pb.IAMPublicServiceClient
	publicPermissionsService pb.IAMPublicPermissionsServiceClient
	publicIdentifyService    pb.IAMPublicIdentityServiceClient
	permissionsService       pb.IAMPermissionsServiceClient

	closeChannel chan struct{}
}

/***********************************************************************************************************************
 * Public
 **********************************************************************************************************************/

// New creates new IAM client.
func New(
	config *config.Config, cryptcoxontext *cryptutils.CryptoContext, insecureConn bool,
) (client *Client, err error) {
	log.Debug("Connecting to IAM...")

	client = &Client{
		closeChannel: make(chan struct{}, 1),
	}

	defer func() {
		if err != nil {
			client.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	securePublicOpt := grpc.WithTransportCredentials(insecure.NewCredentials())
	secureProtectedOpt := grpc.WithTransportCredentials(insecure.NewCredentials())

	if !insecureConn {
		tlsConfig, err := cryptcoxontext.GetClientTLSConfig()
		if err != nil {
			return client, aoserrors.Wrap(err)
		}

		securePublicOpt = grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	}

	if client.publicConnection, err = grpc.DialContext(
		ctx, config.IAMPublicServerURL, securePublicOpt, grpc.WithBlock()); err != nil {
		return client, aoserrors.Wrap(err)
	}

	client.publicService = pb.NewIAMPublicServiceClient(client.publicConnection)
	client.publicPermissionsService = pb.NewIAMPublicPermissionsServiceClient(client.publicConnection)
	client.publicIdentifyService = pb.NewIAMPublicIdentityServiceClient(client.publicConnection)

	if !insecureConn {
		certURL, keyURL, err := client.GetCertificate(config.CertStorage)
		if err != nil {
			return client, err
		}

		tlsConfig, err := cryptcoxontext.GetClientMutualTLSConfig(certURL, keyURL)
		if err != nil {
			return client, aoserrors.Wrap(err)
		}

		secureProtectedOpt = grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	}

	if client.protectedConnection, err = grpc.DialContext(
		ctx, config.IAMProtectedServerURL, secureProtectedOpt, grpc.WithBlock()); err != nil {
		return client, aoserrors.Wrap(err)
	}

	client.permissionsService = pb.NewIAMPermissionsServiceClient(client.protectedConnection)

	log.Debug("Connected to IAM")

	if err = client.getNodeInfo(); err != nil {
		return client, aoserrors.Wrap(err)
	}

	return client, nil
}

// GetCurrentNodeInfo returns node info.
func (client *Client) GetCurrentNodeInfo() (cloudprotocol.NodeInfo, error) {
	return client.nodeInfo, nil
}

// GetCertificate gets certificate and key url from IAM by type.
func (client *Client) GetCertificate(certType string) (certURL, keyURL string, err error) {
	log.WithFields(log.Fields{
		"type": certType,
	}).Debug("Get certificate")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	response, err := client.publicService.GetCert(
		ctx, &pb.GetCertRequest{Type: certType})
	if err != nil {
		return "", "", aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{
		"certURL": response.GetCertUrl(), "keyURL": response.GetKeyUrl(),
	}).Debug("Certificate info")

	return response.GetCertUrl(), response.GetKeyUrl(), nil
}

// RegisterInstance registers new service instance with permissions and create secret.
func (client *Client) RegisterInstance(
	instance aostypes.InstanceIdent, permissions map[string]map[string]string,
) (secret string, err error) {
	log.WithFields(log.Fields{
		"serviceID": instance.ServiceID,
		"subjectID": instance.SubjectID,
		"instance":  instance.Instance,
	}).Debug("Register instance")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	reqPermissions := make(map[string]*pb.Permissions)
	for key, value := range permissions {
		reqPermissions[key] = &pb.Permissions{Permissions: value}
	}

	response, err := client.permissionsService.RegisterInstance(ctx,
		&pb.RegisterInstanceRequest{Instance: pbconvert.InstanceIdentToPB(instance), Permissions: reqPermissions})
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	return response.GetSecret(), nil
}

// UnregisterInstance unregisters service instance.
func (client *Client) UnregisterInstance(instance aostypes.InstanceIdent) (err error) {
	log.WithFields(log.Fields{
		"serviceID": instance.ServiceID,
		"subjectID": instance.SubjectID,
		"instance":  instance.Instance,
	}).Debug("Unregister instance")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	if _, err := client.permissionsService.UnregisterInstance(ctx,
		&pb.UnregisterInstanceRequest{Instance: pbconvert.InstanceIdentToPB(instance)}); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

// GetPermissions gets permissions by secret and functional server ID.
func (client *Client) GetPermissions(
	secret, funcServerID string,
) (instance aostypes.InstanceIdent, permissions map[string]string, err error) {
	log.WithField("funcServerID", funcServerID).Debug("Get permissions")

	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	req := &pb.PermissionsRequest{Secret: secret, FunctionalServerId: funcServerID}

	response, err := client.publicPermissionsService.GetPermissions(ctx, req)
	if err != nil {
		return instance, nil, aoserrors.Wrap(err)
	}

	return aostypes.InstanceIdent{
		ServiceID: response.GetInstance().GetServiceId(),
		SubjectID: response.GetInstance().GetSubjectId(), Instance: response.GetInstance().GetInstance(),
	}, response.GetPermissions().GetPermissions(), nil
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

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (client *Client) getNodeInfo() error {
	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	response, err := client.publicService.GetNodeInfo(ctx, &empty.Empty{})
	if err != nil {
		return aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{
		"nodeID":   response.GetNodeId(),
		"nodeType": response.GetNodeType(),
	}).Debug("Get node Info")

	client.nodeInfo = pbconvert.NodeInfoFromPB(response)

	return nil
}
