package webrtp

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type keyframeSink struct {
	cfg           *Config
	logger        Logger
	format        string
	renderer      keyframeRenderer
	queue         chan keyframeJob
	closeCh       chan struct{}
	closeWg       sync.WaitGroup
	dropOnce      sync.Once
	workers       int
	stateMu       sync.RWMutex
	fx            float64
	fy            float64
	scale         float64
	desk          []point
	rateMu        sync.Mutex
	lastSave      time.Time
	writeFS       bool
	publishMQTT   bool
	mqttPublisher *mqttPublisher
}

var imageEncodeBufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

type decoderWorker struct {
	h264 nativeH264Decoder
}

type keyframeJob struct {
	codec   string
	width   int
	height  int
	annexb  []byte
	frameNo uint32
}

type point struct {
	x float64
	y float64
}

func newKeyframeSink(cfg *Config, logger Logger) *keyframeSink {
	if cfg == nil {
		return nil
	}
	targets, err := parseKeyframeSinkTargets(cfg.KeyframeSink)
	if err != nil {
		logger.Printf("keyframe sink disabled: %v", err)
		return nil
	}
	if len(targets) == 0 {
		return nil
	}
	if targets["fs"] && strings.TrimSpace(cfg.KeyframeOutput) == "" {
		logger.Printf("keyframe sink disabled: fs sink requires keyframeOutput")
		return nil
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		logger.Printf("keyframe sink disabled: ffmpeg not found: %v", err)
		return nil
	}
	format := strings.ToLower(strings.TrimSpace(cfg.KeyframeFormat))
	if format == "" {
		format = "jpg"
	}
	if format == "jpeg" {
		format = "jpg"
	}
	if format != "jpg" && format != "png" {
		logger.Printf("keyframe sink disabled: unsupported format %q", format)
		return nil
	}
	if targets["fs"] {
		if err := os.MkdirAll(cfg.KeyframeOutput, 0o755); err != nil {
			logger.Printf("keyframe sink disabled: create output dir: %v", err)
			return nil
		}
	}
	sink := &keyframeSink{
		cfg:         cfg,
		logger:      logger,
		format:      format,
		renderer:    newKeyframeRenderer(logger),
		scale:       1,
		queue:       make(chan keyframeJob, 8),
		closeCh:     make(chan struct{}),
		workers:     3,
		writeFS:     targets["fs"],
		publishMQTT: targets["mqtt"],
	}
	if sink.publishMQTT {
		publisher, err := newMQTTPublisher(cfg, logger)
		if err != nil {
			logger.Printf("keyframe sink disabled: mqtt publisher: %v", err)
			return nil
		}
		sink.mqttPublisher = publisher
	}
	for i := 0; i < sink.workers; i++ {
		sink.closeWg.Add(1)
		go sink.run(i)
	}
	return sink
}

func (s *keyframeSink) UpdateCalibration(fx, fy, scale float64, deskRaw string) error {
	if s == nil {
		return nil
	}
	desk, err := parseDeskPoints(deskRaw)
	if err != nil {
		return err
	}
	s.stateMu.Lock()
	s.fx = fx
	s.fy = fy
	s.scale = normalizedScale(scale)
	s.desk = desk
	s.stateMu.Unlock()
	return nil
}

func (s *keyframeSink) Close() {
	if s == nil {
		return
	}
	s.dropOnce.Do(func() {
		close(s.closeCh)
		s.closeWg.Wait()
		if s.mqttPublisher != nil {
			s.mqttPublisher.Close()
		}
		if s.renderer != nil {
			s.renderer.Close()
		}
	})
}

func (s *keyframeSink) Enqueue(codec string, width, height int, au [][]byte, frameNo uint32) {
	if s == nil || len(au) == 0 || width <= 0 || height <= 0 {
		return
	}
	now := time.Now()
	s.rateMu.Lock()
	if !s.lastSave.IsZero() && now.Sub(s.lastSave) < time.Second {
		s.rateMu.Unlock()
		return
	}
	s.lastSave = now
	s.rateMu.Unlock()
	job := keyframeJob{
		codec:   strings.ToLower(strings.TrimSpace(codec)),
		width:   width,
		height:  height,
		annexb:  annexbFromAU(au),
		frameNo: frameNo,
	}
	if job.codec == "" || len(job.annexb) == 0 {
		return
	}
	select {
	case s.queue <- job:
	default:
		s.rateMu.Lock()
		s.lastSave = time.Time{}
		s.rateMu.Unlock()
		s.logger.Printf("keyframe sink queue full, dropping frame %d", frameNo)
	}
}

func (s *keyframeSink) run(workerID int) {
	defer s.closeWg.Done()
	var worker decoderWorker
	defer worker.close()
	_ = worker
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
	decodeStart := start
	img, err := decodeKeyframe(worker, job.codec, job.width, job.height, job.annexb)
	if err != nil {
		return err
	}
	decodeDur := time.Since(decodeStart)
	s.stateMu.RLock()
	fx := s.fx
	fy := s.fy
	scale := s.scale
	desk := append([]point(nil), s.desk...)
	s.stateMu.RUnlock()
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
	streamName := sanitizeName(s.cfg.StreamName)
	if streamName == "" {
		streamName = "stream"
	}
	if s.writeFS {
		path := filepath.Join(s.cfg.KeyframeOutput, fmt.Sprintf("%s_%012d.%s", streamName, job.frameNo, s.format))
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			return err
		}
		s.logger.Printf("keyframe sink saved frame %d to %s", job.frameNo, path)
	}
	if s.publishMQTT && s.mqttPublisher != nil {
		publishStart := time.Now()
		if err := s.mqttPublisher.Publish(job.frameNo, s.format, payload); err != nil {
			return err
		}
		publishDur := time.Since(publishStart)
		s.logger.Printf("keyframe sink published frame %d to mqtt topic %s", job.frameNo, s.mqttPublisher.Topic())
		s.logger.Printf(
			"keyframe sink frame %d timings decode=%s undistort=%s rectify=%s render=%s encode=%s publish=%s total=%s renderer=%s",
			job.frameNo,
			decodeDur.Round(time.Millisecond),
			renderStats.Undistort.Round(time.Millisecond),
			renderStats.Rectify.Round(time.Millisecond),
			renderDur.Round(time.Millisecond),
			encodeDur.Round(time.Millisecond),
			publishDur.Round(time.Millisecond),
			time.Since(start).Round(time.Millisecond),
			s.renderer.Name(),
		)
		return nil
	}
	s.logger.Printf(
		"keyframe sink frame %d timings decode=%s undistort=%s rectify=%s render=%s encode=%s publish=%s total=%s renderer=%s",
		job.frameNo,
		decodeDur.Round(time.Millisecond),
		renderStats.Undistort.Round(time.Millisecond),
		renderStats.Rectify.Round(time.Millisecond),
		renderDur.Round(time.Millisecond),
		encodeDur.Round(time.Millisecond),
		time.Duration(0),
		time.Since(start).Round(time.Millisecond),
		s.renderer.Name(),
	)
	return nil
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
	}
	args = append(args,
		"-f", inputFormat,
		"-i", "pipe:0",
		"-frames:v", "1",
		"-pix_fmt", "rgb24",
		"-f", "rawvideo",
		"pipe:1",
	)
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

func normalizedScale(scale float64) float64 {
	if scale <= 0 {
		return 1
	}
	return scale
}

func parseKeyframeSinkTargets(raw string) (map[string]bool, error) {
	targets := make(map[string]bool)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return targets, nil
	}
	for _, part := range strings.Split(raw, ",") {
		target := strings.ToLower(strings.TrimSpace(part))
		if target == "" {
			continue
		}
		switch target {
		case "fs", "mqtt":
			targets[target] = true
		default:
			return nil, fmt.Errorf("unsupported keyframe sink %q", target)
		}
	}
	return targets, nil
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, " ", "_")
	return name
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
	buf := imageEncodeBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer imageEncodeBufferPool.Put(buf)
	switch format {
	case "png":
		if err := png.Encode(buf, img); err != nil {
			return nil, err
		}
	case "jpg":
		if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: 100}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
	return append([]byte(nil), buf.Bytes()...), nil
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

func annexbFromAU(au [][]byte) []byte {
	var out []byte
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, nalu...)
	}
	return out
}

func parseDeskPoints(raw string) ([]point, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ";")
	if len(parts) != 4 {
		return nil, fmt.Errorf("expected 4 points, got %d", len(parts))
	}
	points := make([]point, 0, 4)
	for _, part := range parts {
		var p point
		if _, err := fmt.Sscanf(strings.TrimSpace(part), "%f,%f", &p.x, &p.y); err != nil {
			return nil, fmt.Errorf("parse point %q: %w", part, err)
		}
		if p.x < 0 || p.x > 1 || p.y < 0 || p.y > 1 {
			return nil, fmt.Errorf("point %q out of range [0,1]", part)
		}
		points = append(points, p)
	}
	return points, nil
}

func applyUndistortion(src image.Image, fx, fy, scale float64) *image.RGBA {
	srcRGBA := imageToRGBA(src)
	bounds := srcRGBA.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return dst
	}
	parallelizeRows(height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			v := float64(y) / float64(maxInt(1, height-1))
			dstRow := y * dst.Stride
			for x := 0; x < width; x++ {
				u := float64(x) / float64(maxInt(1, width-1))
				srcU, srcV := distortedSourceUV(u, v, fx, fy, scale)
				c := sampleBilinearRGBA(srcRGBA, srcU, srcV)
				dstPix := dstRow + x*4
				dst.Pix[dstPix+0] = c.R
				dst.Pix[dstPix+1] = c.G
				dst.Pix[dstPix+2] = c.B
				dst.Pix[dstPix+3] = 0xFF
			}
		}
	})
	return dst
}

func distortedSourceUV(u, v, fx, fy, scale float64) (float64, float64) {
	clipX := u*2 - 1
	clipY := 1 - v*2
	mappedX := clipX + (((clipY*clipY)/scale)*clipX/scale)*-fx
	mappedY := clipY + (((mappedX*mappedX)/scale)*clipY/scale)*-fy
	mappedX /= scale
	mappedY /= scale
	return clamp01((mappedX + 1) / 2), clamp01((1 - mappedY) / 2)
}

func remapDeskToUndistorted(desk []point, fx, fy, scale float64) []point {
	if len(desk) == 0 {
		return nil
	}
	out := make([]point, 0, len(desk))
	for _, p := range desk {
		out = append(out, sourceToDisplayTexturePoint(p, fx, fy, scale))
	}
	return out
}

func sourceToDisplayTexturePoint(p point, fx, fy, scale float64) point {
	targetClipX := clamp01(p.x)*2 - 1
	targetClipY := 1 - clamp01(p.y)*2
	guessX := targetClipX
	guessY := targetClipY
	for i := 0; i < 12; i++ {
		mappedX, mappedY := sampleClipFromOutputClip(guessX, guessY, fx, fy, scale)
		guessX += (targetClipX - mappedX) * 0.7
		guessY += (targetClipY - mappedY) * 0.7
		guessX = clampFloat(guessX, -1.2, 1.2)
		guessY = clampFloat(guessY, -1.2, 1.2)
	}
	return point{
		x: clamp01((guessX + 1) / 2),
		y: clamp01((1 - guessY) / 2),
	}
}

func sampleClipFromOutputClip(x, y, fx, fy, scale float64) (float64, float64) {
	if scale <= 0 {
		scale = 1
	}
	x = x + (((y*y)/scale)*x/scale)*-fx
	y = y + (((x*x)/scale)*y/scale)*-fy
	return x / scale, y / scale
}

func sampleBilinear(img image.Image, u, v float64) color.RGBA {
	b := img.Bounds()
	width := b.Dx()
	height := b.Dy()
	if width <= 0 || height <= 0 {
		return color.RGBA{}
	}
	fx := u * float64(width-1)
	fy := v * float64(height-1)
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	x1 := minInt(x0+1, width-1)
	y1 := minInt(y0+1, height-1)
	tx := fx - float64(x0)
	ty := fy - float64(y0)
	c00 := color.RGBAModel.Convert(img.At(b.Min.X+x0, b.Min.Y+y0)).(color.RGBA)
	c10 := color.RGBAModel.Convert(img.At(b.Min.X+x1, b.Min.Y+y0)).(color.RGBA)
	c01 := color.RGBAModel.Convert(img.At(b.Min.X+x0, b.Min.Y+y1)).(color.RGBA)
	c11 := color.RGBAModel.Convert(img.At(b.Min.X+x1, b.Min.Y+y1)).(color.RGBA)
	return color.RGBA{
		R: lerp2D(c00.R, c10.R, c01.R, c11.R, tx, ty),
		G: lerp2D(c00.G, c10.G, c01.G, c11.G, tx, ty),
		B: lerp2D(c00.B, c10.B, c01.B, c11.B, tx, ty),
		A: lerp2D(c00.A, c10.A, c01.A, c11.A, tx, ty),
	}
}

func sampleBilinearRGBA(img *image.RGBA, u, v float64) color.RGBA {
	b := img.Bounds()
	width := b.Dx()
	height := b.Dy()
	if width <= 0 || height <= 0 {
		return color.RGBA{}
	}
	fx := u * float64(width-1)
	fy := v * float64(height-1)
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	x1 := minInt(x0+1, width-1)
	y1 := minInt(y0+1, height-1)
	tx := fx - float64(x0)
	ty := fy - float64(y0)
	c00 := rgbaAt(img, x0, y0)
	c10 := rgbaAt(img, x1, y0)
	c01 := rgbaAt(img, x0, y1)
	c11 := rgbaAt(img, x1, y1)
	return color.RGBA{
		R: lerp2D(c00.R, c10.R, c01.R, c11.R, tx, ty),
		G: lerp2D(c00.G, c10.G, c01.G, c11.G, tx, ty),
		B: lerp2D(c00.B, c10.B, c01.B, c11.B, tx, ty),
		A: lerp2D(c00.A, c10.A, c01.A, c11.A, tx, ty),
	}
}

func rgbaAt(img *image.RGBA, x, y int) color.RGBA {
	idx := y*img.Stride + x*4
	return color.RGBA{
		R: img.Pix[idx+0],
		G: img.Pix[idx+1],
		B: img.Pix[idx+2],
		A: img.Pix[idx+3],
	}
}

func lerp2D(c00, c10, c01, c11 uint8, tx, ty float64) uint8 {
	top := float64(c00)*(1-tx) + float64(c10)*tx
	bottom := float64(c01)*(1-tx) + float64(c11)*tx
	value := top*(1-ty) + bottom*ty
	if value <= 0 {
		return 0
	}
	if value >= 255 {
		return 255
	}
	return uint8(math.Round(value))
}

func clampFloat(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func rectifyDeskView(src image.Image, normalizedQuad []point) (image.Image, error) {
	if len(normalizedQuad) != 4 {
		return nil, fmt.Errorf("rectifyDeskView needs 4 points")
	}
	srcRGBA := imageToRGBA(src)
	b := srcRGBA.Bounds()
	srcWidth := float64(b.Dx())
	srcHeight := float64(b.Dy())
	srcQuad := make([]point, 4)
	for i, p := range normalizedQuad {
		srcQuad[i] = point{x: p.x * srcWidth, y: p.y * srcHeight}
	}
	top := math.Hypot(srcQuad[1].x-srcQuad[0].x, srcQuad[1].y-srcQuad[0].y)
	bottom := math.Hypot(srcQuad[2].x-srcQuad[3].x, srcQuad[2].y-srcQuad[3].y)
	left := math.Hypot(srcQuad[3].x-srcQuad[0].x, srcQuad[3].y-srcQuad[0].y)
	right := math.Hypot(srcQuad[2].x-srcQuad[1].x, srcQuad[2].y-srcQuad[1].y)
	outWidth := maxInt(80, int(math.Round((top+bottom)/2)))
	outHeight := maxInt(80, int(math.Round((left+right)/2)))
	dstQuad := []point{{0, 0}, {float64(outWidth - 1), 0}, {float64(outWidth - 1), float64(outHeight - 1)}, {0, float64(outHeight - 1)}}
	h, err := computeHomography(dstQuad, srcQuad)
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, outWidth, outHeight))
	parallelizeRows(outHeight, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			dstRow := y * dst.Stride
			for x := 0; x < outWidth; x++ {
				sx, sy := projectPoint(h, float64(x), float64(y))
				u := sx / float64(maxInt(1, b.Dx()-1))
				v := sy / float64(maxInt(1, b.Dy()-1))
				c := sampleBilinearRGBA(srcRGBA, clamp01(u), clamp01(v))
				dstPix := dstRow + x*4
				dst.Pix[dstPix+0] = c.R
				dst.Pix[dstPix+1] = c.G
				dst.Pix[dstPix+2] = c.B
				dst.Pix[dstPix+3] = 0xFF
			}
		}
	})
	return dst, nil
}

func parallelizeRows(height int, fn func(y0, y1 int)) {
	if height <= 0 {
		return
	}
	workers := minInt(height, maxInt(1, runtime.GOMAXPROCS(0)))
	if workers <= 1 || height < workers*8 {
		fn(0, height)
		return
	}
	rowsPerWorker := (height + workers - 1) / workers
	var wg sync.WaitGroup
	for start := 0; start < height; start += rowsPerWorker {
		end := minInt(height, start+rowsPerWorker)
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			fn(y0, y1)
		}(start, end)
	}
	wg.Wait()
}

func computeHomography(srcQuad, dstQuad []point) ([9]float64, error) {
	var result [9]float64
	matrix := make([][]float64, 0, 8)
	values := make([]float64, 0, 8)
	for i := 0; i < 4; i++ {
		sx := srcQuad[i].x
		sy := srcQuad[i].y
		dx := dstQuad[i].x
		dy := dstQuad[i].y
		matrix = append(matrix, []float64{sx, sy, 1, 0, 0, 0, -dx * sx, -dx * sy})
		values = append(values, dx)
		matrix = append(matrix, []float64{0, 0, 0, sx, sy, 1, -dy * sx, -dy * sy})
		values = append(values, dy)
	}
	solution, ok := solveLinearSystem(matrix, values)
	if !ok {
		return result, fmt.Errorf("homography solve failed")
	}
	return [9]float64{
		solution[0], solution[1], solution[2],
		solution[3], solution[4], solution[5],
		solution[6], solution[7], 1,
	}, nil
}

func solveLinearSystem(matrix [][]float64, values []float64) ([]float64, bool) {
	n := len(values)
	a := make([][]float64, n)
	for i := 0; i < n; i++ {
		a[i] = append(append([]float64{}, matrix[i]...), values[i])
	}
	for col := 0; col < n; col++ {
		pivot := col
		for row := col + 1; row < n; row++ {
			if math.Abs(a[row][col]) > math.Abs(a[pivot][col]) {
				pivot = row
			}
		}
		if math.Abs(a[pivot][col]) < 1e-8 {
			return nil, false
		}
		if pivot != col {
			a[col], a[pivot] = a[pivot], a[col]
		}
		divisor := a[col][col]
		for k := col; k <= n; k++ {
			a[col][k] /= divisor
		}
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			factor := a[row][col]
			for k := col; k <= n; k++ {
				a[row][k] -= factor * a[col][k]
			}
		}
	}
	solution := make([]float64, n)
	for i := 0; i < n; i++ {
		solution[i] = a[i][n]
	}
	return solution, true
}

func projectPoint(h [9]float64, x, y float64) (float64, float64) {
	den := h[6]*x + h[7]*y + h[8]
	if math.Abs(den) < 1e-8 {
		return 0, 0
	}
	return (h[0]*x + h[1]*y + h[2]) / den, (h[3]*x + h[4]*y + h[5]) / den
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ = log.Print
