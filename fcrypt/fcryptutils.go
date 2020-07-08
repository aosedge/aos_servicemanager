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

package fcrypt

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"strings"

	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	certDERIdent = "CERTIFICATE"
)

/*******************************************************************************
 * Public
 ******************************************************************************/

// GetCertificateOrganizations gives a list of certificate organizations
func GetCertificateOrganizations(provider CertificateProvider) (names []string, err error) {
	resp, err := provider.GetCertificateForSM(RetrieveCertificateRequest{CertType: onlineCertificate})
	if err != nil {
		return names, err
	}

	certURL, err := url.Parse(resp.CrtURL)
	if err != nil {
		return names, err
	}

	if certURL.Scheme != "file" {
		return names, errors.New("Expect to have file for online cert")
	}

	certRaw, err := ioutil.ReadFile(certURL.Path)
	if err != nil {
		return nil, err
	}

	certPemData, _ := pem.Decode(certRaw)
	cert, err := x509.ParseCertificate(certPemData.Bytes)
	if err != nil {
		return nil, err
	}

	if cert.Subject.Organization == nil {
		return nil, errors.New("certificate does not have organizations")
	}

	return cert.Subject.Organization, nil
}

// GetCrtSerialByURL get cerificate  serial by URL
func GetCrtSerialByURL(crtURL string) (serial string, err error) {
	urlVal, err := url.Parse(crtURL)
	if err != nil {
		return "", err
	}

	if urlVal.Scheme != "file" {
		return "", errors.New("wrong certificate scheme")
	}

	pemCrt, err := ioutil.ReadFile(urlVal.Path)
	if err != nil {
		return "", err
	}

	block, _ := pem.Decode(pemCrt)

	if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
		return "", errors.New("invalid PEM Block")
	}

	x509Crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%X", x509Crt.SerialNumber), nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func loadClientCertificate(file string) (certificates [][]byte, err error) {
	certPEMBlock, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}

	for {
		var certDERBlock *pem.Block

		certDERBlock, certPEMBlock = pem.Decode(certPEMBlock)
		if certDERBlock == nil {
			break
		}

		if certDERBlock.Type == certDERIdent {
			certificates = append(certificates, certDERBlock.Bytes)
		}
	}

	if len(certificates) == 0 {
		return nil, errors.New("client certificate does not exist")
	}

	return certificates, nil
}

func removePkcs7Padding(in []byte, blocklen int) ([]byte, error) {
	l := len(in)
	if l%blocklen != 0 {
		return nil, errors.New("padding error")
	}

	pl := int(in[l-1])
	if pl < 1 || pl > blocklen {
		return nil, errors.New("padding error")
	}

	for i := l - pl; i < l; i++ {
		if in[i] != byte(pl) {
			return nil, errors.New("padding error")
		}
	}

	return in[0 : l-pl], nil
}

func decodeAlgNames(algString string) (algName, modeName, paddingName string) {
	// alg string example: AES128/CBC/PKCS7PADDING
	algNamesSlice := strings.Split(algString, "/")
	if len(algNamesSlice) >= 1 {
		algName = algNamesSlice[0]
	} else {
		algName = ""
	}
	if len(algNamesSlice) >= 2 {
		modeName = algNamesSlice[1]
	} else {
		modeName = "CBC"
	}

	if len(algNamesSlice) >= 3 {
		paddingName = algNamesSlice[2]
	} else {
		paddingName = "PKCS7PADDING"
	}

	return algName, modeName, paddingName
}

func loadKey(keyBytes []byte) (crypto.PrivateKey, error) {
	var err error
	var key crypto.PrivateKey

	if key, err = decodePrivateKey(keyBytes); err != nil {
		return nil, err
	}

	switch key := key.(type) {
	case *rsa.PrivateKey:
		return key, nil
	default: // can be only  *ecdsa.PrivateKey after decodePrivateKey
		return nil, errors.New("ECDSA private key not yet supported")
	}
}

func decodeSignAlgNames(algString string) (algName, hashName, paddingName string) {
	// alg string example: RSA/SHA256/PKCS1v1_5 or RSA/SHA256
	algNamesSlice := strings.Split(algString, "/")
	if len(algNamesSlice) >= 1 {
		algName = algNamesSlice[0]
	} else {
		algName = ""
	}
	if len(algNamesSlice) >= 2 {
		hashName = algNamesSlice[1]
	} else {
		hashName = "SHA256"
	}

	if len(algNamesSlice) >= 3 {
		paddingName = algNamesSlice[2]
	} else {
		paddingName = "PKCS1v1_5"
	}

	return algName, hashName, paddingName
}

func decodePrivateKey(bytes []byte) (crypto.PrivateKey, error) {
	var der []byte

	// Try to parse data as PEM. Ignore the rest of the data
	// ToDo: add support private key and certificate in the same file
	block, _ := pem.Decode(bytes)

	if block != nil {
		// bytes is PEM
		der = block.Bytes
	} else {
		der = bytes
	}

	// Try to load key as PKCS1 container
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}

	// Try to load key as PKCS8 container
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		switch key := key.(type) {
		case *rsa.PrivateKey, *ecdsa.PrivateKey:
			return key, nil
		default:
			return nil, errors.New("found unsupported private key type in PKCS8 container")
		}
	}

	// Try to parse as PCKS8 EC private key
	if key, err := x509.ParseECPrivateKey(der); err == nil {
		return key, nil
	}

	// This is not a private key
	return nil, errors.New("failed to parse private key")
}

func getSymmetricAlgInfo(algName string) (keySize int, ivSize int, err error) {
	switch strings.ToUpper(algName) {
	case "AES128":
		return 16, 16, nil
	case "AES192":
		return 24, 16, nil
	case "AES256":
		return 32, 16, nil

	default:
		return 0, 0, errors.New("unsupported symmetric algorithm")
	}
}

func getCaCertPool(rootCaFilePath string) (*x509.CertPool, error) {
	// Load CA cert
	caCert, err := ioutil.ReadFile(rootCaFilePath)
	if err != nil {
		log.Errorf("Error reading CA certificate: %s", err)
		return nil, err
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)
	return caCertPool, nil
}
