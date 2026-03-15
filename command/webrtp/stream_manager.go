package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	Name              *string      `json:"name"`
	SourceType        *string      `json:"sourceType"`
	RtspUrl           string       `json:"rtspUrl"`
	Device            string       `json:"device"`
	Path              string       `json:"path"`
	Codec             string       `json:"codec"`
	Width             *int         `json:"width"`
	Height            *int         `json:"height"`
	FrameRate         *float64     `json:"frameRate"`
	BitrateKbps       *int         `json:"bitrateKbps"`
	ServeStream       *bool        `json:"serveStream,omitempty"`
	CalibrationFrom   string       `json:"calibrationFrom,omitempty"`
	KeyframeSink      string       `json:"keyframeSink,omitempty"`
	KeyframeOutput    string       `json:"keyframeOutput,omitempty"`
	KeyframeFormat    string       `json:"keyframeFormat,omitempty"`
	KeyframeMqttURL   string       `json:"keyframeMqttUrl,omitempty"`
	KeyframeMqttTopic string       `json:"keyframeMqttTopic,omitempty"`
	Enabled           *bool        `json:"enabled"`
	OnDemand          bool         `json:"onDemand"`
	Renditions        []*Rendition `json:"renditions"`
}

type RenditionApiResponse struct {
	Name        string              `json:"name"`
	Width       *int                `json:"width,omitempty"`
	Height      *int                `json:"height,omitempty"`
	FrameRate   *float64            `json:"frameRate,omitempty"`
	BitrateKbps *int                `json:"bitrateKbps,omitempty"`
	OnDemand    bool                `json:"onDemand"`
	WsPath      string              `json:"wsPath"`
	Stats       *webrtp.StreamStats `json:"stats,omitempty"`
}

type StreamApiResponse struct {
	Index             int                     `json:"index"`
	Name              string                  `json:"name"`
	SourceType        string                  `json:"sourceType"`
	RtspUrl           string                  `json:"rtspUrl,omitempty"`
	Device            string                  `json:"device,omitempty"`
	Path              string                  `json:"path,omitempty"`
	Codec             string                  `json:"codec,omitempty"`
	Width             *int                    `json:"width,omitempty"`
	Height            *int                    `json:"height,omitempty"`
	FrameRate         *float64                `json:"frameRate,omitempty"`
	BitrateKbps       *int                    `json:"bitrateKbps,omitempty"`
	ServeStream       bool                    `json:"serveStream"`
	CalibrationFrom   string                  `json:"calibrationFrom,omitempty"`
	KeyframeSink      string                  `json:"keyframeSink,omitempty"`
	KeyframeOutput    string                  `json:"keyframeOutput,omitempty"`
	KeyframeFormat    string                  `json:"keyframeFormat,omitempty"`
	KeyframeMqttURL   string                  `json:"keyframeMqttUrl,omitempty"`
	KeyframeMqttTopic string                  `json:"keyframeMqttTopic,omitempty"`
	Enabled           bool                    `json:"enabled"`
	OnDemand          bool                    `json:"onDemand,omitempty"`
	Url               string                  `json:"url"`
	WsPath            string                  `json:"wsPath"`
	Stats             *webrtp.StreamStats     `json:"stats"`
	Renditions        []*RenditionApiResponse `json:"renditions,omitempty"`
	ActiveRendition   string                  `json:"activeRendition,omitempty"`
}

type StreamCapabilitiesResponse struct {
	Name         string                        `json:"name"`
	SourceType   string                        `json:"sourceType"`
	Device       string                        `json:"device,omitempty"`
	Capabilities *webrtp.UsbDeviceCapabilities `json:"capabilities,omitempty"`
}

type RecordingFileResponse struct {
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	SizeBytes    int64     `json:"sizeBytes"`
	LastModified time.Time `json:"lastModified"`
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
		if upstream.Enabled == nil {
			upstream.Enabled = boolPtr(true)
		}
		if upstream.ServeStream == nil {
			upstream.ServeStream = boolPtr(true)
		}
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
		if !StreamUpstreamEnabled(group.Upstream) || !StreamUpstreamServeStream(group.Upstream) || group.Default == nil {
			continue
		}
		streams = append(streams, group.Default)
	}
	return streams
}

func (r *StreamManager) StreamListExpanded() []*Stream {
	r.mu.RLock()
	defer r.mu.RUnlock()
	streams := make([]*Stream, 0, len(r.streamsByName))
	for _, group := range r.groups {
		if !StreamUpstreamEnabled(group.Upstream) || !StreamUpstreamServeStream(group.Upstream) || len(group.Streams) == 0 {
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
		if !StreamUpstreamEnabled(group.Upstream) || !StreamUpstreamServeStream(group.Upstream) || len(group.Streams) == 0 {
			continue
		}
		if len(group.Streams) == 1 {
			streams = append(streams, group.Streams[0])
			continue
		}
		active := make([]*Stream, 0, len(group.Streams))
		for _, stream := range group.Streams {
			if stream.activeClientCount() > 0 {
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
	return r.streamByNameLocked(name, true)
}

func (r *StreamManager) StreamByNameAny(name string) (*Stream, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.streamByNameLocked(name, false)
}

func (r *StreamManager) streamByNameLocked(name string, requireServed bool) (*Stream, bool) {
	if stream, ok := r.streamsByName[name]; ok {
		if requireServed && !StreamUpstreamServeStream(r.groupsByName[stream.GroupName].Upstream) {
			return nil, false
		}
		return stream, true
	}
	group, ok := r.groupsByName[name]
	if !ok {
		return nil, false
	}
	if !StreamUpstreamEnabled(group.Upstream) || group.Default == nil {
		return nil, false
	}
	if requireServed && !StreamUpstreamServeStream(group.Upstream) {
		return nil, false
	}
	return group.Default, true
}

func (r *StreamManager) StreamByNameQuality(name, quality string) (*Stream, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.streamByNameQualityLocked(name, quality, true)
}

func (r *StreamManager) StreamByNameQualityAny(name, quality string) (*Stream, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.streamByNameQualityLocked(name, quality, false)
}

func (r *StreamManager) streamByNameQualityLocked(name, quality string, requireServed bool) (*Stream, bool) {
	group, ok := r.groupsByName[name]
	if !ok {
		if stream, ok := r.streamsByName[name]; ok {
			if requireServed {
				group, groupOK := r.groupsByName[stream.GroupName]
				if !groupOK || !StreamUpstreamServeStream(group.Upstream) {
					return nil, false
				}
			}
			return stream, true
		}
		return nil, false
	}
	if !StreamUpstreamEnabled(group.Upstream) || group.Default == nil {
		return nil, false
	}
	if requireServed && !StreamUpstreamServeStream(group.Upstream) {
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
	if !StreamUpstreamEnabled(r.groups[index].Upstream) || !StreamUpstreamServeStream(r.groups[index].Upstream) || r.groups[index].Default == nil {
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

func (r *StreamManager) CalibrationTargets(name, quality string) []*Stream {
	r.mu.RLock()
	defer r.mu.RUnlock()

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

	if stream, ok := r.streamByNameQualityLocked(name, quality, false); ok {
		appendStream(stream)
	}
	for _, group := range r.groups {
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
	if req.Enabled == nil {
		upstream.Enabled = oldUpstream.Enabled
	}

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
		Name:              oldUpstream.Name,
		SourceType:        oldUpstream.SourceType,
		RtspUrl:           oldUpstream.RtspUrl,
		Device:            oldUpstream.Device,
		Path:              oldUpstream.Path,
		Codec:             oldUpstream.Codec,
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
		Enabled:           oldUpstream.Enabled,
		OnDemand:          oldUpstream.OnDemand,
		Renditions:        oldUpstream.Renditions,
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

func (r *StreamManager) StreamSetEnabled(name string, enabled bool) (*StreamApiResponse, error) {
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

	oldUpstream := r.config.Upstreams[index]
	oldGroup := r.groups[index]
	upstream := *oldUpstream
	upstream.Enabled = boolPtr(enabled)

	group, err := r.streamGroupCreate(index, &upstream)
	if err != nil {
		return nil, err
	}

	r.config.Upstreams[index] = &upstream
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

func (r *StreamManager) StreamStart(name, quality string) (*StreamApiResponse, error) {
	stream, ok := r.StreamByNameQualityAny(name, quality)
	if !ok {
		return nil, fmt.Errorf("stream not found: %s", name)
	}
	stream.EnsureStarted()
	groupName := stream.GroupName
	if groupName == "" {
		groupName = stream.Name
	}
	item, ok := r.StreamStatus(groupName)
	if !ok {
		return nil, fmt.Errorf("stream not found: %s", name)
	}
	return item, nil
}

func (r *StreamManager) StreamStop(name, quality string) (*StreamApiResponse, error) {
	stream, ok := r.StreamByNameQualityAny(name, quality)
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
	item, ok := r.StreamStatus(groupName)
	if !ok {
		return nil, fmt.Errorf("stream not found: %s", name)
	}
	return item, nil
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
	if !StreamUpstreamEnabled(upstream) {
		return group, nil
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
	} else if sourceType == "file" {
		url = upstream.Path
	}

	inst := webrtp.Init(&webrtp.Config{
		SourceType:        sourceType,
		StreamName:        streamName,
		Rtsp:              upstream.RtspUrl,
		Device:            upstream.Device,
		Path:              upstream.Path,
		Codec:             upstream.Codec,
		Width:             valueOrZero(upstream.Width),
		Height:            valueOrZero(upstream.Height),
		FrameRate:         frameRate,
		BitrateKbps:       bitrateKbps,
		KeyframeSink:      upstream.KeyframeSink,
		KeyframeOutput:    upstream.KeyframeOutput,
		KeyframeFormat:    upstream.KeyframeFormat,
		KeyframeMqttURL:   upstream.KeyframeMqttURL,
		KeyframeMqttTopic: upstream.KeyframeMqttTopic,
		Logger:            logger,
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
	stats := webrtp.StreamStats{}
	if group.Default != nil {
		stats = group.Default.Hub.GetStats(group.Name)
		stats.Name = group.Name
	}
	sourceType := StreamUpstreamSourceType(group.Upstream)
	resp := &StreamApiResponse{
		Index:             index,
		Name:              group.Name,
		SourceType:        sourceType,
		RtspUrl:           group.Upstream.RtspUrl,
		Device:            group.Upstream.Device,
		Path:              group.Upstream.Path,
		Codec:             group.Upstream.Codec,
		Width:             group.Upstream.Width,
		Height:            group.Upstream.Height,
		FrameRate:         group.Upstream.FrameRate,
		BitrateKbps:       group.Upstream.BitrateKbps,
		ServeStream:       StreamUpstreamServeStream(group.Upstream),
		CalibrationFrom:   group.Upstream.CalibrationFrom,
		KeyframeSink:      group.Upstream.KeyframeSink,
		KeyframeOutput:    group.Upstream.KeyframeOutput,
		KeyframeFormat:    group.Upstream.KeyframeFormat,
		KeyframeMqttURL:   group.Upstream.KeyframeMqttURL,
		KeyframeMqttTopic: group.Upstream.KeyframeMqttTopic,
		Enabled:           StreamUpstreamEnabled(group.Upstream),
		OnDemand:          group.Upstream.OnDemand,
		Url:               "",
		WsPath:            "",
		Stats:             &stats,
		ActiveRendition:   "",
	}
	if group.Default != nil {
		resp.Url = group.Default.Url
		if StreamUpstreamServeStream(group.Upstream) {
			resp.WsPath = fmt.Sprintf("/stream/%s", group.Name)
		}
		resp.ActiveRendition = group.Default.RenditionName
	}
	if len(group.Streams) > 1 {
		resp.Renditions = make([]*RenditionApiResponse, 0, len(group.Streams))
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
			resp.Renditions = append(resp.Renditions, &RenditionApiResponse{
				Name:        stream.RenditionName,
				Width:       width,
				Height:      height,
				FrameRate:   frameRate,
				BitrateKbps: bitrate,
				OnDemand:    stream.OnDemand,
				WsPath:      streamWsPath(stream, group.Upstream),
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
	case "file":
		if upstream.Path == "" {
			return fmt.Errorf("file upstream missing required path")
		}
		if upstream.Codec != "" && !strings.EqualFold(upstream.Codec, "h264") {
			return fmt.Errorf("file upstream codec must be h264 when provided")
		}
	default:
		return fmt.Errorf("unsupported sourceType: %s", StreamUpstreamSourceType(upstream))
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
	if sinks["mqtt"] {
		if strings.TrimSpace(upstream.KeyframeMqttURL) == "" {
			return fmt.Errorf("keyframeMqttUrl is required when keyframeSink includes mqtt")
		}
	}
	if upstream.CalibrationFrom != "" {
		if upstream.Name != nil && *upstream.Name == upstream.CalibrationFrom {
			return fmt.Errorf("calibrationFrom cannot reference the same stream")
		}
	}
	if len(upstream.Renditions) > 0 {
		if StreamUpstreamSourceType(upstream) != "usb" {
			return fmt.Errorf("renditions are only supported for usb streams")
		}
		if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
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
			if rendition.Width == nil && rendition.Height == nil && rendition.FrameRate == nil && rendition.BitrateKbps == nil {
				return fmt.Errorf("rendition %s must override at least one of width, height, frameRate, or bitrateKbps", rendition.Name)
			}
		}
	}
	return nil
}

func StreamRequestUpstream(req *StreamApiRequest) (*Upstream, error) {
	upstream := &Upstream{
		Name:              req.Name,
		SourceType:        req.SourceType,
		RtspUrl:           req.RtspUrl,
		Device:            req.Device,
		Path:              req.Path,
		Codec:             req.Codec,
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
		Enabled:           upstream.Enabled,
		OnDemand:          onDemand,
	}
}

func StreamUpstreamEnabled(upstream *Upstream) bool {
	return upstream == nil || upstream.Enabled == nil || *upstream.Enabled
}

func StreamUpstreamServeStream(upstream *Upstream) bool {
	return upstream == nil || upstream.ServeStream == nil || *upstream.ServeStream
}

func boolPtr(v bool) *bool {
	return &v
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

func streamWsPath(stream *Stream, upstream *Upstream) string {
	if stream == nil || !StreamUpstreamServeStream(upstream) {
		return ""
	}
	return fmt.Sprintf("/stream/%s", stream.Name)
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
			return nil, fmt.Errorf("keyframeSink must contain fs and/or mqtt")
		}
	}
	return targets, nil
}

func RecordingsList(root string) ([]*RecordingFileResponse, error) {
	entries := make([]*RecordingFileResponse, 0)
	if strings.TrimSpace(root) == "" {
		root = "recordings"
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return entries, nil
		}
		return nil, fmt.Errorf("stat recordings root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("recordings root is not a directory: %s", root)
	}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if info == nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, &RecordingFileResponse{
			Name:         filepath.Base(path),
			Path:         filepath.ToSlash(rel),
			SizeBytes:    info.Size(),
			LastModified: info.ModTime(),
		})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk recordings root: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].LastModified.Equal(entries[j].LastModified) {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].LastModified.After(entries[j].LastModified)
	})
	return entries, nil
}
