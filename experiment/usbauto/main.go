package main

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	gwebrtp "github.com/bronystylecrazy/go-webrtp"
	"github.com/bronystylecrazy/go-webrtp/usbauto"
	"github.com/gofiber/fiber/v3"
)

//go:embed index.html
var indexHTML string

type CLI struct {
	Device         string  `help:"USB camera device id or name" short:"d" required:""`
	Port           int     `help:"HTTP server port" short:"p" default:"8080"`
	TargetFPS      float64 `help:"Optional preferred camera FPS when auto-picking the best mode; 0 means highest resolution regardless of FPS" default:"0"`
	PreviewHeight  int     `help:"Preview stream height while preserving aspect ratio" default:"720"`
	InputCodec     string  `help:"Optional input codec override (mjpeg, h264, yuyv422, ...)" default:""`
	InputFormat    string  `help:"Optional FFmpeg input format override (dshow, avfoundation, v4l2)" default:""`
	BestBitrate    int     `help:"Optional best-stream bitrate in Kbps" default:"0"`
	PreviewBitrate int     `help:"Optional preview-stream bitrate in Kbps" default:"0"`
}

type calibrationState struct {
	Distort     bool    `json:"distort"`
	DeskEnabled bool    `json:"deskEnabled"`
	FX          float64 `json:"fx"`
	FY          float64 `json:"fy"`
	Scale       float64 `json:"scale"`
}

type calibrationRequest struct {
	Distort bool    `json:"distort"`
	FX      float64 `json:"fx"`
	FY      float64 `json:"fy"`
	Scale   float64 `json:"scale"`
}

type latestJPEGFrame struct {
	FrameNo     uint32
	Width       int
	Height      int
	Payload     []byte
	PublishedAt time.Time
}

type latestJPEGKeyframer struct {
	mu    sync.RWMutex
	frame *latestJPEGFrame
	count uint64
}

func (k *latestJPEGKeyframer) HandleKeyframe(frame *gwebrtp.Keyframe) error {
	if k == nil || frame == nil || len(frame.Payload) == 0 {
		return nil
	}
	item := &latestJPEGFrame{
		FrameNo:     frame.FrameNo,
		Width:       frame.Width,
		Height:      frame.Height,
		Payload:     append([]byte(nil), frame.Payload...),
		PublishedAt: frame.PublishedAt,
	}
	k.mu.Lock()
	k.frame = item
	k.count++
	k.mu.Unlock()
	return nil
}

func (k *latestJPEGKeyframer) Snapshot() *latestJPEGFrame {
	if k == nil {
		return nil
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.frame == nil {
		return nil
	}
	item := *k.frame
	item.Payload = append([]byte(nil), k.frame.Payload...)
	return &item
}

func (k *latestJPEGKeyframer) Info() *latestJPEGFrame {
	if k == nil {
		return nil
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.frame == nil {
		return nil
	}
	return &latestJPEGFrame{
		FrameNo:     k.frame.FrameNo,
		Width:       k.frame.Width,
		Height:      k.frame.Height,
		PublishedAt: k.frame.PublishedAt,
	}
}

func (k *latestJPEGKeyframer) Count() uint64 {
	if k == nil {
		return 0
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.count
}

type appState struct {
	mu            sync.RWMutex
	device        string
	source        usbauto.Mode
	previewWidth  int
	previewHeight int
	codec         string
	calibration   calibrationState
	undistorted   *latestJPEGKeyframer
}

type metaResponse struct {
	Device               string           `json:"device"`
	Source               usbauto.Mode     `json:"source"`
	PreviewWidth         int              `json:"previewWidth"`
	PreviewHeight        int              `json:"previewHeight"`
	Codec                string           `json:"codec,omitempty"`
	MimeType             string           `json:"mimeType,omitempty"`
	AIFrames             uint64           `json:"aiFrames"`
	WSPath               string           `json:"wsPath"`
	UndistortedPath      string           `json:"undistortedPath"`
	UndistortedFrameNo   uint32           `json:"undistortedFrameNo,omitempty"`
	UndistortedWidth     int              `json:"undistortedWidth,omitempty"`
	UndistortedHeight    int              `json:"undistortedHeight,omitempty"`
	UndistortedPublished string           `json:"undistortedPublished,omitempty"`
	Calibration          calibrationState `json:"calibration"`
}

func main() {
	cli := &CLI{}
	kctx := kong.Parse(cli)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	options := []usbauto.Option{
		usbauto.WithTargetFPS(cli.TargetFPS),
		usbauto.WithPreviewHeight(cli.PreviewHeight),
	}
	if strings.TrimSpace(cli.InputCodec) != "" {
		options = append(options, usbauto.WithInputCodec(cli.InputCodec))
	}
	if strings.TrimSpace(cli.InputFormat) != "" {
		options = append(options, usbauto.WithInputFormat(cli.InputFormat))
	}
	if cli.BestBitrate > 0 || cli.PreviewBitrate > 0 {
		options = append(options, usbauto.WithBitrates(cli.BestBitrate, cli.PreviewBitrate))
	}

	camera, err := usbauto.Open(ctx, cli.Device, options...)
	if err != nil {
		kctx.Fatalf("open usbauto camera: %v", err)
	}
	defer camera.Close()

	bridge := gwebrtp.NewH264Bridge(&gwebrtp.Config{
		StreamName: "usbauto-preview",
		Logger:     exampleLogger{},
	})
	defer bridge.Close()

	undistorted := &latestJPEGKeyframer{}
	bestBridge := gwebrtp.NewH264Bridge(&gwebrtp.Config{
		StreamName:     "usbauto-best-undistorted",
		KeyframeFormat: "jpg",
		Keyframer:      undistorted,
		Logger:         exampleLogger{},
	})
	defer bestBridge.Close()

	defaultCalibration := calibrationState{
		Distort:     true,
		DeskEnabled: false,
		FX:          0.12,
		FY:          0.15,
		Scale:       0.95,
	}
	if err := bestBridge.Instance().UpdateKeyframeCalibration(
		defaultCalibration.Distort,
		defaultCalibration.DeskEnabled,
		defaultCalibration.FX,
		defaultCalibration.FY,
		defaultCalibration.Scale,
		"",
	); err != nil {
		kctx.Fatalf("configure undistorted keyframe calibration: %v", err)
	}

	state := &appState{
		device:        firstNonEmpty(camera.Device(), cli.Device),
		source:        camera.SourceMode(),
		previewWidth:  camera.Preview().Width,
		previewHeight: camera.Preview().Height,
		calibration:   defaultCalibration,
		undistorted:   undistorted,
	}

	go func() {
		for au := range camera.Preview().AccessUnits {
			if codec := codecStringFromAU(au); codec != "" {
				state.setCodec(codec)
			}
			bridge.PublishH264AccessUnit(au.NALUs, au.PTS90k)
		}
	}()

	go func() {
		for au := range camera.Best().AccessUnits {
			bestBridge.PublishH264AccessUnit(au.NALUs, au.PTS90k)
		}
	}()

	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(indexHTML)
	})
	app.Get("/api/meta", func(c fiber.Ctx) error {
		return c.JSON(state.meta())
	})
	app.Get("/api/undistorted.jpg", func(c fiber.Ctx) error {
		frame := undistorted.Snapshot()
		if frame == nil || len(frame.Payload) == 0 {
			return fiber.ErrNotFound
		}
		c.Set("Content-Type", "image/jpeg")
		c.Set("Cache-Control", "no-store")
		c.Set("Pragma", "no-cache")
		c.Set("Expires", "0")
		return c.Status(http.StatusOK).Send(frame.Payload)
	})
	app.Post("/api/calibration", func(c fiber.Ctx) error {
		req := &calibrationRequest{}
		if err := c.Bind().Body(req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		next := calibrationState{
			Distort:     req.Distort,
			DeskEnabled: false,
			FX:          req.FX,
			FY:          req.FY,
			Scale:       req.Scale,
		}
		if err := bestBridge.Instance().UpdateKeyframeCalibration(
			next.Distort,
			next.DeskEnabled,
			next.FX,
			next.FY,
			next.Scale,
			"",
		); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		state.setCalibration(next)
		return c.JSON(state.meta())
	})
	app.All("/ws/preview", bridge.Handler())

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.ShutdownWithContext(shutdownCtx)
	}()

	addr := fmt.Sprintf(":%d", cli.Port)
	kctx.Printf("usbauto example listening on http://localhost%s", addr)
	if err := app.Listen(addr); err != nil && ctx.Err() == nil {
		kctx.Fatalf("listen: %v", err)
	}
}

func (s *appState) setCodec(codec string) {
	s.mu.Lock()
	if s.codec == "" {
		s.codec = codec
	}
	s.mu.Unlock()
}

func (s *appState) meta() *metaResponse {
	s.mu.RLock()
	codec := s.codec
	calibration := s.calibration
	undistorted := s.undistorted
	resp := &metaResponse{
		Device:          s.device,
		Source:          s.source,
		PreviewWidth:    s.previewWidth,
		PreviewHeight:   s.previewHeight,
		Codec:           codec,
		WSPath:          "/ws/preview",
		UndistortedPath: "/api/undistorted.jpg",
		Calibration:     calibration,
	}
	s.mu.RUnlock()
	if undistorted != nil {
		resp.AIFrames = undistorted.Count()
		if frame := undistorted.Info(); frame != nil {
			resp.UndistortedFrameNo = frame.FrameNo
			resp.UndistortedWidth = frame.Width
			resp.UndistortedHeight = frame.Height
			if !frame.PublishedAt.IsZero() {
				resp.UndistortedPublished = frame.PublishedAt.Format(time.RFC3339Nano)
			}
		}
	}
	if codec != "" {
		resp.MimeType = fmt.Sprintf("video/mp4; codecs=\"%s\"", codec)
	}
	return resp
}

func (s *appState) setCalibration(next calibrationState) {
	s.mu.Lock()
	s.calibration = next
	s.mu.Unlock()
}

func codecStringFromAU(au gwebrtp.H264AccessUnit) string {
	for _, nalu := range au.NALUs {
		if len(nalu) < 4 {
			continue
		}
		if (nalu[0] & 0x1F) != 7 {
			continue
		}
		return fmt.Sprintf("avc1.%02X%02X%02X", nalu[1], nalu[2], nalu[3])
	}
	return ""
}

func firstNonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

type exampleLogger struct{}

func (exampleLogger) Print(v ...interface{})                 { fmt.Println(v...) }
func (exampleLogger) Printf(format string, v ...interface{}) { fmt.Printf(format+"\n", v...) }
