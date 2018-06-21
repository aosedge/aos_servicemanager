package visclient_test

import (
	"log"
	"os"
	"testing"

	"gitpct.epam.com/epmd-aepr/aos_servicemanager/visclient"
)

/*******************************************************************************
 * Variables
 ******************************************************************************/

var vis *visclient.VisClient

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	vis, err := visclient.New("wss://localhost:8088")
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
