package webrtp

import (
	"testing"
	"time"
)

func TestHandleResumeGapRequestsKeyframeAndWaits(t *testing.T) {
	inst := Init(&Config{Logger: stdLogger{}})
	conn := &fakeKeyframeRequesterConn{fakeSourceConn: &fakeSourceConn{done: make(chan struct{})}}
	inst.conn = conn

	state := &resumeWaitState{}
	frame := &Frame{FrameNo: 5, IsKey: false}

	send, closeConn := inst.handleResumeGap(state, frame, 4)
	if send {
		t.Fatal("expected delta frame after a gap to be withheld")
	}
	if closeConn {
		t.Fatal("did not expect connection close while still within resume timeout")
	}
	if !state.waiting {
		t.Fatal("expected handler to wait for a recovery keyframe")
	}
	if conn.forceCalls != 1 {
		t.Fatalf("expected one keyframe request, got %d", conn.forceCalls)
	}
}

func TestHandleResumeGapResumesOnKeyframe(t *testing.T) {
	inst := Init(&Config{Logger: stdLogger{}})
	state := &resumeWaitState{
		waiting:        true,
		requested:      true,
		waitingStarted: time.Now(),
	}

	send, closeConn := inst.handleResumeGap(state, &Frame{FrameNo: 6, IsKey: true}, 5)
	if !send {
		t.Fatal("expected keyframe to resume delivery")
	}
	if closeConn {
		t.Fatal("did not expect connection close on recovery keyframe")
	}
	if state.waiting {
		t.Fatal("expected waiting state to clear after keyframe")
	}
}

func TestHandleResumeGapClosesAfterTimeout(t *testing.T) {
	inst := Init(&Config{Logger: stdLogger{}})
	state := &resumeWaitState{
		waiting:        true,
		requested:      true,
		waitingStarted: time.Now().Add(-resumeKeyframeTimeout - 100*time.Millisecond),
	}

	send, closeConn := inst.handleResumeGap(state, &Frame{FrameNo: 6, IsKey: false}, 5)
	if send {
		t.Fatal("expected stale delta frame to remain blocked")
	}
	if !closeConn {
		t.Fatal("expected stalled client to be disconnected after timeout")
	}
}
