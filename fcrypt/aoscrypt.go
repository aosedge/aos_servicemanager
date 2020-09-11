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
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"

	"aos_servicemanager/config"
)

const (
	fileBlockSize = 64 * 1024
)

const (
	onlineCertificate  = "online"
	offlineCertificate = "offline"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

//ReceiverInfo info with  receiverInfo
type ReceiverInfo struct {
	Serial string
	Issuer []byte
}

type CryptoSessionKeyInfo struct {
	SessionKey        []byte       `json:"sessionKey"`
	SessionIV         []byte       `json:"sessionIV"`
	SymmetricAlgName  string       `json:"symmetricAlgName"`
	AsymmetricAlgName string       `json:"asymmetricAlgName"`
	ReceiverInfo      ReceiverInfo `json:"recipientInfo"`
}

type CryptoContext struct {
	cryptConfig  config.Crypt
	rootCertPool *x509.CertPool

	certProvider CertificateProvider
}

// SymmetricContextInterface interface for SymmetricCipherContext
type SymmetricContextInterface interface {
	DecryptFile(encryptedFile, clearFile *os.File) (err error)
}

type SymmetricCipherContext struct {
	key         []byte
	iv          []byte
	algName     string
	modeName    string
	paddingName string
	decrypter   cipher.BlockMode
	encrypter   cipher.BlockMode
}

type certificateInfo struct {
	fingerprint string
	certificate *x509.Certificate
}

type certificateChainInfo struct {
	name         string
	fingerprints []string
}

// SignContextInterface interface for SignContext
type SignContextInterface interface {
	AddCertificate(fingerprint string, asn1Bytes []byte) (err error)
	AddCertificateChain(name string, fingerprints []string) (err error)
	VerifySign(f *os.File, chainName string, algName string, signValue []byte) (err error)
}

type SignContext struct {
	cryptoContext         *CryptoContext
	signCertificates      []certificateInfo
	signCertificateChains []certificateChainInfo
}

// RetrieveCertificateRequest request to retrieve certificate
type RetrieveCertificateRequest struct {
	CertType string
	Issuer   []byte
	Serial   string
}

// RetrieveCertificateResponse recponce with certificate
type RetrieveCertificateResponse struct {
	CrtURL string
	KeyURL string
}

// CertificateProvider interface to get certificate
type CertificateProvider interface {
	GetCertificate(certType string, issuer []byte, serial string) (certURL, ketURL string, err error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New create context for crypto operations
func New(conf config.Crypt, provider CertificateProvider) (ctx *CryptoContext, err error) {
	// Create context
	ctx = &CryptoContext{
		cryptConfig:  conf,
		certProvider: provider,
	}

	if conf.CACert != "" {
		// Load CA cert
		rootCAPEM, err := ioutil.ReadFile(conf.CACert)
		if err != nil {
			log.Errorf("error reading CA certificate (%s): %s", conf.CACert, err)
			return nil, err
		}

		ctx.rootCertPool = x509.NewCertPool()
		if !ctx.rootCertPool.AppendCertsFromPEM(rootCAPEM) {
			err = errors.New("invalid or empty CA file")
			log.Errorf("error appending CA certificate (%s): %s", conf.CACert, err)
			return nil, err
		}
	}

	return ctx, nil
}

func (ctx *CryptoContext) CreateSignContext() (SignContextInterface, error) {
	if ctx == nil || ctx.rootCertPool == nil {
		return nil, errors.New("asymmetric context not initialized")
	}

	return &SignContext{cryptoContext: ctx}, nil
}

// GetTLSConfig Provides TLS configuration for HTTPS client
func (ctx *CryptoContext) GetTLSConfig() (cfg *tls.Config, err error) {
	cfg = &tls.Config{}
	var cert tls.Certificate

	certURLStr, keyURLStr, err := ctx.certProvider.GetCertificate(onlineCertificate, nil, "")
	if err != nil {
		return cfg, err
	}

	certURL, err := url.Parse(certURLStr)
	if err != nil {
		return cfg, err
	}

	if certURL.Scheme != "file" {
		return cfg, errors.New("Expect to have file for online cert")
	}

	caCertPool, err := getCaCertPool(ctx.cryptConfig.CACert)
	if err != nil {
		return nil, err
	}

	keyURL, err := url.Parse(keyURLStr)
	if err != nil {
		return cfg, err
	}

	switch keyURL.Scheme {
	case "tpm":
		log.Debug("TLS config uses TPM engine")

		clientCert, err := loadClientCertificate(certURL.Path)
		if err != nil {
			return cfg, err
		}

		onlinePrivate, err := ctx.loadPrivateKeyByURL(keyURL)
		if err != nil {
			return cfg, err
		}

		cert = tls.Certificate{PrivateKey: onlinePrivate, Certificate: clientCert}

		// Important. TPM module only supports SHA1 and SHA-256 hash algorithms with PKCS1 padding scheme
		cert.SupportedSignatureAlgorithms = []tls.SignatureScheme{
			tls.PKCS1WithSHA256,
			tls.PKCS1WithSHA1,
		}

	case "file":
		log.Debug("TLS config uses native crypto")
		cert, err = tls.LoadX509KeyPair(certURL.Path, keyURL.Path)
		if err != nil {
			return cfg, err
		}

	default:
		log.Errorf("Receive unsupported ")
		return cfg, fmt.Errorf("Receive unsupported schema = %s for private key", keyURL.Scheme)
	}

	cfg.RootCAs = caCertPool
	cfg.Certificates = []tls.Certificate{cert}
	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) (err error) {
		return nil
	}

	cfg.BuildNameToCertificate()

	return cfg, nil
}

// DecryptMetadata decrypt envelope
func (ctx *CryptoContext) DecryptMetadata(input []byte) (output []byte, err error) {
	ci, err := unmarshallCMS(input)
	if err != nil {
		return nil, err
	}

	for _, recipient := range ci.EnvelopedData.RecipientInfos {
		dkey, err := ctx.getKeyForEnvelope(recipient.(keyTransRecipientInfo))
		if err != nil {
			log.Warning("Can't get key for envelope")
			continue
		}

		output, err = decryptMessage(&ci.EnvelopedData.EncryptedContentInfo, dkey)
		if err != nil {
			log.Warning("Can't decrypt message ", err)
			continue
		}

		return output, nil
	}

	return output, errors.New("Can't decrypt metadata")
}

// ImportSessionKey function retrieves a symmetric key from crypto context
func (ctx *CryptoContext) ImportSessionKey(keyInfo CryptoSessionKeyInfo) (symContext SymmetricContextInterface, err error) {
	if ctx == nil {
		return nil, errors.New("asymmetric context not initialized")
	}

	_, keyURLStr, err := ctx.certProvider.GetCertificate(offlineCertificate, keyInfo.ReceiverInfo.Issuer, keyInfo.ReceiverInfo.Serial)
	if err != nil {
		return nil, err
	}

	keyURL, err := url.Parse(keyURLStr)
	if err != nil {
		return nil, err
	}

	privKey, err := ctx.loadPrivateKeyByURL(keyURL)
	if err != nil {
		log.Error("Cant load private key ", err)
		return symContext, err
	}

	algName, _, _ := decodeAlgNames(keyInfo.SymmetricAlgName)
	keySize, ivSize, err := getSymmetricAlgInfo(algName)
	if err != nil {
		return nil, err
	}

	if ivSize != len(keyInfo.SessionIV) {
		return nil, errors.New("invalid IV length")
	}

	ctxSym := CreateSymmetricCipherContext()

	switch strings.ToUpper(keyInfo.AsymmetricAlgName) {
	case "RSA/PKCS1V1_5":
		if keyURL.Scheme != "tpm" {
			clearKey := make([]byte, keySize)

			err = rsa.DecryptPKCS1v15SessionKey(nil, privKey.(*rsa.PrivateKey), keyInfo.SessionKey, clearKey)
			if err != nil {
				return nil, err
			}
			if err = ctxSym.set(keyInfo.SymmetricAlgName, clearKey, keyInfo.SessionIV); err != nil {
				return nil, err
			}
		} else {
			decryptor, ok := privKey.(crypto.Decrypter)
			if !ok {
				return nil, errors.New("private key doesn't contain a Decryptor")
			}

			plainText, err := decryptor.Decrypt(rand.Reader, keyInfo.SessionKey, nil)
			if err != nil {
				return nil, err
			}

			if err = ctxSym.set(keyInfo.SymmetricAlgName, plainText, keyInfo.SessionIV); err != nil {
				return nil, err
			}
		}

	case "RSA/OAEP-256", "RSA/OAEP-512", "RSA/OAEP":
		if keyURL.Scheme != "tpm" {
			var hashFunc hash.Hash
			switch strings.ToUpper(keyInfo.AsymmetricAlgName) {
			case "RSA/OAEP":
				hashFunc = sha1.New()

			case "RSA/OAEP-256":
				hashFunc = sha256.New()

			case "RSA/OAEP-512":
				hashFunc = sha512.New()
			}

			clearKey, err := rsa.DecryptOAEP(hashFunc, nil, privKey.(*rsa.PrivateKey), keyInfo.SessionKey, nil)
			if err != nil {
				return nil, err
			}

			if err = ctxSym.set(keyInfo.SymmetricAlgName, clearKey, keyInfo.SessionIV); err != nil {
				return nil, err
			}
		} else {
			switch strings.ToUpper(keyInfo.AsymmetricAlgName) {
			case "RSA/OAEP", "RSA/OAEP-256":
				decryptor, ok := privKey.(crypto.Decrypter)
				if !ok {
					return nil, errors.New("private key doesn't contain a Decryptor")
				}

				plainText, err := decryptor.Decrypt(rand.Reader, keyInfo.SessionKey, &rsa.OAEPOptions{})
				if err != nil {
					return nil, err
				}

				if err = ctxSym.set(keyInfo.SymmetricAlgName, plainText, keyInfo.SessionIV); err != nil {
					return nil, err
				}

			default:
				return nil, errors.New("unsupported OAEP scheme")
			}
		}

	default:
		return nil, errors.New("unsupported asymmetric alg in import key")
	}

	return ctxSym, nil
}

func (ctx *SignContext) AddCertificate(fingerprint string, asn1Bytes []byte) error {
	if ctx == nil {
		return errors.New("sign context not initialized")
	}

	fingerUpper := strings.ToUpper(fingerprint)

	// Check certificate presents
	for _, item := range ctx.signCertificates {
		if item.fingerprint == fingerUpper {
			return nil
		}
	}

	// Parse certificate from ASN1 bytes
	cert, err := x509.ParseCertificate(asn1Bytes)
	if err != nil {
		return fmt.Errorf("error parsing sign certificate: %s", err)
	}

	// Append the new certificate to the collection
	ctx.signCertificates = append(ctx.signCertificates, certificateInfo{fingerprint: fingerUpper, certificate: cert})
	return nil
}

func (ctx *SignContext) AddCertificateChain(name string, fingerprints []string) error {
	if ctx == nil {
		return errors.New("sign context not initialized")
	}

	if len(fingerprints) == 0 {
		return errors.New("can not add certificate chain with an empty fingerprint list")
	}

	// Check certificate presents
	for _, item := range ctx.signCertificateChains {
		if item.name == name {
			return nil
		}
	}

	ctx.signCertificateChains = append(
		ctx.signCertificateChains, certificateChainInfo{name, fingerprints})

	return nil
}

func (ctx *SignContext) VerifySign(f *os.File, chainName string, algName string, signValue []byte) error {
	var err error
	if ctx == nil {
		return errors.New("sign context not initialized")
	}

	if len(ctx.signCertificateChains) == 0 || len(ctx.signCertificates) == 0 {
		return errors.New("sign context not initialized (no certificates)")
	}

	var chain certificateChainInfo
	var signCertFingerprint string

	// Find chain
	for _, chainTmp := range ctx.signCertificateChains {
		if chainTmp.name == chainName {
			chain = chainTmp
			signCertFingerprint = chain.fingerprints[0]
			break
		}
	}
	if chain.name == "" || len(chain.name) == 0 {
		return errors.New("unknown chain name")
	}

	signCert := ctx.getCertificateByFingerprint(signCertFingerprint)

	if signCert == nil {
		return errors.New("signing certificate is absent")
	}

	signAlgName, signHash, signPadding := decodeSignAlgNames(algName)

	var hashFunc crypto.Hash
	switch strings.ToUpper(signHash) {
	case "SHA256":
		hashFunc = crypto.SHA256
	case "SHA384":
		hashFunc = crypto.SHA384
	case "SHA512":
		hashFunc = crypto.SHA512
	case "SHA512/224":
		hashFunc = crypto.SHA512_224
	case "SHA512/256":
		hashFunc = crypto.SHA512_256
	default:
		return errors.New("unknown or unsupported hashing algorithm: " + signHash)
	}

	hash := hashFunc.New()

	_, err = io.Copy(hash, f)
	if err != nil {
		log.Errorf("Error hashing file: %s", err)
		return err
	}
	hashValue := hash.Sum(nil)

	switch signAlgName {
	case "RSA":
		publicKey := signCert.PublicKey.(*rsa.PublicKey)

		switch signPadding {
		case "PKCS1v1_5":
			err = rsa.VerifyPKCS1v15(publicKey, hashFunc.HashFunc(), hashValue, signValue)
		case "PSS":
			err = rsa.VerifyPSS(publicKey, hashFunc.HashFunc(), hashValue, signValue, nil)
		default:
			return errors.New("unknown scheme for RSA signature: " + signPadding)
		}
	default:
		return errors.New("unknown or unsupported signature alg: " + signAlgName)
	}

	if err != nil {
		return err
	}

	// Sign ok
	// Verify certs

	intermediatePool := x509.NewCertPool()
	for _, certFingerprints := range chain.fingerprints[1:] {
		crt := ctx.getCertificateByFingerprint(certFingerprints)
		if crt == nil {
			return fmt.Errorf("cannot find certificate in chain fingerprint=%s", certFingerprints)
		}
		intermediatePool.AddCert(crt)
	}

	verifyOptions := x509.VerifyOptions{
		Intermediates: intermediatePool,
		Roots:         ctx.cryptoContext.rootCertPool,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	_, err = signCert.Verify(verifyOptions)
	if err != nil {
		log.Error("Error verifying certificate chain")
		return err
	}

	return nil
}

func CreateSymmetricCipherContext() *SymmetricCipherContext {
	return &SymmetricCipherContext{}
}

func (ctx *SymmetricCipherContext) DecryptFile(encryptedFile, clearFile *os.File) error {
	if !ctx.isReady() {
		return errors.New("symmetric key is not ready")
	}
	// Get file stat (we need to know file size)
	inputFileStat, err := encryptedFile.Stat()
	if err != nil {
		return err
	}

	fileSize := inputFileStat.Size()
	if fileSize%int64(ctx.decrypter.BlockSize()) != 0 {
		return errors.New("file size is incorrect")
	}

	if _, err = encryptedFile.Seek(0, io.SeekStart); err != nil {
		return err
	}

	chunkEncrypted := make([]byte, fileBlockSize)
	chunkDecrypted := make([]byte, fileBlockSize)
	totalReadSize := int64(0)
	for totalReadSize < fileSize {
		readSize, errRead := encryptedFile.Read(chunkEncrypted)
		if errRead != nil && err != io.EOF {
			return errRead
		}

		totalReadSize += int64(readSize)

		ctx.decrypter.CryptBlocks(chunkDecrypted[:readSize], chunkEncrypted[:readSize])
		if totalReadSize == fileSize {
			// Remove padding from the last block if needed
			padSize, err := ctx.getPaddingSize(chunkDecrypted[:readSize], readSize)
			if err != nil {
				return err
			}
			readSize -= padSize
		}

		// Write decrypted chunk to the out file.
		// We can remove padding, so we should use slice with computed size.
		if _, err = clearFile.Write(chunkDecrypted[:readSize]); err != nil {
			return err
		}
	}

	return nil
}

/*******************************************************************************
 * Private
 ******************************************************************************/

// LoadOfflineKey function loads offline from url
func (ctx *CryptoContext) loadPrivateKeyByURL(keyURL *url.URL) (privKey crypto.PrivateKey, err error) {
	switch keyURL.Scheme {
	case "file":
		keyBytes, err := ioutil.ReadFile(keyURL.Path)
		if err != nil {
			return privKey, fmt.Errorf("error reading offline private key from file: %s", err)
		}

		return loadKey(keyBytes)

	case "tpm":
		result, err := strconv.ParseUint(keyURL.Hostname(), 0, 32)
		if err != nil {
			return nil, err
		}

		handle := tpmHandle(result)

		return loadTpmPrivateKey(ctx.cryptConfig.TpmDevice, handle)
	}

	return nil, fmt.Errorf("Unsupported schema %s for private Key", keyURL.Scheme)
}

func (ctx *CryptoContext) getKeyForEnvelope(keyInfo keyTransRecipientInfo) (key []byte, err error) {
	issuer, err := asn1.Marshal(keyInfo.Rid.Issuer)
	if err != nil {
		return key, err
	}

	_, keyURLStr, err := ctx.certProvider.GetCertificate(offlineCertificate, issuer, fmt.Sprintf("%X", keyInfo.Rid.SerialNumber))
	if err != nil {
		return key, err
	}

	keyURL, err := url.Parse(keyURLStr)
	if err != nil {
		return key, err
	}

	privKey, err := ctx.loadPrivateKeyByURL(keyURL)
	if err != nil {
		return key, err
	}

	switch keyWithType := privKey.(type) {
	case *rsa.PrivateKey, *tpmPrivateKeyRSA:
		decryptor, ok := keyWithType.(crypto.Decrypter)
		if !ok {
			return nil, errors.New("private key doesn't have a decryption suite")
		}

		key, err := decryptCMSKey(&keyInfo, decryptor)
		if err != nil {
			return nil, err
		}
		return key, nil
	default:
		return nil, fmt.Errorf("%T private key not yet supported", keyWithType)
	}
}

func (ctx *SymmetricCipherContext) generateKeyAndIV(algString string) error {
	// Get alg name
	algName, _, _ := decodeAlgNames(algString)

	// Get alg key and IV size in bytes
	keySize, ivSize, err := getSymmetricAlgInfo(algName)
	if err != nil {
		return err
	}

	// Allocate memory and generate cryptographically resistant random values
	key := make([]byte, keySize)
	iv := make([]byte, ivSize)

	if _, err := rand.Read(key); err != nil {
		return err
	}
	if _, err := rand.Read(iv); err != nil {
		return err
	}

	// Check and set values
	return ctx.set(algString, key, iv)
}

func (ctx *SignContext) getCertificateByFingerprint(fingerprint string) *x509.Certificate {
	// Find certificate in the chain
	for _, certTmp := range ctx.signCertificates {
		if certTmp.fingerprint == fingerprint {
			return certTmp.certificate
		}
	}
	return nil
}

func (ctx *SymmetricCipherContext) encryptFile(clearFile, encryptedFile *os.File) error {
	if !ctx.isReady() {
		return errors.New("symmetric key is not ready")
	}

	// Get file stat (we need to know file size)
	inputFileStat, err := clearFile.Stat()
	if err != nil {
		return err
	}
	fileSize := inputFileStat.Size()
	writtenSize := int64(0)
	readSize := 0
	if _, err = clearFile.Seek(0, io.SeekStart); err != nil {
		return err
	}

	// We need more space if the input file size proportional to the fileBlockSize
	chunkEncrypted := make([]byte, fileBlockSize+128)
	chunkClear := make([]byte, fileBlockSize+128)

	for i := 0; i <= int(fileSize/fileBlockSize); i++ {
		currentChunkSize := fileBlockSize
		if writtenSize+int64(currentChunkSize) > fileSize {
			// The last block may need a padding appending
			currentChunkSize = int(fileSize - writtenSize)
			if _, err = clearFile.Read(chunkClear[:currentChunkSize]); err != nil {
				return err
			}
			readSize, err = ctx.appendPadding(chunkClear, currentChunkSize)
			if err != nil {
				return err
			}
		} else {
			if readSize, err = clearFile.Read(chunkClear[:fileBlockSize]); err != nil {
				return err
			}
		}
		ctx.encrypter.CryptBlocks(chunkEncrypted, chunkClear[:readSize])
		if _, err = encryptedFile.Write(chunkEncrypted[:readSize]); err != nil {
			return err
		}
		writtenSize += int64(readSize)
	}
	err = encryptedFile.Sync()

	return nil
}

func (ctx *SymmetricCipherContext) set(algString string, key []byte, iv []byte) error {
	if algString == "" {
		return errors.New("construct symmetric alg context: empty conf string")
	}

	ctx.key = key
	ctx.iv = iv

	ctx.setAlg(algString)

	return ctx.loadKey()
}

func (ctx *SymmetricCipherContext) setAlg(algString string) {
	ctx.algName, ctx.modeName, ctx.paddingName = decodeAlgNames(algString)
}

func (ctx *SymmetricCipherContext) appendPadding(dataIn []byte, dataLen int) (fullSize int, err error) {
	switch strings.ToUpper(ctx.paddingName) {
	case "PKCS7PADDING", "PKCS7":
		return ctx.appendPkcs7Padding(dataIn, dataLen)
	default:
		return 0, errors.New("unsupported padding type")
	}
}

func (ctx *SymmetricCipherContext) getPaddingSize(dataIn []byte, dataLen int) (removedSize int, err error) {
	switch strings.ToUpper(ctx.paddingName) {
	case "PKCS7PADDING", "PKCS7":
		return ctx.removePkcs7Padding(dataIn, dataLen)
	default:
		return 0, errors.New("unsupported padding type")
	}
}

func (ctx *SymmetricCipherContext) appendPkcs7Padding(dataIn []byte, dataLen int) (fullSize int, err error) {
	blockSize := ctx.encrypter.BlockSize()
	appendSize := blockSize - (dataLen % blockSize)

	fullSize = dataLen + appendSize
	if dataLen+appendSize > len(dataIn) {
		err = errors.New("no enough space to add padding")
		return 0, err
	}

	for i := dataLen; i < fullSize; i++ {
		dataIn[i] = byte(appendSize)
	}
	return fullSize, nil
}

func (ctx *SymmetricCipherContext) removePkcs7Padding(dataIn []byte, dataLen int) (removedSize int, err error) {
	blockLen := ctx.decrypter.BlockSize()
	if dataLen%blockLen != 0 || dataLen == 0 {
		return 0, errors.New("padding error (invalid total size)")
	}

	removedSize = int(dataIn[dataLen-1])
	if removedSize < 1 || removedSize > blockLen || removedSize > dataLen {
		return 0, errors.New("padding error")
	}

	for i := dataLen - removedSize; i < dataLen; i++ {
		if dataIn[i] != byte(removedSize) {
			return 0, errors.New("padding error")
		}
	}
	return removedSize, nil
}

func (ctx *SymmetricCipherContext) loadKey() (err error) {
	var block cipher.Block
	keySizeBits := 0

	switch strings.ToUpper(ctx.algName) {
	case "AES128", "AES192", "AES256":
		if keySizeBits, err = strconv.Atoi(ctx.algName[3:]); err != nil {
			return err
		}
		if keySizeBits/8 != len(ctx.key) {
			return errors.New("invalid symmetric key size")
		}
		block, err = aes.NewCipher(ctx.key)
		if err != nil {
			log.Errorf("can't create cipher: %s", err)
			return err
		}

	default:
		err = errors.New("unsupported cryptographic algorithm: " + ctx.algName)
		return err
	}

	if len(ctx.iv) != block.BlockSize() {
		return errors.New("invalid IV size")
	}

	switch ctx.modeName {
	case "CBC":
		ctx.decrypter = cipher.NewCBCDecrypter(block, ctx.iv)
		ctx.encrypter = cipher.NewCBCEncrypter(block, ctx.iv)
	default:
		err = errors.New("unsupported encryption mode: " + ctx.modeName)
		return err
	}

	return nil
}

func (ctx *SymmetricCipherContext) isReady() bool {
	return ctx.encrypter != nil || ctx.decrypter != nil
}
