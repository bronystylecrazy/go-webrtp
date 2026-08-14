package webrtp

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gorilla/websocket"
)

// startLivenessServer serves the instance's websocket handler on a random local port.
func startLivenessServer(t *testing.T, inst *Instance) string {
	t.Helper()

	app := fiber.New()
	app.Get("/ws", inst.Handler())

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() { _ = app.Listener(listener) }()
	t.Cleanup(func() {
		_ = listener.Close()
		_ = app.Shutdown()
	})

	return "ws://" + listener.Addr().String() + "/ws"
}

func dialLivenessClient(t *testing.T, url string) *websocket.Conn {
	t.Helper()

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func isTimeoutErr(err error) bool {
	netErr, ok := err.(net.Error)
	return ok && netErr.Timeout()
}

func TestHandlerClosesClientWhenInitNeverArrives(t *testing.T) {
	origTimeout := initWaitTimeout
	initWaitTimeout = 300 * time.Millisecond
	t.Cleanup(func() { initWaitTimeout = origTimeout })

	inst := Init(&Config{Logger: stdLogger{}})
	url := startLivenessServer(t, inst)
	conn := dialLivenessClient(t, url)

	_ = conn.SetReadDeadline(time.Now().Add(initWaitTimeout + 1500*time.Millisecond))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected the server to close the connection, got a message instead")
	}
	if isTimeoutErr(err) {
		t.Fatalf("server did not close the client within the init-wait timeout: %v", err)
	}
}

func TestHandlerServesLateInitToReconnectingClient(t *testing.T) {
	origTimeout := initWaitTimeout
	initWaitTimeout = 300 * time.Millisecond
	t.Cleanup(func() { initWaitTimeout = origTimeout })

	inst := Init(&Config{Logger: stdLogger{}})
	url := startLivenessServer(t, inst)

	first := dialLivenessClient(t, url)
	_ = first.SetReadDeadline(time.Now().Add(initWaitTimeout + 1500*time.Millisecond))
	if _, _, err := first.ReadMessage(); err == nil || isTimeoutErr(err) {
		t.Fatalf("expected the first client to be closed by the server, got %v", err)
	}

	// The stream becomes ready after the first client timed out.
	inst.hub.SetInit([]byte("init-segment"))

	second := dialLivenessClient(t, url)
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := second.ReadMessage()
	if err != nil {
		t.Fatalf("expected the reconnecting client to receive the init segment: %v", err)
	}
	if string(data) != "init-segment" {
		t.Fatalf("unexpected init payload: %q", string(data))
	}
}

func TestHandlerDropsSubscriberWhenPongsStop(t *testing.T) {
	origPing, origPong := pingPeriod, pongWait
	pingPeriod = 150 * time.Millisecond
	pongWait = 500 * time.Millisecond
	t.Cleanup(func() { pingPeriod, pongWait = origPing, origPong })

	inst := Init(&Config{Logger: stdLogger{}})
	inst.hub.SetInit([]byte("init-segment"))
	url := startLivenessServer(t, inst)

	conn := dialLivenessClient(t, url)
	var pings atomic.Int32
	// Swallow pings without answering so the server sees a silent subscriber.
	conn.SetPingHandler(func(string) error {
		pings.Add(1)
		return nil
	})

	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read init: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected the server to drop the non-responding subscriber")
	}
	if isTimeoutErr(err) {
		t.Fatalf("server did not drop the silent subscriber within pong wait: %v", err)
	}
	if pings.Load() == 0 {
		t.Fatal("expected at least one keepalive ping before the drop")
	}
}

func TestHandlerKeepsSubscriberThatAnswersPings(t *testing.T) {
	origPing, origPong := pingPeriod, pongWait
	pingPeriod = 150 * time.Millisecond
	pongWait = 500 * time.Millisecond
	t.Cleanup(func() { pingPeriod, pongWait = origPing, origPong })

	inst := Init(&Config{Logger: stdLogger{}})
	inst.hub.SetInit([]byte("init-segment"))
	url := startLivenessServer(t, inst)

	conn := dialLivenessClient(t, url)

	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read init: %v", err)
	}

	// No frames are published; only pings and pongs flow. The local deadline
	// must be the only reason the read stops.
	_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected the read to stop at the local deadline")
	}
	if !isTimeoutErr(err) {
		t.Fatalf("server closed a subscriber that answers pings: %v", err)
	}
}
