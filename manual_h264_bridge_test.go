package webrtp

import (
	"context"
	"testing"
	"time"
)

func TestH264BridgePublishesToHub(t *testing.T) {
	bridge := NewH264Bridge(&Config{StreamName: "preview", Logger: stdLogger{}})
	defer func() {
		if err := bridge.Close(); err != nil {
			t.Fatalf("bridge close: %v", err)
		}
	}()

	stream := make(chan H264AccessUnit, 1)
	done := make(chan error, 1)
	go func() {
		done <- bridge.Pump(context.Background(), stream)
	}()

	stream <- H264AccessUnit{NALUs: testH264IDRAccessUnit(), PTS90k: 9000}
	close(stream)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("pump: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not return after stream close")
	}

	if !bridge.Instance().InstanceReady() {
		t.Fatal("expected bridge instance to become ready after first IDR access unit")
	}

	initData, startupFrames := bridge.Instance().GetHub().GetStartupSnapshot()
	if len(initData) == 0 {
		t.Fatal("expected init segment in startup snapshot")
	}
	if len(startupFrames) == 0 {
		t.Fatal("expected startup frame after publishing preview access unit")
	}
	if !startupFrames[0].IsKey {
		t.Fatal("expected startup frame to be marked as keyframe")
	}
}

func TestH264FanoutClonesPerOutput(t *testing.T) {
	fanout := NewH264Fanout(2, 1)
	input := make(chan H264AccessUnit, 1)
	done := make(chan error, 1)
	go func() {
		done <- fanout.Run(context.Background(), input)
	}()

	input <- H264AccessUnit{NALUs: testH264IDRAccessUnit(), PTS90k: 1234}
	close(input)

	first, ok := <-fanout.Output(0)
	if !ok {
		t.Fatal("expected first fanout output item")
	}
	second, ok := <-fanout.Output(1)
	if !ok {
		t.Fatal("expected second fanout output item")
	}

	if first.PTS90k != 1234 || second.PTS90k != 1234 {
		t.Fatalf("unexpected fanout timestamps: %d %d", first.PTS90k, second.PTS90k)
	}

	first.NALUs[0][0] = 0xFF
	if second.NALUs[0][0] == 0xFF {
		t.Fatal("expected fanout to deep-copy access units per output")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fanout run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fanout did not return after input close")
	}
}

func TestH264KeyframeSamplerAcceptsOneIDRPerInterval(t *testing.T) {
	sampler := NewH264KeyframeSampler(1)
	idr := H264AccessUnit{NALUs: testH264IDRAccessUnit()}
	nonIDR := H264AccessUnit{NALUs: [][]byte{{0x41, 0x9A, 0x22}}}

	if sampler.Accept(nonIDR) {
		t.Fatal("expected non-IDR access unit to be rejected")
	}
	if !sampler.Accept(idr) {
		t.Fatal("expected first IDR access unit to be accepted")
	}
	if sampler.Accept(idr) {
		t.Fatal("expected second IDR access unit in same interval to be rejected")
	}

	sampler.mu.Lock()
	sampler.lastUnix = time.Now().Unix() - sampler.interval
	sampler.mu.Unlock()

	if !sampler.Accept(idr) {
		t.Fatal("expected IDR access unit to be accepted after interval elapsed")
	}
}

func testH264IDRAccessUnit() [][]byte {
	return [][]byte{
		{0x67, 0x42, 0xC0, 0x1E, 0xDA, 0x01, 0x40, 0x16, 0xEC, 0x04, 0x40, 0x00, 0x00, 0x03, 0x00, 0x40, 0x00, 0x00, 0x0C, 0x83, 0xC5, 0x0A, 0xA8},
		{0x68, 0xCE, 0x3C, 0x80},
		{0x65, 0x88, 0x84, 0x21, 0xA0},
	}
}
