package webrtp

import (
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
)

const resumeKeyframeTimeout = 2 * time.Second

type resumeWaitState struct {
	waiting         bool
	requested       bool
	waitingStarted  time.Time
}

func (r *Instance) Handler() fiber.Handler {
	return func(c fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}
		return websocket.New(func(conn *websocket.Conn) {
			r.handleHubWebsocket(conn, r.hub)
		})(c)
	}
}

func (r *Instance) HandleWebsocket(conn *websocket.Conn) {
	r.handleHubWebsocket(conn, r.hub)
}

func (r *Instance) handleHubWebsocket(conn *websocket.Conn, hub *Hub) {
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

	initData := hub.GetInit()
	waitTicker := time.NewTicker(100 * time.Millisecond)
	defer waitTicker.Stop()
	waitingLogged := false
	for initData == nil {
		if !waitingLogged {
			r.logger.Printf("stream not ready, waiting %s", conn.RemoteAddr())
			waitingLogged = true
		}
		select {
		case <-waitTicker.C:
			initData = hub.GetInit()
		case <-done:
			r.logger.Printf("client disconnected while waiting: %s", conn.RemoteAddr())
			return
		}
	}

	initData, startupFrames, ch := hub.SubscribeWithStartupSnapshot()
	var startupFrameNo uint64
	for _, startupFrame := range startupFrames {
		if startupFrame != nil && startupFrame.FrameNo > startupFrameNo {
			startupFrameNo = startupFrame.FrameNo
		}
	}

	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, initData); err != nil {
		hub.Unsubscribe(ch)
		return
	}
	for _, startupFrame := range startupFrames {
		if startupFrame == nil {
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(r.cfg.WriteTimeout))
		if err := conn.WriteMessage(websocket.BinaryMessage, startupFrame.Data); err != nil {
			hub.Unsubscribe(ch)
			return
		}
	}
	defer func() {
		hub.Unsubscribe(ch)
		r.logger.Printf("client disconnected: %s", conn.RemoteAddr())
	}()

	expectedNextFrameNo := startupFrameNo + 1
	resumeState := &resumeWaitState{}
	for frame := range ch {
		if frame == nil {
			continue
		}
		if frame.FrameNo <= startupFrameNo {
			continue
		}
		send, closeConn := r.handleResumeGap(resumeState, frame, expectedNextFrameNo)
		if closeConn {
			r.logger.Printf("closing stalled client after frame gap: %s", conn.RemoteAddr())
			return
		}
		if !send {
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(r.cfg.WriteTimeout))
		if err := conn.WriteMessage(websocket.BinaryMessage, frame.Data); err != nil {
			return
		}
		expectedNextFrameNo = frame.FrameNo + 1
	}
}

func (r *Instance) handleResumeGap(state *resumeWaitState, frame *Frame, expectedNextFrameNo uint64) (send bool, closeConn bool) {
	if state == nil || frame == nil {
		return frame != nil, false
	}

	if !state.waiting && expectedNextFrameNo > 0 && frame.FrameNo > expectedNextFrameNo {
		state.waiting = true
		state.waitingStarted = time.Now()
		if !state.requested {
			if err := r.ForceNextKeyFrame(); err == nil {
				r.logger.Printf("requested recovery keyframe after client frame gap")
			}
			state.requested = true
		}
	}

	if !state.waiting {
		return true, false
	}
	if frame.IsKey {
		state.waiting = false
		state.requested = false
		state.waitingStarted = time.Time{}
		return true, false
	}
	if !state.waitingStarted.IsZero() && time.Since(state.waitingStarted) >= resumeKeyframeTimeout {
		return false, true
	}
	return false, false
}
