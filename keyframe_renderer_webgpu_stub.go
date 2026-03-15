//go:build !cgo || !windows

package webrtp

import "fmt"

func newWebGPUKeyframeRenderer(logger Logger) (keyframeRenderer, error) {
	_ = logger
	return nil, fmt.Errorf("webgpu renderer requires CGO-enabled build; using cpu renderer instead")
}
