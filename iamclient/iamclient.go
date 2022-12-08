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
	"github.com/aoscloud/aos_common/aostypes"
	pb "github.com/aoscloud/aos_common/api/iamanager/v4"
	"github.com/aoscloud/aos_common/utils/cryptutils"
	"github.com/golang/protobuf/ptypes/empty"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aoscloud/aos_servicemanager/config"
)

/***********************************************************************************************************************
 * Consts
 **********************************************************************************************************************/

const (
	iamRequestTimeout   = 30 * time.Second
	iamReconnectTimeout = 10 * time.Second
)

const subjectsChangedChannelSize = 1

/***********************************************************************************************************************
 * Types
 **********************************************************************************************************************/

// Client IAM client instance.
type Client struct {
	sync.Mutex

	subjects []string
	nodeID   string
	nodeType string

	publicConnection         *grpc.ClientConn
	protectedConnection      *grpc.ClientConn
	publicService            pb.IAMPublicServiceClient
	publicPermissionsService pb.IAMPublicPermissionsServiceClient
	publicIdentifyService    pb.IAMPublicIdentityServiceClient
	permissionsService       pb.IAMPermissionsServiceClient

	closeChannel           chan struct{}
	subjectsChangedChannel chan []string
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
		closeChannel:           make(chan struct{}, 1),
		subjectsChangedChannel: make(chan []string, subjectsChangedChannelSize),
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

	if client.protectedConnection, err = grpc.DialContext(
		ctx, config.IAMServerURL, secureProtectedOpt, grpc.WithBlock()); err != nil {
		return client, aoserrors.Wrap(err)
	}

	client.permissionsService = pb.NewIAMPermissionsServiceClient(client.protectedConnection)

	log.Debug("Connected to IAM")

	if client.nodeID, client.nodeType, err = client.getNodeInfo(); err != nil {
		return client, aoserrors.Wrap(err)
	}

	if client.subjects, err = client.getSubjects(); err != nil {
		return client, aoserrors.Wrap(err)
	}

	go client.handleSubjectsChanged()

	return client, nil
}

// GetNodeID returns node ID.
func (client *Client) GetNodeID() string {
	return client.nodeID
}

// GetNodeType returns node type.
func (client *Client) GetNodeType() string {
	return client.nodeType
}

// GetSubjects returns current subjects.
func (client *Client) GetSubjects() (subjects []string) {
	client.Lock()
	defer client.Unlock()

	return client.subjects
}

// GetSubjectsChangedChannel returns subjects changed channel.
func (client *Client) GetSubjectsChangedChannel() (channel <-chan []string) {
	return client.subjectsChangedChannel
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
		&pb.RegisterInstanceRequest{Instance: instanceIdentToPB(instance), Permissions: reqPermissions})
	if err != nil {
		return "", aoserrors.Wrap(err)
	}

	return response.Secret, nil
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
		&pb.UnregisterInstanceRequest{Instance: instanceIdentToPB(instance)}); err != nil {
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
		ServiceID: response.Instance.ServiceId,
		SubjectID: response.Instance.SubjectId, Instance: response.Instance.Instance,
	}, response.Permissions.Permissions, nil
}

// GetCertKeyURL gets certificate and key url from IAM.
func (client *Client) GetCertKeyURL(keyType string) (certURL, keyURL string, err error) {
	response, err := client.publicService.GetCert(context.Background(), &pb.GetCertRequest{Type: keyType})
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

/***********************************************************************************************************************
 * Private
 **********************************************************************************************************************/

func (client *Client) getNodeInfo() (nodeID, nodeType string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	response, err := client.publicService.GetNodeInfo(ctx, &empty.Empty{})
	if err != nil {
		return "", "", aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{
		"nodeID":   response.NodeId,
		"nodeType": response.NodeType,
	}).Debug("Get node Info")

	return response.NodeId, response.NodeType, nil
}

func (client *Client) getSubjects() (subjects []string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), iamRequestTimeout)
	defer cancel()

	request := &empty.Empty{}

	response, err := client.publicIdentifyService.GetSubjects(ctx, request)
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	log.WithFields(log.Fields{"subjects": response.Subjects}).Debug("Get subjects")

	return response.Subjects, nil
}

func (client *Client) handleSubjectsChanged() {
	err := client.subscribeSubjectsChanged()

	for {
		if err != nil && len(client.closeChannel) == 0 {
			log.Errorf("Error subscribe subjects changed: %s", err)
			log.Debugf("Reconnect to IAM in %v...", iamReconnectTimeout)
		}

		select {
		case <-client.closeChannel:
			return

		case <-time.After(iamReconnectTimeout):
			err = client.subscribeSubjectsChanged()
		}
	}
}

func (client *Client) subscribeSubjectsChanged() (err error) {
	log.Debug("Subscribe to subjects changed notification")

	request := &empty.Empty{}

	stream, err := client.publicIdentifyService.SubscribeSubjectsChanged(context.Background(), request)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	subjects, err := client.getSubjects()
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if !isSubjectsEqual(subjects, client.subjects) {
		client.Lock()
		client.subjects = subjects
		client.Unlock()

		client.subjectsChangedChannel <- client.subjects
	}

	for {
		notification, err := stream.Recv()
		if err != nil {
			return aoserrors.Wrap(err)
		}

		log.WithFields(log.Fields{"subjects": notification.Subjects}).Debug("Subjects changed notification")

		if !isSubjectsEqual(notification.Subjects, client.subjects) {
			client.Lock()
			client.subjects = notification.Subjects
			client.Unlock()

			client.subjectsChangedChannel <- client.subjects
		}
	}
}

func isSubjectsEqual(subjects1, subjects2 []string) (result bool) {
	if len(subjects1) != len(subjects2) {
		return false
	}

	for i, subject := range subjects1 {
		if subject != subjects2[i] {
			return false
		}
	}

	return true
}

func instanceIdentToPB(ident aostypes.InstanceIdent) *pb.InstanceIdent {
	return &pb.InstanceIdent{ServiceId: ident.ServiceID, SubjectId: ident.SubjectID, Instance: ident.Instance}
}
