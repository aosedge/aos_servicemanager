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

package servicemanager

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"path"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/opencontainers/go-digest"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
)

/**********************************************************************************************************************
 * Consts
 *********************************************************************************************************************/

const manifestFileName = "manifest.json"

const buffSize = 1024 * 1024

/**********************************************************************************************************************
 * Types
 *********************************************************************************************************************/

type ImageParts struct {
	ImageConfigPath   string
	ServiceConfigPath string
	ServiceFSPath     string
	LayersDigest      []string
}

type serviceManifest struct {
	imagespec.Manifest
	AosService *imagespec.Descriptor `json:"aosService,omitempty"`
}

/**********************************************************************************************************************
 * Private
 *********************************************************************************************************************/

func validateUnpackedImage(installDir string) (err error) {
	manifest, err := getImageManifest(installDir)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	// validate image config
	if err = validateDigest(installDir, manifest.Config.Digest); err != nil {
		return aoserrors.Wrap(err)
	}

	// validate aos service config
	if manifest.AosService != nil {
		if err = validateDigest(installDir, manifest.AosService.Digest); err != nil {
			return aoserrors.Wrap(err)
		}
	}

	layersSize := len(manifest.Layers)
	if layersSize == 0 {
		return aoserrors.New("no layers in image")
	}

	// validate service rootfs layer
	if err = validateDigest(installDir, manifest.Layers[0].Digest); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func getImageManifest(installDir string) (manifest *serviceManifest, err error) {
	manifestJSON, err := ioutil.ReadFile(path.Join(installDir, manifestFileName))
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	manifest = new(serviceManifest)
	if err = json.Unmarshal(manifestJSON, manifest); err != nil {
		return nil, aoserrors.Wrap(err)
	}

	return manifest, nil
}

func validateDigest(installDir string, digest digest.Digest) (err error) {
	if err = digest.Validate(); err != nil {
		return aoserrors.Wrap(err)
	}

	file, err := os.Open(path.Join(installDir, "blobs", string(digest.Algorithm()), digest.Hex()))
	if err != nil {
		return aoserrors.Wrap(err)
	}
	defer file.Close()

	buffer := make([]byte, buffSize)
	verifier := digest.Verifier()

	for {
		count, readErr := file.Read(buffer)
		if readErr != nil && readErr != io.EOF {
			return aoserrors.Wrap(err)
		}

		if _, err := verifier.Write(buffer[:count]); err != nil {
			return aoserrors.Wrap(err)
		}

		if readErr != nil {
			break
		}
	}

	if !verifier.Verified() {
		return aoserrors.New("hash missmach")
	}

	return nil
}

func getImageParts(installDir string) (parts ImageParts, err error) {
	manifest, err := getImageManifest(installDir)
	if err != nil {
		return parts, aoserrors.Wrap(err)
	}

	parts.ImageConfigPath = path.Join(installDir, "blobs", string(manifest.Config.Digest.Algorithm()),
		manifest.Config.Digest.Hex())

	if manifest.AosService != nil {
		parts.ServiceConfigPath = path.Join(installDir, "blobs", string(manifest.AosService.Digest.Algorithm()),
			manifest.AosService.Digest.Hex())
	}

	layersSize := len(manifest.Layers)
	if layersSize == 0 {
		return parts, aoserrors.New("no layers in image")
	}

	rootFSDigest := manifest.Layers[0].Digest

	parts.ServiceFSPath = path.Join(installDir, "blobs", string(rootFSDigest.Algorithm()), rootFSDigest.Hex())

	parts.LayersDigest = getLayersFromManifest(manifest)

	return parts, nil
}

func getLayersFromManifest(manifest *serviceManifest) (layers []string) {
	manifest.Layers = manifest.Layers[1:]

	for _, layer := range manifest.Layers {
		layers = append(layers, string(layer.Digest))
	}

	return layers
}

func getManifestChecksum(installDir string) (digest []byte, err error) {
	manifestJSON, err := ioutil.ReadFile(path.Join(installDir, manifestFileName))
	if err != nil {
		return nil, aoserrors.Wrap(err)
	}

	h := sha256.New()
	h.Write(manifestJSON)

	return h.Sum(nil), nil
}
