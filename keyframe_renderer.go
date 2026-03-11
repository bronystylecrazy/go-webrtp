package webrtp

import (
	"fmt"
	"image"
	"math"
	"sync"
	"time"
)

type keyframeRenderStats struct {
	Undistort time.Duration
	Rectify   time.Duration
}

type keyframeRenderer interface {
	Render(img image.Image, fx, fy, scale float64, desk []point) (image.Image, keyframeRenderStats, error)
	Name() string
	Close()
}

type cpuKeyframeRenderer struct {
	mu             sync.Mutex
	undistortCache *undistortMapCache
	rectifyCache   *rectifyMapCache
}

type undistortMapCache struct {
	width  int
	height int
	fx     float64
	fy     float64
	scale  float64
	uv     []float32
}

type rectifyMapCache struct {
	srcWidth  int
	srcHeight int
	quad      [8]float64
	outWidth  int
	outHeight int
	uv        []float32
}

func (r *cpuKeyframeRenderer) Name() string {
	return "cpu"
}

func (r *cpuKeyframeRenderer) Close() {}

func (r *cpuKeyframeRenderer) Render(img image.Image, fx, fy, scale float64, desk []point) (image.Image, keyframeRenderStats, error) {
	var stats keyframeRenderStats
	srcRGBA := imageToRGBA(img)
	undistortStart := time.Now()
	undistorted := r.applyUndistortion(srcRGBA, fx, fy, scale)
	stats.Undistort = time.Since(undistortStart)
	output := image.Image(undistorted)
	if len(desk) == 4 {
		rectifiedDesk := remapDeskToUndistorted(desk, fx, fy, scale)
		rectifyStart := time.Now()
		rectified, err := r.rectifyDeskView(undistorted, rectifiedDesk)
		if err != nil {
			return nil, stats, err
		}
		stats.Rectify = time.Since(rectifyStart)
		output = rectified
	}
	return output, stats, nil
}

func newKeyframeRenderer(logger Logger) keyframeRenderer {
	_ = logger
	return &cpuKeyframeRenderer{}
}

func (r *cpuKeyframeRenderer) applyUndistortion(src *image.RGBA, fx, fy, scale float64) *image.RGBA {
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	if width <= 0 || height <= 0 {
		return dst
	}
	cache := r.getUndistortCache(width, height, fx, fy, scale)
	parallelizeRows(height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			dstRow := y * dst.Stride
			cacheRow := y * width * 2
			for x := 0; x < width; x++ {
				uvIndex := cacheRow + x*2
				c := sampleBilinearRGBA(src, float64(cache.uv[uvIndex]), float64(cache.uv[uvIndex+1]))
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

func (r *cpuKeyframeRenderer) rectifyDeskView(src *image.RGBA, normalizedQuad []point) (image.Image, error) {
	if len(normalizedQuad) != 4 {
		return nil, fmt.Errorf("rectifyDeskView needs 4 points")
	}
	b := src.Bounds()
	cache, err := r.getRectifyCache(b.Dx(), b.Dy(), normalizedQuad)
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, cache.outWidth, cache.outHeight))
	parallelizeRows(cache.outHeight, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			dstRow := y * dst.Stride
			cacheRow := y * cache.outWidth * 2
			for x := 0; x < cache.outWidth; x++ {
				uvIndex := cacheRow + x*2
				c := sampleBilinearRGBA(src, float64(cache.uv[uvIndex]), float64(cache.uv[uvIndex+1]))
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

func (r *cpuKeyframeRenderer) getUndistortCache(width, height int, fx, fy, scale float64) *undistortMapCache {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.undistortCache; c != nil &&
		c.width == width && c.height == height &&
		c.fx == fx && c.fy == fy && c.scale == scale {
		return c
	}
	cache := &undistortMapCache{
		width:  width,
		height: height,
		fx:     fx,
		fy:     fy,
		scale:  scale,
		uv:     make([]float32, width*height*2),
	}
	for y := 0; y < height; y++ {
		v := float64(y) / float64(maxInt(1, height-1))
		row := y * width * 2
		for x := 0; x < width; x++ {
			u := float64(x) / float64(maxInt(1, width-1))
			srcU, srcV := distortedSourceUV(u, v, fx, fy, scale)
			cache.uv[row+x*2] = float32(srcU)
			cache.uv[row+x*2+1] = float32(srcV)
		}
	}
	r.undistortCache = cache
	return cache
}

func (r *cpuKeyframeRenderer) getRectifyCache(srcWidth, srcHeight int, normalizedQuad []point) (*rectifyMapCache, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var quadKey [8]float64
	for i, p := range normalizedQuad {
		quadKey[i*2] = p.x
		quadKey[i*2+1] = p.y
	}
	if c := r.rectifyCache; c != nil &&
		c.srcWidth == srcWidth && c.srcHeight == srcHeight &&
		c.quad == quadKey {
		return c, nil
	}
	srcQuad := make([]point, 4)
	for i, p := range normalizedQuad {
		srcQuad[i] = point{x: p.x * float64(srcWidth), y: p.y * float64(srcHeight)}
	}
	top := math.Hypot(srcQuad[1].x-srcQuad[0].x, srcQuad[1].y-srcQuad[0].y)
	bottom := math.Hypot(srcQuad[2].x-srcQuad[3].x, srcQuad[2].y-srcQuad[3].y)
	left := math.Hypot(srcQuad[3].x-srcQuad[0].x, srcQuad[3].y-srcQuad[0].y)
	right := math.Hypot(srcQuad[2].x-srcQuad[1].x, srcQuad[2].y-srcQuad[1].y)
	outWidth := maxInt(80, int(math.Round((top+bottom)/2)))
	outHeight := maxInt(80, int(math.Round((left+right)/2)))
	dstQuad := []point{
		{0, 0},
		{float64(outWidth - 1), 0},
		{float64(outWidth - 1), float64(outHeight - 1)},
		{0, float64(outHeight - 1)},
	}
	h, err := computeHomography(dstQuad, srcQuad)
	if err != nil {
		return nil, err
	}
	cache := &rectifyMapCache{
		srcWidth:  srcWidth,
		srcHeight: srcHeight,
		quad:      quadKey,
		outWidth:  outWidth,
		outHeight: outHeight,
		uv:        make([]float32, outWidth*outHeight*2),
	}
	for y := 0; y < outHeight; y++ {
		row := y * outWidth * 2
		for x := 0; x < outWidth; x++ {
			sx, sy := projectPoint(h, float64(x), float64(y))
			cache.uv[row+x*2] = float32(clamp01(sx / float64(maxInt(1, srcWidth-1))))
			cache.uv[row+x*2+1] = float32(clamp01(sy / float64(maxInt(1, srcHeight-1))))
		}
	}
	r.rectifyCache = cache
	return cache, nil
}
