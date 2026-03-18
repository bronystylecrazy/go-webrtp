package webrtp

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strings"
	"time"
)

func (s *keyframeSink) run(workerID int) {
	defer s.closeWg.Done()
	var worker decoderWorker
	defer worker.close()
	for {
		select {
		case <-s.closeCh:
			return
		case job, ok := <-s.queue:
			if !ok {
				return
			}
			if err := s.process(&worker, job); err != nil {
				s.logger.Printf("keyframe sink worker %d frame %d failed: %v", workerID, job.frameNo, err)
			}
		}
	}
}

func (s *keyframeSink) process(worker *decoderWorker, job keyframeJob) error {
	start := time.Now()
	queueDur := time.Duration(0)
	if !job.queuedAt.IsZero() {
		queueDur = start.Sub(job.queuedAt)
	}

	distort, deskEnabled, fx, fy, scale, desk := s.snapshotCalibration()
	if s.format == "h264" {
		return s.processEncodedH264(worker, job, start, queueDur, fx, fy, scale, desk)
	}

	decodeStart := start
	img, err := decodeKeyframe(worker, job.codec, job.width, job.height, job.annexb)
	if err != nil {
		return err
	}
	logDecoderInfo(s.logger, worker)
	decodeDur := time.Since(decodeStart)

	renderStart := time.Now()
	output, renderStats, err := s.renderer.Render(img, fx, fy, scale, desk)
	if err != nil {
		return err
	}
	renderDur := time.Since(renderStart)

	encodeStart := time.Now()
	payload, err := encodeImage(output, s.format)
	if err != nil {
		return err
	}
	encodeDur := time.Since(encodeStart)

	if err := s.persistPayload(job.frameNo, payload); err != nil {
		return err
	}
	if err := s.emitCustomKeyframe(job.frameNo, job.codec, job.width, job.height, payload, distort, deskEnabled, fx, fy, scale); err != nil {
		return err
	}
	publishDur, err := s.publishKeyframe(job.frameNo, payload)
	if err != nil {
		return err
	}
	s.logFrameTimings(job.frameNo, queueDur, decodeDur, renderStats, renderDur, encodeDur, publishDur, start, s.renderer.Name())
	return nil
}

func (s *keyframeSink) processEncodedH264(worker *decoderWorker, job keyframeJob, start time.Time, queueDur time.Duration, fx, fy, scale float64, desk []point) error {
	if strings.TrimSpace(job.codec) != "h264" {
		return fmt.Errorf("keyframeFormat=h264 requires h264 source codec, got %q", job.codec)
	}
	decodeDur := time.Duration(0)
	renderDur := time.Duration(0)
	encodeDur := time.Duration(0)
	renderStats := keyframeRenderStats{}
	payload := append([]byte(nil), job.annexb...)
	rendererName := "passthrough"
	if fx != 0 || fy != 0 || normalizedScale(scale) != 1 || len(desk) != 0 {
		if worker == nil {
			return fmt.Errorf("missing decoder worker for transformed h264 output")
		}
		decodeStart := time.Now()
		img, err := decodeKeyframe(worker, job.codec, job.width, job.height, job.annexb)
		if err != nil {
			return err
		}
		logDecoderInfo(s.logger, worker)
		decodeDur = time.Since(decodeStart)
		renderStart := time.Now()
		output, stats, err := s.renderer.Render(img, fx, fy, scale, desk)
		if err != nil {
			return err
		}
		renderDur = time.Since(renderStart)
		renderStats = stats
		encodeStart := time.Now()
		payload, err = encodeH264Image(worker, output)
		if err != nil {
			return err
		}
		encodeDur = time.Since(encodeStart)
		rendererName = s.renderer.Name()
	}

	if err := s.persistPayload(job.frameNo, payload); err != nil {
		return err
	}
	if err := s.emitCustomKeyframe(job.frameNo, job.codec, job.width, job.height, payload, fx != 0 || fy != 0 || normalizedScale(scale) != 1, len(desk) != 0, fx, fy, scale); err != nil {
		return err
	}
	publishDur, err := s.publishKeyframe(job.frameNo, payload)
	if err != nil {
		return err
	}
	s.logFrameTimings(job.frameNo, queueDur, decodeDur, renderStats, renderDur, encodeDur, publishDur, start, rendererName)
	return nil
}

func (s *keyframeSink) logFrameTimings(frameNo uint32, queueDur, decodeDur time.Duration, renderStats keyframeRenderStats, renderDur, encodeDur, publishDur time.Duration, start time.Time, rendererName string) {
	s.logger.Printf(
		"keyframe sink frame %d timings decode=%s undistort=%s rectify=%s render=%s encode=%s publish=%s total=%s renderer=%s",
		frameNo,
		decodeDur.Round(time.Millisecond),
		renderStats.Undistort.Round(time.Millisecond),
		renderStats.Rectify.Round(time.Millisecond),
		renderDur.Round(time.Millisecond),
		encodeDur.Round(time.Millisecond),
		publishDur.Round(time.Millisecond),
		time.Since(start).Round(time.Millisecond),
		rendererName,
	)
	if queueDur > 0 {
		s.logger.Printf("keyframe sink frame %d queue=%s", frameNo, queueDur.Round(time.Millisecond))
	}
}

func logDecoderInfo(logger Logger, worker *decoderWorker) {
	if worker == nil || worker.h264DiagLog {
		return
	}
	dbg, ok := worker.h264.(nativeH264DecoderDebug)
	if !ok {
		return
	}
	if info := strings.TrimSpace(dbg.DebugInfo()); info != "" {
		logger.Printf("keyframe decoder info: %s", info)
		worker.h264DiagLog = true
	}
}

func decodeKeyframe(worker *decoderWorker, codec string, width, height int, annexb []byte) (image.Image, error) {
	if len(annexb) == 0 || width <= 0 || height <= 0 {
		return nil, fmt.Errorf("missing annexb payload")
	}
	switch codec {
	case "h264":
		if worker != nil {
			img, err := worker.decodeH264(annexb)
			if err == nil && img != nil {
				return img, nil
			}
		}
		return decodeKeyframeFFmpeg("h264", width, height, annexb)
	case "h265", "hevc":
		return decodeKeyframeFFmpeg("hevc", width, height, annexb)
	default:
		return nil, fmt.Errorf("unsupported codec %q", codec)
	}
}

func decodeKeyframeFFmpeg(inputFormat string, width, height int, annexb []byte) (image.Image, error) {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-f", inputFormat,
		"-i", "pipe:0",
		"-frames:v", "1",
		"-pix_fmt", "rgb24",
		"-f", "rawvideo",
		"pipe:1",
	}
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdin = bytes.NewReader(annexb)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg decode: %s", msg)
	}
	img, err := readRawFrame(bytes.NewReader(stdout.Bytes()), width, height)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg raw frame read: %w", err)
	}
	return img, nil
}

func encodeH264Image(worker *decoderWorker, img image.Image) ([]byte, error) {
	if worker == nil {
		return nil, fmt.Errorf("missing decoder worker")
	}
	if worker.h264Encoder == nil {
		enc, err := newNativeH264FrameEncoder()
		if err != nil {
			return nil, err
		}
		worker.h264Encoder = enc
	}
	return worker.h264Encoder.Encode(imageToRGBA(img))
}

func readRawFrame(reader io.Reader, width, height int) (image.Image, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid frame size %dx%d", width, height)
	}
	buf := make([]byte, width*height*3)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	src := 0
	dst := 0
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Pix[dst+0] = buf[src+0]
			img.Pix[dst+1] = buf[src+1]
			img.Pix[dst+2] = buf[src+2]
			img.Pix[dst+3] = 0xFF
			src += 3
			dst += 4
		}
	}
	return img, nil
}
