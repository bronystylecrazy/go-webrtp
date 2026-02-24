package webrtp

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

// BuildInitH264 creates an fMP4 init segment for H264 video.
func BuildInitH264(sps, pps []byte) ([]byte, error) {
	init := mp4.CreateEmptyInit()
	trak := init.AddEmptyTrack(90000, "video", "und")
	if err := trak.SetAVCDescriptor("avc1", [][]byte{sps}, [][]byte{pps}, true); err != nil {
		return nil, fmt.Errorf("SetAVCDescriptor: %w", err)
	}
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode init: %w", err)
	}
	return buf.Bytes(), nil
}

// BuildInitH265 creates an fMP4 init segment for H265 video.
func BuildInitH265(vps, sps, pps []byte) ([]byte, error) {
	init := mp4.CreateEmptyInit()
	trak := init.AddEmptyTrack(90000, "video", "und")
	if err := trak.SetHEVCDescriptor("hvc1", [][]byte{vps}, [][]byte{sps}, [][]byte{pps}, nil, true); err != nil {
		return nil, fmt.Errorf("SetHEVCDescriptor: %w", err)
	}
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode init: %w", err)
	}
	return buf.Bytes(), nil
}

// AnnexBtoAVCC converts Annex-B NAL units to AVCC format.
func AnnexBtoAVCC(au [][]byte) []byte {
	var buf bytes.Buffer
	for _, nalu := range au {
		ln := make([]byte, 4)
		binary.BigEndian.PutUint32(ln, uint32(len(nalu)))
		buf.Write(ln)
		buf.Write(nalu)
	}
	return buf.Bytes()
}

// BuildFragment creates an fMP4 media fragment.
func BuildFragment(seqNr uint32, dts uint64, dur uint32, isIDR bool, avcc []byte) ([]byte, error) {
	seg := mp4.NewMediaSegment()
	frag, err := mp4.CreateFragment(seqNr, mp4.DefaultTrakID)
	if err != nil {
		return nil, fmt.Errorf("CreateFragment: %w", err)
	}
	seg.AddFragment(frag)
	flags := mp4.NonSyncSampleFlags
	if isIDR {
		flags = mp4.SyncSampleFlags
	}
	frag.AddFullSample(mp4.FullSample{
		Sample: mp4.Sample{
			Flags:                 flags,
			Dur:                   dur,
			Size:                  uint32(len(avcc)),
			CompositionTimeOffset: 0,
		},
		DecodeTime: dts,
		Data:       avcc,
	})
	var buf bytes.Buffer
	if err := seg.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode segment: %w", err)
	}
	return buf.Bytes(), nil
}
