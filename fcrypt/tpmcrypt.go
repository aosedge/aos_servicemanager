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
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpmutil"
	log "github.com/sirupsen/logrus"

	"aos_servicemanager/config"
)

/*******************************************************************************
 * Variables
 ******************************************************************************/

// Map a crypto.Hash algorithm to a tpm2 constant
var tpmSupportHashFunc = map[crypto.Hash]struct {
	hashAlg   tpm2.Algorithm
	digestLen int
}{
	crypto.SHA1:   {tpm2.AlgSHA1, crypto.SHA1.Size()},
	crypto.SHA256: {tpm2.AlgSHA256, crypto.SHA256.Size()},
}

/*******************************************************************************
 * Types
 ******************************************************************************/

// TPMHash based on crypto.Hash
type tpmHash crypto.Hash

// tpmPrivateKeyRSA provides a TPM crypto interface
type tpmPrivateKeyRSA struct {
	handle   config.TPMHandle
	pub      crypto.PublicKey
	crypt    *TPMCrypto
	password string
	hashAlg  crypto.Hash
}

// TPMCrypto is a basic type TPM crypto struct
type TPMCrypto struct {
	sync.Mutex
	dev io.ReadWriteCloser
}

// tpmCryptoAccessor describes TPM crypto interface
type tpmCryptoAccessor interface {
	Open(path string) (err error)
	CreateRSAPrimaryKey(handle config.TPMHandle, parentPW, ownerPW string, attr tpm2.KeyProp) (crypto.PublicKey, error)
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// Open function create IO socket to direct communication with TPM
func (tpm *TPMCrypto) Open(path string) (err error) {
	tpm.Lock()
	defer tpm.Unlock()

	if tpm.dev != nil {
		log.Warnf("TPM interface %s already opened", path)
		return nil
	}

	tpm.dev, err = tpm2.OpenTPM(path)

	return err
}

// CreateRSAPrimaryKey generates a primary RSA key and makes it persistent under the given handle.
func (tpm *TPMCrypto) CreateRSAPrimaryKey(handle config.TPMHandle, parentPW, ownerPW string, attr tpm2.KeyProp) (pubKey crypto.PublicKey, err error) {
	tpm.Lock()
	defer tpm.Unlock()

	// Define the TPM key template
	pub := tpm2.Public{
		Type:       tpm2.AlgRSA,
		NameAlg:    tpm2.AlgSHA256,
		Attributes: attr,
		RSAParameters: &tpm2.RSAParams{
			Sign: &tpm2.SigScheme{
				Alg:  tpm2.AlgNull,
				Hash: tpm2.AlgNull,
			},
			KeyBits: uint16(2048),
		},
	}

	pcrSelection := tpm2.PCRSelection{}
	signerHandle, pubKey, err := tpm2.CreatePrimary(tpm.dev, tpm2.HandleOwner, pcrSelection, parentPW, ownerPW, pub)
	if err != nil {
		return nil, err
	}
	defer tpm2.FlushContext(tpm.dev, signerHandle)

	return pubKey, tpm2.EvictControl(tpm.dev, ownerPW, tpm2.HandleOwner, signerHandle, tpmutil.Handle(handle))
}

// ExternalLoadRSAPrivateKey uses only for testing handshake with TPM
func (tpm *TPMCrypto) ExternalLoadRSAPrivateKey(private *rsa.PrivateKey) (config.TPMHandle, error) {
	tpm.Lock()
	defer tpm.Unlock()

	public := tpm2.Public{
		Type:       tpm2.AlgRSA,
		NameAlg:    tpm2.AlgSHA1,
		Attributes: tpm2.FlagDecrypt | tpm2.FlagSign | tpm2.FlagUserWithAuth,
		RSAParameters: &tpm2.RSAParams{
			KeyBits:     uint16(private.Size() * 8), // modulus size of bits
			ExponentRaw: uint32(private.PublicKey.E),
			ModulusRaw:  private.PublicKey.N.Bytes(),
		},
	}

	tpmPrivate := tpm2.Private{
		Type:      tpm2.AlgRSA,
		Sensitive: private.Primes[0].Bytes(),
	}

	h, _, err := tpm2.LoadExternal(tpm.dev, public, tpmPrivate, tpm2.HandleNull)
	if err != nil {
		return 0, err
	}

	return config.TPMHandle(h), nil
}

// Public returns the public part of the key
func (k *tpmPrivateKeyRSA) Public() crypto.PublicKey {
	k.crypt.Lock()
	defer k.crypt.Unlock()

	return k.pub
}

// Create function init a crypto.PrivateKey with a public part of key from TPM
func (k *tpmPrivateKeyRSA) Create() error {
	k.crypt.Lock()
	defer k.crypt.Unlock()

	pub, publicKey, err := readPublicKey(k.crypt.dev, tpmutil.Handle(k.handle))
	if err != nil {
		return err
	}

	if pub.Type != tpm2.AlgRSA {
		return fmt.Errorf("unsupported algorithm %T", publicKey)
	}

	k.pub = publicKey

	return nil
}

// Decrypt function is the part of private key interface
func (k *tpmPrivateKeyRSA) Decrypt(rand io.Reader, ciphertext []byte, opts crypto.DecrypterOpts) (plaintext []byte, err error) {
	k.crypt.Lock()
	defer k.crypt.Unlock()

	switch opt := opts.(type) {
	case *rsa.OAEPOptions:
		hashOpt, ok := tpmSupportHashFunc[opt.Hash]
		if !ok {
			return nil, fmt.Errorf("unsupported hash algorithm: %d (%s)", opt.Hash, tpmHash(opt.Hash))
		}

		scheme := &tpm2.AsymScheme{
			Alg:  tpm2.AlgOAEP,
			Hash: hashOpt.hashAlg,
		}

		return tpm2.RSADecrypt(k.crypt.dev, tpmutil.Handle(k.handle), k.password, ciphertext, scheme, string(opt.Label))

	case nil, *rsa.PKCS1v15DecryptOptions:
		scheme := &tpm2.AsymScheme{Alg: tpm2.AlgRSAES}
		return tpm2.RSADecrypt(k.crypt.dev, tpmutil.Handle(k.handle), k.password, ciphertext, scheme, "")

	default:
		return nil, errors.New("required algorithm cannot be applied for this key")
	}
}

// Sign function is to make digital signature by TPM
func (k *tpmPrivateKeyRSA) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	var pssOpts *rsa.PSSOptions

	k.crypt.Lock()
	defer k.crypt.Unlock()

	if k.hashAlg != opts.HashFunc() {
		return nil, fmt.Errorf("required sign algorithm does not support for this key")
	}

	hashOpt, ok := tpmSupportHashFunc[opts.HashFunc()]
	if !ok {
		return nil, fmt.Errorf("undefined hash algorithm: %d (%s)", opts.HashFunc(), tpmHash(opts.HashFunc()))
	}

	if len(digest) != hashOpt.digestLen {
		return nil, fmt.Errorf("digest length is wrong for hash alg (%s)", tpmHash(opts.HashFunc()))
	}

	alg := tpm2.AlgRSASSA
	if pssOpts, ok = opts.(*rsa.PSSOptions); ok {
		if pssOpts.SaltLength != rsa.PSSSaltLengthAuto {
			return nil, errors.New("PSS padding scheme should have a rsa.PSSSaltLengthAuto option")
		}
		alg = tpm2.AlgRSAPSS
	}

	scheme := &tpm2.SigScheme{
		Alg:  alg,
		Hash: hashOpt.hashAlg,
	}

	sig, err := tpm2.Sign(k.crypt.dev, tpmutil.Handle(k.handle), k.password, digest, scheme)
	if err != nil {
		return nil, err
	}

	switch sig.Alg {
	case tpm2.AlgRSASSA:
		if err = rsa.VerifyPKCS1v15(k.pub.(*rsa.PublicKey), opts.HashFunc(), digest, sig.RSA.Signature); err != nil {
			return nil, err
		}

		return sig.RSA.Signature, nil

	case tpm2.AlgRSAPSS:
		if err = rsa.VerifyPSS(k.pub.(*rsa.PublicKey), opts.HashFunc(), digest, sig.RSA.Signature, pssOpts); err != nil {
			return nil, err
		}

		return sig.RSA.Signature, nil

	default:
		return nil, fmt.Errorf("undefined signature algorithm: %d", sig.Alg)
	}
}

func (hash tpmHash) String() (name string) {
	switch crypto.Hash(hash) {
	case crypto.MD4:
		name = "MD4"
	case crypto.MD5:
		name = "MD5"
	case crypto.SHA1:
		name = "SHA-1"
	case crypto.SHA224:
		name = "SHA-224"
	case crypto.SHA256:
		name = "SHA-256"
	case crypto.SHA384:
		name = "SHA-384"
	case crypto.SHA512:
		name = "SHA-512"
	case crypto.MD5SHA1:
		name = "MD5+SHA1"
	case crypto.RIPEMD160:
		name = "RIPEMD-160"
	case crypto.SHA3_224:
		name = "SHA3-224"
	case crypto.SHA3_256:
		name = "SHA3-256"
	case crypto.SHA3_384:
		name = "SHA3-384"
	case crypto.SHA3_512:
		name = "SHA3-512"
	case crypto.SHA512_224:
		name = "SHA-512/224"
	case crypto.SHA512_256:
		name = "SHA-512/256"
	case crypto.BLAKE2s_256:
		name = "BLAKE2s-256"
	case crypto.BLAKE2b_256:
		name = "BLAKE2b-256"
	case crypto.BLAKE2b_384:
		name = "BLAKE2b-384"
	case crypto.BLAKE2b_512:
		name = "BLAKE2b-512"
	default:
		name = "unknown hash value " + strconv.Itoa(int(hash))
	}

	return name
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func readPublicKey(dev io.ReadWriteCloser, handle tpmutil.Handle) (tpm2.Public, crypto.PublicKey, error) {
	pub, _, _, err := tpm2.ReadPublic(dev, handle)
	if err != nil {
		return pub, nil, err
	}
	publicKey, err := pub.Key()

	return pub, publicKey, err
}
