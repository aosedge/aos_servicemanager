package visclient_test

import (
	"os"
	"testing"

	log "github.com/sirupsen/logrus"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/visclient"
)

/*******************************************************************************
 * Variables
 ******************************************************************************/

var vis *visclient.VisClient

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	var err error

	vis, err = visclient.New("wss://localhost:8088")
	if err != nil {
		log.Fatalf("Error connecting to VIS server: %s", err)
	}

	ret := m.Run()

	if err = vis.Close(); err != nil {
		log.Fatalf("Error closing VIS: %s", err)
	}

	os.Exit(ret)
}

/*******************************************************************************
 * Tests
 ******************************************************************************/

func TestGetVIN(t *testing.T) {
	vin, err := vis.GetVIN()
	if err != nil {
		t.Fatalf("Error getting VIN: %s", err)
	}

	if vin == "" {
		t.Fatalf("Wrong VIN value: %s", vin)
	}
}
