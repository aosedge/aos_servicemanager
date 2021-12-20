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

package launcher

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/opencontainers/go-digest"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const manifestFileName = "manifest.json"
const decryptDir = "/tmp/decrypt"

/*******************************************************************************
 * Type
 ******************************************************************************/

type imageHandler struct {
}

type imageParts struct {
	imageConfigPath    string
	aosSrvConfigPath   string
	serviceFSLayerPath string
	layersDigest       []string
}

/*******************************************************************************
 * Private
 ******************************************************************************/

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

	buffer := make([]byte, 1024*1024)
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

	if verifier.Verified() == false {
		return aoserrors.New("hash missmach")
	}

	return nil
}

func packImage(source, name string) (err error) {
	log.WithFields(log.Fields{"source": source, "name": name}).Debug("Pack image")

	_, err = os.Stat(source)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	writer, err := os.Create(name)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	gzWriter := gzip.NewWriter(writer)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	return filepath.Walk(source, func(fileName string, fileInfo os.FileInfo, err error) error {
		header, err := tar.FileInfoHeader(fileInfo, fileInfo.Name())
		if err != nil {
			return aoserrors.Wrap(err)
		}

		header.Name = strings.TrimPrefix(strings.Replace(fileName, source, "", -1), string(filepath.Separator))

		if header.Name == "" {
			return nil
		}

		if err = tarWriter.WriteHeader(header); err != nil {
			return aoserrors.Wrap(err)
		}

		if !fileInfo.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(fileName)
		if err != nil {
			return aoserrors.Wrap(err)
		}
		defer file.Close()

		_, err = io.Copy(tarWriter, file)
		return aoserrors.Wrap(err)
	})
}

func getImageParts(installDir string) (parts imageParts, err error) {
	manifest, err := getImageManifest(installDir)
	if err != nil {
		return parts, aoserrors.Wrap(err)
	}

	parts.imageConfigPath = path.Join(installDir, "blobs", string(manifest.Config.Digest.Algorithm()), string(manifest.Config.Digest.Hex()))

	if manifest.AosService != nil {
		parts.aosSrvConfigPath = path.Join(installDir, "blobs", string(manifest.AosService.Digest.Algorithm()), string(manifest.AosService.Digest.Hex()))
	}

	layersSize := len(manifest.Layers)
	if layersSize == 0 {
		return parts, aoserrors.New("no layers in image")
	}

	rootFSDigest := manifest.Layers[0].Digest

	parts.serviceFSLayerPath = path.Join(installDir, "blobs", string(rootFSDigest.Algorithm()), string(rootFSDigest.Hex()))

	parts.layersDigest = getLayersFromManifest(manifest)

	return parts, nil
}

func getServiceLayers(installDir string) (layers []string, err error) {
	manifest, err := getImageManifest(installDir)
	if err != nil {
		return layers, aoserrors.Wrap(err)
	}

	return getLayersFromManifest(manifest), nil
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

func validateImageManifest(service Service) (err error) {
	digest, err := getManifestChecksum(service.Path)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if !bytes.Equal(digest, service.ManifestDigest) {
		return aoserrors.New("manifest image digest does not match")
	}

	return nil
}
