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

// Package fcrypt Provides cryptographic interfaces for Fusion
package fcrypt

import (
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"

	log "github.com/sirupsen/logrus"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

const (
	certDERIdent = "CERTIFICATE"
)

/*******************************************************************************
 * Variables
 ******************************************************************************/

var fcryptCfg config.Crypt

// Init initializes of fcrypt package
func Init(conf config.Crypt) error {

	fcryptCfg = conf

	return nil
}

// LoadTpmPrivateKey load private tmp key
func loadTpmPrivateKey(tpmInterface string, handel tpmHandle) (tpmPrivKey *tpmPrivateKeyRSA, err error) {
	tpmCrypto := &TPMCrypto{}

	err = tpmCrypto.Open(tpmInterface)
	if err != nil {
		return tpmPrivKey, err
	}

	tpmPrivKey = &tpmPrivateKeyRSA{crypt: tpmCrypto, handle: handel, hashAlg: crypto.SHA256}

	err = tpmPrivKey.Create()
	if err != nil {
		return nil, err
	}

	return tpmPrivKey, nil
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

func verifyCert(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	return nil
}

func getCaCertPool() (*x509.CertPool, error) {
	// Load CA cert
	caCert, err := ioutil.ReadFile(fcryptCfg.CACert)
	if err != nil {
		log.Errorf("Error reading CA certificate: %s", err)
		return nil, err
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)
	return caCertPool, nil
}

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

func extractKeyFromCert(cert *x509.Certificate) (*rsa.PublicKey, error) {
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		return pub, nil
	default:
		return nil, errors.New("unknown public key type")
	}

}

func parseCertificates(pemData string) (ret []*x509.Certificate, err error) {

	for block, remainder := pem.Decode([]byte(pemData)); block != nil; block, remainder = pem.Decode(remainder) {
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("invalid PEM Block")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			log.Errorf("Error parsing certificate: %s", err)
			return nil, err
		}
		ret = append(ret, cert)
	}
	return
}
func getAndVerifySignCert(certificates string) (ret *x509.Certificate, err error) {
	certs, err := parseCertificates(certificates)
	if err != nil {
		return nil, err
	}

	if len(certs) == 0 {
		return nil, errors.New("no certificates found in certificate chain")
	}

	signCertificate := certs[0]

	intermediatePool := x509.NewCertPool()
	for _, cert := range certs[1:] {
		intermediatePool.AddCert(cert)
	}

	caCertPool, err := getCaCertPool()
	if err != nil {
		return
	}

	verifyOptions := x509.VerifyOptions{
		Intermediates: intermediatePool,
		Roots:         caCertPool,
		// TODO: Use more sensible ExtKeyUsage
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	_, err = signCertificate.Verify(verifyOptions)
	if err != nil {
		log.Error("Error verifying certificate chain")
		return
	}

	return signCertificate, nil
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
