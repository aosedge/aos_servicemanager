package downloadmanager

import (
	"fmt"
	//"os"
	//"time"

	"github.com/cavaliercoder/grab"
	amqp "gitpct.epam.com/epmd-aepr/aos_servicemanager/amqphandler"
)

func DownloadPkg(storage string, servInfo amqp.ServiseInfoFromCloud, filepath chan string) {
	client := grab.NewClient()
	req, err := grab.NewRequest(storage, servInfo.DownloadUrl)

	if err != nil {
		fmt.Printf("error = ", err)
		filepath <- ""
		return
	}

	// start download
	fmt.Printf("Downloading %v...\n", req.URL())
	resp := client.Do(req)
	fmt.Printf("  %v\n", resp.HTTPResponse.Status)

	// start UI loop
	//t := time.NewTicker(1000 * time.Millisecond)
	//defer t.Stop()

	/*Loop:
	for {
		select {
		case <-t.C:
			fmt.Printf("  transferred %v / %v bytes (%.2f%%)\n",
				resp.BytesComplete(),
				resp.Size,
				100*resp.Progress())

		case <-resp.Done:
			// download is complete
			break Loop
		}
	}

	// check for errors
	if err := resp.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Download failed: %v\n", err)
		os.Exit(1)
	}*/

	fmt.Printf("Download saved to ./%v \n", resp.Filename)
	filepath <- resp.Filename
	fmt.Printf("ret  \n")

	return
}
