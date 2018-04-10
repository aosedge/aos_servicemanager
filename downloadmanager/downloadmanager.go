package downloadmanager

import (
	"encoding/hex"
	"io/ioutil"
	"os"
	"strings"

	"github.com/cavaliercoder/grab"
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/fcrypt"
)

func DownloadPkg(servInfo amqp.ServiceInfoFromCloud) (outputFile string, err error) {
	client := grab.NewClient()

	destDir, err := ioutil.TempDir("", "blob")
	if err != nil {
		log.Error("Can't create tmp dir : ", err)
		return outputFile, err
	}
	req, err := grab.NewRequest(destDir, servInfo.DownloadUrl)
	if err != nil {
		log.Error("Can't download package: ", err)
		return outputFile, err
	}

	// start download
	resp := client.Do(req)
	defer os.RemoveAll(destDir)

	log.WithField("filename", resp.Filename).Debug("Start downloading")

	// wait when finished
	resp.Wait()

	if err := resp.Err(); err != nil {
		log.Error("Can't download package: ", err)
		return outputFile, err
	}

	imageSignature, err := hex.DecodeString(servInfo.ImageSignature)
	if err != nil {
		log.Error("Error decoding HEX string for signature: ", err)
		return outputFile, err
	}

	encryptionKey, err := hex.DecodeString(servInfo.EncryptionKey)
	if err != nil {
		log.Error("Error decoding HEX string for key: ", err)
		return outputFile, err
	}

	encryptionModeParams, err := hex.DecodeString(servInfo.EncryptionModeParams)
	if err != nil {
		log.Error("Error decoding HEX string for IV: ", err)
		return outputFile, err
	}

	certificateChain := strings.Replace(servInfo.CertificateChain, "\\n", "", -1)
	outputFile, err = fcrypt.DecryptImage(
		resp.Filename,
		imageSignature,
		encryptionKey,
		encryptionModeParams,
		servInfo.SignatureAlgorithm,
		servInfo.SignatureAlgorithmHash,
		servInfo.SignatureScheme,
		servInfo.EncryptionAlgorythm,
		servInfo.EncryptionMode,
		certificateChain)

	if err != nil {
		log.Error("Can't decrypt image: ", err)
		return outputFile, err
	}

	log.WithField("filename", outputFile).Debug("Decrypt image")

	return outputFile, nil
}
