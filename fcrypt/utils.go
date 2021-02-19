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
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"strings"
)

/*******************************************************************************
 * Public
 ******************************************************************************/

// GetCertificateOrganizations gives a list of certificate organizations
func GetCertificateOrganizations(provider CertificateProvider) (names []string, err error) {
	certURLStr, _, err := provider.GetCertificate(onlineCertificate, nil, "")
	if err != nil {
		return names, err
	}

	certURL, err := url.Parse(certURLStr)
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

// GetCrtSerialByURL get certificate serial by URL
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

	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
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
