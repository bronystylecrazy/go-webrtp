package streamcore

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type sharedSourceConn interface {
	Close()
	Done() <-chan struct{}
}

type sharedSourceController struct {
	startFn func(context.Context) (sharedSourceConn, error)
	streams []*Stream
	logger  globalLogger

	mu      sync.Mutex
	conn    sharedSourceConn
	cancel  context.CancelFunc
	started atomic.Bool

	stopTimerMu sync.Mutex
	stopTimer   *time.Timer
}

type globalLogger interface {
	Printf(format string, v ...interface{})
}

func (c *sharedSourceController) EnsureStarted() {
	if c == nil || c.startFn == nil || c.started.Load() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started.Load() {
		return
	}
	c.stopTimerMu.Lock()
	if c.stopTimer != nil {
		c.stopTimer.Stop()
		c.stopTimer = nil
	}
	c.stopTimerMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := c.startFn(ctx)
	if err != nil {
		cancel()
		if c.logger != nil {
			c.logger.Printf("shared source start failed: %v", err)
		}
		return
	}
	c.cancel = cancel
	c.conn = conn
	c.started.Store(true)

	go func(expected sharedSourceConn) {
		<-expected.Done()
		c.mu.Lock()
		if c.conn == expected {
			c.conn = nil
			if c.cancel != nil {
				c.cancel()
				c.cancel = nil
			}
		}
		shouldReset := c.started.Swap(false)
		c.mu.Unlock()
		if !shouldReset {
			return
		}
		for _, stream := range c.streams {
			if stream == nil || stream.Stop == nil {
				continue
			}
			_ = stream.Stop()
		}
	}(conn)
}

func (c *sharedSourceController) HasActiveRecording() bool {
	if c == nil {
		return false
	}
	for _, stream := range c.streams {
		if stream != nil && stream.Inst != nil && stream.Inst.RecordingStatus().Active {
			return true
		}
	}
	return false
}

func (c *sharedSourceController) activeClientCount() int32 {
	var total int32
	if c == nil {
		return 0
	}
	for _, stream := range c.streams {
		if stream != nil {
			total += stream.ActiveClientCount()
		}
	}
	return total
}

func (c *sharedSourceController) StopNow() error {
	if c == nil {
		return nil
	}
	c.stopTimerMu.Lock()
	if c.stopTimer != nil {
		c.stopTimer.Stop()
		c.stopTimer = nil
	}
	c.stopTimerMu.Unlock()

	c.mu.Lock()
	conn := c.conn
	cancel := c.cancel
	c.conn = nil
	c.cancel = nil
	c.started.Store(false)
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		conn.Close()
	}

	var firstErr error
	for _, stream := range c.streams {
		if stream == nil || stream.Stop == nil {
			continue
		}
		if err := stream.Stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *sharedSourceController) MaybeScheduleStop(idle time.Duration) {
	if c == nil || c.HasActiveRecording() || c.activeClientCount() > 0 {
		return
	}
	c.stopTimerMu.Lock()
	defer c.stopTimerMu.Unlock()
	if c.stopTimer != nil {
		c.stopTimer.Stop()
	}
	c.stopTimer = time.AfterFunc(idle, func() {
		if c.activeClientCount() > 0 || c.HasActiveRecording() {
			return
		}
		_ = c.StopNow()
	})
}
