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
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
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

type CryptoSessionKeyInfo struct {
	SessionKey        []byte `json:"sessionKey"`
	SessionIV         []byte `json:"sessionIV"`
	SymmetricAlgName  string `json:"symmetricAlgName"`
	AsymmetricAlgName string `json:"asymmetricAlgName"`
	RecipientInfo     string `json:"recipientInfo"`
}

type CryptoContext struct {
	cryptConfig  config.Crypt
	rootCertPool *x509.CertPool

	privateKey crypto.PrivateKey
}

// SymmetricContextInterface interface for SymmetricCipherContext
type SymmetricContextInterface interface {
	GenerateKeyAndIV(algString string) (err error)
	DecryptFile(encryptedFile, clearFile *os.File) (err error)
	EncryptFile(clearFile, encryptedFile *os.File) (err error)
	IsReady() (ready bool)
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
	CrtURI string
	KeyURI string
}

// CertificateProvider interface ti get certificate for SM
type CertificateProvider interface {
	GetCertificateForSM(request RetrieveCertificateRequest) (resp RetrieveCertificateResponse, err error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

func CreateContext(conf config.Crypt) (*CryptoContext, error) {
	// Create context
	ctx := &CryptoContext{
		cryptConfig: conf,
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

func (ctx *SignContext) getCertificateByFingerprint(fingerprint string) *x509.Certificate {
	// Find certificate in the chain
	for _, certTmp := range ctx.signCertificates {
		if certTmp.fingerprint == fingerprint {
			return certTmp.certificate
		}
	}
	return nil
}

// LoadOfflineKeyFile sets a new offline key file and load it
func (ctx *CryptoContext) LoadOfflineKeyFile(filepath string) error {
	ctx.cryptConfig.OfflinePrivKey = filepath
	return ctx.LoadOfflineKey()
}

// LoadOfflineKey function loads offline key into context
func (ctx *CryptoContext) LoadOfflineKey() error {
	if !ctx.cryptConfig.TPMEngine.Enabled {
		if ctx.cryptConfig.OfflinePrivKey == "" {
			return errors.New("OfflinePrivKey not set")
		}

		keyBytes, err := ioutil.ReadFile(ctx.cryptConfig.OfflinePrivKey)
		if err != nil {
			return fmt.Errorf("error reading offline private key from file: %s", err)
		}

		return ctx.LoadKeyFromBytes(keyBytes)
	}

	ctx.privateKey = offlinePrivate

	return nil
}

// LoadKeyFromBytes function loads online key which represents in a byte data into context
func (ctx *CryptoContext) LoadKeyFromBytes(data []byte) (err error) {
	ctx.privateKey, err = loadKey(data)
	return err
}

// ImportSessionKey function retrieves a symmetric key from crypto context
func (ctx *CryptoContext) ImportSessionKey(keyInfo CryptoSessionKeyInfo) (symContext SymmetricContextInterface, err error) {
	if ctx == nil || ctx.privateKey == nil {
		return nil, errors.New("asymmetric context not initialized")
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
		if !ctx.cryptConfig.TPMEngine.Enabled {
			clearKey := make([]byte, keySize)

			err = rsa.DecryptPKCS1v15SessionKey(nil, ctx.privateKey.(*rsa.PrivateKey), keyInfo.SessionKey, clearKey)
			if err != nil {
				return nil, err
			}
			if err = ctxSym.set(keyInfo.SymmetricAlgName, clearKey, keyInfo.SessionIV); err != nil {
				return nil, err
			}
		} else {
			decryptor, ok := ctx.privateKey.(crypto.Decrypter)
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
		if !ctx.cryptConfig.TPMEngine.Enabled {
			var hashFunc hash.Hash
			switch strings.ToUpper(keyInfo.AsymmetricAlgName) {
			case "RSA/OAEP":
				hashFunc = sha1.New()

			case "RSA/OAEP-256":
				hashFunc = sha256.New()

			case "RSA/OAEP-512":
				hashFunc = sha512.New()
			}

			clearKey, err := rsa.DecryptOAEP(hashFunc, nil, ctx.privateKey.(*rsa.PrivateKey), keyInfo.SessionKey, nil)
			if err != nil {
				return nil, err
			}

			if err = ctxSym.set(keyInfo.SymmetricAlgName, clearKey, keyInfo.SessionIV); err != nil {
				return nil, err
			}
		} else {
			switch strings.ToUpper(keyInfo.AsymmetricAlgName) {
			case "RSA/OAEP", "RSA/OAEP-256":
				decryptor, ok := ctx.privateKey.(crypto.Decrypter)
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

func CreateSymmetricCipherContext() *SymmetricCipherContext {
	return &SymmetricCipherContext{}
}

func (ctx *SymmetricCipherContext) GenerateKeyAndIV(algString string) error {
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

func (ctx *SymmetricCipherContext) DecryptFile(encryptedFile, clearFile *os.File) error {
	if !ctx.IsReady() {
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

func (ctx *SymmetricCipherContext) EncryptFile(clearFile, encryptedFile *os.File) error {
	if !ctx.IsReady() {
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

func (ctx *SymmetricCipherContext) IsReady() bool {
	return ctx.encrypter != nil || ctx.decrypter != nil
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

// GetCertificateOrganizations gives a list of certificate organizations
func GetCertificateOrganizations(provider CertificateProvider) (names []string, err error) {
	resp, err := provider.GetCertificateForSM(RetrieveCertificateRequest{CertType: onlineCertificate})
	if err != nil {
		return names, err
	}

	certURI, err := url.Parse(resp.CrtURI)
	if err != nil {
		return names, err
	}

	if certURI.Scheme != "file" {
		return names, errors.New("Expect to have file for online cert")
	}

	certRaw, err := ioutil.ReadFile(certURI.Path)
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
