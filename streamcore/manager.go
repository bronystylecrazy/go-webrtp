package streamcore

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gwebrtp "github.com/bronystylecrazy/go-webrtp"
	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
	"gopkg.in/yaml.v3"
)

type Config struct {
	TelemetryServiceName *string     `yaml:"telemetryServiceName"`
	TelemetryEndpoint    *string     `yaml:"telemetryEndpoint"`
	Upstreams            []*Upstream `yaml:"upstreams"`
}

type Upstream struct {
	Name              *string           `yaml:"name" json:"name,omitempty"`
	SourceType        *string           `yaml:"sourceType" json:"sourceType,omitempty"`
	RtspUrl           string            `yaml:"rtspUrl" json:"rtspUrl,omitempty"`
	Device            string            `yaml:"device" json:"device,omitempty"`
	Path              string            `yaml:"path" json:"path,omitempty"`
	Codec             string            `yaml:"codec" json:"codec,omitempty"`
	FFmpegInputFormat string            `yaml:"ffmpegInputFormat" json:"ffmpegInputFormat,omitempty"`
	FFmpegInputArgs   []string          `yaml:"ffmpegInputArgs" json:"ffmpegInputArgs,omitempty"`
	FFmpegFilter      string            `yaml:"ffmpegFilter" json:"ffmpegFilter,omitempty"`
	FFmpegEncoder     string            `yaml:"ffmpegEncoder" json:"ffmpegEncoder,omitempty"`
	FFmpegEncoderArgs []string          `yaml:"ffmpegEncoderArgs" json:"ffmpegEncoderArgs,omitempty"`
	H264Profile       *string           `yaml:"h264Profile" json:"h264Profile,omitempty"`
	Width             *int              `yaml:"width" json:"width,omitempty"`
	Height            *int              `yaml:"height" json:"height,omitempty"`
	FrameRate         *float64          `yaml:"frameRate" json:"frameRate,omitempty"`
	BitrateKbps       *int              `yaml:"bitrateKbps" json:"bitrateKbps,omitempty"`
	ServeStream       *bool             `yaml:"serveStream,omitempty" json:"serveStream,omitempty"`
	CalibrationFrom   string            `yaml:"calibrationFrom,omitempty" json:"calibrationFrom,omitempty"`
	KeyframeSink      string            `yaml:"keyframeSink" json:"keyframeSink,omitempty"`
	KeyframeOutput    string            `yaml:"keyframeOutput" json:"keyframeOutput,omitempty"`
	KeyframeFormat    string            `yaml:"keyframeFormat" json:"keyframeFormat,omitempty"`
	KeyframeMqttURL   string            `yaml:"keyframeMqttUrl,omitempty" json:"keyframeMqttUrl,omitempty"`
	KeyframeMqttTopic string            `yaml:"keyframeMqttTopic,omitempty" json:"keyframeMqttTopic,omitempty"`
	Keyframer         gwebrtp.Keyframer `yaml:"-" json:"-"`
	Enabled           *bool             `yaml:"enabled" json:"enabled,omitempty"`
	OnDemand          bool              `yaml:"onDemand" json:"onDemand,omitempty"`
	Renditions        []*Rendition      `yaml:"renditions" json:"renditions,omitempty"`
}

type Rendition struct {
	Name         string   `yaml:"name" json:"name"`
	Width        *int     `yaml:"width,omitempty" json:"width,omitempty"`
	Height       *int     `yaml:"height,omitempty" json:"height,omitempty"`
	FrameRate    *float64 `yaml:"frameRate,omitempty" json:"frameRate,omitempty"`
	BitrateKbps  *int     `yaml:"bitrateKbps,omitempty" json:"bitrateKbps,omitempty"`
	FFmpegFilter string   `yaml:"ffmpegFilter,omitempty" json:"ffmpegFilter,omitempty"`
	OnDemand     *bool    `yaml:"onDemand,omitempty" json:"onDemand,omitempty"`
}

type Stream struct {
	Name          string
	GroupName     string
	RenditionName string
	URL           string
	Inst          *gwebrtp.Instance
	Hub           *gwebrtp.Hub
	Stop          func() error
	OnDemand      bool
	startMu       sync.Mutex
	started       atomic.Bool
	stopTimerMu   sync.Mutex
	stopTimer     *time.Timer
	shared        *sharedSourceController
}

type Group struct {
	Name     string
	Upstream *Upstream
	Streams  []*Stream
	Default  *Stream
}

type StreamRequest struct {
	Name              *string           `json:"name"`
	SourceType        *string           `json:"sourceType"`
	RtspUrl           string            `json:"rtspUrl"`
	Device            string            `json:"device"`
	Path              string            `json:"path"`
	Codec             string            `json:"codec"`
	FFmpegInputFormat string            `json:"ffmpegInputFormat"`
	FFmpegInputArgs   []string          `json:"ffmpegInputArgs"`
	FFmpegFilter      string            `json:"ffmpegFilter"`
	FFmpegEncoder     string            `json:"ffmpegEncoder"`
	FFmpegEncoderArgs []string          `json:"ffmpegEncoderArgs"`
	Width             *int              `json:"width"`
	Height            *int              `json:"height"`
	FrameRate         *float64          `json:"frameRate"`
	BitrateKbps       *int              `json:"bitrateKbps"`
	ServeStream       *bool             `json:"serveStream,omitempty"`
	CalibrationFrom   string            `json:"calibrationFrom,omitempty"`
	KeyframeSink      string            `json:"keyframeSink,omitempty"`
	KeyframeOutput    string            `json:"keyframeOutput,omitempty"`
	KeyframeFormat    string            `json:"keyframeFormat,omitempty"`
	KeyframeMqttURL   string            `json:"keyframeMqttUrl,omitempty"`
	KeyframeMqttTopic string            `json:"keyframeMqttTopic,omitempty"`
	Keyframer         gwebrtp.Keyframer `json:"-"`
	Enabled           *bool             `json:"enabled"`
	OnDemand          bool              `json:"onDemand"`
	Renditions        []*Rendition      `json:"renditions"`
}

type ModeRequest struct {
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	FrameRate *float64 `json:"frameRate"`
}

type RenditionResponse struct {
	Name         string               `json:"name"`
	Width        *int                 `json:"width,omitempty"`
	Height       *int                 `json:"height,omitempty"`
	FrameRate    *float64             `json:"frameRate,omitempty"`
	BitrateKbps  *int                 `json:"bitrateKbps,omitempty"`
	FFmpegFilter string               `json:"ffmpegFilter,omitempty"`
	OnDemand     bool                 `json:"onDemand"`
	WsPath       string               `json:"wsPath"`
	Stats        *gwebrtp.StreamStats `json:"stats,omitempty"`
}

type StreamResponse struct {
	Index             int                  `json:"index"`
	Name              string               `json:"name"`
	SourceType        string               `json:"sourceType"`
	RtspUrl           string               `json:"rtspUrl,omitempty"`
	Device            string               `json:"device,omitempty"`
	Path              string               `json:"path,omitempty"`
	Codec             string               `json:"codec,omitempty"`
	FFmpegInputFormat string               `json:"ffmpegInputFormat,omitempty"`
	FFmpegInputArgs   []string             `json:"ffmpegInputArgs,omitempty"`
	FFmpegFilter      string               `json:"ffmpegFilter,omitempty"`
	FFmpegEncoder     string               `json:"ffmpegEncoder,omitempty"`
	FFmpegEncoderArgs []string             `json:"ffmpegEncoderArgs,omitempty"`
	Width             *int                 `json:"width,omitempty"`
	Height            *int                 `json:"height,omitempty"`
	FrameRate         *float64             `json:"frameRate,omitempty"`
	BitrateKbps       *int                 `json:"bitrateKbps,omitempty"`
	ServeStream       bool                 `json:"serveStream"`
	CalibrationFrom   string               `json:"calibrationFrom,omitempty"`
	KeyframeSink      string               `json:"keyframeSink,omitempty"`
	KeyframeOutput    string               `json:"keyframeOutput,omitempty"`
	KeyframeFormat    string               `json:"keyframeFormat,omitempty"`
	KeyframeMqttURL   string               `json:"keyframeMqttUrl,omitempty"`
	KeyframeMqttTopic string               `json:"keyframeMqttTopic,omitempty"`
	Enabled           bool                 `json:"enabled"`
	OnDemand          bool                 `json:"onDemand,omitempty"`
	URL               string               `json:"url"`
	WsPath            string               `json:"wsPath"`
	Stats             *gwebrtp.StreamStats `json:"stats"`
	Renditions        []*RenditionResponse `json:"renditions,omitempty"`
	ActiveRendition   string               `json:"activeRendition,omitempty"`
}

type CapabilitiesResponse struct {
	Name         string                         `json:"name"`
	SourceType   string                         `json:"sourceType"`
	Device       string                         `json:"device,omitempty"`
	Capabilities *gwebrtp.UsbDeviceCapabilities `json:"capabilities,omitempty"`
}

type Manager struct {
	mu            sync.RWMutex
	configPath    string
	config        *Config
	groups        []*Group
	groupsByName  map[string]*Group
	streamsByName map[string]*Stream
}

type globalLogWriter struct{}

func (globalLogWriter) Write(p []byte) (int, error) {
	return log.Writer().Write(p)
}

type ManagerOption func(*managerOptions) error

type managerOptions struct {
	configPath string
	config     *Config
}

func WithConfigFile(path string) ManagerOption {
	return func(opts *managerOptions) error {
		opts.configPath = path
		return nil
	}
}

func WithConfig(cfg *Config) ManagerOption {
	return func(opts *managerOptions) error {
		opts.config = cfg
		return nil
	}
}

func NewManager(options ...ManagerOption) (*Manager, error) {
	opts := managerOptions{
		config: &Config{},
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&opts); err != nil {
			return nil, err
		}
	}
	if opts.config == nil {
		opts.config = &Config{}
	}
	m := &Manager{
		configPath:    opts.configPath,
		config:        opts.config,
		groups:        make([]*Group, 0),
		groupsByName:  make(map[string]*Group),
		streamsByName: make(map[string]*Stream),
	}
	for _, upstream := range opts.config.Upstreams {
		if upstream.Enabled == nil {
			upstream.Enabled = boolPtr(true)
		}
		if upstream.ServeStream == nil {
			upstream.ServeStream = boolPtr(true)
		}
		group, err := m.groupCreate(len(m.groups), upstream)
		if err != nil {
			m.Close()
			return nil, err
		}
		m.groups = append(m.groups, group)
		m.groupsByName[group.Name] = group
		for _, stream := range group.Streams {
			m.streamsByName[stream.Name] = stream
		}
	}
	return m, nil
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (m *Manager) Save() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.saveLocked()
}

func (m *Manager) Close() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, group := range m.groups {
		for _, stream := range group.Streams {
			_ = stream.Stop()
		}
	}
}

func (m *Manager) List() []*Stream {
	m.mu.RLock()
	defer m.mu.RUnlock()
	streams := make([]*Stream, 0, len(m.groups))
	for _, group := range m.groups {
		if !UpstreamEnabled(group.Upstream) || !UpstreamServeStream(group.Upstream) || group.Default == nil {
			continue
		}
		streams = append(streams, group.Default)
	}
	return streams
}

func (m *Manager) ListExpanded() []*Stream {
	m.mu.RLock()
	defer m.mu.RUnlock()
	streams := make([]*Stream, 0, len(m.streamsByName))
	for _, group := range m.groups {
		if !UpstreamEnabled(group.Upstream) || !UpstreamServeStream(group.Upstream) || len(group.Streams) == 0 {
			continue
		}
		if len(group.Streams) == 1 {
			streams = append(streams, group.Streams[0])
			continue
		}
		streams = append(streams, group.Streams...)
	}
	return streams
}

func (m *Manager) ListExpandedActive() []*Stream {
	m.mu.RLock()
	defer m.mu.RUnlock()
	streams := make([]*Stream, 0, len(m.streamsByName))
	for _, group := range m.groups {
		if !UpstreamEnabled(group.Upstream) || !UpstreamServeStream(group.Upstream) || len(group.Streams) == 0 {
			continue
		}
		if len(group.Streams) == 1 {
			streams = append(streams, group.Streams[0])
			continue
		}
		active := make([]*Stream, 0, len(group.Streams))
		for _, stream := range group.Streams {
			if stream.ActiveClientCount() > 0 {
				active = append(active, stream)
			}
		}
		if len(active) > 0 {
			streams = append(streams, active...)
			continue
		}
		streams = append(streams, group.Default)
	}
	return streams
}

func (m *Manager) ListResponses() []*StreamResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]*StreamResponse, 0, len(m.groups))
	for idx, group := range m.groups {
		items = append(items, m.streamResponse(idx, group))
	}
	return items
}

func (m *Manager) Get(name string) (*StreamResponse, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for idx, group := range m.groups {
		if group.Name == name {
			return m.streamResponse(idx, group), true
		}
	}
	return nil, false
}

func (m *Manager) StreamByIndex(index int) (*Stream, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if index < 0 || index >= len(m.groups) {
		return nil, false
	}
	if !UpstreamEnabled(m.groups[index].Upstream) || !UpstreamServeStream(m.groups[index].Upstream) || m.groups[index].Default == nil {
		return nil, false
	}
	return m.groups[index].Default, true
}

func (m *Manager) Capabilities(name string) (*CapabilitiesResponse, bool, error) {
	m.mu.RLock()
	group, ok := m.groupsByName[name]
	m.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	resp := &CapabilitiesResponse{
		Name:       group.Name,
		SourceType: UpstreamSourceType(group.Upstream),
		Device:     group.Upstream.Device,
	}
	if resp.SourceType != "usb" {
		return resp, true, nil
	}
	caps, err := gwebrtp.UsbDeviceCapabilitiesGet(group.Upstream.Device)
	if err != nil {
		return nil, true, err
	}
	resp.Capabilities = caps
	return resp, true, nil
}

func (m *Manager) CalibrationTargets(name, quality string) []*Stream {
	m.mu.RLock()
	defer m.mu.RUnlock()

	targets := make([]*Stream, 0, 4)
	seen := make(map[string]struct{})
	appendStream := func(stream *Stream) {
		if stream == nil {
			return
		}
		if _, ok := seen[stream.Name]; ok {
			return
		}
		seen[stream.Name] = struct{}{}
		targets = append(targets, stream)
	}

	if stream, ok := m.streamByNameQualityLocked(name, quality, false); ok {
		appendStream(stream)
	}
	for _, group := range m.groups {
		if group == nil || group.Upstream == nil || group.Upstream.CalibrationFrom != name || len(group.Streams) == 0 {
			continue
		}
		if quality == "" {
			appendStream(group.Default)
			continue
		}
		matched := false
		for _, stream := range group.Streams {
			if strings.EqualFold(stream.RenditionName, quality) {
				appendStream(stream)
				matched = true
				break
			}
		}
		if !matched {
			appendStream(group.Default)
		}
	}
	return targets
}

func (m *Manager) StreamByName(name string) (*Stream, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.streamByNameLocked(name, true)
}

func (m *Manager) StreamByNameAny(name string) (*Stream, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.streamByNameLocked(name, false)
}

func (m *Manager) StreamByNameQuality(name, quality string) (*Stream, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.streamByNameQualityLocked(name, quality, true)
}

func (m *Manager) StreamByNameQualityAny(name, quality string) (*Stream, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.streamByNameQualityLocked(name, quality, false)
}

func (m *Manager) Create(req *StreamRequest) (*StreamResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	upstream, err := RequestUpstream(req)
	if err != nil {
		return nil, err
	}
	name := UpstreamName(len(m.groups), upstream)
	if _, ok := m.groupsByName[name]; ok {
		return nil, fmt.Errorf("stream name already exists: %s", name)
	}
	group, err := m.groupCreate(len(m.groups), upstream)
	if err != nil {
		return nil, err
	}
	m.config.Upstreams = append(m.config.Upstreams, upstream)
	m.groups = append(m.groups, group)
	m.groupsByName[group.Name] = group
	for _, stream := range group.Streams {
		m.streamsByName[stream.Name] = stream
	}
	if err := m.saveLocked(); err != nil {
		delete(m.groupsByName, group.Name)
		m.groups = m.groups[:len(m.groups)-1]
		m.config.Upstreams = m.config.Upstreams[:len(m.config.Upstreams)-1]
		for _, stream := range group.Streams {
			delete(m.streamsByName, stream.Name)
			_ = stream.Stop()
		}
		return nil, err
	}
	return m.streamResponse(len(m.groups)-1, group), nil
}

func (m *Manager) Update(name string, req *StreamRequest) (*StreamResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	index := -1
	for idx, group := range m.groups {
		if group.Name == name {
			index = idx
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("stream not found: %s", name)
	}
	upstream, err := RequestUpstream(req)
	if err != nil {
		return nil, err
	}
	newName := UpstreamName(index, upstream)
	if newName != name {
		if _, ok := m.groupsByName[newName]; ok {
			return nil, fmt.Errorf("stream name already exists: %s", newName)
		}
	}
	oldUpstream := m.config.Upstreams[index]
	oldGroup := m.groups[index]
	if req.Enabled == nil {
		upstream.Enabled = oldUpstream.Enabled
	}
	if req.Keyframer == nil {
		upstream.Keyframer = oldUpstream.Keyframer
	}
	group, err := m.groupCreate(index, upstream)
	if err != nil {
		return nil, err
	}
	m.config.Upstreams[index] = upstream
	m.groups[index] = group
	delete(m.groupsByName, name)
	m.groupsByName[group.Name] = group
	for _, stream := range oldGroup.Streams {
		delete(m.streamsByName, stream.Name)
	}
	for _, stream := range group.Streams {
		m.streamsByName[stream.Name] = stream
	}
	if err := m.saveLocked(); err != nil {
		m.config.Upstreams[index] = oldUpstream
		m.groups[index] = oldGroup
		delete(m.groupsByName, group.Name)
		m.groupsByName[oldGroup.Name] = oldGroup
		for _, stream := range group.Streams {
			delete(m.streamsByName, stream.Name)
			_ = stream.Stop()
		}
		for _, stream := range oldGroup.Streams {
			m.streamsByName[stream.Name] = stream
		}
		return nil, err
	}
	for _, stream := range oldGroup.Streams {
		_ = stream.Stop()
	}
	return m.streamResponse(index, group), nil
}

func (m *Manager) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	index := -1
	var group *Group
	for idx, item := range m.groups {
		if item.Name == name {
			index = idx
			group = item
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("stream not found: %s", name)
	}
	oldUpstreams := append(make([]*Upstream, 0, len(m.config.Upstreams)), m.config.Upstreams...)
	oldGroups := append(make([]*Group, 0, len(m.groups)), m.groups...)
	oldGroupsByName := cloneGroupMap(m.groupsByName)
	oldStreamsByName := cloneStreamMap(m.streamsByName)
	m.config.Upstreams = append(m.config.Upstreams[:index], m.config.Upstreams[index+1:]...)
	m.groups = append(m.groups[:index], m.groups[index+1:]...)
	delete(m.groupsByName, name)
	for _, stream := range group.Streams {
		delete(m.streamsByName, stream.Name)
	}
	if err := m.saveLocked(); err != nil {
		m.config.Upstreams = oldUpstreams
		m.groups = oldGroups
		m.groupsByName = oldGroupsByName
		m.streamsByName = oldStreamsByName
		return err
	}
	for _, stream := range group.Streams {
		_ = stream.Stop()
	}
	return nil
}

func (m *Manager) SetEnabled(name string, enabled bool) (*StreamResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	index := -1
	for idx, group := range m.groups {
		if group.Name == name {
			index = idx
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("stream not found: %s", name)
	}
	oldUpstream := m.config.Upstreams[index]
	oldGroup := m.groups[index]
	upstream := *oldUpstream
	upstream.Enabled = boolPtr(enabled)
	group, err := m.groupCreate(index, &upstream)
	if err != nil {
		return nil, err
	}
	m.config.Upstreams[index] = &upstream
	m.groups[index] = group
	delete(m.groupsByName, name)
	m.groupsByName[group.Name] = group
	for _, stream := range oldGroup.Streams {
		delete(m.streamsByName, stream.Name)
	}
	for _, stream := range group.Streams {
		m.streamsByName[stream.Name] = stream
	}
	if err := m.saveLocked(); err != nil {
		m.config.Upstreams[index] = oldUpstream
		m.groups[index] = oldGroup
		delete(m.groupsByName, group.Name)
		m.groupsByName[oldGroup.Name] = oldGroup
		for _, stream := range group.Streams {
			delete(m.streamsByName, stream.Name)
			_ = stream.Stop()
		}
		for _, stream := range oldGroup.Streams {
			m.streamsByName[stream.Name] = stream
		}
		return nil, err
	}
	for _, stream := range oldGroup.Streams {
		_ = stream.Stop()
	}
	return m.streamResponse(index, group), nil
}

func (m *Manager) Start(name, quality string) (*StreamResponse, error) {
	stream, ok := m.StreamByNameQualityAny(name, quality)
	if !ok {
		return nil, fmt.Errorf("stream not found: %s", name)
	}
	stream.EnsureStarted()
	groupName := stream.GroupName
	if groupName == "" {
		groupName = stream.Name
	}
	item, ok := m.Get(groupName)
	if !ok {
		return nil, fmt.Errorf("stream not found: %s", name)
	}
	return item, nil
}

func (m *Manager) Stop(name, quality string) (*StreamResponse, error) {
	stream, ok := m.StreamByNameQualityAny(name, quality)
	if !ok {
		return nil, fmt.Errorf("stream not found: %s", name)
	}
	if err := stream.StopNow(); err != nil {
		return nil, err
	}
	groupName := stream.GroupName
	if groupName == "" {
		groupName = stream.Name
	}
	item, ok := m.Get(groupName)
	if !ok {
		return nil, fmt.Errorf("stream not found: %s", name)
	}
	return item, nil
}

func (m *Manager) UpdateMode(name string, req *ModeRequest) (*StreamResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	index := -1
	for idx, group := range m.groups {
		if group.Name == name {
			index = idx
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("stream not found: %s", name)
	}
	if req.Width <= 0 || req.Height <= 0 {
		return nil, fmt.Errorf("mode update requires positive width and height")
	}
	oldUpstream := m.config.Upstreams[index]
	upstream := &Upstream{
		Name:              oldUpstream.Name,
		SourceType:        oldUpstream.SourceType,
		RtspUrl:           oldUpstream.RtspUrl,
		Device:            oldUpstream.Device,
		Path:              oldUpstream.Path,
		Codec:             oldUpstream.Codec,
		FFmpegInputFormat: oldUpstream.FFmpegInputFormat,
		FFmpegInputArgs:   oldUpstream.FFmpegInputArgs,
		FFmpegFilter:      oldUpstream.FFmpegFilter,
		FFmpegEncoder:     oldUpstream.FFmpegEncoder,
		FFmpegEncoderArgs: oldUpstream.FFmpegEncoderArgs,
		H264Profile:       oldUpstream.H264Profile,
		Width:             &req.Width,
		Height:            &req.Height,
		FrameRate:         req.FrameRate,
		BitrateKbps:       oldUpstream.BitrateKbps,
		ServeStream:       oldUpstream.ServeStream,
		CalibrationFrom:   oldUpstream.CalibrationFrom,
		KeyframeSink:      oldUpstream.KeyframeSink,
		KeyframeOutput:    oldUpstream.KeyframeOutput,
		KeyframeFormat:    oldUpstream.KeyframeFormat,
		KeyframeMqttURL:   oldUpstream.KeyframeMqttURL,
		KeyframeMqttTopic: oldUpstream.KeyframeMqttTopic,
		Keyframer:         oldUpstream.Keyframer,
		Enabled:           oldUpstream.Enabled,
		OnDemand:          oldUpstream.OnDemand,
		Renditions:        oldUpstream.Renditions,
	}
	group, err := m.groupCreate(index, upstream)
	if err != nil {
		return nil, err
	}
	oldGroup := m.groups[index]
	m.config.Upstreams[index] = upstream
	m.groups[index] = group
	delete(m.groupsByName, name)
	m.groupsByName[group.Name] = group
	for _, stream := range oldGroup.Streams {
		delete(m.streamsByName, stream.Name)
	}
	for _, stream := range group.Streams {
		m.streamsByName[stream.Name] = stream
	}
	if err := m.saveLocked(); err != nil {
		m.config.Upstreams[index] = oldUpstream
		m.groups[index] = oldGroup
		delete(m.groupsByName, group.Name)
		m.groupsByName[oldGroup.Name] = oldGroup
		for _, stream := range group.Streams {
			delete(m.streamsByName, stream.Name)
			_ = stream.Stop()
		}
		for _, stream := range oldGroup.Streams {
			m.streamsByName[stream.Name] = stream
		}
		return nil, err
	}
	for _, stream := range oldGroup.Streams {
		_ = stream.Stop()
	}
	return m.streamResponse(index, group), nil
}

func (m *Manager) groupCreate(index int, upstream *Upstream) (*Group, error) {
	if err := ValidateUpstream(upstream); err != nil {
		return nil, err
	}
	if UpstreamUsesUSBFFmpeg(upstream) {
		return m.groupCreateUSBFFmpeg(index, upstream)
	}
	groupName := UpstreamName(index, upstream)
	group := &Group{
		Name:     groupName,
		Upstream: upstream,
		Streams:  make([]*Stream, 0),
	}
	if !UpstreamEnabled(upstream) {
		return group, nil
	}
	renditions := upstream.Renditions
	if len(renditions) == 0 {
		stream, err := m.streamCreate(groupName, "", upstream)
		if err != nil {
			return nil, err
		}
		group.Streams = append(group.Streams, stream)
		group.Default = stream
		return group, nil
	}
	defaultIdx := RenditionDefaultIndex(renditions)
	for idx, rendition := range renditions {
		streamUpstream := UpstreamWithRendition(upstream, rendition)
		stream, err := m.streamCreate(groupName, rendition.Name, streamUpstream)
		if err != nil {
			for _, item := range group.Streams {
				_ = item.Stop()
			}
			return nil, err
		}
		group.Streams = append(group.Streams, stream)
		if idx == defaultIdx {
			group.Default = stream
		}
	}
	if group.Default == nil && len(group.Streams) > 0 {
		group.Default = group.Streams[0]
	}
	return group, nil
}

func (m *Manager) streamCreate(groupName, renditionName string, upstream *Upstream) (*Stream, error) {
	loggerName := groupName
	streamName := groupName
	if renditionName != "" {
		streamName = VariantName(groupName, renditionName)
		loggerName = fmt.Sprintf("%s/%s", groupName, renditionName)
	}
	logger := log.New(globalLogWriter{}, fmt.Sprintf("[%s] ", loggerName), log.LstdFlags)
	sourceType := UpstreamSourceType(upstream)
	frameRate := 0.0
	if upstream.FrameRate != nil {
		frameRate = *upstream.FrameRate
	}
	bitrateKbps := 0
	if upstream.BitrateKbps != nil {
		bitrateKbps = *upstream.BitrateKbps
	}
	url := upstream.RtspUrl
	if sourceType == "usb" {
		url = upstream.Device
	} else if sourceType == "file" {
		url = upstream.Path
	}
	inst := gwebrtp.Init(&gwebrtp.Config{
		SourceType:        sourceType,
		StreamName:        streamName,
		Rtsp:              upstream.RtspUrl,
		Device:            upstream.Device,
		Path:              upstream.Path,
		Codec:             upstream.Codec,
		H264Profile:       valueOrEmpty(upstream.H264Profile),
		Width:             valueOrZero(upstream.Width),
		Height:            valueOrZero(upstream.Height),
		FrameRate:         frameRate,
		BitrateKbps:       bitrateKbps,
		KeyframeSink:      upstream.KeyframeSink,
		KeyframeOutput:    upstream.KeyframeOutput,
		KeyframeFormat:    upstream.KeyframeFormat,
		KeyframeMqttURL:   upstream.KeyframeMqttURL,
		KeyframeMqttTopic: upstream.KeyframeMqttTopic,
		Keyframer:         upstream.Keyframer,
		Logger:            logger,
	})
	stream := &Stream{
		Name:          streamName,
		GroupName:     groupName,
		RenditionName: renditionName,
		URL:           url,
		Inst:          inst,
		Hub:           inst.GetHub(),
		Stop:          inst.Stop,
		OnDemand:      upstream.OnDemand,
	}
	if stream.OnDemand {
		stream.started.Store(false)
	} else {
		stream.started.Store(true)
		go func() {
			if err := stream.Inst.Connect(); err != nil {
				logger.Printf("stream %s: %v", stream.Name, err)
			}
			stream.started.Store(false)
		}()
	}
	return stream, nil
}

func (m *Manager) saveLocked() error {
	if strings.TrimSpace(m.configPath) == "" {
		return nil
	}
	data, err := yaml.Marshal(m.config)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(m.configPath, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (m *Manager) streamByNameLocked(name string, requireServed bool) (*Stream, bool) {
	if stream, ok := m.streamsByName[name]; ok {
		if requireServed && !UpstreamServeStream(m.groupsByName[stream.GroupName].Upstream) {
			return nil, false
		}
		return stream, true
	}
	group, ok := m.groupsByName[name]
	if !ok {
		return nil, false
	}
	if !UpstreamEnabled(group.Upstream) || group.Default == nil {
		return nil, false
	}
	if requireServed && !UpstreamServeStream(group.Upstream) {
		return nil, false
	}
	return group.Default, true
}

func (m *Manager) streamByNameQualityLocked(name, quality string, requireServed bool) (*Stream, bool) {
	group, ok := m.groupsByName[name]
	if !ok {
		if stream, ok := m.streamsByName[name]; ok {
			if requireServed {
				group, groupOK := m.groupsByName[stream.GroupName]
				if !groupOK || !UpstreamServeStream(group.Upstream) {
					return nil, false
				}
			}
			return stream, true
		}
		return nil, false
	}
	if !UpstreamEnabled(group.Upstream) || group.Default == nil {
		return nil, false
	}
	if requireServed && !UpstreamServeStream(group.Upstream) {
		return nil, false
	}
	if quality == "" {
		return group.Default, true
	}
	for _, stream := range group.Streams {
		if strings.EqualFold(stream.RenditionName, quality) {
			return stream, true
		}
	}
	return group.Default, true
}

func (m *Manager) streamResponse(index int, group *Group) *StreamResponse {
	stats := gwebrtp.StreamStats{}
	if group.Default != nil {
		stats = group.Default.Hub.GetStats(group.Name)
		stats.Name = group.Name
	}
	sourceType := UpstreamSourceType(group.Upstream)
	resp := &StreamResponse{
		Index:             index,
		Name:              group.Name,
		SourceType:        sourceType,
		RtspUrl:           group.Upstream.RtspUrl,
		Device:            group.Upstream.Device,
		Path:              group.Upstream.Path,
		Codec:             group.Upstream.Codec,
		FFmpegInputFormat: group.Upstream.FFmpegInputFormat,
		FFmpegInputArgs:   append([]string(nil), group.Upstream.FFmpegInputArgs...),
		FFmpegFilter:      group.Upstream.FFmpegFilter,
		FFmpegEncoder:     group.Upstream.FFmpegEncoder,
		FFmpegEncoderArgs: append([]string(nil), group.Upstream.FFmpegEncoderArgs...),
		Width:             group.Upstream.Width,
		Height:            group.Upstream.Height,
		FrameRate:         group.Upstream.FrameRate,
		BitrateKbps:       group.Upstream.BitrateKbps,
		ServeStream:       UpstreamServeStream(group.Upstream),
		CalibrationFrom:   group.Upstream.CalibrationFrom,
		KeyframeSink:      group.Upstream.KeyframeSink,
		KeyframeOutput:    group.Upstream.KeyframeOutput,
		KeyframeFormat:    group.Upstream.KeyframeFormat,
		KeyframeMqttURL:   group.Upstream.KeyframeMqttURL,
		KeyframeMqttTopic: group.Upstream.KeyframeMqttTopic,
		Enabled:           UpstreamEnabled(group.Upstream),
		OnDemand:          group.Upstream.OnDemand,
		URL:               "",
		WsPath:            "",
		Stats:             &stats,
	}
	if group.Default != nil {
		resp.URL = group.Default.URL
		if UpstreamServeStream(group.Upstream) {
			resp.WsPath = groupWsPath(group.Name)
		}
		resp.ActiveRendition = group.Default.RenditionName
	}
	if len(group.Streams) > 1 {
		resp.Renditions = make([]*RenditionResponse, 0, len(group.Streams))
		for _, stream := range group.Streams {
			var bitrate *int
			var width *int
			var height *int
			var frameRate *float64
			for _, rendition := range group.Upstream.Renditions {
				if rendition.Name == stream.RenditionName {
					bitrate = rendition.BitrateKbps
					width = rendition.Width
					height = rendition.Height
					frameRate = rendition.FrameRate
					break
				}
			}
			stats := stream.Hub.GetStats(stream.Name)
			stats.Name = stream.Name
			resp.Renditions = append(resp.Renditions, &RenditionResponse{
				Name:         stream.RenditionName,
				Width:        width,
				Height:       height,
				FrameRate:    frameRate,
				BitrateKbps:  bitrate,
				FFmpegFilter: valueOrEmptyRenditionFilter(group.Upstream.Renditions, stream.RenditionName),
				OnDemand:     stream.OnDemand,
				WsPath:       streamWsPath(stream, group.Upstream),
				Stats:        &stats,
			})
		}
	}
	return resp
}

func (s *Stream) FiberHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}
		return websocket.New(func(conn *websocket.Conn) {
			s.HandleWebsocket(conn)
		})(c)
	}
}

func (s *Stream) HandleWebsocket(conn *websocket.Conn) {
	s.Inst.HandleWebsocket(conn)
	s.MaybeScheduleStop(5 * time.Second)
}

func (s *Stream) EnsureStarted() {
	if s.shared != nil {
		s.shared.EnsureStarted()
		return
	}
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
	if s.shared != nil {
		return s.shared.HasActiveRecording()
	}
	return s.Inst.RecordingStatus().Active
}

func (s *Stream) StopNow() error {
	if s.shared != nil {
		return s.shared.StopNow()
	}
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
	if s.shared != nil {
		s.shared.MaybeScheduleStop(idle)
		return
	}
	if !s.OnDemand || s.HasActiveRecording() || s.ActiveClientCount() > 0 {
		return
	}
	s.stopTimerMu.Lock()
	defer s.stopTimerMu.Unlock()
	if s.stopTimer != nil {
		s.stopTimer.Stop()
	}
	s.stopTimer = time.AfterFunc(idle, func() {
		if s.ActiveClientCount() > 0 || s.HasActiveRecording() {
			return
		}
		_ = s.StopNow()
	})
}

func (s *Stream) ActiveClientCount() int32 {
	return s.Hub.GetStats(s.Name).ClientCount
}

func UpstreamSourceType(upstream *Upstream) string {
	sourceType := "rtsp"
	if upstream.SourceType != nil && *upstream.SourceType != "" {
		sourceType = strings.ToLower(*upstream.SourceType)
	}
	return sourceType
}

func UpstreamName(index int, upstream *Upstream) string {
	name := strconv.Itoa(index)
	if upstream.Name != nil && *upstream.Name != "" {
		name = *upstream.Name
	}
	return name
}

func VariantName(groupName, renditionName string) string {
	return fmt.Sprintf("%s~%s", groupName, renditionName)
}

func ValidateUpstream(upstream *Upstream) error {
	switch UpstreamSourceType(upstream) {
	case "rtsp":
		if upstream.RtspUrl == "" {
			return fmt.Errorf("rtsp upstream missing required rtspUrl")
		}
	case "usb":
		if upstream.Device == "" {
			return fmt.Errorf("usb upstream missing required device")
		}
		if upstream.Codec == "" {
			return fmt.Errorf("usb upstream missing required codec")
		}
	case "file":
		if upstream.Path == "" {
			return fmt.Errorf("file upstream missing required path")
		}
		if upstream.Codec != "" && !strings.EqualFold(upstream.Codec, "h264") {
			return fmt.Errorf("file upstream codec must be h264 when provided")
		}
	default:
		return fmt.Errorf("unsupported sourceType: %s", UpstreamSourceType(upstream))
	}
	if UpstreamUsesUSBFFmpeg(upstream) {
		if strings.TrimSpace(upstream.FFmpegInputFormat) == "" {
			return fmt.Errorf("usb ffmpeg upstream missing required ffmpegInputFormat")
		}
		if upstream.Codec != "" && !strings.EqualFold(upstream.Codec, "h264") {
			return fmt.Errorf("usb ffmpeg upstream codec must be h264")
		}
		for _, rendition := range upstream.Renditions {
			if rendition != nil && rendition.OnDemand != nil {
				return fmt.Errorf("usb ffmpeg renditions do not support per-rendition onDemand overrides")
			}
		}
	}
	if upstream.H264Profile != nil && *upstream.H264Profile != "" {
		profile := strings.ToLower(strings.TrimSpace(*upstream.H264Profile))
		if profile != "baseline" && profile != "main" && profile != "high" {
			return fmt.Errorf("h264Profile must be baseline, main, or high")
		}
	}
	if upstream.KeyframeFormat != "" {
		format := strings.ToLower(upstream.KeyframeFormat)
		if format != "jpg" && format != "jpeg" && format != "png" && format != "h264" {
			return fmt.Errorf("keyframeFormat must be jpg, png, or h264")
		}
	}
	sinks, err := parseKeyframeSinkTargets(upstream.KeyframeSink)
	if err != nil {
		return err
	}
	if sinks["fs"] && strings.TrimSpace(upstream.KeyframeOutput) == "" {
		return fmt.Errorf("keyframeOutput is required when keyframeSink includes fs")
	}
	if sinks["mqtt"] && strings.TrimSpace(upstream.KeyframeMqttURL) == "" {
		return fmt.Errorf("keyframeMqttUrl is required when keyframeSink includes mqtt")
	}
	if upstream.CalibrationFrom != "" {
		if upstream.Name != nil && *upstream.Name == upstream.CalibrationFrom {
			return fmt.Errorf("calibrationFrom cannot reference the same stream")
		}
	}
	if len(upstream.Renditions) > 0 {
		if UpstreamSourceType(upstream) != "usb" {
			return fmt.Errorf("renditions are only supported for usb streams")
		}
		if !UpstreamUsesUSBFFmpeg(upstream) && runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
			return fmt.Errorf("usb renditions are only supported on macos and windows")
		}
		for _, rendition := range upstream.Renditions {
			if rendition == nil {
				return fmt.Errorf("rendition entry is required")
			}
			if rendition.Name == "" {
				return fmt.Errorf("rendition missing required name")
			}
			if rendition.Width != nil && *rendition.Width <= 0 {
				return fmt.Errorf("rendition %s has invalid width", rendition.Name)
			}
			if rendition.Height != nil && *rendition.Height <= 0 {
				return fmt.Errorf("rendition %s has invalid height", rendition.Name)
			}
			if rendition.FrameRate != nil && *rendition.FrameRate <= 0 {
				return fmt.Errorf("rendition %s has invalid frameRate", rendition.Name)
			}
			if rendition.BitrateKbps != nil && *rendition.BitrateKbps <= 0 {
				return fmt.Errorf("rendition %s has invalid bitrateKbps", rendition.Name)
			}
			if strings.TrimSpace(rendition.FFmpegFilter) != "" && !UpstreamUsesUSBFFmpeg(upstream) {
				return fmt.Errorf("rendition %s ffmpegFilter requires usb ffmpeg mode", rendition.Name)
			}
			if rendition.Width == nil && rendition.Height == nil && rendition.FrameRate == nil && rendition.BitrateKbps == nil && strings.TrimSpace(rendition.FFmpegFilter) == "" {
				return fmt.Errorf("rendition %s must override at least one of width, height, frameRate, bitrateKbps, or ffmpegFilter", rendition.Name)
			}
		}
	}
	return nil
}

func RequestUpstream(req *StreamRequest) (*Upstream, error) {
	upstream := &Upstream{
		Name:              req.Name,
		SourceType:        req.SourceType,
		RtspUrl:           req.RtspUrl,
		Device:            req.Device,
		Path:              req.Path,
		Codec:             req.Codec,
		FFmpegInputFormat: req.FFmpegInputFormat,
		FFmpegInputArgs:   req.FFmpegInputArgs,
		FFmpegFilter:      req.FFmpegFilter,
		FFmpegEncoder:     req.FFmpegEncoder,
		FFmpegEncoderArgs: req.FFmpegEncoderArgs,
		Width:             req.Width,
		Height:            req.Height,
		FrameRate:         req.FrameRate,
		BitrateKbps:       req.BitrateKbps,
		ServeStream:       req.ServeStream,
		CalibrationFrom:   req.CalibrationFrom,
		KeyframeSink:      req.KeyframeSink,
		KeyframeOutput:    req.KeyframeOutput,
		KeyframeFormat:    req.KeyframeFormat,
		KeyframeMqttURL:   req.KeyframeMqttURL,
		KeyframeMqttTopic: req.KeyframeMqttTopic,
		Keyframer:         req.Keyframer,
		Enabled:           req.Enabled,
		OnDemand:          req.OnDemand,
		Renditions:        req.Renditions,
	}
	if upstream.Enabled == nil {
		upstream.Enabled = boolPtr(true)
	}
	if upstream.ServeStream == nil {
		upstream.ServeStream = boolPtr(true)
	}
	if err := ValidateUpstream(upstream); err != nil {
		return nil, err
	}
	return upstream, nil
}

func UpstreamUsesUSBFFmpeg(upstream *Upstream) bool {
	if upstream == nil || UpstreamSourceType(upstream) != "usb" {
		return false
	}
	return strings.TrimSpace(upstream.FFmpegInputFormat) != "" ||
		len(upstream.FFmpegInputArgs) > 0 ||
		strings.TrimSpace(upstream.FFmpegFilter) != "" ||
		strings.TrimSpace(upstream.FFmpegEncoder) != "" ||
		len(upstream.FFmpegEncoderArgs) > 0
}

func RenditionDefaultIndex(renditions []*Rendition) int {
	for idx, rendition := range renditions {
		name := strings.ToLower(rendition.Name)
		if name == "mid" || name == "medium" || name == "default" {
			return idx
		}
	}
	if len(renditions) == 0 {
		return 0
	}
	return len(renditions) / 2
}

func UpstreamWithRendition(upstream *Upstream, rendition *Rendition) *Upstream {
	name := UpstreamName(0, upstream)
	sourceType := UpstreamSourceType(upstream)
	width := upstream.Width
	if rendition.Width != nil {
		width = rendition.Width
	}
	height := upstream.Height
	if rendition.Height != nil {
		height = rendition.Height
	}
	frameRate := upstream.FrameRate
	if rendition.FrameRate != nil {
		frameRate = rendition.FrameRate
	}
	bitrate := upstream.BitrateKbps
	if rendition.BitrateKbps != nil {
		bitrate = rendition.BitrateKbps
	}
	onDemand := upstream.OnDemand
	if rendition.OnDemand != nil {
		onDemand = *rendition.OnDemand
	}
	return &Upstream{
		Name:              &name,
		SourceType:        &sourceType,
		RtspUrl:           upstream.RtspUrl,
		Device:            upstream.Device,
		Path:              upstream.Path,
		Codec:             upstream.Codec,
		FFmpegInputFormat: upstream.FFmpegInputFormat,
		FFmpegInputArgs:   append([]string(nil), upstream.FFmpegInputArgs...),
		FFmpegFilter:      upstream.FFmpegFilter,
		FFmpegEncoder:     upstream.FFmpegEncoder,
		FFmpegEncoderArgs: append([]string(nil), upstream.FFmpegEncoderArgs...),
		H264Profile:       upstream.H264Profile,
		Width:             width,
		Height:            height,
		FrameRate:         frameRate,
		BitrateKbps:       bitrate,
		ServeStream:       upstream.ServeStream,
		CalibrationFrom:   upstream.CalibrationFrom,
		KeyframeSink:      upstream.KeyframeSink,
		KeyframeOutput:    upstream.KeyframeOutput,
		KeyframeFormat:    upstream.KeyframeFormat,
		KeyframeMqttURL:   upstream.KeyframeMqttURL,
		KeyframeMqttTopic: upstream.KeyframeMqttTopic,
		Keyframer:         upstream.Keyframer,
		Enabled:           upstream.Enabled,
		OnDemand:          onDemand,
	}
}

func UpstreamEnabled(upstream *Upstream) bool {
	return upstream == nil || upstream.Enabled == nil || *upstream.Enabled
}

func UpstreamServeStream(upstream *Upstream) bool {
	return upstream == nil || upstream.ServeStream == nil || *upstream.ServeStream
}

func SortResponses(items []*StreamResponse) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Index < items[j].Index
	})
}

func boolPtr(v bool) *bool {
	return &v
}

func valueOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func valueOrEmptyRenditionFilter(renditions []*Rendition, name string) string {
	for _, rendition := range renditions {
		if rendition != nil && rendition.Name == name {
			return strings.TrimSpace(rendition.FFmpegFilter)
		}
	}
	return ""
}

func streamWsPath(stream *Stream, upstream *Upstream) string {
	if stream == nil || !UpstreamServeStream(upstream) {
		return ""
	}
	groupName := strings.TrimSpace(stream.GroupName)
	if groupName == "" {
		groupName = stream.Name
	}
	path := groupWsPath(groupName)
	if rendition := strings.TrimSpace(stream.RenditionName); rendition != "" {
		return path + "?map=" + url.QueryEscape(rendition)
	}
	return path
}

func groupWsPath(name string) string {
	return fmt.Sprintf("/streams/%s", url.PathEscape(strings.TrimSpace(name)))
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
			return nil, fmt.Errorf("unsupported keyframeSink target: %s", target)
		}
	}
	return targets, nil
}

func cloneGroupMap(input map[string]*Group) map[string]*Group {
	output := make(map[string]*Group, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneStreamMap(input map[string]*Stream) map[string]*Stream {
	output := make(map[string]*Stream, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
