package webrtp

import (
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
)

func (r *Instance) Handler() fiber.Handler {
	return func(c fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}
		return websocket.New(func(conn *websocket.Conn) {
			r.HandleWebsocket(conn)
		})(c)
	}
}

func (r *Instance) HandleWebsocket(conn *websocket.Conn) {
	defer conn.Close()
	r.logger.Printf("client connected: %s", conn.RemoteAddr())

	initData := r.hub.GetInit()
	if initData == nil {
		r.logger.Printf("stream not ready, closing %s", conn.RemoteAddr())
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, initData); err != nil {
		return
	}

	ch := r.hub.Subscribe()
	defer func() {
		r.hub.Unsubscribe(ch)
		r.logger.Printf("client disconnected: %s", conn.RemoteAddr())
	}()

	for frag := range ch {
		_ = conn.SetWriteDeadline(time.Now().Add(r.cfg.WriteTimeout))
		if err := conn.WriteMessage(websocket.BinaryMessage, frag); err != nil {
			return
		}
	}
}
