package downloadmanager

import (
	"github.com/cavaliercoder/grab"
	log "github.com/sirupsen/logrus"

	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
	"gitpct.epam.com/epmd-aepr/aos_servicemanager/fcrypt"
)

func DownloadPkg(destDir string, servInfo amqp.ServiseInfoFromCloud, filepath chan string) {
	client := grab.NewClient()

	req, err := grab.NewRequest(destDir, servInfo.DownloadUrl)
	if err != nil {
		log.Error("Can't download package: ", err)
		filepath <- ""
		return
	}

	// start download
	resp := client.Do(req)

	log.WithField("filename", resp.Filename).Debug("Start downloading")

	// wait when finished
	resp.Wait()

	if err := resp.Err(); err != nil {
		log.Error("Can't download package: ", err)
		filepath <- ""
		return
	}

	outFileName, err := fcrypt.DecryptImage(
		resp.Filename,
		[]byte(servInfo.ImageSignature),
		[]byte(servInfo.EncryptionKey),
		[]byte(servInfo.EncryptionModeParams),
		servInfo.SignatureAlgorithm,
		servInfo.EncryptionMode,
		servInfo.CertificateChain)

	if err != nil {
		log.Error("Can't decrypt image: ", err)
		filepath <- ""
		return
	}

	log.WithField("filename", outFileName).Debug("Decrypt image")

	filepath <- outFileName

	return
}
