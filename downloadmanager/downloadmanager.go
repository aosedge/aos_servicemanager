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

func DownloadPkg(servInfo amqp.ServiceInfoFromCloud, filepath chan string) {
	client := grab.NewClient()

	destDir, err := ioutil.TempDir("", "blob")
	if err != nil {
		log.Error("Can't create tmp dir : ", err)
		filepath <- ""
		return
	}
	req, err := grab.NewRequest(destDir, servInfo.DownloadUrl)
	if err != nil {
		log.Error("Can't download package: ", err)
		filepath <- ""
		return
	}

	// start download
	resp := client.Do(req)
	defer os.RemoveAll(destDir)

	log.WithField("filename", resp.Filename).Debug("Start downloading")

	// wait when finished
	resp.Wait()

	if err := resp.Err(); err != nil {
		log.Error("Can't download package: ", err)
		filepath <- ""
		return
	}

	imageSignature, err := hex.DecodeString(servInfo.ImageSignature)
	if err != nil {
		log.Error("Error decoding HEX string for signature", err)
		return
	}

	encryptionKey, err := hex.DecodeString(servInfo.EncryptionKey)
	if err != nil {
		log.Error("Error decoding HEX string for key", err)
		return
	}

	encryptionModeParams, err := hex.DecodeString(servInfo.EncryptionModeParams)
	if err != nil {
		log.Error("Error decoding HEX string for IV", err)
		return
	}

	certificateChain := strings.Replace(servInfo.CertificateChain, "\\n", "", -1)
	outFileName, err := fcrypt.DecryptImage(
		resp.Filename,
		imageSignature,
		encryptionKey,
		encryptionModeParams,
		servInfo.SignatureAlgorithmHash,
		servInfo.EncryptionMode,
		certificateChain)

	if err != nil {
		log.Error("Can't decrypt image: ", err)
		filepath <- ""
		return
	}

	log.WithField("filename", outFileName).Debug("Decrypt image")

	filepath <- outFileName

	return
}
