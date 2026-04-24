// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package media

import (
	"fmt"
	"math"
	"sync/atomic"
)

const (
	gainFractionBits = 16
	unityGain        = 1 << gainFractionBits
)

// PCM16GainWriter scales every PCM16 sample by a configurable gain before
// forwarding it to the underlying writer.
//
// It is intended for runtime volume/ducking control on a PCM16 audio path:
// callers update the gain concurrently with WriteSample via SetGain and the
// next WriteSample picks up the new value atomically.
type PCM16GainWriter struct {
	w    PCM16Writer
	gain atomic.Uint32
}

func NewPCM16GainWriter(w PCM16Writer) *PCM16GainWriter {
	g := &PCM16GainWriter{w: w}
	g.gain.Store(unityGain)
	return g
}

func (g *PCM16GainWriter) SetGain(v float32) error {
	if v < 0 || math.IsNaN(float64(v)) {
		return fmt.Errorf("invalid gain %v: must be >= 0 and not NaN", v)
	}
	g.gain.Store(uint32(v * unityGain))
	return nil
}

func (g *PCM16GainWriter) WriteSample(sample PCM16Sample) error {
	gainFP := g.gain.Load()
	switch gainFP {
	case unityGain:
		return g.w.WriteSample(sample)
	case 0:
		// Preserve frame cadence for buffered downstream consumers.
		return g.w.WriteSample(make(PCM16Sample, len(sample)))
	}
	scaled := make(PCM16Sample, len(sample))
	for i, v := range sample {
		s := int64(v) * int64(gainFP) >> gainFractionBits
		if s > math.MaxInt16 {
			s = math.MaxInt16
		} else if s < math.MinInt16 {
			s = math.MinInt16
		}
		scaled[i] = int16(s)
	}
	return g.w.WriteSample(scaled)
}

func (g *PCM16GainWriter) SampleRate() int {
	return g.w.SampleRate()
}

func (g *PCM16GainWriter) Close() error {
	return g.w.Close()
}

func (g *PCM16GainWriter) String() string {
	return fmt.Sprintf("PCM16GainWriter(%.3f) -> %s", float32(g.gain.Load())/unityGain, g.w)
}
