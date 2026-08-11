package usbauto

import "testing"

func TestStaticStreamDetectorFiresOnceOnSkipFrameRun(t *testing.T) {
	detector := newStaticStreamDetector()
	events := map[staticStreamEvent]int{}
	// * two chunks of frozen stream shaped like the real capture, tiny skips with a large keyframe each gop
	for i := 0; i < staticStreamChunkSize*2; i++ {
		size := 171
		if i%60 == 0 {
			size = 22290
		}
		events[detector.Push(size)]++
	}
	if events[staticStreamBecameStatic] != 1 {
		t.Fatalf("expected exactly one static event, got %d", events[staticStreamBecameStatic])
	}
	if events[staticStreamBecameActive] != 0 {
		t.Fatalf("expected no active event, got %d", events[staticStreamBecameActive])
	}
	if !detector.Static() {
		t.Fatal("expected detector to report static")
	}
}

func TestStaticStreamDetectorIgnoresMovingContent(t *testing.T) {
	detector := newStaticStreamDetector()
	for i := 0; i < staticStreamChunkSize*3; i++ {
		size := 30000
		if i%7 == 0 {
			size = 900
		}
		if event := detector.Push(size); event != staticStreamNone {
			t.Fatalf("unexpected event %v at sample %d", event, i)
		}
	}
}

func TestStaticStreamDetectorReportsRecovery(t *testing.T) {
	detector := newStaticStreamDetector()
	for i := 0; i < staticStreamChunkSize; i++ {
		detector.Push(171)
	}
	if !detector.Static() {
		t.Fatal("expected static after frozen chunk")
	}
	var recovered bool
	for i := 0; i < staticStreamChunkSize; i++ {
		if detector.Push(28000) == staticStreamBecameActive {
			recovered = true
		}
	}
	if !recovered {
		t.Fatal("expected recovery event once motion resumes")
	}
}

func TestStaticStreamDetectorToleratesLowMotionScenes(t *testing.T) {
	detector := newStaticStreamDetector()
	// * a real static bench camera still carries sensor noise well above skip size
	for i := 0; i < staticStreamChunkSize*2; i++ {
		if event := detector.Push(4000); event != staticStreamNone {
			t.Fatalf("real low motion content must not alarm, got %v", event)
		}
	}
}
