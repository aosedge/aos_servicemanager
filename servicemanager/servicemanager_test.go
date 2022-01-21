// SPDX-License-Identifier: Apache-2.0
//
// Copyright (C) 2022 Renesas Electronics Corporation.
// Copyright (C) 2022 EPAM Systems, Inc.
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

package servicemanager_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoscloud/aos_common/aoserrors"
	"github.com/aoscloud/aos_common/image"
	"github.com/opencontainers/go-digest"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	log "github.com/sirupsen/logrus"

	"github.com/aoscloud/aos_servicemanager/config"
	"github.com/aoscloud/aos_servicemanager/servicemanager"
)

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/**********************************************************************************************************************
 * Consts
 *********************************************************************************************************************/

const (
	errorAddServiceID = "errorAddServiceID"
	errorGetServicID  = "errorGetServicID"
)

/**********************************************************************************************************************
 * Types
 *********************************************************************************************************************/

type testServiceStorage struct {
	Services []servicemanager.ServiceInfo
}

/**********************************************************************************************************************
 * Vars
 *********************************************************************************************************************/

var tmpDir string

/**********************************************************************************************************************
 * Init
 *********************************************************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true,
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/**********************************************************************************************************************
* Main
**********************************************************************************************************************/

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("Error setting up: %s", err)
	}

	ret := m.Run()

	cleanup()

	os.Exit(ret)
}

/**********************************************************************************************************************
* Tests
**********************************************************************************************************************/

func TestInstallService(t *testing.T) {
	serviceStorage := &testServiceStorage{
		Services: []servicemanager.ServiceInfo{
			{ServiceID: "id1", AosVersion: 1, GID: 5000},
			{ServiceID: "id2", AosVersion: 1, GID: 5001},
		},
	}

	config := &config.Config{
		WorkingDir:  tmpDir,
		ServicesDir: path.Join(tmpDir, "servicemanager", "services"),
		DownloadDir: path.Join(tmpDir, "downloads"),
	}

	if _, err := servicemanager.New(config, serviceStorage); err != nil {
		t.Fatalf("Can't create SM: %s", err)
	}

	sm, err := servicemanager.New(config, serviceStorage)
	if err != nil {
		t.Fatalf("Can't create SM: %s", err)
	}

	// install services
	serviceID := "testService0"

	serviceURL, fileInfo, err := prepareService("Service content")
	if err != nil {
		t.Fatalf("Can't prepare test service: %s", err)
	}

	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: serviceID, AosVersion: 1},
		serviceURL, fileInfo); err != nil {
		t.Errorf("Can't install service: %s", err)
	}

	// update service
	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: serviceID, AosVersion: 2},
		serviceURL, fileInfo); err != nil {
		t.Errorf("Can't update service: %s", err)
	}

	os.RemoveAll(serviceURL)

	// test install from remote domain
	fileServerDir, err := ioutil.TempDir("", "sm_fileserver")
	if err != nil {
		t.Fatalf("Error create temporary dir: %s", err)
	}

	defer os.RemoveAll(fileServerDir)

	serviceID = "testIDRemoteService"

	if serviceURL, fileInfo, err = prepareService("SomeContnet"); err != nil {
		t.Fatalf("Can't prepare test service: %s", err)
	}

	urlVal, err := url.Parse(serviceURL)
	if err != nil {
		t.Fatalf("Can't parse url: %s", err)
	}

	_ = os.Rename(path.Join(urlVal.Path), path.Join(fileServerDir, "downloadImage"))

	server := &http.Server{Addr: ":9000", Handler: http.FileServer(http.Dir(fileServerDir))}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Errorf("Can't serve http server: %s", err)
		}
	}()

	time.Sleep(1 * time.Second)

	defer server.Close()

	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: serviceID, AosVersion: 1},
		"http://:9000/downloadImage", fileInfo); err != nil {
		t.Errorf("Can't install service from remote domain: %s", err)
	}

	os.RemoveAll(path.Join(fileServerDir, "downloadImage"))

	// test version missmatch
	serviceID = "testID1"

	if serviceURL, fileInfo, err = prepareService("SomeContnet"); err != nil {
		t.Fatalf("Can't prepare test service: %s", err)
	}

	serviceStorage.Services = append(serviceStorage.Services, servicemanager.ServiceInfo{
		AosVersion: 1,
		ServiceID:  serviceID,
	})

	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: serviceID, AosVersion: 1},
		serviceURL, fileInfo); err == nil {
		t.Error("Should be error version mismatch")
	} else if !errors.Is(err, servicemanager.ErrVersionMismatch) {
		t.Errorf("Should be error version mismatch, but have: %s", err)
	}

	os.RemoveAll(serviceURL)

	// check incorrect check sum
	serviceID = "testID2"

	if serviceURL, fileInfo, err = prepareService("SomeContnet"); err != nil {
		t.Fatalf("Can't prepare test service: %s", err)
	}

	fileInfo.Sha256 = []byte{0}
	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: serviceID, AosVersion: 1},
		serviceURL, fileInfo); err == nil {
		t.Error("Should be error")
	}

	// untar error
	notTarFile := path.Join(tmpDir, "notTar")
	if err := ioutil.WriteFile(notTarFile, []byte("testContent"), 0o600); err != nil {
		t.Errorf("Can't create file: %s", err)
	}

	if fileInfo, err = image.CreateFileInfo(context.Background(), notTarFile); err != nil {
		t.Errorf("Can't create file info: %s", err)
	}

	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: serviceID, AosVersion: 1},
		"file://"+notTarFile, fileInfo); err == nil {
		t.Error("Should be error can't untar")
	}

	// check download error
	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: serviceID, AosVersion: 1},
		"url://notexist", fileInfo); err == nil {
		t.Error("Should be error")
	}

	// test incorrect url
	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: serviceID, AosVersion: 1},
		"\n", fileInfo); err == nil {
		t.Error("Should be error")
	}

	os.RemoveAll(serviceURL)

	// test get service error
	if serviceURL, fileInfo, err = prepareService("SomeContnet"); err != nil {
		t.Fatalf("Can't prepare test service: %s", err)
	}

	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: errorGetServicID, AosVersion: 1},
		serviceURL, fileInfo); err == nil {
		t.Error("Should be can't add service")
	}

	// test add service error
	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: errorAddServiceID, AosVersion: 1},
		serviceURL, fileInfo); err == nil {
		t.Error("Should be can't add service")
	}

	os.RemoveAll(serviceURL)
}

func TestImageParts(t *testing.T) {
	serviceStorage := &testServiceStorage{}

	config := &config.Config{
		WorkingDir:  tmpDir,
		ServicesDir: path.Join(tmpDir, "servicemanager", "services"),
		DownloadDir: path.Join(tmpDir, "downloads"),
	}

	sm, err := servicemanager.New(config, serviceStorage)
	if err != nil {
		t.Fatalf("Can't create SM: %s", err)
	}

	// install services
	serviceID := "testService0"

	serviceURL, fileInfo, err := prepareService("Service content")
	if err != nil {
		t.Fatalf("Can't prepare test service: %s", err)
	}

	if err = sm.InstallService(servicemanager.ServiceInfo{ServiceID: serviceID, AosVersion: 1},
		serviceURL, fileInfo); err != nil {
		t.Errorf("Can't install service: %s", err)
	}

	serviceInfo, err := sm.GetServiceInfo(serviceID)
	if err != nil {
		t.Errorf("Can't get service info: %s", err)
	}

	imageParts, err := sm.GetImageParts(serviceInfo)
	if err != nil {
		t.Errorf("Can't get image parts: %s", err)
	}

	if imageParts.ImageConfigPath == "" {
		t.Error("Image config path should not be empty")
	}

	if imageParts.ServiceConfigPath == "" {
		t.Error("Service config path should not be empty")
	}

	if imageParts.ServiceFSPath == "" {
		t.Error("Service fs path should npt be empty")
	}

	if len(imageParts.LayersDigest) != 0 {
		t.Error("count of liers should be 0")
	}
}

/**********************************************************************************************************************
* Interfaces
**********************************************************************************************************************/

func (storage *testServiceStorage) GetService(serviceID string) (service servicemanager.ServiceInfo, err error) {
	if serviceID == errorGetServicID {
		return service, aoserrors.New("can't get service")
	}

	for _, service = range storage.Services {
		if service.ServiceID == serviceID {
			return service, nil
		}
	}

	return service, servicemanager.ErrNotExist
}

func (storage *testServiceStorage) GetAllServices() (services []servicemanager.ServiceInfo, err error) {
	return storage.Services, err
}

func (storage *testServiceStorage) AddService(service servicemanager.ServiceInfo) (err error) {
	if service.ServiceID == errorAddServiceID {
		return aoserrors.New("can't add service")
	}

	storage.Services = append(storage.Services, service)

	return err
}

/**********************************************************************************************************************
* Private
**********************************************************************************************************************/

func setup() (err error) {
	if tmpDir, err = ioutil.TempDir("", "aos_"); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}

func cleanup() {
	os.RemoveAll(tmpDir)
}

func prepareService(testContent string) (outputURL string, fileInfo image.FileInfo, err error) {
	imageDir, err := ioutil.TempDir("", "aos_")
	if err != nil {
		return outputURL, fileInfo, aoserrors.Wrap(err)
	}

	defer os.RemoveAll(imageDir)

	// create dir
	if err := os.MkdirAll(path.Join(imageDir, "rootfs", "home"), 0o755); err != nil {
		return outputURL, fileInfo, aoserrors.Wrap(err)
	}

	if err := ioutil.WriteFile(path.Join(imageDir, "rootfs", "home", "service.py"),
		[]byte(testContent), 0o600); err != nil {
		return outputURL, fileInfo, aoserrors.Wrap(err)
	}

	fsDigest, err := generateFsLayer(imageDir, path.Join(imageDir, "rootfs"))
	if err != nil {
		return outputURL, fileInfo, aoserrors.Wrap(err)
	}

	aosSrvConfigDigest, err := generateAndSaveDigest(path.Join(imageDir, "blobs"), []byte("{}"))
	if err != nil {
		return outputURL, fileInfo, aoserrors.Wrap(err)
	}

	imgSpecDigestDigest, err := generateAndSaveDigest(path.Join(imageDir, "blobs"), []byte("{}"))
	if err != nil {
		return outputURL, fileInfo, aoserrors.Wrap(err)
	}

	if err := genarateImageManfest(imageDir, &imgSpecDigestDigest, &aosSrvConfigDigest, &fsDigest, nil); err != nil {
		return outputURL, fileInfo, aoserrors.Wrap(err)
	}

	imageFile, err := ioutil.TempFile("", "aos_")
	if err != nil {
		return outputURL, fileInfo, aoserrors.Wrap(err)
	}

	outputURL = imageFile.Name()
	imageFile.Close()

	if err = packImage(imageDir, outputURL); err != nil {
		return outputURL, fileInfo, aoserrors.Wrap(err)
	}

	if fileInfo, err = image.CreateFileInfo(context.Background(), outputURL); err != nil {
		return outputURL, fileInfo, aoserrors.Wrap(err)
	}

	return "file://" + outputURL, fileInfo, nil
}

func generateFsLayer(imgFolder, rootfs string) (digest digest.Digest, err error) {
	blobsDir := path.Join(imgFolder, "blobs")
	if err := os.MkdirAll(blobsDir, 0o755); err != nil {
		return digest, aoserrors.Wrap(err)
	}

	tarFile := path.Join(blobsDir, "_temp.tar.gz")

	if output, err := exec.Command("tar", "-C", rootfs, "-czf", tarFile, "./").CombinedOutput(); err != nil {
		return digest, aoserrors.Errorf("error: %s, code: %s", string(output), err)
	}
	defer os.Remove(tarFile)

	file, err := os.Open(tarFile)
	if err != nil {
		return digest, aoserrors.Wrap(err)
	}
	defer file.Close()

	byteValue, err := ioutil.ReadAll(file)
	if err != nil {
		return digest, aoserrors.Wrap(err)
	}

	digest, err = generateAndSaveDigest(blobsDir, byteValue)
	if err != nil {
		return digest, aoserrors.Wrap(err)
	}

	os.RemoveAll(rootfs)

	return digest, nil
}

func generateAndSaveDigest(folder string, data []byte) (retDigest digest.Digest, err error) {
	fullPath := path.Join(folder, "sha256")
	if err := os.MkdirAll(fullPath, 0o755); err != nil {
		return retDigest, aoserrors.Wrap(err)
	}

	h := sha256.New()
	h.Write(data)
	retDigest = digest.NewDigest("sha256", h)

	file, err := os.Create(path.Join(fullPath, retDigest.Hex()))
	if err != nil {
		return retDigest, aoserrors.Wrap(err)
	}
	defer file.Close()

	_, err = file.Write(data)
	if err != nil {
		return retDigest, aoserrors.Wrap(err)
	}

	return retDigest, nil
}

func genarateImageManfest(folderPath string, imgConfig, aosSrvConfig, rootfsLayer *digest.Digest,
	srvLayers []digest.Digest) (err error) {
	type serviceManifest struct {
		imagespec.Manifest
		AosService *imagespec.Descriptor `json:"aosService,omitempty"`
	}

	var manifest serviceManifest
	manifest.SchemaVersion = 2

	manifest.Config = imagespec.Descriptor{
		MediaType: "application/vnd.oci.image.config.v1+json",
		Digest:    *imgConfig,
	}

	if aosSrvConfig != nil {
		manifest.AosService = &imagespec.Descriptor{
			MediaType: "application/vnd.aos.service.config.v1+json",
			Digest:    *aosSrvConfig,
		}
	}

	layerDescriptor := imagespec.Descriptor{
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		Digest:    *rootfsLayer,
	}

	manifest.Layers = append(manifest.Layers, layerDescriptor)

	for _, layerDigest := range srvLayers {
		layerDescriptor := imagespec.Descriptor{
			MediaType: "application/vnd.aos.image.layer.v1.tar",
			Digest:    layerDigest,
		}

		manifest.Layers = append(manifest.Layers, layerDescriptor)
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		return aoserrors.Wrap(err)
	}

	jsonFile, err := os.Create(path.Join(folderPath, "manifest.json"))
	if err != nil {
		return aoserrors.Wrap(err)
	}

	if _, err := jsonFile.Write(data); err != nil {
		return aoserrors.Wrap(err)
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

	if err = filepath.Walk(source, func(fileName string, fileInfo os.FileInfo, inErr error) error {
		header, err := tar.FileInfoHeader(fileInfo, fileInfo.Name())
		if err != nil {
			return aoserrors.Wrap(err)
		}

		header.Name = strings.TrimPrefix(strings.ReplaceAll(fileName, source, ""), string(filepath.Separator))

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
	}); err != nil {
		return aoserrors.Wrap(err)
	}

	return nil
}
