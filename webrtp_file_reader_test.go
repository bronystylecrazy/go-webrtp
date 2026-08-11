package webrtp

import (
	"bytes"
	"io"
	"testing"
	"testing/iotest"
)

func annexbStream(nalus ...[]byte) []byte {
	var buf bytes.Buffer
	for idx, nalu := range nalus {
		if idx%2 == 0 {
			buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
		} else {
			buf.Write([]byte{0x00, 0x00, 0x01})
		}
		buf.Write(nalu)
	}
	return buf.Bytes()
}

func readAllNalus(t *testing.T, rd io.Reader) [][]byte {
	reader := newAnnexBNALUReader(rd)
	nalus := make([][]byte, 0)
	for {
		nalu, err := reader.Next()
		if err == io.EOF {
			return nalus
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		nalus = append(nalus, nalu)
	}
}

func TestAnnexBNALUReaderSplitsMixedStartCodes(t *testing.T) {
	want := [][]byte{{0x67, 0x01}, {0x68, 0x02}, {0x65, 0x03, 0x04}}
	got := readAllNalus(t, bytes.NewReader(annexbStream(want...)))
	if len(got) != len(want) {
		t.Fatalf("expected %d nalus, got %d", len(want), len(got))
	}
	for idx := range want {
		if !bytes.Equal(got[idx], want[idx]) {
			t.Fatalf("nalu %d mismatch: %x vs %x", idx, got[idx], want[idx])
		}
	}
}

func TestAnnexBNALUReaderHandlesOneByteReads(t *testing.T) {
	want := [][]byte{{0x67, 0xAA}, {0x65, 0x00, 0x00, 0x02, 0xBB}}
	got := readAllNalus(t, iotest.OneByteReader(bytes.NewReader(annexbStream(want...))))
	if len(got) != len(want) {
		t.Fatalf("expected %d nalus, got %d", len(want), len(got))
	}
	for idx := range want {
		if !bytes.Equal(got[idx], want[idx]) {
			t.Fatalf("nalu %d mismatch: %x vs %x", idx, got[idx], want[idx])
		}
	}
}

func TestAnnexBNALUReaderSkipsGarbagePrefix(t *testing.T) {
	stream := append(bytes.Repeat([]byte{0xFF}, 300*1024), annexbStream([]byte{0x65, 0x01})...)
	got := readAllNalus(t, bytes.NewReader(stream))
	if len(got) != 1 || !bytes.Equal(got[0], []byte{0x65, 0x01}) {
		t.Fatalf("unexpected nalus: %x", got)
	}
}

func TestAnnexBNALUReaderLargeNaluAcrossManyFills(t *testing.T) {
	large := bytes.Repeat([]byte{0x42}, 3*1024*1024)
	large[0] = 0x65
	want := [][]byte{large, {0x67, 0x01}}
	got := readAllNalus(t, bytes.NewReader(annexbStream(want...)))
	if len(got) != 2 || !bytes.Equal(got[0], want[0]) || !bytes.Equal(got[1], want[1]) {
		t.Fatalf("large nalu mismatch: %d nalus, first len %d", len(got), len(got[0]))
	}
}

func BenchmarkAnnexBNALUReaderLargeNalus(b *testing.B) {
	nalu := bytes.Repeat([]byte{0x37}, 1024*1024)
	nalu[0] = 0x65
	stream := annexbStream(nalu, nalu, nalu, nalu)
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		reader := newAnnexBNALUReader(bytes.NewReader(stream))
		for {
			if _, err := reader.Next(); err != nil {
				break
			}
		}
	}
}
