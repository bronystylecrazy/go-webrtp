package webrtp

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"
)

func (s *keyframeSink) snapshotCalibration() (distort, deskEnabled bool, fx, fy, scale float64, desk []point) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	distort = s.distort
	deskEnabled = s.deskEnabled
	fx = s.fx
	fy = s.fy
	scale = s.scale
	desk = append([]point(nil), s.desk...)
	if !distort {
		fx = 0
		fy = 0
		scale = 1
	}
	if !deskEnabled {
		desk = nil
	}
	return distort, deskEnabled, fx, fy, scale, desk
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
	srcWidth := b.Dx()
	srcHeight := b.Dy()
	if srcWidth <= 0 || srcHeight <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}
	quad := make([]point, 4)
	for i, p := range normalizedQuad {
		quad[i] = point{
			x: clamp01(p.x) * float64(srcWidth-1),
			y: clamp01(p.y) * float64(srcHeight-1),
		}
	}
	top := math.Hypot(quad[1].x-quad[0].x, quad[1].y-quad[0].y)
	bottom := math.Hypot(quad[2].x-quad[3].x, quad[2].y-quad[3].y)
	left := math.Hypot(quad[3].x-quad[0].x, quad[3].y-quad[0].y)
	right := math.Hypot(quad[2].x-quad[1].x, quad[2].y-quad[1].y)
	outWidth := maxInt(80, int(math.Round((top+bottom)/2)))
	outHeight := maxInt(80, int(math.Round((left+right)/2)))
	dst := image.NewRGBA(image.Rect(0, 0, outWidth, outHeight))
	parallelizeRows(outHeight, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			dstRow := y * dst.Stride
			v := float64(y) / float64(maxInt(1, outHeight-1))
			for x := 0; x < outWidth; x++ {
				u := float64(x) / float64(maxInt(1, outWidth-1))
				srcPt := bilinearQuadPoint(quad, u, v)
				c := sampleBilinearRGBA(
					srcRGBA,
					clamp01(srcPt.x/float64(maxInt(1, srcWidth-1))),
					clamp01(srcPt.y/float64(maxInt(1, srcHeight-1))),
				)
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

func bilinearQuadPoint(q []point, u, v float64) point {
	a := q[0]
	b := q[1]
	c := q[2]
	d := q[3]

	au := 1 - u
	av := 1 - v

	return point{
		x: a.x*au*av + b.x*u*av + c.x*u*v + d.x*au*v,
		y: a.y*au*av + b.y*u*av + c.y*u*v + d.y*au*v,
	}
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
