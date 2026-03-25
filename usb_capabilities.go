package webrtp

type UsbCapabilityMode struct {
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	Fps          []float64 `json:"fps,omitempty"`
	PixelFormats []string  `json:"pixelFormats,omitempty"`
}

type UsbCapabilityRendition struct {
	Name      string   `json:"name"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	FrameRate *float64 `json:"frameRate,omitempty"`
}

type UsbDeviceCapabilities struct {
	Device              *UsbDevice                `json:"device,omitempty"`
	Codecs              []string                  `json:"codecs,omitempty"`
	Modes               []*UsbCapabilityMode      `json:"modes,omitempty"`
	SuggestedRenditions []*UsbCapabilityRendition `json:"suggestedRenditions,omitempty"`
	BitrateControl      string                    `json:"bitrateControl,omitempty"`
}

func populateSuggestedUsbRenditions(caps *UsbDeviceCapabilities) {
	if caps == nil || len(caps.Modes) == 0 {
		return
	}
	type modeInfo struct {
		mode *UsbCapabilityMode
		area int
	}
	infos := make([]modeInfo, 0, len(caps.Modes))
	for _, mode := range caps.Modes {
		if mode == nil || mode.Width <= 0 || mode.Height <= 0 {
			continue
		}
		infos = append(infos, modeInfo{mode: mode, area: mode.Width * mode.Height})
	}
	if len(infos) == 0 {
		return
	}
	// simple stable ascending sort by pixel area
	for i := 0; i < len(infos)-1; i++ {
		for j := i + 1; j < len(infos); j++ {
			if infos[j].area < infos[i].area {
				infos[i], infos[j] = infos[j], infos[i]
			}
		}
	}
	indexes := []int{0}
	if len(infos) > 2 {
		indexes = append(indexes, len(infos)/2)
	}
	if len(infos) > 1 {
		last := len(infos) - 1
		seen := false
		for _, idx := range indexes {
			if idx == last {
				seen = true
				break
			}
		}
		if !seen {
			indexes = append(indexes, last)
		}
	}
	names := []string{"low", "mid", "high"}
	suggestions := make([]*UsbCapabilityRendition, 0, len(indexes))
	for i, idx := range indexes {
		if idx < 0 || idx >= len(infos) {
			continue
		}
		mode := infos[idx].mode
		suggestion := &UsbCapabilityRendition{
			Name:   names[minInt(i, len(names)-1)],
			Width:  mode.Width,
			Height: mode.Height,
		}
		if len(mode.Fps) > 0 {
			fps := mode.Fps[len(mode.Fps)-1]
			suggestion.FrameRate = &fps
		}
		suggestions = append(suggestions, suggestion)
	}
	caps.SuggestedRenditions = suggestions
}
