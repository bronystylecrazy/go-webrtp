package webrtp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeSourceConn struct {
	done chan struct{}
}

func (c *fakeSourceConn) Close() {}

func (c *fakeSourceConn) Done() <-chan struct{} { return c.done }

type fakeKeyframeRequesterConn struct {
	*fakeSourceConn
	forceErr   error
	forceCalls int
}

func (c *fakeKeyframeRequesterConn) ForceNextKeyFrame() error {
	c.forceCalls++
	return c.forceErr
}

type captureKeyframer struct {
	frame *Keyframe
}

func (c *captureKeyframer) HandleKeyframe(frame *Keyframe) error {
	c.frame = frame
	return nil
}

func TestPublishH264AccessUnitInvokesRawHandler(t *testing.T) {
	received := make(chan H264AccessUnit, 1)
	inst := Init(&Config{
		Logger: stdLogger{},
		H264AccessUnitHandler: func(au H264AccessUnit) {
			received <- au
		},
	})

	source := [][]byte{{0x67, 0x01, 0x02}, {0x68, 0x03}}
	inst.PublishH264AccessUnit(source, 9000)

	select {
	case got := <-received:
		if got.PTS90k != 9000 {
			t.Fatalf("unexpected pts: %d", got.PTS90k)
		}
		if len(got.NALUs) != 2 {
			t.Fatalf("unexpected nalu count: %d", len(got.NALUs))
		}
		source[0][0] = 0x00
		if got.NALUs[0][0] != 0x67 {
			t.Fatalf("expected callback to receive a cloned access unit, got %#v", got.NALUs[0])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected raw h264 callback")
	}
}

func TestForceNextKeyFrameDelegatesToSource(t *testing.T) {
	inst := Init(&Config{Logger: stdLogger{}})
	conn := &fakeKeyframeRequesterConn{fakeSourceConn: &fakeSourceConn{done: make(chan struct{})}}
	inst.conn = conn

	if err := inst.ForceNextKeyFrame(); err != nil {
		t.Fatalf("ForceNextKeyFrame: %v", err)
	}
	if conn.forceCalls != 1 {
		t.Fatalf("expected one keyframe request, got %d", conn.forceCalls)
	}
}

func TestWaitForReconnectStopsRecorderOnSourceExit(t *testing.T) {
	inst := Init(&Config{Logger: stdLogger{}})
	inst.hub.SetInit([]byte{0x01, 0x02, 0x03})

	recordPath := filepath.Join(t.TempDir(), "offline-stop.mp4")
	if err := inst.recorder.Start(recordPath, "instant", "stop"); err != nil {
		t.Fatalf("start recorder: %v", err)
	}
	inst.recorder.SetInit([]byte{0x01, 0x02, 0x03})

	conn := &fakeSourceConn{done: make(chan struct{})}
	inst.conn = conn
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- inst.waitForReconnect(ctx, cancel, conn)
	}()

	close(conn.done)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForReconnect: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForReconnect did not return after source exit")
	}

	if inst.conn != nil {
		t.Fatal("expected connection to be cleared after source exit")
	}
	if status := inst.recorder.Status(); status.Active {
		t.Fatal("expected recorder to stop after offline source exit")
	}
}

func TestRecorderOfflineGapRequiresIDRToResumeLive(t *testing.T) {
	recorder := NewRecorder(stdLogger{})
	recorder.SetSourceInfo("h264", 1280, 720, 30)

	recordPath := filepath.Join(t.TempDir(), "black-transition.mp4")
	if err := recorder.Start(recordPath, "instant", "black"); err != nil {
		t.Fatalf("start recorder: %v", err)
	}
	recorder.SetInit([]byte{0x01, 0x02, 0x03})

	recorder.mu.Lock()
	recorder.offlineGap = true
	recorder.mu.Unlock()

	recorder.RecordSample([]byte{0, 0, 0, 1}, 9000, false)
	if got := recorder.Status().BytesWritten; got != 3 {
		t.Fatalf("delta frame should not resume from offline gap, bytes=%d", got)
	}

	recorder.RecordSample([]byte{0, 0, 0, 2}, 9000, true)
	if got := recorder.Status().BytesWritten; got <= 3 {
		t.Fatalf("idr frame should resume recording after offline gap, bytes=%d", got)
	}
}

func TestStartRecordingDefaultsToExactWhenSourceCanForceKeyframe(t *testing.T) {
	inst := Init(&Config{Logger: stdLogger{}})
	inst.hub.SetInit([]byte{0x01, 0x02, 0x03})
	conn := &fakeKeyframeRequesterConn{fakeSourceConn: &fakeSourceConn{done: make(chan struct{})}}
	inst.conn = conn

	recordPath := filepath.Join(t.TempDir(), "exact-default.mp4")
	if err := inst.StartRecording(recordPath, "", "pause"); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}

	if conn.forceCalls != 1 {
		t.Fatalf("expected exact mode to request a keyframe once, got %d", conn.forceCalls)
	}
	if inst.recorder == nil || inst.recorder.mode != "exact" {
		t.Fatalf("expected recorder mode exact, got %q", inst.recorder.mode)
	}
}

func TestStartRecordingFallsBackToInstantWhenKeyframeRequestFails(t *testing.T) {
	inst := Init(&Config{Logger: stdLogger{}})
	inst.hub.SetInit([]byte{0x01, 0x02, 0x03})
	conn := &fakeKeyframeRequesterConn{
		fakeSourceConn: &fakeSourceConn{done: make(chan struct{})},
		forceErr:       errors.New("no keyframe control"),
	}
	inst.conn = conn

	recordPath := filepath.Join(t.TempDir(), "exact-fallback.mp4")
	if err := inst.StartRecording(recordPath, "", "pause"); err != nil {
		t.Fatalf("StartRecording fallback: %v", err)
	}

	if conn.forceCalls != 1 {
		t.Fatalf("expected one keyframe request attempt, got %d", conn.forceCalls)
	}
	if inst.recorder == nil || inst.recorder.mode != "instant" {
		t.Fatalf("expected recorder mode instant after fallback, got %q", inst.recorder.mode)
	}
}

func TestWaitForReconnectTriggersOnReadyWithoutFrames(t *testing.T) {
	inst := Init(&Config{Logger: stdLogger{}})
	inst.hub.SetInit([]byte{0x01, 0x02, 0x03})
	inst.recorder.SetSourceInfo("h264", 1280, 720, 30)

	recordPath := filepath.Join(t.TempDir(), "no-frames-stop.mp4")
	if err := inst.recorder.Start(recordPath, "instant", "stop"); err != nil {
		t.Fatalf("start recorder: %v", err)
	}
	inst.recorder.SetInit([]byte{0x01, 0x02, 0x03})

	conn := &fakeSourceConn{done: make(chan struct{})}
	inst.conn = conn
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	if err := inst.waitForReconnect(ctx, cancel, conn); err != nil {
		t.Fatalf("waitForReconnect: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 450*time.Millisecond {
		t.Fatalf("expected ticker-based reconnect path, returned too early: %s", elapsed)
	}
	if inst.conn != nil {
		t.Fatal("expected connection to be cleared after frame timeout")
	}
	if status := inst.recorder.Status(); status.Active {
		t.Fatal("expected recorder to stop after frame timeout offline transition")
	}
}

func TestKeyframeSinkEnqueueReplacesStaleQueuedFrame(t *testing.T) {
	logger := stdLogger{}
	sink := &keyframeSink{
		cfg:    &Config{StreamName: "demo"},
		logger: logger,
		queue:  make(chan keyframeJob, 1),
	}

	sink.Enqueue("h264", 640, 480, [][]byte{{0x01}}, 1)
	sink.rateMu.Lock()
	sink.lastSave = time.Time{}
	sink.rateMu.Unlock()
	sink.Enqueue("h264", 640, 480, [][]byte{{0x02}}, 2)

	select {
	case job := <-sink.queue:
		if job.frameNo != 2 {
			t.Fatalf("expected newer frame to replace stale queued frame, got %d", job.frameNo)
		}
	default:
		t.Fatal("expected queued keyframe job")
	}
}

func TestKeyframeSinkPersistPayloadWritesSanitizedFilename(t *testing.T) {
	outputDir := t.TempDir()
	sink := &keyframeSink{
		cfg:     &Config{StreamName: "demo/loop"},
		logger:  stdLogger{},
		format:  "jpg",
		writeFS: true,
	}
	sink.cfg.KeyframeOutput = outputDir

	payload := []byte{0x01, 0x02, 0x03}
	if err := sink.persistPayload(7, payload); err != nil {
		t.Fatalf("persistPayload: %v", err)
	}

	path := filepath.Join(outputDir, "demo_loop_000000000007.jpg")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted payload: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("unexpected payload content: %v", data)
	}
}

func TestKeyframeSinkEmitCustomKeyframeCopiesPayloadAndMetadata(t *testing.T) {
	capture := &captureKeyframer{}
	sink := &keyframeSink{
		cfg:             &Config{StreamName: "desk"},
		logger:          stdLogger{},
		format:          "png",
		customKeyframer: capture,
	}

	payload := []byte{0xaa, 0xbb}
	if err := sink.emitCustomKeyframe(12, "h264", 640, 480, payload, true, true, 0.1, 0.2, 0.9); err != nil {
		t.Fatalf("emitCustomKeyframe: %v", err)
	}
	if capture.frame == nil {
		t.Fatal("expected custom keyframer to receive a frame")
	}
	if capture.frame.StreamName != "desk" || capture.frame.FrameNo != 12 || capture.frame.Format != "png" {
		t.Fatalf("unexpected keyframe metadata: %+v", capture.frame)
	}
	payload[0] = 0xff
	if capture.frame.Payload[0] != 0xaa {
		t.Fatal("expected payload copy to be isolated from caller mutation")
	}
}

func TestPublishH264AccessUnitInvokesCustomKeyframerWithoutSinkTarget(t *testing.T) {
	capture := &captureKeyframer{}
	inst := Init(&Config{
		Logger:         stdLogger{},
		StreamName:     "full",
		KeyframeFormat: "h264",
		Keyframer:      capture,
	})
	defer func() {
		if err := inst.Stop(); err != nil {
			t.Fatalf("stop instance: %v", err)
		}
	}()
	if err := inst.UpdateKeyframeCalibration(false, false, 0, 0, 1, ""); err != nil {
		t.Fatalf("disable keyframe transforms: %v", err)
	}

	inst.PublishH264AccessUnit(testH264IDRAccessUnit(), 9000)

	deadline := time.Now().Add(2 * time.Second)
	for capture.frame == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if capture.frame == nil {
		t.Fatal("expected custom keyframer to receive a frame without keyframeSink targets")
	}
	if capture.frame.StreamName != "full" || capture.frame.Format != "h264" {
		t.Fatalf("unexpected keyframe payload metadata: %+v", capture.frame)
	}
	if len(capture.frame.Payload) == 0 {
		t.Fatal("expected custom keyframer payload")
	}
}
