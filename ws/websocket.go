package ws

import (
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

func ProxyWebSocket(w http.ResponseWriter, req *http.Request) {
	u := url.URL{Scheme: "ws", Host: "127.0.0.1:8000", Path: req.RequestURI}
	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Println(err)
		return
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	io.Copy(w, conn.NetConn())
}
