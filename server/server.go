package server

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type Server struct {
	Port int
}

func NewServer() *Server {
	return &Server{}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Adjust this in production to restrict domains
	},
}

func HandleWebSocket(ctx *gin.Context) {
	// upgrade http to websocket

	conn, err := upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
	if err != nil {
		log.Println("Failed to set up websocket:", err)
		return
	}
	defer conn.Close()

	for {
		msgType, p, err := conn.ReadMessage()

		if err != nil {
			log.Println("Error reading msg: ", err)
			break
		}

		log.Printf("Received: %s\n", p)

		if err := conn.WriteMessage(msgType, p); err != nil {
			log.Println("Error writing message:", err)
			break
		}

	}

}
