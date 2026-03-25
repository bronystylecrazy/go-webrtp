package main

import (
	"context"
	"crypto/sha1"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/bronystylecrazy/go-webrtp"
	"github.com/bronystylecrazy/go-webrtp/streamcore"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/mattn/go-isatty"
)

//go:embed index.html
var indexHtml []byte

//go:embed index.scss
var indexCss []byte

//go:embed stream.html
var streamHtml []byte

//go:embed dashboard.html
var dashboardHtml []byte

var (
	buildVersion  = "dev"
	buildCommit   = "unknown"
	buildTime     = ""
	serverStarted = time.Now()
)

var CLI struct {
	Config         string `help:"Config file path" short:"c" default:"config.yml"`
	Interface      bool   `help:"Use graphical interface" short:"i" default:"false"`
	Port           int    `help:"HTTP server port" short:"p" default:"8080"`
	ListUsbDevices bool   `help:"List available USB video devices and exit" default:"false"`
}

type tickMsg struct{ time.Time }

type RecordRequest struct {
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	OfflineMode string `json:"offlineMode"`
}

type RecordResponse struct {
	Stream    string                 `json:"stream"`
	Quality   string                 `json:"quality,omitempty"`
	Recording webrtp.RecordingStatus `json:"recording"`
}

type DeviceCapabilitiesResponse struct {
	Device       *webrtp.UsbDevice             `json:"device,omitempty"`
	Capabilities *webrtp.UsbDeviceCapabilities `json:"capabilities,omitempty"`
}

type HealthResponse struct {
	Status        string        `json:"status"`
	StartedAt     time.Time     `json:"startedAt"`
	Uptime        time.Duration `json:"uptime"`
	Streams       int           `json:"streams"`
	Enabled       int           `json:"enabled"`
	Served        int           `json:"served"`
	ActiveClients int           `json:"activeClients"`
}

type VersionResponse struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime,omitempty"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

type Model struct {
	manager      *streamcore.Manager
	page         int
	pageSize     int
	stats        []webrtp.StreamStats
	logMu        sync.Mutex
	logs         []string
	quitting     bool
	windowWidth  int
	windowHeight int
}

type deskViewBBoxPublishPayload struct {
	TableHeightMM float64               `json:"tableHeightMM"`
	TableWidthMM  float64               `json:"tableWidthMM"`
	BBoxs         []*DeskViewBBoxRecord `json:"bboxs"`
}

func normalizeDeskViewBBoxPayload(msg *DeskViewSyncMessage) (string, []byte, error) {
	if msg == nil {
		return "", nil, nil
	}
	payload := &deskViewBBoxPublishPayload{
		TableHeightMM: msg.TableHeightMM,
		TableWidthMM:  msg.TableWidthMM,
		BBoxs:         make([]*DeskViewBBoxRecord, 0, len(msg.BBoxs)),
	}
	topicTableID := 0
	for _, box := range msg.BBoxs {
		if box == nil {
			continue
		}
		item := &DeskViewBBoxRecord{
			ID:           box.ID,
			TableID:      box.TableID,
			ColorKey:     normalizeBBoxKey(box.ColorKey, "B", "G", "GR", "R", "W", "Y"),
			DirectionKey: normalizeBBoxKey(box.DirectionKey, "L", "R", "T", "B"),
			X1:           clamp01(box.X1),
			Y1:           clamp01(box.Y1),
			X2:           clamp01(box.X2),
			Y2:           clamp01(box.Y2),
			WireColorKey: normalizeBBoxKey(box.WireColorKey, "GR", "BL"),
		}
		if item.X2 < item.X1 {
			item.X1, item.X2 = item.X2, item.X1
		}
		if item.Y2 < item.Y1 {
			item.Y1, item.Y2 = item.Y2, item.Y1
		}
		if topicTableID == 0 && item.TableID > 0 {
			topicTableID = item.TableID
		}
		payload.BBoxs = append(payload.BBoxs, item)
	}
	if len(payload.BBoxs) == 0 && payload.TableHeightMM == 0 && payload.TableWidthMM == 0 {
		return "", nil, nil
	}
	if topicTableID <= 0 {
		topicTableID = 1
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("/v1/tables/%d/inspections/request", topicTableID), body, nil
}

func normalizeBBoxKey(raw *string, allowed ...string) *string {
	if raw == nil {
		return nil
	}
	value := strings.ToUpper(strings.TrimSpace(*raw))
	if value == "" {
		return nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			out := candidate
			return &out
		}
	}
	return nil
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
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
			if (m.page+1)*m.pageSize < len(m.manager.ListExpandedActive()) {
				m.page++
			}
		}
	case tickMsg:
		m.stats = nil
		streams := m.manager.ListExpandedActive()
		for _, s := range streams {
			m.stats = append(m.stats, s.Hub.GetStats(tuiStreamDisplayName(s)))
		}
		MetricsUpdate(m.manager.List())
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

const tuiLogLines = 10

func (m *Model) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}

	viewWidth := m.windowWidth
	if viewWidth <= 0 {
		viewWidth = 120
	}
	viewHeight := m.windowHeight
	if viewHeight <= 0 {
		viewHeight = 32
	}

	var rows []table.Row
	m.stats = nil
	streams := m.manager.ListExpandedActive()

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

	// Layout: title(4) + nav(1) + gap/header(2) + logs(10)
	tableHeight := 10
	if available := viewHeight - 17 - tuiLogLines; available > 5 {
		tableHeight = available
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
	logWidth := max(20, viewWidth-2)
	logLines := make([]string, 0, tuiLogLines)
	m.logMu.Lock()
	if len(m.logs) > 0 {
		start := len(m.logs) - tuiLogLines
		if start < 0 {
			start = 0
		}
		logLines = append(logLines, formatLogLines(m.logs[start:], logWidth)...)
	}
	m.logMu.Unlock()
	for len(logLines) < tuiLogLines {
		logLines = append(logLines, "")
	}
	logsView := dimStyle.Width(logWidth).Height(tuiLogLines).Render(strings.Join(logLines[:tuiLogLines], "\n"))

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

func streamDisplayName(s *streamcore.Stream) string {
	if s.GroupName != "" {
		return s.GroupName
	}
	return s.Name
}

func tuiStreamDisplayName(s *streamcore.Stream) string {
	if s.GroupName != "" && s.RenditionName != "" {
		return fmt.Sprintf("%s/%s", s.GroupName, s.RenditionName)
	}
	return streamDisplayName(s)
}

func formatLogLines(lines []string, width int) []string {
	if width < 8 {
		width = 8
	}
	formatted := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
		if line == "" {
			continue
		}
		formatted = append(formatted, clampLineWidth(line, width))
	}
	return formatted
}

func clampLineWidth(s string, width int) string {
	if width < 4 || lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:max(0, width-2)]) + ".."
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

func defaultRecordingPath(stream *streamcore.Stream) string {
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

func recordingResponse(stream *streamcore.Stream) *RecordResponse {
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

func hashDeviceID(raw string) string {
	sum := sha1.Sum([]byte(raw))
	return fmt.Sprintf("%x", sum)
}

func sanitizeUsbDevice(device *webrtp.UsbDevice) *webrtp.UsbDevice {
	if device == nil {
		return nil
	}
	return &webrtp.UsbDevice{
		Id:   hashDeviceID(device.Id),
		Name: device.Name,
	}
}

func sanitizeUsbDevices(devices []*webrtp.UsbDevice) []*webrtp.UsbDevice {
	items := make([]*webrtp.UsbDevice, 0, len(devices))
	for _, device := range devices {
		if sanitized := sanitizeUsbDevice(device); sanitized != nil {
			items = append(items, sanitized)
		}
	}
	return items
}

func resolveHashedDeviceID(id string) (string, error) {
	devices, err := webrtp.UsbDeviceList()
	if err != nil {
		return "", err
	}
	for _, device := range devices {
		if device == nil {
			continue
		}
		if device.Id == id || hashDeviceID(device.Id) == id {
			return device.Id, nil
		}
	}
	return "", fiber.ErrNotFound
}

func preferredStreamVariant(quality, mapped string) string {
	if quality = strings.TrimSpace(quality); quality != "" {
		return quality
	}
	return strings.TrimSpace(mapped)
}

func streamVariantQuery(c fiber.Ctx) string {
	return preferredStreamVariant(c.Query("quality"), c.Query("map"))
}

func resolveStreamRouteName(manager *streamcore.Manager, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if _, ok := manager.Get(name); ok {
		return name
	}
	if stream, ok := manager.StreamByNameAny(name); ok && stream != nil {
		if groupName := strings.TrimSpace(stream.GroupName); groupName != "" {
			return groupName
		}
		return stream.Name
	}
	for _, item := range manager.ListResponses() {
		if item == nil || item.SourceType != "usb" || item.Device == "" {
			continue
		}
		if item.Device == name || hashDeviceID(item.Device) == name {
			return item.Name
		}
	}
	return name
}

func apiDeviceCapabilities(deviceID string) (*DeviceCapabilitiesResponse, error) {
	rawID, err := resolveHashedDeviceID(deviceID)
	if err != nil {
		return nil, err
	}
	caps, err := webrtp.UsbDeviceCapabilitiesGet(rawID)
	if err != nil {
		return nil, err
	}
	resp := &DeviceCapabilitiesResponse{
		Capabilities: caps,
	}
	if caps != nil && caps.Device != nil {
		resp.Device = sanitizeUsbDevice(caps.Device)
	} else {
		resp.Device = &webrtp.UsbDevice{Id: hashDeviceID(rawID), Name: rawID}
	}
	if resp.Capabilities != nil && resp.Capabilities.Device != nil {
		resp.Capabilities.Device = sanitizeUsbDevice(resp.Capabilities.Device)
	}
	return resp, nil
}

func apiHealth(manager *streamcore.Manager) *HealthResponse {
	streams := manager.ListResponses()
	resp := &HealthResponse{
		Status:    "ok",
		StartedAt: serverStarted,
		Uptime:    time.Since(serverStarted).Round(time.Second),
		Streams:   len(streams),
	}
	for _, stream := range streams {
		if stream.Enabled {
			resp.Enabled++
		}
		if stream.ServeStream {
			resp.Served++
		}
		if stream.Stats != nil {
			resp.ActiveClients += int(stream.Stats.ClientCount)
		}
	}
	return resp
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

	var tuiModel *Model
	if CLI.Interface && isatty.IsTerminal(os.Stdout.Fd()) {
		tuiModel = &Model{
			pageSize: 10,
			logs:     []string{},
		}
		log.SetOutput(&logWriter{model: tuiModel})
		log.SetFlags(0)
	}

	manager, err := streamcore.NewManager(
		streamcore.WithConfigFile(CLI.Config),
		streamcore.WithConfig(cfg),
	)
	if err != nil {
		log.Fatalf("stream manager: %v", err)
	}
	if tuiModel != nil {
		tuiModel.manager = manager
	}

	// Create single fiber instance
	app := fiber.New()
	app.Use(cors.New())
	deskViewBroker := NewDeskViewSocketBroker()

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
		return stream.FiberHandler()(c)
	})
	app.All("/stream/:name", func(c fiber.Ctx) error {
		stream, ok := manager.StreamByNameQuality(resolveStreamRouteName(manager, c.Params("name")), streamVariantQuery(c))
		if !ok {
			return fiber.ErrNotFound
		}
		stream.EnsureStarted()
		return stream.FiberHandler()(c)
	})
	app.All("/streams/:name", func(c fiber.Ctx) error {
		name := resolveStreamRouteName(manager, c.Params("name"))
		if websocket.IsWebSocketUpgrade(c) {
			stream, ok := manager.StreamByNameQuality(name, streamVariantQuery(c))
			if !ok {
				return fiber.ErrNotFound
			}
			stream.EnsureStarted()
			return stream.FiberHandler()(c)
		}
		if _, ok := manager.StreamByName(name); !ok {
			return fiber.ErrNotFound
		}
		return c.Type("html").Send(streamHtml)
	})

	app.Get("/info", func(c fiber.Ctx) error {
		streams := manager.List()
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
		items := manager.ListResponses()
		streamcore.SortResponses(items)
		return c.JSON(items)
	})
	app.Get("/api/devices", func(c fiber.Ctx) error {
		items, err := webrtp.UsbDeviceList()
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(sanitizeUsbDevices(items))
	})
	app.Get("/api/devices/:id/capabilities", func(c fiber.Ctx) error {
		item, err := apiDeviceCapabilities(c.Params("id"))
		if err != nil {
			if err == fiber.ErrNotFound {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(item)
	})
	app.Get("/api/recordings", func(c fiber.Ctx) error {
		items, err := RecordingsList("recordings")
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		return c.JSON(items)
	})
	app.Get("/api/health", func(c fiber.Ctx) error {
		return c.JSON(apiHealth(manager))
	})
	app.Get("/api/version", func(c fiber.Ctx) error {
		return c.JSON(&VersionResponse{
			Version:   buildVersion,
			Commit:    buildCommit,
			BuildTime: buildTime,
			GoVersion: runtime.Version(),
			Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		})
	})
	app.All("/ws/info", StatusSocketHandler(manager))
	app.All("/ws/dashboard", DashboardSocketHandler(manager))
	app.All("/ws/devices", DeviceSocketHandler())
	app.All("/ws/deskview", DeskViewSocketHandler(deskViewBroker))
	app.Post("/api/streams", func(c fiber.Ctx) error {
		req := &streamcore.StreamRequest{}
		if err := c.Bind().Body(req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		item, err := manager.Create(req)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.Status(fiber.StatusCreated).JSON(item)
	})
	app.Get("/api/streams/:name", func(c fiber.Ctx) error {
		item, ok := manager.Get(resolveStreamRouteName(manager, c.Params("name")))
		if !ok {
			return fiber.ErrNotFound
		}
		return c.JSON(item)
	})
	app.Post("/api/streams/:name/enable", func(c fiber.Ctx) error {
		item, err := manager.SetEnabled(resolveStreamRouteName(manager, c.Params("name")), true)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(item)
	})
	app.Post("/api/streams/:name/disable", func(c fiber.Ctx) error {
		item, err := manager.SetEnabled(resolveStreamRouteName(manager, c.Params("name")), false)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(item)
	})
	app.Post("/api/streams/:name/start", func(c fiber.Ctx) error {
		item, err := manager.Start(resolveStreamRouteName(manager, c.Params("name")), streamVariantQuery(c))
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(item)
	})
	app.Post("/api/streams/:name/stop", func(c fiber.Ctx) error {
		item, err := manager.Stop(resolveStreamRouteName(manager, c.Params("name")), streamVariantQuery(c))
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(item)
	})
	app.Post("/api/streams/:name/mode", func(c fiber.Ctx) error {
		req := &streamcore.ModeRequest{}
		if err := c.Bind().Body(req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		item, err := manager.UpdateMode(resolveStreamRouteName(manager, c.Params("name")), req)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(item)
	})
	app.Get("/api/streams/:name/capabilities", func(c fiber.Ctx) error {
		item, ok, err := manager.Capabilities(resolveStreamRouteName(manager, c.Params("name")))
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		if !ok {
			return fiber.ErrNotFound
		}
		return c.JSON(item)
	})
	app.Post("/api/streams/:name/calibration", func(c fiber.Ctx) error {
		targets := manager.CalibrationTargets(resolveStreamRouteName(manager, c.Params("name")), streamVariantQuery(c))
		if len(targets) == 0 {
			return fiber.ErrNotFound
		}
		req := &DeskViewSyncMessage{}
		if err := c.Bind().Body(req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		for _, stream := range targets {
			if err := stream.Inst.UpdateKeyframeCalibration(req.Distort, req.DeskEnabled, req.FX, req.FY, req.Scale, req.Desk); err != nil {
				return fiber.NewError(fiber.StatusBadRequest, err.Error())
			}
			topic, payload, err := normalizeDeskViewBBoxPayload(req)
			if err != nil {
				return fiber.NewError(fiber.StatusBadRequest, err.Error())
			}
			if len(payload) > 0 {
				if err := stream.Inst.PublishDeskViewMetadata(topic, payload); err != nil {
					return fiber.NewError(fiber.StatusBadRequest, err.Error())
				}
			}
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	app.Put("/api/streams/:name", func(c fiber.Ctx) error {
		req := &streamcore.StreamRequest{}
		if err := c.Bind().Body(req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		item, err := manager.Update(resolveStreamRouteName(manager, c.Params("name")), req)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(item)
	})
	app.Delete("/api/streams/:name", func(c fiber.Ctx) error {
		if err := manager.Delete(resolveStreamRouteName(manager, c.Params("name"))); err != nil {
			if strings.Contains(err.Error(), "not found") {
				return fiber.ErrNotFound
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	app.Post("/api/streams/:name/record/start", func(c fiber.Ctx) error {
		stream, ok := manager.StreamByNameQualityAny(resolveStreamRouteName(manager, c.Params("name")), streamVariantQuery(c))
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
		if err := stream.Inst.StartRecording(path, req.Mode, req.OfflineMode); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.Status(fiber.StatusCreated).JSON(recordingResponse(stream))
	})
	app.Post("/api/streams/:name/record/stop", func(c fiber.Ctx) error {
		stream, ok := manager.StreamByNameQualityAny(resolveStreamRouteName(manager, c.Params("name")), streamVariantQuery(c))
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
		stream, ok := manager.StreamByNameQualityAny(resolveStreamRouteName(manager, c.Params("name")), streamVariantQuery(c))
		if !ok {
			return fiber.ErrNotFound
		}
		return c.JSON(recordingResponse(stream))
	})

	app.Get("/", func(c fiber.Ctx) error {
		return c.Type("html").Send(indexHtml)
	})
	app.Get("/deskview/:name", func(c fiber.Ctx) error {
		if _, ok := manager.StreamByName(resolveStreamRouteName(manager, c.Params("name"))); !ok {
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
		for i, s := range manager.List() {
			log.Printf("  - /stream/no/%d (%s) -> %s", i, streamDisplayName(s), s.URL)
		}
		if err := app.Listen(addr); err != nil {
			log.Printf("HTTP: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if tuiModel != nil {
		runTUI(ctx, tuiModel)
	} else {
		runServer(ctx, manager)
	}
}

func runServer(ctx context.Context, manager *streamcore.Manager) {
	_ = ctx
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("shutting down")
	manager.Close()
}

func runTUI(ctx context.Context, model *Model) {
	_ = ctx
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		log.Fatalf("Model: %v", err)
	}
}

type logWriter struct {
	model *Model
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	lines := strings.Split(string(p), "\n")
	w.model.logMu.Lock()
	defer w.model.logMu.Unlock()
	for _, line := range lines {
		msg := strings.TrimSpace(line)
		if msg == "" {
			continue
		}
		w.model.logs = append(w.model.logs, msg)
		if len(w.model.logs) > 100 {
			w.model.logs = w.model.logs[len(w.model.logs)-100:]
		}
	}
	return len(p), nil
}
