package webrtp

import "time"

func (r *Recorder) OnOffline() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.active {
		return
	}
	switch r.offlineMode {
	case "pause":
		return
	case "black":
		r.offlineGap = true
		if err := r.startOfflineGeneratorLocked(); err != nil {
			r.logger.Printf("recording black offline unavailable, falling back to pause: %v", err)
			return
		}
		return
	case "stop":
		if r.file != nil {
			_ = r.file.Close()
			r.file = nil
		}
		if r.requestedStopAt.IsZero() {
			r.requestedStopAt = time.Now()
		}
		r.active = false
		r.stopOfflineGeneratorLocked()
		r.logger.Printf("recording stopped due to offline source: %s", r.path)
	}
}

func (r *Recorder) canRecordLiveLocked() bool {
	return r.active && r.file != nil && len(r.initData) > 0
}

func (r *Recorder) transitionToLiveLocked(isIDR bool) bool {
	if !r.offlineGap {
		return true
	}
	if !isIDR {
		return false
	}
	r.offlineGap = false
	r.stopOfflineGeneratorLocked()
	return true
}

func (r *Recorder) acceptLiveSampleLocked(isIDR bool) bool {
	if !r.waitForIDR {
		return true
	}
	if !isIDR {
		return false
	}
	r.waitForIDR = false
	return true
}

func (r *Recorder) startOfflineGeneratorLocked() error {
	if r.offlineRun || !r.active || r.file == nil || len(r.initData) == 0 {
		return nil
	}
	gen, err := newOfflineFrameGenerator(r.codec, r.width, r.height)
	if err != nil {
		return err
	}
	r.offlineGen = gen
	r.offlineRun = true
	go r.runOfflineGenerator()
	r.logger.Printf("recording switched to black offline filler: %s", r.path)
	return nil
}

func (r *Recorder) stopOfflineGeneratorLocked() {
	if r.offlineGen != nil {
		_ = r.offlineGen.Close()
		r.offlineGen = nil
	}
	r.offlineRun = false
}

func (r *Recorder) offlineFrameIntervalLocked() time.Duration {
	if r.lastDur > 0 {
		return time.Duration(r.lastDur) * time.Second / 90000
	}
	if r.frameRate > 0 {
		return time.Duration(float64(time.Second) / r.frameRate)
	}
	return 100 * time.Millisecond
}

func (r *Recorder) runOfflineGenerator() {
	for {
		r.mu.Lock()
		if !r.active || !r.offlineGap || r.offlineGen == nil || r.file == nil {
			r.stopOfflineGeneratorLocked()
			r.mu.Unlock()
			return
		}
		interval := r.offlineFrameIntervalLocked()
		r.mu.Unlock()

		time.Sleep(interval)

		r.mu.Lock()
		if !r.active || !r.offlineGap || r.offlineGen == nil || r.file == nil {
			r.stopOfflineGeneratorLocked()
			r.mu.Unlock()
			return
		}
		avcc, isIDR, err := r.offlineGen.NextFrame()
		if err != nil {
			r.logger.Printf("recording black offline filler failed: %v", err)
			r.stopOfflineGeneratorLocked()
			r.mu.Unlock()
			return
		}
		dur := r.lastDur
		if dur == 0 {
			dur = uint32(interval.Seconds() * 90000)
			if dur == 0 {
				dur = 9000
			}
		}
		r.writeFragmentLocked(avcc, dur, isIDR, "recording black fragment")
		r.mu.Unlock()
	}
}
