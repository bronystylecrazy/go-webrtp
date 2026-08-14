package webrtp

import (
	"errors"
	"net"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
)

const resumeKeyframeTimeout = 2 * time.Second

// Subscriber liveness defaults; override per instance through Config.
const (
	defaultInitWaitTimeout = 15 * time.Second
	defaultPingPeriod      = 15 * time.Second
	defaultPongWait        = 45 * time.Second
)

// rawConnExposer exposes the raw socket behind a hijacked connection.
type rawConnExposer interface {
	UnsafeConn() net.Conn
}

// closeUnderlyingConn closes the raw socket behind a websocket connection.
// Hijacked fasthttp connections ignore Close until the websocket wrapper has
// returned, so this is the only way to unblock a reader from the handler.
func closeUnderlyingConn(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	raw := conn.UnderlyingConn()
	if exposer, ok := raw.(rawConnExposer); ok {
		_ = exposer.UnsafeConn().Close()
		return
	}
	_ = raw.Close()
}

type resumeWaitState struct {
	waiting        bool
	requested      bool
	waitingStarted time.Time
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
	r.logger.Printf("client connected: %s", conn.RemoteAddr())

	done := make(chan struct{})
	go func() {
		defer close(done)
		// A subscriber that stays silent for pongWait (no pong, no close
		// frame) fails the read deadline and is dropped. Any read error is
		// terminal on this connection, which is fine here: it is closed.
		_ = conn.SetReadDeadline(time.Now().Add(r.cfg.PongWait))
		conn.SetPongHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(r.cfg.PongWait))
			return nil
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					r.logger.Printf("subscriber silent for %s, dropping: %s", r.cfg.PongWait, conn.RemoteAddr())
				}
				break
			}
		}
	}()

	// ch is nil until the client is subscribed. Cleanup tells the client to
	// reconnect with a close frame, forces the underlying socket closed so
	// the reader goroutine unblocks, and joins it before returning: nothing
	// may touch the connection after the websocket wrapper recycles it.
	var ch chan *Frame
	defer func() {
		_ = conn.SetWriteDeadline(time.Now().Add(r.cfg.WriteTimeout))
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "stream unavailable"))
		closeUnderlyingConn(conn)
		<-done
		if ch != nil {
			hub.Unsubscribe(ch)
		}
		r.logger.Printf("client disconnected: %s", conn.RemoteAddr())
	}()

	initData := hub.GetInit()
	if initData == nil {
		waitTicker := time.NewTicker(100 * time.Millisecond)
		defer waitTicker.Stop()
		initDeadline := time.NewTimer(r.cfg.InitWaitTimeout)
		defer initDeadline.Stop()
		waitingLogged := false
		for initData == nil {
			if !waitingLogged {
				r.logger.Printf("stream not ready, waiting %s", conn.RemoteAddr())
				waitingLogged = true
			}
			select {
			case <-waitTicker.C:
				initData = hub.GetInit()
			case <-initDeadline.C:
				r.logger.Printf("stream not ready after %s, closing client %s", r.cfg.InitWaitTimeout, conn.RemoteAddr())
				return
			case <-done:
				return
			}
		}
	}

	subscriptionInit, startupFrames, subscribed := hub.SubscribeWithStartupSnapshot()
	initData = subscriptionInit
	ch = subscribed
	var startupFrameNo uint64
	for _, startupFrame := range startupFrames {
		if startupFrame != nil && startupFrame.FrameNo > startupFrameNo {
			startupFrameNo = startupFrame.FrameNo
		}
	}

	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, initData); err != nil {
		return
	}
	for _, startupFrame := range startupFrames {
		if startupFrame == nil {
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(r.cfg.WriteTimeout))
		if err := conn.WriteMessage(websocket.BinaryMessage, startupFrame.Data); err != nil {
			return
		}
	}

	expectedNextFrameNo := startupFrameNo + 1
	resumeState := &resumeWaitState{}
	pingTicker := time.NewTicker(r.cfg.PingPeriod)
	defer pingTicker.Stop()
	for {
		select {
		case frame, ok := <-ch:
			if !ok {
				return
			}
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
		case <-pingTicker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(r.cfg.WriteTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
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
			} else {
				r.logger.Printf("recovery keyframe unavailable after client frame gap: %v", err)
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
