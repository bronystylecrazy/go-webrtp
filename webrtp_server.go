package webrtp

import (
	"context"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
)

func (r *Instance) Start(addr string) error {
	r.stop.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	conn, err := r.connectSource(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("source connect: %w", err)
	}
	r.conn = conn

	app := fiber.New()
	app.All("/ws", r.Handler())

	r.logger.Printf("listening on http://localhost%s", addr)
	return app.Listen(addr)
}

func (r *Instance) Connect() error {
	r.stop.Store(false)
	for {
		if r.stop.Load() {
			return nil
		}
		r.hub.Reset()

		ctx, cancel := context.WithCancel(context.Background())
		r.cancel = cancel

		conn, err := r.connectSource(ctx)
		if err != nil {
			if recorder := r.currentRecorder(); recorder != nil {
				recorder.OnOffline()
			}
			if r.stop.Load() {
				cancel()
				return nil
			}
			r.logger.Printf("source connect failed: %v", err)
			cancel()
			time.Sleep(10 * time.Second)
			continue
		}
		r.conn = conn
		var doneCh <-chan struct{}
		if doneConn, ok := conn.(interface{ Done() <-chan struct{} }); ok {
			doneCh = doneConn.Done()
		}

		// Wait for connection to drop or frame timeout
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				if recorder := r.currentRecorder(); recorder != nil {
					recorder.OnOffline()
				}
				if r.stop.Load() {
					return nil
				}
				r.logger.Printf("source connection dropped, reconnecting")
				goto reconnect
			case <-doneCh:
				if recorder := r.currentRecorder(); recorder != nil {
					recorder.OnOffline()
				}
				if r.stop.Load() {
					return nil
				}
				r.logger.Printf("source process exited, reconnecting")
				cancel()
				goto reconnect
			case <-ticker.C:
				if r.hub.ready.Load() && !r.hub.IsReceivingFrames() {
					if recorder := r.currentRecorder(); recorder != nil {
						recorder.OnOffline()
					}
					r.logger.Printf("no frame received for 1s, reconnecting")
					cancel()
					goto reconnect
				}
			}
		}
	reconnect:
	}
}

func (r *Instance) Stop() error {
	r.stop.Store(true)
	if r.cancel != nil {
		r.cancel()
	}
	if r.conn != nil {
		r.conn.Close()
	}
	r.hub.Reset()
	if recorder := r.currentRecorder(); recorder != nil {
		_ = recorder.Stop()
	}
	if r.keyframes != nil {
		r.keyframes.Close()
	}
	return nil
}
