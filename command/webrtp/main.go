package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/connectedtechco/go-webrtp"
	"github.com/dustin/go-humanize"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/mattn/go-isatty"
	"gopkg.in/yaml.v3"
)

//go:embed index.html
var indexHtml []byte

//go:embed index.css
var indexCss []byte

//go:embed stream.html
var streamHtml []byte

//go:embed dashboard.html
var dashboardHtml []byte

var CLI struct {
	Config         string `help:"Config file path" short:"c" default:"config.yml"`
	Interface      bool   `help:"Use graphical interface" short:"i" default:"false"`
	Port           int    `help:"HTTP server port" short:"p" default:"8080"`
	ListUsbDevices bool   `help:"List available USB video devices and exit" default:"false"`
}

type Config struct {
	TelemetryServiceName *string     `yaml:"telemetryServiceName"`
	TelemetryEndpoint    *string     `yaml:"telemetryEndpoint"`
	Upstreams            []*Upstream `yaml:"upstreams"`
}

type Upstream struct {
	Name        *string      `yaml:"name"`
	SourceType  *string      `yaml:"sourceType"`
	RtspUrl     string       `yaml:"rtspUrl"`
	Device      string       `yaml:"device"`
	Path        string       `yaml:"path"`
	Codec       string       `yaml:"codec"`
	Width       *int         `yaml:"width"`
	Height      *int         `yaml:"height"`
	FrameRate   *float64     `yaml:"frameRate"`
	BitrateKbps *int         `yaml:"bitrateKbps"`
	Enabled     *bool        `yaml:"enabled"`
	OnDemand    bool         `yaml:"onDemand"`
	Renditions  []*Rendition `yaml:"renditions"`
}

type Rendition struct {
	Name        string `yaml:"name" json:"name"`
	BitrateKbps int    `yaml:"bitrateKbps" json:"bitrateKbps"`
	OnDemand    *bool  `yaml:"onDemand,omitempty" json:"onDemand,omitempty"`
}

type Stream struct {
	Name          string
	GroupName     string
	RenditionName string
	Url           string
	Inst          *webrtp.Instance
	Hub           *webrtp.Hub
	Handler       fiber.Handler
	Stop          func() error
	OnDemand      bool
	startMu       sync.Mutex
	started       atomic.Bool
	stopTimerMu   sync.Mutex
	stopTimer     *time.Timer
}

type tickMsg struct{ time.Time }

type RecordRequest struct {
	Path        string `json:"path"`
	OfflineMode string `json:"offlineMode"`
}

type ModeRequest struct {
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	FrameRate *float64 `json:"frameRate"`
}

type RecordResponse struct {
	Stream    string                 `json:"stream"`
	Quality   string                 `json:"quality,omitempty"`
	Recording webrtp.RecordingStatus `json:"recording"`
}

type Model struct {
	manager      *StreamManager
	page         int
	pageSize     int
	stats        []webrtp.StreamStats
	logs         []string
	quitting     bool
	windowWidth  int
	windowHeight int
}

func (m *Model) Init() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg{t}
	})
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		m.windowHeight = msg.Height
		if msg.Height > 18 {
			m.pageSize = msg.Height - 18
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "left", "h":
			if m.page > 0 {
				m.page--
			}
		case "right", "l":
			if (m.page+1)*m.pageSize < len(m.manager.StreamListExpandedActive()) {
				m.page++
			}
		}
	case tickMsg:
		m.stats = nil
		streams := m.manager.StreamListExpandedActive()
		for _, s := range streams {
			m.stats = append(m.stats, s.Hub.GetStats(tuiStreamDisplayName(s)))
		}
		MetricsUpdate(m.manager.StreamList())
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg {
			return tickMsg{t}
		})
	}
	return m, nil
}

var (
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	whiteStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

func (m *Model) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}

	var rows []table.Row
	m.stats = nil
	streams := m.manager.StreamListExpandedActive()

	start := m.page * m.pageSize
	end := start + m.pageSize
	if end > len(streams) {
		end = len(streams)
	}

	for i := start; i < end && i < len(streams); i++ {
		s := streams[i]
		stats := s.Hub.GetStats(tuiStreamDisplayName(s))

		status := "Ready"
		if !stats.Ready {
			status = "Waiting"
		}

		name := tuiStreamDisplayName(s)
		if name == strconv.Itoa(i) {
			name = "N/A"
		}

		rows = append(rows, table.Row{
			strconv.Itoa(i),
			truncateCell(name, 15),
			truncateCell(status, 10),
			truncateCell(stats.Codec, 8),
			truncateCell(fmt.Sprintf("%dx%d", stats.Width, stats.Height), 12),
			truncateCell(fmt.Sprintf("%.1f", stats.Framerate), 11),
			truncateCell(fmt.Sprintf("%.1f kbps", stats.Bitrate), 14),
			truncateCell(humanize.Bytes(stats.BytesRecv), 12),
			truncateCell(fmt.Sprintf("%d", stats.FrameNo), 11),
			truncateCell(fmt.Sprintf("%d", stats.ClientCount), 9),
			formatUptime(stats.Uptime),
		})
	}

	// Calculate table height based on window size
	// Layout: header(5) + logs(10) + nav(3) = ~18 fixed lines
	tableHeight := 10 // default
	if m.windowHeight > 18 {
		tableHeight = m.windowHeight - 18
	}

	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "#", Width: 3},
			{Title: "Name", Width: 15},
			{Title: "Status", Width: 10},
			{Title: "Codec", Width: 8},
			{Title: "Resolution", Width: 12},
			{Title: "Framerate", Width: 11},
			{Title: "Bitrate", Width: 14},
			{Title: "Bandwidth", Width: 12},
			{Title: "Frames", Width: 11},
			{Title: "Clients", Width: 9},
			{Title: "Uptime", Width: 10},
		}),
		table.WithRows(rows),
		table.WithHeight(tableHeight),
		table.WithFocused(false),
	)

	s := table.DefaultStyles()
	s.Header = headerStyle
	s.Cell = lipgloss.NewStyle()
	s.Selected = lipgloss.Style{}
	t.SetStyles(s)

	totalPages := 1
	if len(streams) > 0 {
		totalPages = (len(streams) + m.pageSize - 1) / m.pageSize
	}
	nav := dimStyle.Render(fmt.Sprintf("Page %d/%d (←/→ to navigate, q to quit)", m.page+1, totalPages))

	// Build logs view (last 10 lines)
	var logsView string
	if len(m.logs) > 0 {
		start := len(m.logs) - 10
		if start < 0 {
			start = 0
		}
		logsView = dimStyle.Render(strings.Join(m.logs[start:], "\n"))
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		whiteStyle.Render("┌────────────────────────────────────────┐"),
		whiteStyle.Render("│ WebRTP Streamer                        │"),
		lipgloss.JoinHorizontal(lipgloss.Left,
			whiteStyle.Render("│"),
			dimStyle.Render(" © 2026 Connected Tech Co.,Ltd.         "),
			whiteStyle.Render("│"),
		),
		whiteStyle.Render("└────────────────────────────────────────┘"),
		t.View(),
		nav,
		"",
		headerStyle.Render("Logs"),
		logsView,
	)
}

func formatUptime(d time.Duration) string {
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %02d:%02d:%02d", days, hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func truncateCell(s string, maxWidth int) string {
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	return strings.TrimRight(s[:maxWidth-2], " ") + "… "
}

func streamDisplayName(s *Stream) string {
	if s.GroupName != "" {
		return s.GroupName
	}
	return s.Name
}

func tuiStreamDisplayName(s *Stream) string {
	if s.GroupName != "" && s.RenditionName != "" {
		return fmt.Sprintf("%s/%s", s.GroupName, s.RenditionName)
	}
	return streamDisplayName(s)
}

func (s *Stream) EnsureStarted() {
	if !s.OnDemand || s.started.Load() {
		return
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.started.Load() {
		return
	}
	s.stopTimerMu.Lock()
	if s.stopTimer != nil {
		s.stopTimer.Stop()
		s.stopTimer = nil
	}
	s.stopTimerMu.Unlock()
	s.started.Store(true)
	go func() {
		if err := s.Inst.Connect(); err != nil {
			log.Printf("stream %s: %v", s.Name, err)
		}
		s.started.Store(false)
	}()
}

func (s *Stream) HasActiveRecording() bool {
	return s.Inst.RecordingStatus().Active
}

func (s *Stream) StopNow() error {
	s.stopTimerMu.Lock()
	if s.stopTimer != nil {
		s.stopTimer.Stop()
		s.stopTimer = nil
	}
	s.stopTimerMu.Unlock()
	s.started.Store(false)
	return s.Stop()
}

func (s *Stream) MaybeScheduleStop(idle time.Duration) {
	if !s.OnDemand {
		return
	}
	if s.HasActiveRecording() {
		return
	}
	if s.Hub.GetStats(s.Name).ClientCount > 0 {
		return
	}
	s.stopTimerMu.Lock()
	defer s.stopTimerMu.Unlock()
	if s.stopTimer != nil {
		s.stopTimer.Stop()
	}
	s.stopTimer = time.AfterFunc(idle, func() {
		if s.Hub.GetStats(s.Name).ClientCount > 0 {
			return
		}
		if s.HasActiveRecording() {
			return
		}
		_ = s.StopNow()
	})
}

func sanitizeRecordingName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "stream"
	}
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	return strings.Trim(b.String(), "-")
}

func defaultRecordingPath(stream *Stream) string {
	base := sanitizeRecordingName(stream.GroupName)
	if base == "" {
		base = sanitizeRecordingName(stream.Name)
	}
	if stream.RenditionName != "" {
		base = base + "-" + sanitizeRecordingName(stream.RenditionName)
	}
	filename := fmt.Sprintf("%s-%s.mp4", base, time.Now().Format("20060102-150405"))
	return filepath.Join("recordings", filename)
}

func recordingResponse(stream *Stream) *RecordResponse {
	name := stream.GroupName
	if name == "" {
		name = stream.Name
	}
	return &RecordResponse{
		Stream:    name,
		Quality:   stream.RenditionName,
		Recording: stream.Inst.RecordingStatus(),
	}
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	for _, u := range cfg.Upstreams {
		if err := StreamValidateUpstream(u); err != nil {
			return nil, err
		}
	}

	return &cfg, nil
}

func main() {
	kong.Parse(&CLI)

	if CLI.ListUsbDevices {
		devices, err := webrtp.UsbDeviceList()
		if err != nil {
			log.Fatalf("list usb devices: %v", err)
		}
		if len(devices) == 0 {
			log.Printf("No USB video devices found")
			return
		}
		for _, device := range devices {
			log.Printf("%s\t%s", device.Id, device.Name)
		}
		return
	}

	cfg, err := loadConfig(CLI.Config)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	manager, err := StreamManagerNew(CLI.Config, cfg)
	if err != nil {
		log.Fatalf("stream manager: %v", err)
	}

	// Create single fiber instance
	app := fiber.New()
	app.Use(cors.New())

	// Register routes
	app.All("/stream/no/:index<int>", func(c fiber.Ctx) error {
		index, err := strconv.Atoi(c.Params("index"))
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid stream index")
		}
		stream, ok := manager.StreamByIndex(index)
		if !ok {
			return fiber.ErrNotFound
		}
		stream.EnsureStarted()
		return stream.Handler(c)
	})
	app.All("/stream/:name", func(c fiber.Ctx) error {
		stream, ok := manager.StreamByNameQuality(c.Params("name"), c.Query("quality"))
		if !ok {
			return fiber.ErrNotFound
		}
		stream.EnsureStarted()
		return stream.Handler(c)
	})

	app.Get("/info", func(c fiber.Ctx) error {
		streams := manager.StreamList()
		stats := make([]*webrtp.StreamStats, len(streams))
		for i, s := range streams {
			streamStats := s.Hub.GetStats(streamDisplayName(s))
			streamStats.Name = streamDisplayName(s)
			stats[i] = &streamStats
		}
		MetricsUpdate(streams)
		return c.JSON(webrtp.Status{Streams: stats})
	})
	app.Get("/api/streams", func(c fiber.Ctx) error {
		items := manager.StreamStatusList()
		StreamResponsesSort(items)
		return c.JSON(items)
	})
	app.All("/ws/info", StatusSocketHandler(manager))
	app.All("/ws/dashboard", DashboardSocketHandler(manager))
	app.Post("/api/streams", func(c fiber.Ctx) error {
		req := &StreamApiRequest{}
		if err := c.Bind().Body(req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		item, err := manager.StreamCreate(req)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.Status(fiber.StatusCreated).JSON(item)
	})
	app.Get("/api/streams/:name", func(c fiber.Ctx) error {
		item, ok := manager.StreamStatus(c.Params("name"))
		if !ok {
			return fiber.ErrNotFound
		}
		return c.JSON(item)
	})
	app.Post("/api/streams/:name/mode", func(c fiber.Ctx) error {
		req := &ModeRequest{}
		if err := c.Bind().Body(req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		item, err := manager.StreamModeUpdate(c.Params("name"), req)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(item)
	})
	app.Get("/api/streams/:name/capabilities", func(c fiber.Ctx) error {
		item, ok, err := manager.StreamCapabilities(c.Params("name"))
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		if !ok {
			return fiber.ErrNotFound
		}
		return c.JSON(item)
	})
	app.Put("/api/streams/:name", func(c fiber.Ctx) error {
		req := &StreamApiRequest{}
		if err := c.Bind().Body(req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		item, err := manager.StreamUpdate(c.Params("name"), req)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(item)
	})
	app.Delete("/api/streams/:name", func(c fiber.Ctx) error {
		if err := manager.StreamDelete(c.Params("name")); err != nil {
			if strings.Contains(err.Error(), "not found") {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	app.Post("/api/streams/:name/record/start", func(c fiber.Ctx) error {
		stream, ok := manager.StreamByNameQuality(c.Params("name"), c.Query("quality"))
		if !ok {
			return fiber.ErrNotFound
		}
		req := &RecordRequest{}
		if len(c.Body()) > 0 {
			if err := c.Bind().Body(req); err != nil {
				return fiber.NewError(fiber.StatusBadRequest, err.Error())
			}
		}
		stream.EnsureStarted()
		path := strings.TrimSpace(req.Path)
		if path == "" {
			path = defaultRecordingPath(stream)
		}
		if err := stream.Inst.StartRecording(path, "", req.OfflineMode); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.Status(fiber.StatusCreated).JSON(recordingResponse(stream))
	})
	app.Post("/api/streams/:name/record/stop", func(c fiber.Ctx) error {
		stream, ok := manager.StreamByNameQuality(c.Params("name"), c.Query("quality"))
		if !ok {
			return fiber.ErrNotFound
		}
		if err := stream.Inst.StopRecording(); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		stream.MaybeScheduleStop(5 * time.Second)
		return c.JSON(recordingResponse(stream))
	})
	app.Get("/api/streams/:name/record/status", func(c fiber.Ctx) error {
		stream, ok := manager.StreamByNameQuality(c.Params("name"), c.Query("quality"))
		if !ok {
			return fiber.ErrNotFound
		}
		return c.JSON(recordingResponse(stream))
	})

	app.Get("/", func(c fiber.Ctx) error {
		return c.Type("html").Send(indexHtml)
	})
	app.Get("/streams/:name", func(c fiber.Ctx) error {
		if _, ok := manager.StreamByName(c.Params("name")); !ok {
			return fiber.ErrNotFound
		}
		return c.Type("html").Send(streamHtml)
	})
	app.Get("/dashboard", func(c fiber.Ctx) error {
		return c.Type("html").Send(dashboardHtml)
	})

	app.Get("/index.css", func(c fiber.Ctx) error {
		c.Set("Content-Type", "text/css")
		return c.Send(indexCss)
	})

	// Init metrics
	serviceName := "webrtp"
	if cfg.TelemetryServiceName != nil && *cfg.TelemetryServiceName != "" {
		serviceName = *cfg.TelemetryServiceName
	}
	endpoint := ""
	if cfg.TelemetryEndpoint != nil && *cfg.TelemetryEndpoint != "" {
		endpoint = *cfg.TelemetryEndpoint
	}
	if err := MetricsInit(serviceName, endpoint); err != nil {
		log.Printf("metrics init: %v", err)
	} else {
		MetricsRoute(app)
	}

	// Start fiber server
	go func() {
		addr := fmt.Sprintf(":%d", CLI.Port)
		log.Printf("HTTP server listening on http://localhost%s", addr)
		log.Printf("Streams available:")
		for i, s := range manager.StreamList() {
			log.Printf("  - /stream/no/%d (%s) -> %s", i, streamDisplayName(s), s.Url)
		}
		if err := app.Listen(addr); err != nil {
			log.Printf("HTTP: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if CLI.Interface && isatty.IsTerminal(os.Stdout.Fd()) {
		runTUI(ctx, manager)
	} else {
		runServer(ctx, manager)
	}
}

func runServer(ctx context.Context, manager *StreamManager) {
	_ = ctx
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("shutting down")
	manager.StreamStopAll()
}

func runTUI(ctx context.Context, manager *StreamManager) {
	_ = ctx
	m := &Model{
		manager:  manager,
		pageSize: 10,
		logs:     []string{},
	}

	p := tea.NewProgram(m, tea.WithAltScreen())

	// Create a log writer that sends to Model
	logWriter := &logWriter{logs: &m.logs}
	log.SetOutput(logWriter)
	log.SetFlags(0)

	if _, err := p.Run(); err != nil {
		log.Fatalf("Model: %v", err)
	}
}

type logWriter struct {
	logs *[]string
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	msg := strings.TrimSpace(string(p))
	if msg != "" {
		*w.logs = append(*w.logs, msg)
		// Keep only last 100 logs
		if len(*w.logs) > 100 {
			*w.logs = (*w.logs)[len(*w.logs)-100:]
		}
	}
	return len(p), nil
}
