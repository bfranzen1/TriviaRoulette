package handlers

//TODO: start a goroutine that connects to the RabbitMQ server,
//reads events off the queue, and broadcasts them to all of
//the existing WebSocket connections that should hear about
//that event. If you get an error writing to the WebSocket,
//just close it and remove it from the list
//(client went away without closing from
//their end). Also make sure you start a read pump that
//reads incoming control messages, as described in the
//Gorilla WebSocket API documentation:
//http://godoc.org/github.com/gorilla/websocket

import (
	"encoding/json"
	"fmt"
	"github.com/TriviaRoulette/servers/trivia/models"
	"github.com/gorilla/websocket"
	"github.com/streadway/amqp"
	"net/http"
	"strings"
	"sync"
)

// SocketStore contains client connection information
// and a queue channel for sending notifications
type SocketStore struct {
	Connections map[int64]*websocket.Conn
	lock        sync.Mutex
}

// NewSocketStore returns a new socket store containing a map of player id's to
// a websocket, a mutex lock for concurrent use and a queue channel for real time
// notifications
func NewSocketStore() *SocketStore {
	return &SocketStore{Connections: map[int64]*websocket.Conn{}}
}

// Control messages for websocket
const (
	// TextMessage denotes a text data message. The text message payload is
	// interpreted as UTF-8 encoded text data.
	TextMessage = 1

	// BinaryMessage denotes a binary data message.
	BinaryMessage = 2

	// CloseMessage denotes a close control message. The optional message
	// payload contains a numeric code and text. Use the FormatCloseMessage
	// function to format a close message payload.
	CloseMessage = 8

	// PingMessage denotes a ping control message. The optional message payload
	// is UTF-8 encoded text.
	PingMessage = 9

	// PongMessage denotes a pong control message. The optional message payload
	// is UTF-8 encoded text.
	PongMessage = 10

	// name of rabbitmq queue to use for games
	qName = "trivia"
)

// InsertConnection is a Thread-safe method for inserting a connection
func (s *SocketStore) InsertConnection(id int64, conn *websocket.Conn) {
	s.lock.Lock()
	// insert socket connection
	s.Connections[id] = conn
	s.lock.Unlock()
}

// RemoveConnection is a Thread-safe method for removing a connection
func (s *SocketStore) RemoveConnection(id int64) {
	s.lock.Lock()
	_, ok := s.Connections[id]
	if ok {
		delete(s.Connections, id)
	}
	s.lock.Unlock()
}

// WriteToValidConnections sends messages to a subset of connections
// (if the message is intended for a private channel), or to all of them (if the message
// is posted on a public channel
func (s *SocketStore) WriteToValidConnections(playerIDs []int64, messageType int, data []byte) error {
	var writeError error
	if len(playerIDs) > 0 { // private channel
		for _, id := range playerIDs {
			writeError = s.Connections[id].WriteMessage(messageType, data)
			if writeError != nil {
				return writeError
			}
		}
	} else { // public channel
		for _, conn := range s.Connections {
			writeError = conn.WriteMessage(messageType, data)
			if writeError != nil {
				return writeError
			}
		}
	}

	return nil
}

// Message is a struct to read our message into
type Message struct {
	Type      string                 `json:"type"`
	Channel   map[string]interface{} `json:"channel,omitempty"`
	ChannelID int64                  `json:"channelID,omitempty"`
	Message   map[string]interface{} `json:"message,omitempty"`
	MessageID int64                  `json:"messageID,omitempty"`
	UserIDs   []int64                `json:"userIDs,omitempty"`
}

// upgrader is a variable that stores websocket information and verifies
// the origin of the client request to authenticate
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return strings.Contains(r.Header.Get("Origin"), "bfranzen.me")
	},
}

// PlayerConnectionHandler handles when the client visits the trivia endpoint
// if the user is valid (request comes from proper host, user exists) upgrade is performed
// and connection is stored for duration of client session
func (ctx *TriviaContext) PlayerConnectionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-User") == "" {
		http.Error(w, "Unauthorized Access", 401)
	}

	player := models.Player{}
	if err := json.Unmarshal([]byte(r.Header.Get("X-User")), &player); err != nil {
		fmt.Printf("error getting message body, %v", err)
	}

	// handle the websocket handshake
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "Failed to open websocket connection", 401)
		return
	}

	// Insert our connection onto our datastructure for ongoing usage
	ctx.UserConnections.InsertConnection(player.ID, conn)
	// Invoke a goroutine for handling control messages from this connection
	go (func(conn *websocket.Conn, playerID int64) {
		defer conn.Close()
		defer ctx.UserConnections.RemoveConnection(playerID)

		for {
			messageType, p, err := conn.ReadMessage()
			var j map[string]interface{}
			if err := json.Unmarshal(p, &j); err != nil {
				fmt.Println("error unmarshaling json")
			}

			if messageType == CloseMessage {
				fmt.Println("Close message received...")
				break
			} else if err != nil {
				fmt.Println("error connecting, closing...")
				break
			}
			// ignore ping and pong messages
		}

	})(conn, player.ID)
}

// ConnectQueue connects to the RabbitMQ service at the address defined in the addr variable
// and creates a channel and queue to send/receive messages to. It returns the go channel
// which contains messages living on the RabbitMQ queue. Errors are returned if the
// connection fails
func (s *SocketStore) ConnectQueue(addr string) (<-chan amqp.Delivery, error) {
	con, err := amqp.Dial("amqp://" + addr)
	if err != nil {
		return nil, fmt.Errorf("Unable to connect to MQ, %v", err)
	}

	chann, err := con.Channel()
	if err != nil {
		return nil, fmt.Errorf("error creating channel, %v", err)
	}

	queue, err := chann.QueueDeclare(qName, true, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("error declaring queue, %v", err)
	}

	events, err := chann.Consume(queue.Name, "", false, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("error retreiving messages, %v", err)
	}
	return events, nil
}

// Read reads events off the passed go channel created by the ConnectQueue method
// and sends the messages to the proper websockets in the SocketStore
func (s *SocketStore) Read(events <-chan amqp.Delivery) {
	for e := range events {
		event := Message{}
		if err := json.Unmarshal(e.Body, &event); err != nil {
			fmt.Printf("error getting message body, %v", err)
			break
		}
		s.WriteToValidConnections(event.UserIDs, TextMessage, e.Body)

	}
}