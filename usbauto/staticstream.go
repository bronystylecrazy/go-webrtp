package usbauto

type staticStreamEvent int

const (
	staticStreamNone staticStreamEvent = iota
	staticStreamBecameStatic
	staticStreamBecameActive
)

// * an encoder handed identical frames emits only skip sized packets, and no error path ever reports that
const staticStreamSkipThreshold = 1024
const staticStreamChunkSize = 120

type staticStreamDetector struct {
	skipThreshold int
	chunkSize     int
	skipCount     int
	sampleCount   int
	static        bool
}

func newStaticStreamDetector() *staticStreamDetector {
	return &staticStreamDetector{
		skipThreshold: staticStreamSkipThreshold,
		chunkSize:     staticStreamChunkSize,
	}
}

func (r *staticStreamDetector) Push(accessUnitSize int) staticStreamEvent {
	r.sampleCount++
	if accessUnitSize <= r.skipThreshold {
		r.skipCount++
	}
	if r.sampleCount < r.chunkSize {
		return staticStreamNone
	}
	// * keyframes stay large even for a frozen picture, so the chunk tolerates a small share of big units
	staticChunk := r.skipCount*20 >= r.chunkSize*19
	r.sampleCount = 0
	r.skipCount = 0
	if staticChunk && !r.static {
		r.static = true
		return staticStreamBecameStatic
	}
	if !staticChunk && r.static {
		r.static = false
		return staticStreamBecameActive
	}
	return staticStreamNone
}

func (r *staticStreamDetector) Static() bool {
	return r.static
}
