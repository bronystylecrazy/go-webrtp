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

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}()

	initData := r.hub.GetInit()
	for initData == nil {
		r.logger.Printf("stream not ready, waiting %s", conn.RemoteAddr())
		select {
		case <-time.After(100 * time.Millisecond):
			initData = r.hub.GetInit()
		case <-done:
			r.logger.Printf("client disconnected while waiting: %s", conn.RemoteAddr())
			return
		}
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

	for frame := range ch {
		if frame == nil {
			continue
		}
		selected := frame
		for {
			select {
			case next, ok := <-ch:
				if !ok {
					return
				}
				if next == nil {
					continue
				}
				selected = next
			default:
				goto writeFrame
			}
		}
	writeFrame:
		_ = conn.SetWriteDeadline(time.Now().Add(r.cfg.WriteTimeout))
		if err := conn.WriteMessage(websocket.BinaryMessage, selected.Data); err != nil {
			return
		}
	}
}
