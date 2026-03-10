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

	initData, startupFrames, ch := r.hub.SubscribeWithStartupSnapshot()
	var startupFrameNo uint64
	for _, startupFrame := range startupFrames {
		if startupFrame != nil && startupFrame.FrameNo > startupFrameNo {
			startupFrameNo = startupFrame.FrameNo
		}
	}

	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, initData); err != nil {
		r.hub.Unsubscribe(ch)
		return
	}
	for _, startupFrame := range startupFrames {
		if startupFrame == nil {
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(r.cfg.WriteTimeout))
		if err := conn.WriteMessage(websocket.BinaryMessage, startupFrame.Data); err != nil {
			r.hub.Unsubscribe(ch)
			return
		}
	}
	defer func() {
		r.hub.Unsubscribe(ch)
		r.logger.Printf("client disconnected: %s", conn.RemoteAddr())
	}()

	waitForResumeKeyframe := false
	expectedNextFrameNo := startupFrameNo + 1
	for frame := range ch {
		if frame == nil {
			continue
		}
		if frame.FrameNo <= startupFrameNo {
			continue
		}
		if expectedNextFrameNo > 0 && frame.FrameNo > expectedNextFrameNo {
			waitForResumeKeyframe = true
		}
		if waitForResumeKeyframe {
			if !frame.IsKey {
				continue
			}
			waitForResumeKeyframe = false
		}
		_ = conn.SetWriteDeadline(time.Now().Add(r.cfg.WriteTimeout))
		if err := conn.WriteMessage(websocket.BinaryMessage, frame.Data); err != nil {
			return
		}
		expectedNextFrameNo = frame.FrameNo + 1
	}
}
