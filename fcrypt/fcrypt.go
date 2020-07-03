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
	"crypto/tls"
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
var onlinePrivate *tpmPrivateKeyRSA
var offlinePrivate *tpmPrivateKeyRSA

// Init initializes of fcrypt package
func Init(conf config.Crypt) error {
	var err error

	fcryptCfg = conf

	log.Debug("CAcert:         ", fcryptCfg.CACert)
	log.Debug("ClientCert:     ", fcryptCfg.ClientCert)
	log.Debug("ClientKey:      ", fcryptCfg.ClientKey)
	log.Debug("OfflinePrivKey: ", fcryptCfg.OfflinePrivKey)
	log.Debug("OfflineCert:    ", fcryptCfg.OfflineCert)

	if fcryptCfg.TPMEngine.Enabled {
		log.Debugf("Open hardware crypto interface %s", fcryptCfg.TPMEngine.Interface)

		tpmCrypto := &TPMCrypto{}

		err = tpmCrypto.Open(fcryptCfg.TPMEngine.Interface)
		if err != nil {
			return err
		}

		onlinePrivate = &tpmPrivateKeyRSA{crypt: tpmCrypto, handle: fcryptCfg.TPMEngine.OnlineHandle, hashAlg: crypto.SHA256}

		err = onlinePrivate.Create()
		if err != nil {
			return err
		}

		log.Debugf("TPM creates private online key object from 0x%X", uint32(fcryptCfg.TPMEngine.OnlineHandle))

		offlinePrivate = &tpmPrivateKeyRSA{crypt: tpmCrypto, handle: fcryptCfg.TPMEngine.OfflineHandle, hashAlg: crypto.SHA256}

		err = offlinePrivate.Create()
		if err != nil {
			return err
		}

		log.Debugf("TPM creates private offline key object from 0x%X", uint32(fcryptCfg.TPMEngine.OfflineHandle))
	}

	return nil
}

// GetCrtSerialByURL get cerificate  serial by URI
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

// GetTLSConfig Provides TLS configuration for HTTPS client
func GetTLSConfig() (*tls.Config, error) {
	cfg := &tls.Config{}
	var cert tls.Certificate
	var err error

	ClientCert, err := loadClientCertificate(fcryptCfg.ClientCert)
	if err != nil {
		return cfg, err
	}

	caCertPool, err := getCaCertPool()
	if err != nil {
		return nil, err
	}

	if fcryptCfg.TPMEngine.Enabled {
		log.Debug("TLS config uses TPM engine")
		cert = tls.Certificate{PrivateKey: onlinePrivate, Certificate: ClientCert}

		// Important. TPM module only supports SHA1 and SHA-256 hash algorithms with PKCS1 padding scheme
		cert.SupportedSignatureAlgorithms = []tls.SignatureScheme{
			tls.PKCS1WithSHA256,
			tls.PKCS1WithSHA1,
		}
	} else {
		log.Debug("TLS config uses native crypto")
		cert, err = tls.LoadX509KeyPair(fcryptCfg.ClientCert, fcryptCfg.ClientKey)
		if err != nil {
			return cfg, err
		}
	}

	cfg.RootCAs = caCertPool
	cfg.Certificates = []tls.Certificate{cert}
	cfg.VerifyPeerCertificate = verifyCert

	cfg.BuildNameToCertificate()
	return cfg, nil
}

func getPrivKey() (key crypto.PrivateKey, err error) {
	if fcryptCfg.TPMEngine.Enabled {
		key = offlinePrivate
	} else {
		keyBytes, err := ioutil.ReadFile(fcryptCfg.OfflinePrivKey)
		if err != nil {
			return nil, fmt.Errorf("error reading private key: %s", err)
		}

		if key, err = decodePrivateKey(keyBytes); err != nil {
			return nil, err
		}
	}

	return key, err
}

func getOfflineCert() (*x509.Certificate, error) {
	pemCert, err := ioutil.ReadFile(fcryptCfg.OfflineCert)
	if err != nil {
		log.Errorf("Error reading offline certificate: %s", err)
		return nil, err
	}

	var block *pem.Block
	block, _ = pem.Decode(pemCert)
	if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
		return nil, errors.New("invalid PEM Block")
	}

	return x509.ParseCertificate(block.Bytes)
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
