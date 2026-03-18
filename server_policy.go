package webrtp

import (
	"context"
	"time"
)

func (r *Instance) runReconnectLoop() error {
	r.stop.Store(false)
	for {
		if r.stop.Load() {
			return nil
		}
		if err := r.connectWithRetry(); err != nil {
			return err
		}
	}
}

func (r *Instance) connectWithRetry() error {
	r.hub.Reset()
	r.ensureKeyframeSink()

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	conn, err := r.connectSource(ctx)
	if err != nil {
		r.handleOffline()
		if r.stop.Load() {
			cancel()
			return nil
		}
		r.logger.Printf("source connect failed: %v", err)
		cancel()
		time.Sleep(10 * time.Second)
		return nil
	}
	r.conn = conn
	return r.waitForReconnect(ctx, cancel, conn)
}

func (r *Instance) waitForReconnect(ctx context.Context, cancel context.CancelFunc, conn sourceConn) error {
	doneCh := connectionDone(conn)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.handleOffline()
			if r.stop.Load() {
				return nil
			}
			r.logger.Printf("source connection dropped, reconnecting")
			return nil
		case <-doneCh:
			r.handleOffline()
			if r.stop.Load() {
				return nil
			}
			r.logger.Printf("source process exited, reconnecting")
			cancel()
			return nil
		case <-ticker.C:
			if r.hub.ready.Load() && !r.hub.IsReceivingFrames() {
				r.handleOffline()
				r.logger.Printf("no frame received for 1s, reconnecting")
				cancel()
				return nil
			}
		}
	}
}

func connectionDone(conn sourceConn) <-chan struct{} {
	if doneConn, ok := conn.(interface{ Done() <-chan struct{} }); ok {
		return doneConn.Done()
	}
	return nil
}

func (r *Instance) handleOffline() {
	r.conn = nil
	if recorder := r.currentRecorder(); recorder != nil {
		recorder.OnOffline()
	}
}
