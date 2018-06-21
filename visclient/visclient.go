package visclient

import (
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Consts
 ******************************************************************************/

/*******************************************************************************
 * Types
 ******************************************************************************/

// UserChangedNtf user changed notification
type UserChangedNtf struct {
	Users []string
}

// VisClient VIS client object
type VisClient struct {
	webConn *websocket.Conn
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New creates new visclient
func New(urlStr string) (vis *VisClient, err error) {
	log.WithField("url", urlStr).Debug("New VIS client")

	webConn, _, err := websocket.DefaultDialer.Dial(urlStr, nil)
	if err != nil {
		return vis, err
	}

	vis = &VisClient{webConn: webConn}

	return vis, err
}

// Close closes vis client
func (vis *VisClient) Close() (err error) {
	log.Debug("Close VIS client")

	if err := vis.webConn.Close(); err != nil {
		return err
	}

	return nil
}
