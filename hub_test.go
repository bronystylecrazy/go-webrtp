package webrtp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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

func TestProcessAuRecordsEachSampleOnce(t *testing.T) {
	tmpDir := t.TempDir()
	recordPath := filepath.Join(tmpDir, "recording.mp4")
	initData := []byte{0x01, 0x02, 0x03}
	au := [][]byte{{0x11, 0x22, 0x33}}

	inst := Init(&Config{Logger: stdLogger{}})
	inst.hub.SetInit(initData)

	if err := inst.recorder.Start(recordPath, "instant", "pause"); err != nil {
		t.Fatalf("start recorder: %v", err)
	}
	inst.recorder.SetInit(initData)

	handler := &videoHandler{
		hub:      inst.hub,
		logger:   stdLogger{},
		instance: inst,
	}
	handler.processAu(au, 0, true, true)

	expectedFrag, err := BuildFragment(1, 0, 9000, true, AnnexbToAvcc(au))
	if err != nil {
		t.Fatalf("build expected fragment: %v", err)
	}
	expectedBytes := int64(len(initData) + len(expectedFrag))

	if got := inst.recorder.Status().BytesWritten; got != expectedBytes {
		t.Fatalf("unexpected bytes written: got %d want %d", got, expectedBytes)
	}

	info, err := os.Stat(recordPath)
	if err != nil {
		t.Fatalf("stat recording file: %v", err)
	}
	if info.Size() != expectedBytes {
		t.Fatalf("unexpected recording file size: got %d want %d", info.Size(), expectedBytes)
	}
}

func TestRecorderBlackOfflineSupportContract(t *testing.T) {
	recorder := NewRecorder(stdLogger{})
	recorder.SetSourceInfo("h264", 1280, 720, 30)

	err := recorder.Start(filepath.Join(t.TempDir(), "black.mp4"), "instant", "black")
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "encoder") {
			t.Fatalf("unexpected native black mode error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("expected offlineMode=black to be unsupported without a native encoder backend")
	}
}
