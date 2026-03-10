package webrtp

import "testing"

func TestHubStartupSnapshotCachesLatestGOP(t *testing.T) {
	hub := NewHub()

	initData := []byte{0x01, 0x02, 0x03}
	hub.SetInit(initData)
	hub.Broadcast([]byte{0xaa}, false)
	hub.Broadcast([]byte{0xbb, 0xcc}, true)
	hub.Broadcast([]byte{0xdd}, false)
	hub.Broadcast([]byte{0xee}, false)

	snapshotInit, snapshotFrames := hub.GetStartupSnapshot()
	if string(snapshotInit) != string(initData) {
		t.Fatalf("unexpected init data: %v", snapshotInit)
	}
	if len(snapshotFrames) != 3 {
		t.Fatalf("expected 3 cached frames, got %d", len(snapshotFrames))
	}
	if !snapshotFrames[0].IsKey {
		t.Fatal("expected first startup frame to be a keyframe")
	}
	if snapshotFrames[0].FrameNo != 2 || snapshotFrames[1].FrameNo != 3 || snapshotFrames[2].FrameNo != 4 {
		t.Fatalf("unexpected startup frame numbers: %d, %d, %d", snapshotFrames[0].FrameNo, snapshotFrames[1].FrameNo, snapshotFrames[2].FrameNo)
	}
	if len(snapshotFrames[0].Data) < 9 {
		t.Fatalf("unexpected frame payload length: %d", len(snapshotFrames[0].Data))
	}

	snapshotInit[0] = 0xff
	snapshotFrames[0].Data[8] = 0xff

	nextInit, nextFrames := hub.GetStartupSnapshot()
	if nextInit[0] != initData[0] {
		t.Fatal("startup init should be copied")
	}
	if len(nextFrames) != 3 || nextFrames[0].Data[8] != 0xbb {
		t.Fatal("startup frames should be copied")
	}
}

func TestHubStartupSnapshotResetClearsCachedState(t *testing.T) {
	hub := NewHub()

	hub.SetInit([]byte{0x01})
	hub.Broadcast([]byte{0xaa}, true)
	hub.Reset()

	initData, frame := hub.GetStartupSnapshot()
	if initData != nil {
		t.Fatalf("expected nil init after reset, got %v", initData)
	}
	if frame != nil {
		t.Fatalf("expected nil frame after reset, got %+v", frame)
	}
}

func TestHubSubscribeWithStartupSnapshotQueuesOnlyNewerFrames(t *testing.T) {
	hub := NewHub()

	hub.SetInit([]byte{0x01})
	hub.Broadcast([]byte{0xaa}, true)
	hub.Broadcast([]byte{0xbb}, false)

	_, startupFrames, ch := hub.SubscribeWithStartupSnapshot()
	defer hub.Unsubscribe(ch)

	if len(startupFrames) != 2 {
		t.Fatalf("expected 2 startup frames, got %d", len(startupFrames))
	}

	hub.Broadcast([]byte{0xcc}, false)

	next := <-ch
	if next.FrameNo <= startupFrames[len(startupFrames)-1].FrameNo {
		t.Fatalf("expected only newer live frames, got frame %d after startup frame %d", next.FrameNo, startupFrames[len(startupFrames)-1].FrameNo)
	}
}
