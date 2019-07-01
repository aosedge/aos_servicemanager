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
	"github.com/cloudflare/cfssl/log"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/config"
	"hash"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
)

const (
	fileBlockSize = 64 * 1024
)

type CryptoSessionKeyInfo struct {
	sessionKey        []byte `json:"sessionKey"`
	sessionIV         []byte `json:"sessionIV"`
	symmetricAlgName  string `json:"symmetricAlgName"`
	asymmetricAlgName string `json:"asymmetricAlgName"`
	recipientInfo     string
}

type CryptoContext struct {
	cryptConfig  config.Crypt
	rootCertPool *x509.CertPool

	privateKey crypto.PrivateKey
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

func CreateContext(conf config.Crypt) (ctx *CryptoContext, err error) {
	// Create context
	tmpCtx := &CryptoContext{
		cryptConfig: conf,
	}

	if conf.CACert != "" {
		// Load CA cert
		rootCAPEM, err := ioutil.ReadFile(conf.CACert)
		if err != nil {
			log.Errorf("error reading CA certificate (%s): %s", conf.CACert, err)
			return nil, err
		}

		tmpCtx.rootCertPool = x509.NewCertPool()
		if !tmpCtx.rootCertPool.AppendCertsFromPEM(rootCAPEM) {
			err = errors.New("invalid or empty CA file")
			log.Errorf("error appending CA certificate (%s): %s", conf.CACert, err)
			return nil, err
		}
	}

	ctx = tmpCtx

	return
}

func (ctx *CryptoContext) LoadOfflineKey() error {
	if ctx.cryptConfig.OfflinePrivKey == "" {
		return errors.New("OfflinePrivKey not set")
	}

	keyBytes, err := ioutil.ReadFile(ctx.cryptConfig.OfflinePrivKey)
	if err != nil {
		return errors.New(fmt.Sprintf("error reading offline private key from file: %s", err))
	}

	return ctx.LoadKeyFromBytes(keyBytes)
}

func (ctx *CryptoContext) LoadKeyFromBytes(data []byte) error {
	var err error
	ctx.privateKey, err = loadKey(data)
	return err
}

func (ctx *CryptoContext) LoadOnlineKey() error {
	if ctx.cryptConfig.ClientKey == "" {
		return errors.New("OfflinePrivKey not set")
	}

	keyBytes, err := ioutil.ReadFile(ctx.cryptConfig.OfflinePrivKey)
	if err != nil {
		return errors.New(fmt.Sprintf("error reading online private key from file: %s", err))
	}

	ctx.privateKey, err = loadKey(keyBytes)
	return err
}

func (ctx *CryptoContext) ImportSessionKey(keyInfo CryptoSessionKeyInfo) (*SymmetricCipherContext, error) {

	if ctx == nil || ctx.privateKey == nil {
		return nil, errors.New("asymmetric context not initialized")
	}

	var err error

	algName, _, _ := decodeAlgNames(keyInfo.symmetricAlgName)
	keySize, ivSize, err := getSymmetricAlgInfo(algName)
	if err != nil {
		return nil, err
	}

	if ivSize != len(keyInfo.sessionIV) {
		return nil, errors.New("invalid IV length")
	}

	ctxSym := CreateSymmetricCipherContext()

	switch strings.ToUpper(keyInfo.asymmetricAlgName) {
	case "RSA/PKCS1V1_5":
		clearKey := make([]byte, keySize)
		err = rsa.DecryptPKCS1v15SessionKey(nil, ctx.privateKey.(*rsa.PrivateKey), keyInfo.sessionKey, clearKey)
		if err != nil {
			return nil, err
		}
		if err = ctxSym.set(keyInfo.symmetricAlgName, clearKey, keyInfo.sessionIV); err != nil {
			return nil, err
		}

	case "RSA/OAEP-256", "RSA/OAEP-512", "RSA/OAEP":
		var hashFunc hash.Hash
		switch strings.ToUpper(keyInfo.asymmetricAlgName) {
		case "RSA/OAEP":
			hashFunc = sha1.New()
		case "RSA/OAEP-256":
			hashFunc = sha256.New()
		case "RSA/OAEP-512":
			hashFunc = sha512.New()
		}
		clearKey, err := rsa.DecryptOAEP(hashFunc, nil, ctx.privateKey.(*rsa.PrivateKey), keyInfo.sessionKey, nil)
		if err != nil {
			return nil, err
		}
		if err = ctxSym.set(keyInfo.symmetricAlgName, clearKey, keyInfo.sessionIV); err != nil {
			return nil, err
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

	return
}

func (ctx *SymmetricCipherContext) DecryptFile(encryptedFile, clearFile *os.File) (err error) {
	if !ctx.IsReady() {
		return errors.New("symmetric key is not ready")
	}
	// Get file stat (we need to know file size)
	inputFileStat, err := encryptedFile.Stat()
	if err != nil {
		return
	}

	fileSize := inputFileStat.Size()
	if fileSize%int64(ctx.decrypter.BlockSize()) != 0 {
		err = errors.New("file size is incorrect")
		return
	}

	if _, err = encryptedFile.Seek(0, io.SeekStart); err != nil {
		return
	}

	chunkEncrypted := make([]byte, fileBlockSize)
	chunkDecrypted := make([]byte, fileBlockSize)
	totalReadSize := int64(0)
	for totalReadSize < fileSize {
		readSize, errRead := encryptedFile.Read(chunkEncrypted)
		if errRead != nil && err != io.EOF {
			err = errRead
			return
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
			return
		}
	}

	return
}

func (ctx *SymmetricCipherContext) EncryptFile(clearFile, encryptedFile *os.File) (err error) {
	if !ctx.IsReady() {
		return errors.New("symmetric key is not ready")
	}

	// Get file stat (we need to know file size)
	inputFileStat, err := clearFile.Stat()
	if err != nil {
		return
	}
	fileSize := inputFileStat.Size()
	writtenSize := int64(0)
	readSize := 0
	if _, err = clearFile.Seek(0, io.SeekStart); err != nil {
		return
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
				return
			}
			readSize, err = ctx.appendPadding(chunkClear, currentChunkSize)
			if err != nil {
				return
			}
		} else {
			if readSize, err = clearFile.Read(chunkClear[:fileBlockSize]); err != nil {
				return
			}
		}
		ctx.encrypter.CryptBlocks(chunkEncrypted, chunkClear[:readSize])
		if _, err = encryptedFile.Write(chunkEncrypted[:readSize]); err != nil {
			return
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
			return
		}
		if keySizeBits/8 != len(ctx.key) {
			return errors.New("invalid symmetric key size")
		}
		block, err = aes.NewCipher(ctx.key)
		if err != nil {
			log.Errorf("can't create cipher: %s", err)
			return
		}
		break
	default:
		err = errors.New("unsupported cryptographic algorithm: " + ctx.algName)
		return
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
		return
	}

	return
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
	case "PKCS7PADDING":
		return ctx.appendPkcs7Padding(dataIn, dataLen)
	default:
		return 0, errors.New("unsupported padding type")
	}
}

func (ctx *SymmetricCipherContext) getPaddingSize(dataIn []byte, dataLen int) (removedSize int, err error) {
	switch strings.ToUpper(ctx.paddingName) {
	case "PKCS7PADDING":
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
		return
	}

	for i := dataLen; i < fullSize; i++ {
		dataIn[i] = byte(appendSize)
	}
	return
}

func (ctx *SymmetricCipherContext) removePkcs7Padding(dataIn []byte, dataLen int) (removedSize int, err error) {
	blockLen := ctx.decrypter.BlockSize()
	if dataLen%blockLen != 0 || dataLen == 0 {
		err = errors.New("padding error (invalid total size)")
		return
	}

	removedSize = int(dataIn[dataLen-1])
	if removedSize < 1 || removedSize > blockLen || removedSize > dataLen {
		err = errors.New("padding error")
		return
	}

	for i := dataLen - removedSize; i < dataLen; i++ {
		if dataIn[i] != byte(removedSize) {
			err = errors.New("padding error")
			return
		}
	}
	return
}
