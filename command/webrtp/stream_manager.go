package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/connectedtechco/go-webrtp"
	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
	"gopkg.in/yaml.v3"
)

type StreamManager struct {
	mu            sync.RWMutex
	configPath    string
	config        *Config
	groups        []*StreamGroup
	groupsByName  map[string]*StreamGroup
	streamsByName map[string]*Stream
}

type StreamGroup struct {
	Name     string
	Upstream *Upstream
	Streams  []*Stream
	Default  *Stream
}

type StreamApiRequest struct {
	Name        *string      `json:"name"`
	SourceType  *string      `json:"sourceType"`
	RtspUrl     string       `json:"rtspUrl"`
	Device      string       `json:"device"`
	Codec       string       `json:"codec"`
	Width       *int         `json:"width"`
	Height      *int         `json:"height"`
	FrameRate   *float64     `json:"frameRate"`
	BitrateKbps *int         `json:"bitrateKbps"`
	OnDemand    bool         `json:"onDemand"`
	Renditions  []*Rendition `json:"renditions"`
}

type RenditionApiResponse struct {
	Name        string               `json:"name"`
	BitrateKbps int                  `json:"bitrateKbps"`
	OnDemand    bool                 `json:"onDemand"`
	WsPath      string               `json:"wsPath"`
	Stats       *webrtp.StreamStats  `json:"stats,omitempty"`
}

type StreamApiResponse struct {
	Index           int                     `json:"index"`
	Name            string                  `json:"name"`
	SourceType      string                  `json:"sourceType"`
	RtspUrl         string                  `json:"rtspUrl,omitempty"`
	Device          string                  `json:"device,omitempty"`
	Codec           string                  `json:"codec,omitempty"`
	Width           *int                    `json:"width,omitempty"`
	Height          *int                    `json:"height,omitempty"`
	FrameRate       *float64                `json:"frameRate,omitempty"`
	BitrateKbps     *int                    `json:"bitrateKbps,omitempty"`
	OnDemand        bool                    `json:"onDemand,omitempty"`
	Url             string                  `json:"url"`
	WsPath          string                  `json:"wsPath"`
	Stats           *webrtp.StreamStats     `json:"stats"`
	Renditions      []*RenditionApiResponse `json:"renditions,omitempty"`
	ActiveRendition string                  `json:"activeRendition,omitempty"`
}

type StreamCapabilitiesResponse struct {
	Name         string                         `json:"name"`
	SourceType   string                         `json:"sourceType"`
	Device       string                         `json:"device,omitempty"`
	Capabilities *webrtp.UsbDeviceCapabilities `json:"capabilities,omitempty"`
}

func StreamManagerNew(configPath string, cfg *Config) (*StreamManager, error) {
	r := &StreamManager{
		configPath:    configPath,
		config:        cfg,
		groups:        make([]*StreamGroup, 0),
		groupsByName:  make(map[string]*StreamGroup),
		streamsByName: make(map[string]*Stream),
	}
	for _, upstream := range cfg.Upstreams {
		group, err := r.streamGroupCreate(len(r.groups), upstream)
		if err != nil {
			r.streamsStop()
			return nil, err
		}
		r.groups = append(r.groups, group)
		r.groupsByName[group.Name] = group
		for _, stream := range group.Streams {
			r.streamsByName[stream.Name] = stream
		}
	}
	return r, nil
}

func (r *StreamManager) StreamList() []*Stream {
	r.mu.RLock()
	defer r.mu.RUnlock()
	streams := make([]*Stream, 0, len(r.groups))
	for _, group := range r.groups {
		streams = append(streams, group.Default)
	}
	return streams
}

func (r *StreamManager) StreamListExpanded() []*Stream {
	r.mu.RLock()
	defer r.mu.RUnlock()
	streams := make([]*Stream, 0, len(r.streamsByName))
	for _, group := range r.groups {
		if len(group.Streams) == 0 {
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

func (r *StreamManager) StreamListExpandedActive() []*Stream {
	r.mu.RLock()
	defer r.mu.RUnlock()
	streams := make([]*Stream, 0, len(r.streamsByName))
	for _, group := range r.groups {
		if len(group.Streams) == 0 {
			continue
		}
		if len(group.Streams) == 1 {
			streams = append(streams, group.Streams[0])
			continue
		}
		active := make([]*Stream, 0, len(group.Streams))
		for _, stream := range group.Streams {
			if stream.Hub.GetStats(stream.Name).ClientCount > 0 {
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

func (r *StreamManager) StreamByName(name string) (*Stream, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if stream, ok := r.streamsByName[name]; ok {
		return stream, true
	}
	group, ok := r.groupsByName[name]
	if !ok {
		return nil, false
	}
	return group.Default, true
}

func (r *StreamManager) StreamByNameQuality(name, quality string) (*Stream, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	group, ok := r.groupsByName[name]
	if !ok {
		if stream, ok := r.streamsByName[name]; ok {
			return stream, true
		}
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

func (r *StreamManager) StreamByIndex(index int) (*Stream, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if index < 0 || index >= len(r.groups) {
		return nil, false
	}
	return r.groups[index].Default, true
}

func (r *StreamManager) StreamStatusList() []*StreamApiResponse {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]*StreamApiResponse, 0, len(r.groups))
	for idx, group := range r.groups {
		items = append(items, r.streamResponse(idx, group))
	}
	return items
}

func (r *StreamManager) StreamStatus(name string) (*StreamApiResponse, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for idx, group := range r.groups {
		if group.Name == name {
			return r.streamResponse(idx, group), true
		}
	}
	return nil, false
}

func (r *StreamManager) StreamCapabilities(name string) (*StreamCapabilitiesResponse, bool, error) {
	r.mu.RLock()
	group, ok := r.groupsByName[name]
	r.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	resp := &StreamCapabilitiesResponse{
		Name:       group.Name,
		SourceType: StreamUpstreamSourceType(group.Upstream),
		Device:     group.Upstream.Device,
	}
	if resp.SourceType != "usb" {
		return resp, true, nil
	}
	caps, err := webrtp.UsbDeviceCapabilitiesGet(group.Upstream.Device)
	if err != nil {
		return nil, true, err
	}
	resp.Capabilities = caps
	return resp, true, nil
}

func (r *StreamManager) StreamCreate(req *StreamApiRequest) (*StreamApiResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	upstream, err := StreamRequestUpstream(req)
	if err != nil {
		return nil, err
	}
	name := StreamUpstreamName(len(r.groups), upstream)
	if _, ok := r.groupsByName[name]; ok {
		return nil, fmt.Errorf("stream name already exists: %s", name)
	}

	group, err := r.streamGroupCreate(len(r.groups), upstream)
	if err != nil {
		return nil, err
	}

	r.config.Upstreams = append(r.config.Upstreams, upstream)
	r.groups = append(r.groups, group)
	r.groupsByName[group.Name] = group
	for _, stream := range group.Streams {
		r.streamsByName[stream.Name] = stream
	}

	if err := r.configSave(); err != nil {
		delete(r.groupsByName, group.Name)
		r.groups = r.groups[:len(r.groups)-1]
		r.config.Upstreams = r.config.Upstreams[:len(r.config.Upstreams)-1]
		for _, stream := range group.Streams {
			delete(r.streamsByName, stream.Name)
			_ = stream.Stop()
		}
		return nil, err
	}
	return r.streamResponse(len(r.groups)-1, group), nil
}

func (r *StreamManager) StreamUpdate(name string, req *StreamApiRequest) (*StreamApiResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	index := -1
	for idx, group := range r.groups {
		if group.Name == name {
			index = idx
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("stream not found: %s", name)
	}

	upstream, err := StreamRequestUpstream(req)
	if err != nil {
		return nil, err
	}
	newName := StreamUpstreamName(index, upstream)
	if newName != name {
		if _, ok := r.groupsByName[newName]; ok {
			return nil, fmt.Errorf("stream name already exists: %s", newName)
		}
	}

	oldUpstream := r.config.Upstreams[index]
	oldGroup := r.groups[index]

	group, err := r.streamGroupCreate(index, upstream)
	if err != nil {
		return nil, err
	}

	r.config.Upstreams[index] = upstream
	r.groups[index] = group
	delete(r.groupsByName, name)
	r.groupsByName[group.Name] = group
	for _, stream := range oldGroup.Streams {
		delete(r.streamsByName, stream.Name)
	}
	for _, stream := range group.Streams {
		r.streamsByName[stream.Name] = stream
	}

	if err := r.configSave(); err != nil {
		r.config.Upstreams[index] = oldUpstream
		r.groups[index] = oldGroup
		delete(r.groupsByName, group.Name)
		r.groupsByName[oldGroup.Name] = oldGroup
		for _, stream := range group.Streams {
			delete(r.streamsByName, stream.Name)
			_ = stream.Stop()
		}
		for _, stream := range oldGroup.Streams {
			r.streamsByName[stream.Name] = stream
		}
		return nil, err
	}

	for _, stream := range oldGroup.Streams {
		_ = stream.Stop()
	}
	return r.streamResponse(index, group), nil
}

func (r *StreamManager) StreamModeUpdate(name string, req *ModeRequest) (*StreamApiResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	index := -1
	for idx, group := range r.groups {
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

	oldUpstream := r.config.Upstreams[index]
	upstream := &Upstream{
		Name:        oldUpstream.Name,
		SourceType:  oldUpstream.SourceType,
		RtspUrl:     oldUpstream.RtspUrl,
		Device:      oldUpstream.Device,
		Codec:       oldUpstream.Codec,
		Width:       &req.Width,
		Height:      &req.Height,
		FrameRate:   req.FrameRate,
		BitrateKbps: oldUpstream.BitrateKbps,
		OnDemand:    oldUpstream.OnDemand,
		Renditions:  oldUpstream.Renditions,
	}

	group, err := r.streamGroupCreate(index, upstream)
	if err != nil {
		return nil, err
	}

	oldGroup := r.groups[index]
	r.config.Upstreams[index] = upstream
	r.groups[index] = group
	delete(r.groupsByName, name)
	r.groupsByName[group.Name] = group
	for _, stream := range oldGroup.Streams {
		delete(r.streamsByName, stream.Name)
	}
	for _, stream := range group.Streams {
		r.streamsByName[stream.Name] = stream
	}

	if err := r.configSave(); err != nil {
		r.config.Upstreams[index] = oldUpstream
		r.groups[index] = oldGroup
		delete(r.groupsByName, group.Name)
		r.groupsByName[oldGroup.Name] = oldGroup
		for _, stream := range group.Streams {
			delete(r.streamsByName, stream.Name)
			_ = stream.Stop()
		}
		for _, stream := range oldGroup.Streams {
			r.streamsByName[stream.Name] = stream
		}
		return nil, err
	}

	for _, stream := range oldGroup.Streams {
		_ = stream.Stop()
	}
	return r.streamResponse(index, group), nil
}

func (r *StreamManager) StreamDelete(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	index := -1
	var group *StreamGroup
	for idx, item := range r.groups {
		if item.Name == name {
			index = idx
			group = item
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("stream not found: %s", name)
	}

	oldUpstreams := append(make([]*Upstream, 0, len(r.config.Upstreams)), r.config.Upstreams...)
	oldGroups := append(make([]*StreamGroup, 0, len(r.groups)), r.groups...)
	oldGroupsByName := make(map[string]*StreamGroup, len(r.groupsByName))
	for key, value := range r.groupsByName {
		oldGroupsByName[key] = value
	}
	oldStreamsByName := make(map[string]*Stream, len(r.streamsByName))
	for key, value := range r.streamsByName {
		oldStreamsByName[key] = value
	}

	r.config.Upstreams = append(r.config.Upstreams[:index], r.config.Upstreams[index+1:]...)
	r.groups = append(r.groups[:index], r.groups[index+1:]...)
	delete(r.groupsByName, name)
	for _, stream := range group.Streams {
		delete(r.streamsByName, stream.Name)
	}

	if err := r.configSave(); err != nil {
		r.config.Upstreams = oldUpstreams
		r.groups = oldGroups
		r.groupsByName = oldGroupsByName
		r.streamsByName = oldStreamsByName
		return err
	}

	for _, stream := range group.Streams {
		_ = stream.Stop()
	}
	return nil
}

func (r *StreamManager) StreamStopAll() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, group := range r.groups {
		for _, stream := range group.Streams {
			_ = stream.Stop()
		}
	}
}

func (r *StreamManager) streamGroupCreate(index int, upstream *Upstream) (*StreamGroup, error) {
	if err := StreamValidateUpstream(upstream); err != nil {
		return nil, err
	}
	groupName := StreamUpstreamName(index, upstream)
	group := &StreamGroup{
		Name:     groupName,
		Upstream: upstream,
		Streams:  make([]*Stream, 0),
	}

	renditions := upstream.Renditions
	if len(renditions) == 0 {
		stream, err := r.streamCreate(groupName, "", upstream)
		if err != nil {
			return nil, err
		}
		group.Streams = append(group.Streams, stream)
		group.Default = stream
		return group, nil
	}

	defaultIdx := StreamRenditionDefaultIndex(renditions)
	for idx, rendition := range renditions {
		streamUpstream := StreamUpstreamWithRendition(upstream, rendition)
		stream, err := r.streamCreate(groupName, rendition.Name, streamUpstream)
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

func (r *StreamManager) streamCreate(groupName, renditionName string, upstream *Upstream) (*Stream, error) {
	loggerName := groupName
	streamName := groupName
	if renditionName != "" {
		streamName = StreamVariantName(groupName, renditionName)
		loggerName = fmt.Sprintf("%s/%s", groupName, renditionName)
	}
	prefix := fmt.Sprintf("[%s]", loggerName)
	logger := NewLogger(prefix, log.Default())
	sourceType := StreamUpstreamSourceType(upstream)
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
	}

	inst := webrtp.Init(&webrtp.Config{
		SourceType:  sourceType,
		Rtsp:        upstream.RtspUrl,
		Device:      upstream.Device,
		Codec:       upstream.Codec,
		Width:       valueOrZero(upstream.Width),
		Height:      valueOrZero(upstream.Height),
		FrameRate:   frameRate,
		BitrateKbps: bitrateKbps,
		Logger:      logger,
	})
	stream := &Stream{
		Name:          streamName,
		GroupName:     groupName,
		RenditionName: renditionName,
		Url:           url,
		Inst:          inst,
		Hub:           inst.GetHub(),
		Stop:          inst.Stop,
		OnDemand:      upstream.OnDemand,
	}
	stream.Handler = func(c fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}
		return websocket.New(func(conn *websocket.Conn) {
			stream.Inst.HandleWebsocket(conn)
			stream.MaybeScheduleStop(5 * time.Second)
		})(c)
	}
	if stream.OnDemand {
		stream.started.Store(false)
	} else {
		stream.started.Store(true)
		go func() {
			if err := stream.Inst.Connect(); err != nil {
				log.Printf("stream %s: %v", stream.Name, err)
			}
			stream.started.Store(false)
		}()
	}
	return stream, nil
}

func (r *StreamManager) streamResponse(index int, group *StreamGroup) *StreamApiResponse {
	stats := group.Default.Hub.GetStats(group.Name)
	stats.Name = group.Name
	sourceType := StreamUpstreamSourceType(group.Upstream)
	resp := &StreamApiResponse{
		Index:           index,
		Name:            group.Name,
		SourceType:      sourceType,
		RtspUrl:         group.Upstream.RtspUrl,
		Device:          group.Upstream.Device,
		Codec:           group.Upstream.Codec,
		Width:           group.Upstream.Width,
		Height:          group.Upstream.Height,
		FrameRate:       group.Upstream.FrameRate,
		BitrateKbps:     group.Upstream.BitrateKbps,
		OnDemand:        group.Upstream.OnDemand,
		Url:             group.Default.Url,
		WsPath:          fmt.Sprintf("/stream/%s", group.Name),
		Stats:           &stats,
		ActiveRendition: group.Default.RenditionName,
	}
	if len(group.Streams) > 1 {
		resp.Renditions = make([]*RenditionApiResponse, 0, len(group.Streams))
		for _, stream := range group.Streams {
			bitrate := 0
			for _, rendition := range group.Upstream.Renditions {
				if rendition.Name == stream.RenditionName {
					bitrate = rendition.BitrateKbps
					break
				}
			}
			stats := stream.Hub.GetStats(stream.Name)
			stats.Name = stream.Name
			resp.Renditions = append(resp.Renditions, &RenditionApiResponse{
				Name:        stream.RenditionName,
				BitrateKbps: bitrate,
				OnDemand:    stream.OnDemand,
				WsPath:      fmt.Sprintf("/stream/%s", stream.Name),
				Stats:       &stats,
			})
		}
	}
	return resp
}

func (r *StreamManager) configSave() error {
	data, err := yaml.Marshal(r.config)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(r.configPath, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (r *StreamManager) streamsStop() {
	for _, group := range r.groups {
		for _, stream := range group.Streams {
			_ = stream.Stop()
		}
	}
}

func StreamUpstreamSourceType(upstream *Upstream) string {
	sourceType := "rtsp"
	if upstream.SourceType != nil && *upstream.SourceType != "" {
		sourceType = strings.ToLower(*upstream.SourceType)
	}
	return sourceType
}

func StreamUpstreamName(index int, upstream *Upstream) string {
	name := strconv.Itoa(index)
	if upstream.Name != nil && *upstream.Name != "" {
		name = *upstream.Name
	}
	return name
}

func StreamVariantName(groupName, renditionName string) string {
	return fmt.Sprintf("%s~%s", groupName, renditionName)
}

func StreamValidateUpstream(upstream *Upstream) error {
	switch StreamUpstreamSourceType(upstream) {
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
	default:
		return fmt.Errorf("unsupported sourceType: %s", StreamUpstreamSourceType(upstream))
	}
	if len(upstream.Renditions) > 0 {
		if StreamUpstreamSourceType(upstream) != "usb" {
			return fmt.Errorf("renditions are only supported for usb streams")
		}
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("usb renditions are only supported on macos")
		}
		for _, rendition := range upstream.Renditions {
			if rendition == nil {
				return fmt.Errorf("rendition entry is required")
			}
			if rendition.Name == "" {
				return fmt.Errorf("rendition missing required name")
			}
			if rendition.BitrateKbps <= 0 {
				return fmt.Errorf("rendition %s missing valid bitrateKbps", rendition.Name)
			}
		}
	}
	return nil
}

func StreamRequestUpstream(req *StreamApiRequest) (*Upstream, error) {
	upstream := &Upstream{
		Name:        req.Name,
		SourceType:  req.SourceType,
		RtspUrl:     req.RtspUrl,
		Device:      req.Device,
		Codec:       req.Codec,
		Width:       req.Width,
		Height:      req.Height,
		FrameRate:   req.FrameRate,
		BitrateKbps: req.BitrateKbps,
		OnDemand:    req.OnDemand,
		Renditions:  req.Renditions,
	}
	if err := StreamValidateUpstream(upstream); err != nil {
		return nil, err
	}
	return upstream, nil
}

func StreamRenditionDefaultIndex(renditions []*Rendition) int {
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

func StreamUpstreamWithRendition(upstream *Upstream, rendition *Rendition) *Upstream {
	name := StreamUpstreamName(0, upstream)
	sourceType := StreamUpstreamSourceType(upstream)
	bitrate := rendition.BitrateKbps
	onDemand := upstream.OnDemand
	if rendition.OnDemand != nil {
		onDemand = *rendition.OnDemand
	}
	return &Upstream{
		Name:        &name,
		SourceType:  &sourceType,
		RtspUrl:     upstream.RtspUrl,
		Device:      upstream.Device,
		Codec:       upstream.Codec,
		Width:       upstream.Width,
		Height:      upstream.Height,
		FrameRate:   upstream.FrameRate,
		BitrateKbps: &bitrate,
		OnDemand:    onDemand,
	}
}

func StreamResponsesSort(items []*StreamApiResponse) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Index < items[j].Index
	})
}

func valueOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
