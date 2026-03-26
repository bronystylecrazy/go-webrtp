package webrtp

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *keyframeSink) PublishDeskViewMetadata(topic string, payload []byte) error {
	if s == nil || !s.publishMQTT || s.mqttPublisher == nil || len(payload) == 0 || strings.TrimSpace(topic) == "" {
		return nil
	}
	if tableID := tableIDFromInspectionTopic(topic); tableID > 0 {
		s.deskMetaMu.Lock()
		s.snapshotTableID = tableID
		s.deskMetaMu.Unlock()
	}
	s.deskMetaMu.Lock()
	s.deskMeta = &deskViewMetadataJob{
		topic:   strings.TrimSpace(topic),
		payload: append([]byte(nil), payload...),
	}
	s.deskMetaMu.Unlock()
	select {
	case s.deskMetaWake <- struct{}{}:
	default:
	}
	return nil
}

func (s *keyframeSink) runDeskMetadataPublisher() {
	defer s.closeWg.Done()
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-s.closeCh:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.deskMetaWake:
			s.deskMetaMu.Lock()
			hasPending := s.deskMeta != nil
			wait := time.Second - time.Since(s.deskMetaLast)
			s.deskMetaMu.Unlock()
			if !hasPending {
				continue
			}
			if wait <= 0 {
				s.flushDeskMetadata()
				continue
			}
			if timer == nil {
				timer = time.NewTimer(wait)
				timerC = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(wait)
			}
		case <-timerC:
			s.flushDeskMetadata()
			timerC = nil
		}
	}
}

func (s *keyframeSink) flushDeskMetadata() {
	s.deskMetaMu.Lock()
	job := s.deskMeta
	s.deskMeta = nil
	s.deskMetaMu.Unlock()
	if job == nil || s.mqttPublisher == nil {
		return
	}
	if err := s.mqttPublisher.PublishRawTopic(job.topic, job.payload); err != nil {
		s.logger.Printf("desk metadata publish failed: %v", err)
		return
	}
	s.deskMetaMu.Lock()
	s.deskMetaLast = time.Now()
	s.deskMetaMu.Unlock()
}

func (s *keyframeSink) snapshotTable() int {
	s.deskMetaMu.Lock()
	defer s.deskMetaMu.Unlock()
	if s.snapshotTableID <= 0 {
		return 1
	}
	return s.snapshotTableID
}

func tableIDFromInspectionTopic(topic string) int {
	parts := strings.Split(strings.Trim(topic, "/"), "/")
	if len(parts) < 5 {
		return 0
	}
	if parts[0] != "v1" || parts[1] != "tables" || parts[3] != "inspections" || parts[4] != "request" {
		return 0
	}
	value := 0
	for _, ch := range parts[2] {
		if ch < '0' || ch > '9' {
			return 0
		}
		value = value*10 + int(ch-'0')
	}
	return value
}

func (s *keyframeSink) persistPayload(frameNo uint32, payload []byte) error {
	if !s.writeFS {
		return nil
	}
	path := filepathForFrame(s.cfg.KeyframeOutput, s.cfg.StreamName, frameNo, s.format)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return err
	}
	s.logger.Printf("keyframe sink saved frame %d to %s", frameNo, path)
	return nil
}

func (s *keyframeSink) publishKeyframe(frameNo uint32, payload []byte) (time.Duration, error) {
	if !s.publishMQTT || s.mqttPublisher == nil {
		return 0, nil
	}
	publishStart := time.Now()
	topic, err := s.mqttPublisher.PublishDeskSnapshot(s.snapshotTable(), frameNo, payload)
	if err != nil {
		return 0, err
	}
	s.logger.Printf("keyframe sink published frame %d to mqtt topic %s", frameNo, topic)
	return time.Since(publishStart), nil
}

func (s *keyframeSink) emitCustomKeyframe(frameNo uint32, codec string, width, height int, payload []byte, distort, deskEnabled bool, fx, fy, scale float64) error {
	if s.customKeyframer == nil {
		return nil
	}
	return s.customKeyframer.HandleKeyframe(&Keyframe{
		StreamName:  s.cfg.StreamName,
		FrameNo:     frameNo,
		Codec:       codec,
		Format:      s.format,
		Width:       width,
		Height:      height,
		Payload:     append([]byte(nil), payload...),
		Distort:     distort,
		DeskEnabled: deskEnabled,
		Fx:          fx,
		Fy:          fy,
		Scale:       scale,
		PublishedAt: time.Now(),
	})
}

func filepathForFrame(outputDir, streamName string, frameNo uint32, format string) string {
	name := sanitizeName(streamName)
	if name == "" {
		name = "stream"
	}
	return filepath.Join(outputDir, fmt.Sprintf("%s_%012d.%s", name, frameNo, format))
}

func writeImage(path string, img image.Image, format string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	switch format {
	case "png":
		return png.Encode(file, img)
	case "jpg":
		return jpeg.Encode(file, img, &jpeg.Options{Quality: 90})
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func encodeImage(img image.Image, format string) ([]byte, error) {
	quality := defaultKeyframeJPEGQuality()
	if payload, ok, err := tryEncodeImageNative(img, format, quality); ok {
		return payload, err
	}
	buf := imageEncodeBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer imageEncodeBufferPool.Put(buf)
	switch format {
	case "png":
		if err := png.Encode(buf, img); err != nil {
			return nil, err
		}
	case "jpg":
		if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
	return append([]byte(nil), buf.Bytes()...), nil
}
