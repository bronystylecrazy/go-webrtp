//go:build (!windows && !darwin && !linux) || !cgo

package webrtp

import "fmt"

func validateOfflineBlackSupport(codec string, width, height int) error {
	if codec != "h264" {
		return fmt.Errorf("offlineMode=black is currently only supported for h264 recordings")
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("offlineMode=black requires initialized stream dimensions")
	}
	return fmt.Errorf("offlineMode=black requires the native h264 encoder backend, currently available on windows/darwin/linux cgo builds")
}

func newOfflineFrameGenerator(codec string, width, height int) (offlineFrameGenerator, error) {
	return nil, validateOfflineBlackSupport(codec, width, height)
}
