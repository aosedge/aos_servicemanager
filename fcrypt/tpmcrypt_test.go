// SPDX-License-Identifier: Apache-2.0
//
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

package fcrypt

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"reflect"
	"testing"

	"github.com/google/go-tpm-tools/simulator"
	"github.com/google/go-tpm/tpm2"
	log "github.com/sirupsen/logrus"
)

const (
	clientKeyHandle = 0x81000000
	parentPassword  = ""
	ownerPassword   = ""
	keyAttributes   = tpm2.FlagSign | tpm2.FlagDecrypt | tpm2.FlagFixedTPM | tpm2.FlagUserWithAuth | tpm2.FlagFixedParent | tpm2.FlagSensitiveDataOrigin
	clearPlain      = "Test TPM encrypt"
)

func TestCreateRSAPrimaryKey(t *testing.T) {
	TPMDev, err := simulator.Get()
	if err != nil {
		log.Fatalf("Error create simulator: %s", err)
	}
	defer TPMDev.Close()

	tpmCrypto := &TPMCrypto{dev: TPMDev}

	_, err = tpmCrypto.CreateRSAPrimaryKey(clientKeyHandle, parentPassword, ownerPassword, keyAttributes)
	if err != nil {
		t.Fatalf("Error create primary key: %s", err)
	}

	privateRSAKey := &tpmPrivateKeyRSA{crypt: tpmCrypto, handle: clientKeyHandle, hashAlg: crypto.SHA256}

	err = privateRSAKey.Create()
	if err != nil {
		t.Fatalf("Error create RSA private context: %s", err)
	}
	_, ok := privateRSAKey.Public().(*rsa.PublicKey)
	if !ok {
		t.Fatal("Returned RSA public key is not a public")
	}

	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, privateRSAKey.Public().(*rsa.PublicKey), []byte(clearPlain))
	if err != nil {
		t.Fatalf("Can't encrypt plain text: %s", err)
	}

	plain, err := privateRSAKey.Decrypt(rand.Reader, encrypted, &rsa.PKCS1v15DecryptOptions{})
	if err != nil {
		t.Fatalf("Can't decrypt chiper text by TPM: %s", err)
	}

	if !reflect.DeepEqual(plain, []byte(clearPlain)) {
		t.Fatal("Decoded and original plain text mismatch")
	}
}
