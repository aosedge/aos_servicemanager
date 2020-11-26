package fcrypt

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

import (
	"bufio"
	"crypto"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"io/ioutil"
	"net"
	"testing"
	"time"

	"github.com/google/go-tpm-tools/simulator"
	log "github.com/sirupsen/logrus"
)

const (
	caCert = `-----BEGIN CERTIFICATE-----
MIICxjCCAa6gAwIBAgIJANWuMWMQSxvdMA0GCSqGSIb3DQEBBQUAMBMxETAPBgNV
BAMTCE15VGVzdENBMB4XDTE0MDEyNzE5NTIyMloXDTI0MDEyNTE5NTIyMlowEzER
MA8GA1UEAxMITXlUZXN0Q0EwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIB
AQDBsIrkW4ob9Z/gzR2/Maa2stbutry6/vvz8eiJwIKIbaHGwqtFOUGiWeKw7H76
IH3SjTAhNQY2hoKPyH41D36sDJkYBRyHFJTK/6ffvOhpyLnuXJAnoS62eKPSNUAx
5i/lkHj42ESutYAH9qbHCI/gBm9G4WmhGAyA16xzC1n07JObl6KFoY1PqHKl823z
mvF47I24DzemEfjdwC9nAAX/pGYOg9FA9nQv7NnhlsJMxueCx55RNU1ADRoqsbfE
T0CQTOT4ryugGrUp9J4Cwen6YbXZrS6+Kff5SQCAns0Qu8/bwj0DKkuBGLF+Mnwe
mq9bMzyZPUrPM3Gu48ao8YAfAgMBAAGjHTAbMAwGA1UdEwQFMAMBAf8wCwYDVR0P
BAQDAgEGMA0GCSqGSIb3DQEBBQUAA4IBAQCBwXGblRxIEOlEP6ANZ1C8AHWyG8lR
CQduFclc0tmyCCz5fnyLK0aGu9LhXXe6/HSKqgs4mJqeqYOojdjkfOme/YdwDzjK
WIf0kRYQHcB6NeyEZwW8C7subTP1Xw6zbAmjvQrtCGvRM+fi3/cs1sSSkd/EoRk4
7GM9qQl/JIIoCOGncninf2NQm5YSpbit6/mOQD7EhqXsw+bX+IRh3DHC1Apv/PoA
HlDNeM4vjWaBxsmvRSndrIvew1czboFM18oRSSIqAkU7dKZ0SbC11grzmNxMG2aD
f9y8FIG6RK/SEaOZuc+uBGXx7tj7dczpE/2puqYcaVGwcv4kkrC/ZuRm
-----END CERTIFICATE-----
`

	serverCert = `-----BEGIN CERTIFICATE-----
MIIC8zCCAdugAwIBAgIBATANBgkqhkiG9w0BAQUFADATMREwDwYDVQQDEwhNeVRl
c3RDQTAeFw0xNDAxMjcxOTUyMjNaFw0yNDAxMjUxOTUyMjNaMCUxEjAQBgNVBAMT
CTEyNy4wLjAuMTEPMA0GA1UEChMGc2VydmVyMIIBIjANBgkqhkiG9w0BAQEFAAOC
AQ8AMIIBCgKCAQEAxYAKbeGyg0gP0xwVsZsufzk/SUCtD44Gp3lQYQ9QumQ1IVZu
PmZWwPWrzI93a1Abruz6ZhXaB3jcL5QPAy1N44IiFgVN45CZXBsqkpJe/abzRFOV
DRnHxattPDHdgwML5d3nURKGUM/7+ACj5E4pZEDlM3RIjIKVd+doJsL7n6myO8FE
tIpt4vTz1MFp3F+ntPnHU3BZ/VZ1UjSlFWnCjT0CR0tnXsPmlIaC98HThS8x5zNB
fvvSN+Zln8RWdNLnEVHVdqYtOQ828QbCx8s1HfClGgaVoSDrzz+qQgtZFO4wW264
2CWkNd8DSJUJ/HlPNXmbXsrRMgvGaL7YUz2yRQIDAQABo0AwPjAJBgNVHRMEAjAA
MAsGA1UdDwQEAwIFIDATBgNVHSUEDDAKBggrBgEFBQcDATAPBgNVHREECDAGhwR/
AAABMA0GCSqGSIb3DQEBBQUAA4IBAQAE2g+wAFf9Xg5svcnb7+mfseYV16k9l5WG
onrmR3FLsbTxfbr4PZJMHrswPbi2NRk0+ETPUpcv1RP7pUB7wSEvuS1NPGcU92iP
58ycP3dYtLzmuu6BkgToZqwsCU8fC2zM0wt3+ifzPpDMffWWOioVuA3zdM9WPQYz
+Ofajd0XaZwFZS8uTI5WXgObz7Xqfmln4tF3Sq1CTyuJ44qK4p83XOKFq+L04aD0
d0c8w3YQNUENny/vMP9mDu3FQ3SnDz2GKl1LSjGe2TUnkoMkDfdk4wSzndTz/ecb
QiCPKijwVPWNOWV3NDE2edMxDPxDoKoEm5F4UGfGjxSRnYCIoZLh
-----END CERTIFICATE-----
`

	serverKey = `-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAxYAKbeGyg0gP0xwVsZsufzk/SUCtD44Gp3lQYQ9QumQ1IVZu
PmZWwPWrzI93a1Abruz6ZhXaB3jcL5QPAy1N44IiFgVN45CZXBsqkpJe/abzRFOV
DRnHxattPDHdgwML5d3nURKGUM/7+ACj5E4pZEDlM3RIjIKVd+doJsL7n6myO8FE
tIpt4vTz1MFp3F+ntPnHU3BZ/VZ1UjSlFWnCjT0CR0tnXsPmlIaC98HThS8x5zNB
fvvSN+Zln8RWdNLnEVHVdqYtOQ828QbCx8s1HfClGgaVoSDrzz+qQgtZFO4wW264
2CWkNd8DSJUJ/HlPNXmbXsrRMgvGaL7YUz2yRQIDAQABAoIBAGsyEvcPAGg3DbfE
z5WFp9gPx2TIAOanbL8rnlAAEw4H47qDgfTGcSHsdeHioKuTYGMyZrpP8/YISGJe
l0NfLJ5mfH+9Q0hXrJWMfS/u2DYOjo0wXH8u1fpZEEISwqsgVS3fonSjfFmSea1j
E5GQRvEONBkYbWQuYFgjNqmLPS2r5lKbWCQvc1MB/vvVBwOTiO0ON7m/EkM5RKt9
cDT5ZhhVjBpdmd9HpVbKTdBj8Q0l5/ZHZUEgZA6FDZEwYxTd9l87Z4YT+5SR0z9t
k8/Z0CHd3x3Rv891t7m66ZJkaOda8NC65/432MQEQwJltmrKnc22dS8yI26rrmpp
g3tcbSUCgYEA5nMXdQKS4vF+Kp10l/HqvGz2sU8qQaWYZQIg7Th3QJPo6N52po/s
nn3UF0P5mT1laeZ5ZQJKx4gnmuPnIZ2ZtJQDyFhIbRPcZ+2hSNSuLYVcrumOC3EP
3OZyFtFE1THO73aFe5e1jEdtoOne3Bds/Hq6NF45fkVdL+M9e8pfXIsCgYEA22W8
zGjbWyrFOYvKknMQVtHnMx8BJEtsvWRknP6CWAv/8WyeZpE128Pve1m441AQnopS
CuOF5wFK0iUXBFbS3Pe1/1j3em6yfVznuUHqJ7Qc+dNzxVvkTK8jGB6x+vm+M9Hg
muHUM726IUxckoSNXbPNAVPIZab1NdSxam7F9m8CgYEAx55QZmIJXJ41XLKxqWC7
peZ5NpPNlbncrTpPzUzJN94ntXfmrVckbxGt401VayEctMQYyZ9XqUlOjUP3FU5Q
M3S3Zhba/eljVX8o406fZf0MkNLs4QpZ5E6V6x/xEP+pMhKng6yhbVb+JpIPIvUD
yhyBKRWplbB+DRo5Sv685gsCgYA7l5m9h+m1DJv/cnn2Z2yTuHXtC8namuYRV1iA
0ByFX9UINXGc+GpBpCnDPm6ax5+MAJQiQwSW52H0TIDA+/hQbrQvhHHL/o9av8Zt
Kns4h5KrRQUYIUqUjamhnozHV9iS6LnyN87Usv8AlmY6oehoADN53dD702qdUYVT
HH2G3wKBgCdvqyw78FR/n8cUWesTPnxx5HCeWJ1J+2BESnUnPmKZ71CV1H7uweja
vPUxuuuGLKfNx84OKCfRDbtOgMOeyh9T1RmXry6Srz/7/udjlF0qmFiRXfBNAgoR
tNb0+Ri/vY0AHrQ7UnCbl12qPVaqhEXLr+kCGNEPFqpMJPPEeMK0
-----END RSA PRIVATE KEY-----`

	clientCert = `-----BEGIN CERTIFICATE-----
MIIC4jCCAcqgAwIBAgIBAjANBgkqhkiG9w0BAQUFADATMREwDwYDVQQDEwhNeVRl
c3RDQTAeFw0xNDAxMjcxOTUyMjNaFw0yNDAxMjUxOTUyMjNaMCUxEjAQBgNVBAMT
CTEyNy4wLjAuMTEPMA0GA1UEChMGY2xpZW50MIIBIjANBgkqhkiG9w0BAQEFAAOC
AQ8AMIIBCgKCAQEAu7LMqd+agoH168Bsi0WJ36ulYqDypq+GZPF7uWOo2pE0raKH
B++31/hjnkt6yC5kLKVZZ0EfolBa9q4Cy6swfGaEMafy44ZCRneLnt1azL1N6Kfz
+U0KsOqyQDoMxYJG1gVTEZN19/U/ew2eazcxKyERI3oGCQ4SbpkxBTbfxtAFk49e
xIB3obsuMVUrmtXE4FkUkvG7NgpPUgrhp0yxYpj9zruZGzGGT1zNhcarbQ/4i7It
ZMbnv6pqQWtYDgnGX2TDRcEiXGeO+KrzhfpTRLfO3K4np8e8cmTyXM+4lMlWUgma
KrRdu1QXozGqRs47u2prGKGdSQWITpqNVCY8fQIDAQABoy8wLTAJBgNVHRMEAjAA
MAsGA1UdDwQEAwIHgDATBgNVHSUEDDAKBggrBgEFBQcDAjANBgkqhkiG9w0BAQUF
AAOCAQEAhCuBCLznPc4O96hT3P8Fx19L3ltrWbc/pWrx8JjxUaGk8kNmjMjY+/Mt
JBbjUBx2kJwaY0EHMAfw7D1f1wcCeNycx/0dyb0E6xzhmPw5fY15GGNg8rzWwqSY
+i/1iqU0IRkmRHV7XCF+trd2H0Ec+V1Fd/61E2ccJfOL5aSAyWbMCUtWxS3QMnqH
FBfKdVEiY9WNht5hnvsXQBRaNhowJ6Cwa7/1/LZjmhcXiJ0xrc1Hggj3cvS+4vll
Ew+20a0tPKjD/v/2oSQL+qkeYKV4fhCGkaBHCpPlSJrqorb7B6NmPy3nS26ETKE/
o2UCfZc5g2MU1ENa31kT1iuhKZapsA==
-----END CERTIFICATE-----
`

	clientKey = `-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAu7LMqd+agoH168Bsi0WJ36ulYqDypq+GZPF7uWOo2pE0raKH
B++31/hjnkt6yC5kLKVZZ0EfolBa9q4Cy6swfGaEMafy44ZCRneLnt1azL1N6Kfz
+U0KsOqyQDoMxYJG1gVTEZN19/U/ew2eazcxKyERI3oGCQ4SbpkxBTbfxtAFk49e
xIB3obsuMVUrmtXE4FkUkvG7NgpPUgrhp0yxYpj9zruZGzGGT1zNhcarbQ/4i7It
ZMbnv6pqQWtYDgnGX2TDRcEiXGeO+KrzhfpTRLfO3K4np8e8cmTyXM+4lMlWUgma
KrRdu1QXozGqRs47u2prGKGdSQWITpqNVCY8fQIDAQABAoIBAGSEn3hFyEAmCyYi
2b5IEksXaC2GlgxQKb/7Vs/0oCPU6YonZPsKFMFzQx4tu+ZiecEzF8rlJGTPdbdv
fw3FcuTcHeVd1QSmDO4h7UK5tnu40XVMJKsY6CXQun8M13QajYbmORNLjjypOULU
C0fNueYoAj6mhX7p61MRdSAev/5+0+bVQQG/tSVDQzdngvKpaCunOphiB2VW2Aa0
7aYPOFCoPB2uo0DwUmBB0yfx9x4hXX9ovQI0YFou7bq6iYJ0vlZBvYQ9YrVdxjKL
avcz1N5xM3WFAkZJSVT/Ho5+uTbZx4RrJ8b5T+t2spOKmXyAjwS2rL/XMAh8YRZ1
u44duoECgYEA4jpK2qshgQ0t49rjVHEDKX5x7ElEZefl0rHZ/2X/uHUDKpKj2fTq
3TQzHquiQ4Aof7OEB9UE3DGrtpvo/j/PYxL5Luu5VR4AIEJm+CA8GYuE96+uIL0Z
M2r3Lux6Bp30Z47Eit2KiY4fhrWs59WB3NHHoFxgzHSVbnuA02gcX2ECgYEA1GZw
iXIVYaK07ED+q/0ObyS5hD1cMhJ7ifSN9BxuG0qUpSigbkTGj09fUDS4Fqsz9dvz
F0P93fZvyia242TIfDUwJEsDQCgHk7SGa4Rx/p/3x/obIEERk7K76Hdg93U5NXhV
NvczvgL0HYxnb+qtumwMgGPzncB4lGcTnRyOfp0CgYBTIsDnYwRI/KLknUf1fCKB
WSpcfwBXwsS+jQVjygQTsUyclI8KResZp1kx6DkVPT+kzj+y8SF8GfTUgq844BJC
gnJ4P8A3+3JoaH6WqKHtcUxICZOgDF36e1CjOdwOGnX6qIipz4hdzJDhXFpSSDAV
CjKmR8x61k0j8NcC2buzgQKBgFr7eo9VwBTvpoJhIPY5UvqHB7S+uAR26FZi3H/J
wdyM6PmKWpaBfXCb9l8cBhMnyP0y94FqzY9L5fz48nSbkkmqWvHg9AaCXySFOuNJ
e68vhOszlnUNimLzOAzPPkkh/JyL7Cy8XXyyNTGHGDPXmg12BTDmH8/eR4iCUuOE
/QD9AoGBALQ/SkvfO3D5+k9e/aTHRuMJ0+PWdLUMTZ39oJQxUx+qj7/xpjDvWTBn
eDmF/wjnIAg+020oXyBYo6plEZfDz3EYJQZ+3kLLEU+O/A7VxCakPYPwCr7N/InL
Ccg/TVSIXxw/6uJnojoAjMIEU45NoP6RMp0mWYYb2OlteEv08Ovp
-----END RSA PRIVATE KEY-----`

	serverURL = "127.0.0.1:4444"
)

func parseRsaPrivateKeyFromPemStr(privPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privPEM))
	if block == nil {
		return nil, errors.New("failed to parse PEM block containing the key")
	}

	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	return priv, nil
}

func parseRsaPrivateKeyFromFile(file string) (*rsa.PrivateKey, error) {
	data, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}

	return parseRsaPrivateKeyFromPemStr(string(data))
}

func createTLSServer(url string, config *tls.Config) error {
	listener, err := net.Listen("tcp", url)
	if err != nil {
		return err
	}

	tlsListener := tls.NewListener(listener, config)

	time.Sleep(time.Second)

	go func() {
		defer tlsListener.Close()
		for {
			conn, err := tlsListener.Accept()
			if err != nil {
				continue
			}
			go func(conn net.Conn) {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					msg, err := r.ReadString('\n')
					if err != nil {
						return
					}
					_, err = conn.Write([]byte(msg))
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	return nil
}

func tlsClientConfig() (*tls.Config, error) {
	var cert tls.Certificate
	cfg := &tls.Config{}

	cfg.RootCAs = x509.NewCertPool()
	cfg.RootCAs.AppendCertsFromPEM([]byte(caCert))

	certPEMBlock := []byte(clientCert)
	for {
		var certDERBlock *pem.Block
		certDERBlock, certPEMBlock = pem.Decode(certPEMBlock)
		if certDERBlock == nil {
			break
		}
		if certDERBlock.Type == "CERTIFICATE" {
			cert.Certificate = append(cert.Certificate, certDERBlock.Bytes)
		}
	}

	if len(cert.Certificate) == 0 {
		return cfg, errors.New("Client certificate does not exist")
	}

	TPMDev, err := simulator.Get()
	if err != nil {
		return cfg, err
	}
	defer TPMDev.Close()

	privateKey, err := parseRsaPrivateKeyFromPemStr(clientKey)
	if err != nil {
		return cfg, err
	}

	tpmCrypt := &TPMCrypto{dev: TPMDev}

	handle, err := tpmCrypt.ExternalLoadRSAPrivateKey(privateKey)
	if err != nil {
		return cfg, err
	}

	tpmPrivateKey := &tpmPrivateKeyRSA{crypt: tpmCrypt, handle: handle, hashAlg: crypto.SHA256}

	err = tpmPrivateKey.Create()
	if err != nil {
		return cfg, err
	}

	cert.PrivateKey = tpmPrivateKey

	// Very important option because TPM module supports only SHA256 and SHA1 as possible hash algorithm
	// We will pass this option into TLS stack to produce correct interruction with certVerify option
	cert.SupportedSignatureAlgorithms = []tls.SignatureScheme{
		tls.PKCS1WithSHA256,
		tls.PKCS1WithSHA1,
	}

	cfg.Certificates = []tls.Certificate{cert}
	cfg.InsecureSkipVerify = true

	return cfg, nil
}

func tlsServerConfig() (*tls.Config, error) {
	cfg := &tls.Config{}

	cfg.ClientCAs = x509.NewCertPool()
	cfg.ClientCAs.AppendCertsFromPEM([]byte(caCert))

	cert, err := tls.X509KeyPair([]byte(serverCert), []byte(serverKey))
	if err != nil {
		return cfg, err
	}

	cfg.MinVersion = tls.VersionTLS12
	cfg.MaxVersion = tls.VersionTLS12
	cfg.PreferServerCipherSuites = true

	cfg.Certificates = append(cfg.Certificates, cert)
	cfg.ClientAuth = tls.RequireAndVerifyClientCert

	return cfg, nil
}

func TestTpmTLSHandshake(t *testing.T) {
	ServerConfig, err := tlsServerConfig()
	if err != nil {
		t.Fatalf("Can't create a new TLS config for local server: %s", err)
	}

	err = createTLSServer(serverURL, ServerConfig)
	if err != nil {
		t.Fatalf("Can't create secure server: %s", err)
	}

	ClientConfig, err := tlsClientConfig()
	if err != nil {
		t.Fatalf("Can't create a new TLS config for client: %s", err)
	}

	client, err := tls.Dial("tcp", serverURL, ClientConfig)
	if err != nil {
		t.Fatalf("expected to open a TLS connection, got err: %s", err)
	}
	defer client.Close()

	if !client.ConnectionState().HandshakeComplete {
		t.Errorf("Error TLS handshake")
	}

	var message = "Test message\n"

	_, err = io.WriteString(client, message)
	if err != nil {
		log.Fatalf("Can't write to server socket: %s", err)
	}

	buff := make([]byte, len(message))
	n, err := client.Read(buff)
	if err != nil {
		t.Errorf("Can't read data: %s", err)
	}

	if string(buff[:n]) != message {
		t.Error("Received data mismatch")
	}
}
