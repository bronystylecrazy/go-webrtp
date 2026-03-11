//go:build cgo

package webrtp

import "fmt"

func newWebGPUKeyframeRenderer(logger Logger) (keyframeRenderer, error) {
	return nil, fmt.Errorf("webgpu renderer requires a CGO-disabled build; using cpu renderer instead")
}
