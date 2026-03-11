//go:build !cgo

package webrtp

import (
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"sync"
	"time"
	"unsafe"

	"github.com/go-webgpu/webgpu/wgpu"
	"github.com/gogpu/gputypes"
)

const webGPUKeyframeShaderWGSL = `
struct Params {
    fx: f32,
    fy: f32,
    scale: f32,
    mode: f32,
    out_size: vec2<f32>,
    _pad0: vec2<f32>,
    h0: vec4<f32>,
    h1: vec4<f32>,
    h2: vec4<f32>,
}

@group(0) @binding(0) var srcSampler: sampler;
@group(0) @binding(1) var srcTexture: texture_2d<f32>;
@group(0) @binding(2) var<uniform> params: Params;

struct VSOut {
    @builtin(position) position: vec4<f32>,
}

@vertex
fn vs_main(@builtin(vertex_index) vertex_index: u32) -> VSOut {
    var positions = array<vec2<f32>, 3>(
        vec2<f32>(-1.0, -3.0),
        vec2<f32>(-1.0,  1.0),
        vec2<f32>( 3.0,  1.0),
    );
    var out: VSOut;
    out.position = vec4<f32>(positions[vertex_index], 0.0, 1.0);
    return out;
}

fn clamp_uv(uv: vec2<f32>) -> vec2<f32> {
    return clamp(uv, vec2<f32>(0.0, 0.0), vec2<f32>(1.0, 1.0));
}

fn undistort_uv(dst_uv: vec2<f32>) -> vec2<f32> {
    let clip_x = dst_uv.x * 2.0 - 1.0;
    let clip_y = 1.0 - dst_uv.y * 2.0;
    let mapped_x = clip_x + (((clip_y * clip_y) / params.scale) * clip_x / params.scale) * -params.fx;
    let mapped_y = clip_y + (((mapped_x * mapped_x) / params.scale) * clip_y / params.scale) * -params.fy;
    return clamp_uv(vec2<f32>((mapped_x / params.scale + 1.0) * 0.5, (1.0 - mapped_y / params.scale) * 0.5));
}

fn homography_uv(dst_pixel: vec2<f32>) -> vec2<f32> {
    let sx = params.h0.x * dst_pixel.x + params.h0.y * dst_pixel.y + params.h0.z;
    let sy = params.h1.x * dst_pixel.x + params.h1.y * dst_pixel.y + params.h1.z;
    let sw = params.h2.x * dst_pixel.x + params.h2.y * dst_pixel.y + params.h2.z;
    let src_pixel = vec2<f32>(sx / sw, sy / sw);
    let src_size = vec2<f32>(textureDimensions(srcTexture, 0));
    let denom = max(src_size - vec2<f32>(1.0, 1.0), vec2<f32>(1.0, 1.0));
    return clamp_uv(src_pixel / denom);
}

@fragment
fn fs_main(@builtin(position) position: vec4<f32>) -> @location(0) vec4<f32> {
    let dst_pixel = position.xy - vec2<f32>(0.5, 0.5);
    let denom = max(params.out_size - vec2<f32>(1.0, 1.0), vec2<f32>(1.0, 1.0));
    let dst_uv = clamp_uv(dst_pixel / denom);
    let sample_uv = select(undistort_uv(dst_uv), homography_uv(dst_pixel), params.mode > 0.5);
    return textureSampleLevel(srcTexture, srcSampler, sample_uv, 0.0);
}
`

type webGPUKeyframeRenderer struct {
	logger          Logger
	mu              sync.Mutex
	instance        *wgpu.Instance
	adapter         *wgpu.Adapter
	device          *wgpu.Device
	queue           *wgpu.Queue
	sampler         *wgpu.Sampler
	pipeline        *wgpu.RenderPipeline
	bindGroupLayout *wgpu.BindGroupLayout
}

func newWebGPUKeyframeRenderer(logger Logger) (keyframeRenderer, error) {
	if err := wgpu.Init(); err != nil {
		return nil, fmt.Errorf("wgpu init: %w", err)
	}
	instance, err := wgpu.CreateInstance(nil)
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	adapter, err := instance.RequestAdapter(nil)
	if err != nil {
		instance.Release()
		return nil, fmt.Errorf("request adapter: %w", err)
	}
	device, err := adapter.RequestDevice(nil)
	if err != nil {
		adapter.Release()
		instance.Release()
		return nil, fmt.Errorf("request device: %w", err)
	}
	queue := device.GetQueue()
	if queue == nil {
		device.Release()
		adapter.Release()
		instance.Release()
		return nil, fmt.Errorf("get queue failed")
	}
	sampler := device.CreateLinearSampler()
	if sampler == nil {
		queue.Release()
		device.Release()
		adapter.Release()
		instance.Release()
		return nil, fmt.Errorf("create sampler failed")
	}
	shader := device.CreateShaderModuleWGSL(webGPUKeyframeShaderWGSL)
	if shader == nil {
		sampler.Release()
		queue.Release()
		device.Release()
		adapter.Release()
		instance.Release()
		return nil, fmt.Errorf("create shader module failed")
	}
	pipeline := device.CreateRenderPipeline(&wgpu.RenderPipelineDescriptor{
		Vertex: wgpu.VertexState{
			Module:     shader,
			EntryPoint: "vs_main",
		},
		Fragment: &wgpu.FragmentState{
			Module:     shader,
			EntryPoint: "fs_main",
			Targets: []wgpu.ColorTargetState{
				{Format: gputypes.TextureFormatRGBA8Unorm, WriteMask: gputypes.ColorWriteMaskAll},
			},
		},
		Primitive: wgpu.PrimitiveState{
			Topology:  gputypes.PrimitiveTopologyTriangleList,
			FrontFace: gputypes.FrontFaceCCW,
			CullMode:  gputypes.CullModeNone,
		},
		Multisample: wgpu.MultisampleState{
			Count: 1,
			Mask:  0xFFFFFFFF,
		},
	})
	shader.Release()
	if pipeline == nil {
		sampler.Release()
		queue.Release()
		device.Release()
		adapter.Release()
		instance.Release()
		return nil, fmt.Errorf("create render pipeline failed")
	}
	bindGroupLayout := pipeline.GetBindGroupLayout(0)
	if bindGroupLayout == nil {
		pipeline.Release()
		sampler.Release()
		queue.Release()
		device.Release()
		adapter.Release()
		instance.Release()
		return nil, fmt.Errorf("get bind group layout failed")
	}
	return &webGPUKeyframeRenderer{
		logger:          logger,
		instance:        instance,
		adapter:         adapter,
		device:          device,
		queue:           queue,
		sampler:         sampler,
		pipeline:        pipeline,
		bindGroupLayout: bindGroupLayout,
	}, nil
}

func (r *webGPUKeyframeRenderer) Name() string {
	return "webgpu"
}

func (r *webGPUKeyframeRenderer) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bindGroupLayout != nil {
		r.bindGroupLayout.Release()
		r.bindGroupLayout = nil
	}
	if r.pipeline != nil {
		r.pipeline.Release()
		r.pipeline = nil
	}
	if r.sampler != nil {
		r.sampler.Release()
		r.sampler = nil
	}
	if r.queue != nil {
		r.queue.Release()
		r.queue = nil
	}
	if r.device != nil {
		r.device.Release()
		r.device = nil
	}
	if r.adapter != nil {
		r.adapter.Release()
		r.adapter = nil
	}
	if r.instance != nil {
		r.instance.Release()
		r.instance = nil
	}
}

func (r *webGPUKeyframeRenderer) Render(img image.Image, fx, fy, scale float64, desk []point) (image.Image, keyframeRenderStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var stats keyframeRenderStats
	if r.device == nil || r.queue == nil || r.pipeline == nil || r.bindGroupLayout == nil || r.sampler == nil {
		return nil, stats, fmt.Errorf("webgpu renderer closed")
	}

	srcRGBA := imageToRGBA(img)
	srcBounds := srcRGBA.Bounds()
	srcWidth := srcBounds.Dx()
	srcHeight := srcBounds.Dy()
	if srcWidth <= 0 || srcHeight <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 0, 0)), stats, nil
	}

	srcTexture, srcView, err := r.createSourceTexture(srcRGBA)
	if err != nil {
		return nil, stats, err
	}
	defer srcView.Release()
	defer srcTexture.Release()

	tempTexture, tempView, err := r.createRenderTexture(srcWidth, srcHeight, gputypes.TextureUsageRenderAttachment|gputypes.TextureUsageTextureBinding|gputypes.TextureUsageCopySrc)
	if err != nil {
		return nil, stats, err
	}
	defer tempView.Release()
	defer tempTexture.Release()

	encoder := r.device.CreateCommandEncoder(nil)
	if encoder == nil {
		return nil, stats, fmt.Errorf("create command encoder failed")
	}
	defer encoder.Release()

	undistortUniform := r.createUniformBuffer(webGPUUniformBytes(float32(fx), float32(fy), float32(normalizedScale(scale)), 0, float32(srcWidth), float32(srcHeight), [9]float64{}))
	if undistortUniform == nil {
		return nil, stats, fmt.Errorf("create undistort uniform buffer failed")
	}
	defer undistortUniform.Release()

	undistortBindGroup := r.createBindGroup(srcView, undistortUniform)
	if undistortBindGroup == nil {
		return nil, stats, fmt.Errorf("create undistort bind group failed")
	}
	defer undistortBindGroup.Release()

	undistortStart := time.Now()
	if err := r.recordRenderPass(encoder, tempView, undistortBindGroup, srcWidth, srcHeight); err != nil {
		return nil, stats, err
	}
	stats.Undistort = time.Since(undistortStart)

	targetTexture := tempTexture
	targetWidth := srcWidth
	targetHeight := srcHeight

	var finalTexture *wgpu.Texture
	var finalView *wgpu.TextureView
	if len(desk) == 4 {
		targetWidth, targetHeight = deskOutputSize(srcWidth, srcHeight, desk)
		h, err := deskHomography(srcWidth, srcHeight, desk, targetWidth, targetHeight)
		if err != nil {
			return nil, stats, err
		}
		finalTexture, finalView, err = r.createRenderTexture(targetWidth, targetHeight, gputypes.TextureUsageRenderAttachment|gputypes.TextureUsageCopySrc)
		if err != nil {
			return nil, stats, err
		}
		defer finalView.Release()
		defer finalTexture.Release()
		deskUniform := r.createUniformBuffer(webGPUUniformBytes(0, 0, 1, 1, float32(targetWidth), float32(targetHeight), h))
		if deskUniform == nil {
			return nil, stats, fmt.Errorf("create desk uniform buffer failed")
		}
		defer deskUniform.Release()
		deskBindGroup := r.createBindGroup(tempView, deskUniform)
		if deskBindGroup == nil {
			return nil, stats, fmt.Errorf("create desk bind group failed")
		}
		defer deskBindGroup.Release()
		rectifyStart := time.Now()
		if err := r.recordRenderPass(encoder, finalView, deskBindGroup, targetWidth, targetHeight); err != nil {
			return nil, stats, err
		}
		stats.Rectify = time.Since(rectifyStart)
		targetTexture = finalTexture
	}

	outImg, err := r.copyTextureToImage(encoder, targetTexture, targetWidth, targetHeight)
	if err != nil {
		return nil, stats, err
	}
	return outImg, stats, nil
}

func (r *webGPUKeyframeRenderer) createSourceTexture(img *image.RGBA) (*wgpu.Texture, *wgpu.TextureView, error) {
	width := img.Bounds().Dx()
	height := img.Bounds().Dy()
	textureDesc := &wgpu.TextureDescriptor{
		Usage:     gputypes.TextureUsageTextureBinding | gputypes.TextureUsageCopyDst,
		Dimension: gputypes.TextureDimension2D,
		Size: gputypes.Extent3D{
			Width:              uint32(width),
			Height:             uint32(height),
			DepthOrArrayLayers: 1,
		},
		Format:        gputypes.TextureFormatRGBA8Unorm,
		MipLevelCount: 1,
		SampleCount:   1,
	}
	texture := r.device.CreateTexture(textureDesc)
	if texture == nil {
		return nil, nil, fmt.Errorf("create source texture failed")
	}
	view := texture.CreateView(nil)
	if view == nil {
		texture.Release()
		return nil, nil, fmt.Errorf("create source texture view failed")
	}
	data, bytesPerRow := rgbaToPaddedBytes(img)
	r.queue.WriteTexture(
		&wgpu.TexelCopyTextureInfo{
			Texture:  texture.Handle(),
			MipLevel: 0,
			Origin:   gputypes.Origin3D{},
			Aspect:   wgpu.TextureAspectAll,
		},
		data,
		&wgpu.TexelCopyBufferLayout{
			Offset:       0,
			BytesPerRow:  uint32(bytesPerRow),
			RowsPerImage: uint32(height),
		},
		&gputypes.Extent3D{
			Width:              uint32(width),
			Height:             uint32(height),
			DepthOrArrayLayers: 1,
		},
	)
	return texture, view, nil
}

func (r *webGPUKeyframeRenderer) createRenderTexture(width, height int, usage gputypes.TextureUsage) (*wgpu.Texture, *wgpu.TextureView, error) {
	texture := r.device.CreateTexture(&wgpu.TextureDescriptor{
		Usage:     usage,
		Dimension: gputypes.TextureDimension2D,
		Size: gputypes.Extent3D{
			Width:              uint32(width),
			Height:             uint32(height),
			DepthOrArrayLayers: 1,
		},
		Format:        gputypes.TextureFormatRGBA8Unorm,
		MipLevelCount: 1,
		SampleCount:   1,
	})
	if texture == nil {
		return nil, nil, fmt.Errorf("create render texture failed")
	}
	view := texture.CreateView(nil)
	if view == nil {
		texture.Release()
		return nil, nil, fmt.Errorf("create render texture view failed")
	}
	return texture, view, nil
}

func (r *webGPUKeyframeRenderer) createUniformBuffer(data []byte) *wgpu.Buffer {
	buffer := r.device.CreateBuffer(&wgpu.BufferDescriptor{
		Usage:            gputypes.BufferUsageUniform | gputypes.BufferUsageCopyDst,
		Size:             uint64(len(data)),
		MappedAtCreation: wgpu.False,
	})
	if buffer == nil {
		return nil
	}
	r.queue.WriteBuffer(buffer, 0, data)
	return buffer
}

func (r *webGPUKeyframeRenderer) createBindGroup(textureView *wgpu.TextureView, uniform *wgpu.Buffer) *wgpu.BindGroup {
	return r.device.CreateBindGroupSimple(r.bindGroupLayout, []wgpu.BindGroupEntry{
		wgpu.SamplerBindingEntry(0, r.sampler),
		wgpu.TextureBindingEntry(1, textureView),
		wgpu.BufferBindingEntry(2, uniform, 0, uniform.GetSize()),
	})
}

func (r *webGPUKeyframeRenderer) recordRenderPass(encoder *wgpu.CommandEncoder, targetView *wgpu.TextureView, bindGroup *wgpu.BindGroup, width, height int) error {
	renderPass := encoder.BeginRenderPass(&wgpu.RenderPassDescriptor{
		ColorAttachments: []wgpu.RenderPassColorAttachment{
			{
				View:       targetView,
				LoadOp:     gputypes.LoadOpClear,
				StoreOp:    gputypes.StoreOpStore,
				ClearValue: wgpu.Color{R: 0, G: 0, B: 0, A: 1},
			},
		},
	})
	if renderPass == nil {
		return fmt.Errorf("begin render pass failed")
	}
	defer renderPass.Release()
	renderPass.SetViewport(0, 0, float32(width), float32(height), 0, 1)
	renderPass.SetScissorRect(0, 0, uint32(width), uint32(height))
	renderPass.SetPipeline(r.pipeline)
	renderPass.SetBindGroup(0, bindGroup, nil)
	renderPass.Draw(3, 1, 0, 0)
	renderPass.End()
	return nil
}

func (r *webGPUKeyframeRenderer) copyTextureToImage(encoder *wgpu.CommandEncoder, texture *wgpu.Texture, width, height int) (image.Image, error) {
	bytesPerRow := alignTo(width*4, 256)
	bufferSize := bytesPerRow * height
	readback := r.device.CreateBuffer(&wgpu.BufferDescriptor{
		Usage:            gputypes.BufferUsageMapRead | gputypes.BufferUsageCopyDst,
		Size:             uint64(bufferSize),
		MappedAtCreation: wgpu.False,
	})
	if readback == nil {
		return nil, fmt.Errorf("create readback buffer failed")
	}
	defer readback.Release()

	encoder.CopyTextureToBuffer(
		&wgpu.TexelCopyTextureInfo{
			Texture:  texture.Handle(),
			MipLevel: 0,
			Origin:   gputypes.Origin3D{},
			Aspect:   wgpu.TextureAspectAll,
		},
		&wgpu.TexelCopyBufferInfo{
			Buffer: readback.Handle(),
			Layout: wgpu.TexelCopyBufferLayout{
				Offset:       0,
				BytesPerRow:  uint32(bytesPerRow),
				RowsPerImage: uint32(height),
			},
		},
		&gputypes.Extent3D{
			Width:              uint32(width),
			Height:             uint32(height),
			DepthOrArrayLayers: 1,
		},
	)

	cmdBuffer := encoder.Finish(nil)
	if cmdBuffer == nil {
		return nil, fmt.Errorf("finish command buffer failed")
	}
	r.queue.Submit(cmdBuffer)
	cmdBuffer.Release()

	if err := readback.MapAsync(r.device, wgpu.MapModeRead, 0, uint64(bufferSize)); err != nil {
		return nil, fmt.Errorf("map readback: %w", err)
	}
	defer readback.Unmap()

	ptr := readback.GetMappedRange(0, uint64(bufferSize))
	if ptr == nil {
		return nil, fmt.Errorf("mapped readback pointer is nil")
	}
	src := unsafe.Slice((*byte)(ptr), bufferSize)
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	rowBytes := width * 4
	for y := 0; y < height; y++ {
		copy(dst.Pix[y*dst.Stride:y*dst.Stride+rowBytes], src[y*bytesPerRow:y*bytesPerRow+rowBytes])
	}
	return dst, nil
}

func rgbaToPaddedBytes(img *image.RGBA) ([]byte, int) {
	width := img.Bounds().Dx()
	height := img.Bounds().Dy()
	rowBytes := width * 4
	alignedRowBytes := alignTo(rowBytes, 256)
	data := make([]byte, alignedRowBytes*height)
	for y := 0; y < height; y++ {
		srcStart := y * img.Stride
		copy(data[y*alignedRowBytes:y*alignedRowBytes+rowBytes], img.Pix[srcStart:srcStart+rowBytes])
	}
	return data, alignedRowBytes
}

func deskOutputSize(srcWidth, srcHeight int, normalizedQuad []point) (int, int) {
	srcQuad := make([]point, 4)
	for i, p := range normalizedQuad {
		srcQuad[i] = point{x: p.x * float64(srcWidth), y: p.y * float64(srcHeight)}
	}
	top := math.Hypot(srcQuad[1].x-srcQuad[0].x, srcQuad[1].y-srcQuad[0].y)
	bottom := math.Hypot(srcQuad[2].x-srcQuad[3].x, srcQuad[2].y-srcQuad[3].y)
	left := math.Hypot(srcQuad[3].x-srcQuad[0].x, srcQuad[3].y-srcQuad[0].y)
	right := math.Hypot(srcQuad[2].x-srcQuad[1].x, srcQuad[2].y-srcQuad[1].y)
	return maxInt(80, int(math.Round((top+bottom)/2))), maxInt(80, int(math.Round((left+right)/2)))
}

func deskHomography(srcWidth, srcHeight int, normalizedQuad []point, outWidth, outHeight int) ([9]float64, error) {
	srcQuad := make([]point, 4)
	for i, p := range normalizedQuad {
		srcQuad[i] = point{x: p.x * float64(srcWidth), y: p.y * float64(srcHeight)}
	}
	dstQuad := []point{
		{0, 0},
		{float64(outWidth - 1), 0},
		{float64(outWidth - 1), float64(outHeight - 1)},
		{0, float64(outHeight - 1)},
	}
	return computeHomography(dstQuad, srcQuad)
}

func webGPUUniformBytes(fx, fy, scale, mode, outWidth, outHeight float32, h [9]float64) []byte {
	data := make([]byte, 80)
	putFloat32(data, 0, fx)
	putFloat32(data, 4, fy)
	putFloat32(data, 8, scale)
	putFloat32(data, 12, mode)
	putFloat32(data, 16, outWidth)
	putFloat32(data, 20, outHeight)
	putFloat32(data, 32, float32(h[0]))
	putFloat32(data, 36, float32(h[1]))
	putFloat32(data, 40, float32(h[2]))
	putFloat32(data, 48, float32(h[3]))
	putFloat32(data, 52, float32(h[4]))
	putFloat32(data, 56, float32(h[5]))
	putFloat32(data, 64, float32(h[6]))
	putFloat32(data, 68, float32(h[7]))
	putFloat32(data, 72, float32(h[8]))
	return data
}

func putFloat32(dst []byte, offset int, value float32) {
	binary.LittleEndian.PutUint32(dst[offset:offset+4], math.Float32bits(value))
}

func alignTo(value, alignment int) int {
	if alignment <= 0 {
		return value
	}
	return ((value + alignment - 1) / alignment) * alignment
}
