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

// Package launcher provides set of API to controls services lifecycle
package launcher

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"

	"github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"

	"aos_servicemanager/platform"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const serviceStorageFolder = "/home/service/storage"

/*******************************************************************************
 * Types
 ******************************************************************************/

type serviceSpec struct {
	ocSpec   specs.Spec
	fileName string
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func loadServiceSpec(fileName string) (spec *serviceSpec, err error) {
	log.WithField("fileName", fileName).Debug("Load service spec")

	spec = &serviceSpec{fileName: fileName}

	specJSON, err := ioutil.ReadFile(spec.fileName)
	if err != nil {
		return nil, err
	}

	if err = json.Unmarshal(specJSON, &spec.ocSpec); err != nil {
		return spec, err
	}

	return spec, nil
}

func (spec *serviceSpec) save() (err error) {
	log.WithField("fileName", spec.fileName).Debug("Save service spec")

	file, err := os.Create(spec.fileName)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "\t")

	return encoder.Encode(spec.ocSpec)
}

func (spec *serviceSpec) addStorageFolder(storageFolder string) (err error) {
	absStorageFolder, err := filepath.Abs(storageFolder)
	if err != nil {
		return err
	}

	newMount := specs.Mount{
		Destination: serviceStorageFolder,
		Type:        "bind",
		Source:      absStorageFolder,
		Options:     []string{"bind", "rw"}}

	storageIndex := len(spec.ocSpec.Mounts)

	for i, mount := range spec.ocSpec.Mounts {
		if mount.Destination == serviceStorageFolder {
			storageIndex = i
			break
		}
	}

	if storageIndex == len(spec.ocSpec.Mounts) {
		spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, newMount)

		return nil
	}

	spec.ocSpec.Mounts[storageIndex] = newMount

	return nil
}

func (spec *serviceSpec) removeStorageFolder() (err error) {
	var mounts []specs.Mount

	for _, mount := range spec.ocSpec.Mounts {
		if mount.Destination == serviceStorageFolder {

			continue
		}

		mounts = append(mounts, mount)
	}

	spec.ocSpec.Mounts = mounts

	return nil
}

func (spec *serviceSpec) setUser(user string) (err error) {
	spec.ocSpec.Process.User.UID, spec.ocSpec.Process.User.GID, err = platform.GetUserUIDGID(user)
	if err != nil {
		return err
	}

	return nil
}

func (spec *serviceSpec) mountHostFS(workingDir string) (err error) {
	mounts := []specs.Mount{
		specs.Mount{Destination: "/bin", Type: "bind", Source: "/bin", Options: []string{"bind", "ro"}},
		specs.Mount{Destination: "/sbin", Type: "bind", Source: "/sbin", Options: []string{"bind", "ro"}},
		specs.Mount{Destination: "/lib", Type: "bind", Source: "/lib", Options: []string{"bind", "ro"}},
		specs.Mount{Destination: "/usr", Type: "bind", Source: "/usr", Options: []string{"bind", "ro"}},
		// TODO: mount individual tmp
		// "destination": "/tmp",
		// "type": "tmpfs",
		// "source": "tmpfs",
		// "options": ["nosuid","strictatime","mode=755","size=65536k"]
		specs.Mount{Destination: "/tmp", Type: "bind", Source: "/tmp", Options: []string{"bind", "rw"}},
	}

	spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, mounts...)

	// add lib64 if exists
	if _, err := os.Stat("/lib64"); err == nil {
		spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, specs.Mount{Destination: "/lib64", Type: "bind", Source: "/lib64", Options: []string{"bind", "ro"}})
	}

	// add hosts
	hosts, _ := filepath.Abs(path.Join(workingDir, "etc", "hosts"))
	if _, err := os.Stat(hosts); err != nil {
		hosts = "/etc/hosts"
	}
	spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "hosts"), Type: "bind", Source: hosts, Options: []string{"bind", "ro"}})

	// add resolv.conf
	resolvConf, _ := filepath.Abs(path.Join(workingDir, "etc", "resolv.conf"))
	if _, err := os.Stat(resolvConf); err != nil {
		resolvConf = "/etc/resolv.conf"
	}
	spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "resolv.conf"), Type: "bind", Source: resolvConf, Options: []string{"bind", "ro"}})

	// add nsswitch.conf
	nsswitchConf, _ := filepath.Abs(path.Join(workingDir, "etc", "nsswitch.conf"))
	if _, err := os.Stat(nsswitchConf); err != nil {
		nsswitchConf = "/etc/nsswitch.conf"
	}
	spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "nsswitch.conf"), Type: "bind", Source: nsswitchConf, Options: []string{"bind", "ro"}})

	// TODO: all services should have their own certificates
	// this mound for demo only and should be removed
	// mount /etc/ssl
	spec.ocSpec.Mounts = append(spec.ocSpec.Mounts, specs.Mount{Destination: path.Join("/etc", "ssl"), Type: "bind", Source: path.Join("/etc", "ssl"), Options: []string{"bind", "ro"}})

	return nil
}

func (spec *serviceSpec) disableTerminal() (err error) {
	spec.ocSpec.Process.Terminal = false

	return nil
}

func (spec *serviceSpec) addPrestartHook(path string) (err error) {
	if spec.ocSpec.Hooks == nil {
		spec.ocSpec.Hooks = &specs.Hooks{}
	}

	spec.ocSpec.Hooks.Prestart = append(spec.ocSpec.Hooks.Prestart, specs.Hook{Path: path})

	return nil
}
