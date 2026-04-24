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
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPCM16GainWriter_Passthrough(t *testing.T) {
	var frames []PCM16Sample
	g := NewPCM16GainWriter(NewPCM16FrameWriter(&frames, 8000))

	in := PCM16Sample{100, -200, 30000, -30000, 0}
	require.NoError(t, g.WriteSample(in))

	require.Len(t, frames, 1)
	require.Equal(t, []int16{100, -200, 30000, -30000, 0}, []int16(frames[0]))
}

func TestPCM16GainWriter_Squelch(t *testing.T) {
	var frames []PCM16Sample
	g := NewPCM16GainWriter(NewPCM16FrameWriter(&frames, 8000))
	require.NoError(t, g.SetGain(0))

	in := PCM16Sample{100, -200, 30000}
	require.NoError(t, g.WriteSample(in))

	require.Len(t, frames, 1)
	require.Len(t, frames[0], len(in))
	for _, v := range frames[0] {
		require.Equal(t, int16(0), v)
	}
}

func TestPCM16GainWriter_HalfAttenuation(t *testing.T) {
	var frames []PCM16Sample
	g := NewPCM16GainWriter(NewPCM16FrameWriter(&frames, 8000))
	require.NoError(t, g.SetGain(0.5))

	in := PCM16Sample{1000, -2000, 10000, -10000}
	require.NoError(t, g.WriteSample(in))

	require.Len(t, frames, 1)
	require.Equal(t, PCM16Sample{500, -1000, 5000, -5000}, frames[0])
}

func TestPCM16GainWriter_Amplification(t *testing.T) {
	var frames []PCM16Sample
	g := NewPCM16GainWriter(NewPCM16FrameWriter(&frames, 8000))
	require.NoError(t, g.SetGain(2.0))

	in := PCM16Sample{100, -200, 5000, -5000}
	require.NoError(t, g.WriteSample(in))

	require.Len(t, frames, 1)
	require.Equal(t, PCM16Sample{200, -400, 10000, -10000}, frames[0])
}

func TestPCM16GainWriter_Overflow(t *testing.T) {
	var frames []PCM16Sample
	g := NewPCM16GainWriter(NewPCM16FrameWriter(&frames, 8000))
	require.NoError(t, g.SetGain(4.0))

	// 10000 × 4 = 40000 > MaxInt16 (32767); -10000 × 4 = -40000 < MinInt16.
	// The in-range values (±1000 × 4 = ±4000) don't saturate.
	in := PCM16Sample{10000, -10000, 1000, -1000}
	require.NoError(t, g.WriteSample(in))

	require.Len(t, frames, 1)
	require.Equal(t, PCM16Sample{math.MaxInt16, math.MinInt16, 4000, -4000}, frames[0])
}

func TestPCM16GainWriter_InvalidGain(t *testing.T) {
	cases := map[string]float32{
		"negative": -0.5,
		"nan":      float32(math.NaN()),
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			var frames []PCM16Sample
			g := NewPCM16GainWriter(NewPCM16FrameWriter(&frames, 8000))
			require.Error(t, g.SetGain(v))

			in := PCM16Sample{10000}
			require.NoError(t, g.WriteSample(in))
			require.Equal(t, PCM16Sample{10000}, frames[0])
		})
	}
}
