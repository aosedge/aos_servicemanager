// Provides cryptographic interfaces for Fusion
package fcrypt

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"io/ioutil"
	"log"
	"os"
)

type Configuration struct {
	CACert         string
	ClientCert     string
	ClientKey      string
	OfflinePrivKey string
	OfflineCert    string
}

var config = Configuration{}

func init() {
	file, err := os.Open("fcrypt.json")
	if err != nil {
		log.Fatal("Error while opening fcrypt configurataion file: ", err)
	}

	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		log.Fatal("Erro while parsing fcrypt.json: ", err)
	}

	log.Println("CAcert:         ", config.CACert)
	log.Println("ClientCert:     ", config.ClientCert)
	log.Println("ClientKey:      ", config.ClientKey)
	log.Println("OfflinePrivKey: ", config.OfflinePrivKey)
	log.Println("OfflineCert:    ", config.OfflineCert)
}

func verify_cert(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	return nil
}

func getCaCertPool() (*x509.CertPool, error) {
	// Load CA cert
	caCert, err := ioutil.ReadFile(config.CACert)
	if err != nil {
		log.Println("Error reading CA certificate", err)
		return nil, err
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)
	return caCertPool, nil
}

// Provides TLS configuration which can be used with HTTPS client
func GetTlsConfig() (*tls.Config, error) {
	// Load client cert
	cert, err := tls.LoadX509KeyPair(config.ClientCert, config.ClientKey)
	if err != nil {
		log.Fatal(err)
	}

	caCertPool, err := getCaCertPool()
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		Certificates:          []tls.Certificate{cert},
		RootCAs:               caCertPool,
		VerifyPeerCertificate: verify_cert,
	}

	tlsConfig.BuildNameToCertificate()
	return tlsConfig, nil
}

func getPrivKey() (*rsa.PrivateKey, error) {
	keydata, err := ioutil.ReadFile(config.OfflinePrivKey)
	if err != nil {
		log.Println("Error reading private key:", err)
		return nil, err
	}
	return x509.ParsePKCS1PrivateKey(keydata)
}

func getOfflineCert() (*x509.Certificate, error) {
	pemCert, err := ioutil.ReadFile(config.OfflineCert)
	if err != nil {
		log.Println("Error reading offlinie certificate", err)
		return nil, err
	}

	var block *pem.Block
	block, pemCert = pem.Decode(pemCert)
	if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
		return nil, errors.New("Invalid PEM Block")
	}

	return x509.ParseCertificate(block.Bytes)
}

func extractKeyFromCert(cert *x509.Certificate) (*rsa.PublicKey, error) {
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		return pub, nil
	default:
		return nil, errors.New("Unknown public key type")
	}

}

func decrypt(fin *os.File, fout *os.File, key []byte, iv []byte,
	cryptoAlgMode string) error {

	log.Printf("IV: %v", iv)
	log.Printf("Key: %v", key)
	var mode cipher.BlockMode
	switch cryptoAlgMode {
	case "CBC":
		block, err := aes.NewCipher(key)
		if err != nil {
			log.Println("Can't create cipher: ", err)
			return err
		}
		mode = cipher.NewCBCDecrypter(block, iv)
	default:
		return errors.New("Unknown encryption mode or alg")
	}

	indata, err := ioutil.ReadAll(fin)
	if err != nil {
		log.Println("Can't read image file: ", err)
		return err
	}

	outdata := make([]byte, len(indata))
	mode.CryptBlocks(outdata, indata)
	outdata, err = removePkcs7Padding(outdata, mode.BlockSize())
	if err != nil {
		return err
	}

	fout.Write(outdata)
	return nil
}

func parseCertificates(pem_data string) (ret []*x509.Certificate, err error) {

	for block, remainder := pem.Decode([]byte(pem_data)); block != nil; block, remainder = pem.Decode(remainder) {
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("Invalid PEM Block")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			log.Println("Error parsing certifiate: ", err)
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
		return nil, errors.New("No certificates found in certificate chain")
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
		log.Println("Error verifying chain\n")
		// TODO: Check certificate chain
		//		return
	}

	return signCertificate, nil
}

func checkSign(f *os.File, signatureAlg string, signature []byte, certificates string) error {

	signCert, err := getAndVerifySignCert(certificates)
	if err != nil {
		return err
	}
	f.Seek(0, 0)
	switch signatureAlg {
	case "RSA-PKCS1_5-SHA1":
		hash := sha1.New()
		_, err := io.Copy(hash, f)
		if err != nil {
			log.Println("Error hashing file: ", err)
			return err
		}

		key, err := extractKeyFromCert(signCert)
		if err != nil {
			return err
		}

		h := hash.Sum(nil)
		log.Println("Hash: ", hex.EncodeToString(h))
		return rsa.VerifyPKCS1v15(key,
			crypto.SHA1, h, signature)
	case "SHA256":
		hash := sha256.New()
		_, err := io.Copy(hash, f)
		if err != nil {
			log.Println("Error hashing file: ", err)
			return err
		}

		key, err := extractKeyFromCert(signCert)
		if err != nil {
			return err
		}

		h := hash.Sum(nil)
		log.Println("Hash: ", hex.EncodeToString(h))
		return rsa.VerifyPKCS1v15(key,
			crypto.SHA256, h, signature)
	default:
		return errors.New("Unknown signature alg: " + signatureAlg)
	}

	return nil
}

func removePkcs7Padding(in []byte, blocklen int) ([]byte, error) {
	l := len(in)
	if l%blocklen != 0 {
		return nil, errors.New("Padding error")
	}

	pl := int(in[l-1])
	if pl < 1 || pl > blocklen {
		return nil, errors.New("Padding error")
	}

	for i := l - pl; i < l; i++ {
		if in[i] != byte(pl) {
			return nil, errors.New("Padding error")
		}
	}

	return in[0 : l-pl], nil
}

func DecryptMetadata(der []byte) ([]byte, error) {
	cert, err := getOfflineCert()
	if err != nil {
		return nil, err
	}

	key, err := getPrivKey()
	if err != nil {
		return nil, err
	}

	return DecryptMessage(der, key, cert)
}

// Decrypts given image into temprorary file
func DecryptImage(fname string, signature []byte, key []byte, iv []byte,
	signatureAlg string, cryptoAlgMode string,
	certificates string) (outfname string, err error) {

	// Create tmp file with output data
	fout, err := ioutil.TempFile("", "fcrypt")
	if err != nil {
		log.Println("Can't create temp file for image:", err)
		return outfname, err
	}
	defer fout.Close()
	log.Println("Temp file name: ", fout.Name())

	// Open file
	f, err := os.Open(fname)
	if err != nil {
		log.Println("Error openinng encrypted image:", err)
		return outfname, err
	}
	defer f.Close()

	// Decrypt file into fout
	err = decrypt(f, fout, key, iv, cryptoAlgMode)
	if err != nil {
		log.Println("Error decrypting file:", err)
		return outfname, err
	}

	err = checkSign(fout, signatureAlg, signature, certificates)
	if err != nil {
		log.Println("Signature verify error:", err)
		return outfname, err
	}
	return fout.Name(), nil
}
