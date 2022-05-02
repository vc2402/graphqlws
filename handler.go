package graphqlws

import (
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// HandlerConfig stores the configuration of a GraphQL WebSocket handler.
type HandlerConfig struct {
	SubscriptionManager SubscriptionManager
	Authenticate        AuthenticateFunc
	ReqAuthenticate     RequestAuthenticateFunc
	Logger              *logrus.Entry
}

// NewHandler creates a WebSocket handler for GraphQL WebSocket connections.
// This handler takes a SubscriptionManager and adds/removes subscriptions
// as they are started/stopped by the client.
func NewHandler(config HandlerConfig) http.Handler {
	logger := config.Logger
	if logger == nil {
		logger = NewLogger("handler")
	}
	// Create a WebSocket upgrader that requires clients to implement
	// the "graphql-ws" protocol
	var upgrader = websocket.Upgrader{
		CheckOrigin:  func(r *http.Request) bool { return true },
		Subprotocols: []string{"graphql-ws"},
	}

	subscriptionManager := config.SubscriptionManager

	// Create a map (used like a set) to manage client connections
	var connections = make(map[Connection]bool)
	connlock := sync.Mutex{}

	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// Establish a WebSocket connection
			var ws, err = upgrader.Upgrade(w, r, nil)

			// Bail out if the WebSocket connection could not be established
			if err != nil {
				logger.Warn("Failed to establish WebSocket connection", err)
				return
			}

			// Close the connection early if it doesn't implement the graphql-ws protocol
			if ws.Subprotocol() != "graphql-ws" {
				logger.Warn("Connection does not implement the GraphQL WS protocol")
				ws.Close()
				return
			}

			var auth interface{}
			if config.ReqAuthenticate != nil {
				auth, _ = config.ReqAuthenticate(r)
			}
			// Establish a GraphQL WebSocket connection
			conn := NewConnection(
				ws,
				ConnectionConfig{
					Authenticate: config.Authenticate,
					EventHandlers: ConnectionEventHandlers{
						Close: func(conn Connection) {
							logger.WithFields(logrus.Fields{
								"conn": conn.ID(),
								"user": conn.User(),
							}).Debug("Closing connection")

							subscriptionManager.RemoveSubscriptions(conn)

							connlock.Lock()
							defer connlock.Unlock()
							delete(connections, conn)
						},
						StartOperation: func(
							conn Connection,
							opID string,
							data *StartMessagePayload,
						) []error {
							logger.WithFields(logrus.Fields{
								"conn": conn.ID(),
								"op":   opID,
								"user": conn.User(),
							}).Debug("Start operation")

							return subscriptionManager.AddSubscription(conn, &Subscription{
								ID:            opID,
								Query:         data.Query,
								Variables:     data.Variables,
								OperationName: data.OperationName,
								Connection:    conn,
								SendData: func(data *DataMessagePayload) bool {
									return conn.SendData(opID, data)
								},
							})
						},
						StopOperation: func(conn Connection, opID string) {
							subscriptionManager.RemoveSubscription(conn, &Subscription{
								ID: opID,
							})
						},
					},
					Logger: logger,
				},
				auth,
			)

			connlock.Lock()
			defer connlock.Unlock()
			connections[conn] = true
		},
	)
}
