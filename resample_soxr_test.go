//go:build cgo

// Copyright 2024 LiveKit, Inc.
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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The writer only passes whole frames, so this calls soxr directly with less than one.
func TestSoxrReturnsOnPartialFrame(t *testing.T) {
	const hangGuard = 10 * time.Second
	var out PCM16Sample
	w := newResampleWriter(NewPCM16BufferWriter(&out, 16000), 48000, &resampleOptions{Channels: 2}).(*resampleWriter)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = w.r.Resample(nil, PCM16Sample{0})
	}()
	select {
	case <-done:
	case <-time.After(hangGuard):
		t.Fatal("soxr spun on a partial frame")
	}
}

// Explicit options layer over the defaults, so a channels option keeps sip's predictable default.
func TestResampleOptionsKeepDefaults(t *testing.T) {
	old := DefaultResampleOptions
	t.Cleanup(func() { DefaultResampleOptions = old })
	DefaultResampleOptions = []ResampleOption{WithPredictableResample(true)}
	w := ResampleWriter(NewPCM16BufferWriter(new(PCM16Sample), 16000), 48000, WithResampleChannels(2))
	require.IsType(t, (*resampleWriterBeep)(nil), w, "the channels option dropped the defaults")
}

// soxr runs under every call's audio, so its cgo out-parameters must not move to the heap on each call.
// This skips the writer because CI swaps its mutex for go-deadlock's, which allocates on Lock.
func TestSoxrDoesNotAllocate(t *testing.T) {
	w := newResampleWriter(&chunkCounter{}, 8000, &resampleOptions{Channels: 1}).(*resampleWriter)
	frame := make(PCM16Sample, 160)
	var out PCM16Sample
	require.Zero(t, testing.AllocsPerRun(100, func() { out, _ = w.r.Resample(out[:0], frame) }))
}

func qualityTone(rate, n int, freq float64) PCM16Sample {
	src := make(PCM16Sample, n)
	for i := range src {
		src[i] = int16(math.Round(10000 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate))))
	}
	return src
}

// Correlate against sine and cosine to measure the fundamental independent of phase.
// The central second contains an integer number of periods and excludes both filter edges.
func qualityAmplitude(out PCM16Sample) float64 {
	var s, c float64
	for i, v := range out[12000:36000] {
		phase := 2 * math.Pi * 3400 * float64(i) / 24000
		s += float64(v) * math.Sin(phase)
		c += float64(v) * math.Cos(phase)
	}
	return 2 * math.Hypot(s, c) / 24000
}

func TestResampleQualityNativeRecipes(t *testing.T) {
	// SoX's documented recipe identifiers; HIGH and VERY_HIGH are not consecutive.
	for _, tc := range []struct {
		q      ResampleQuality
		recipe int
	}{
		{ResampleQualityQuick, 0}, {ResampleQualityLow, 1}, {ResampleQualityMedium, 2},
		{ResampleQualityHigh, 4}, {ResampleQualityVeryHigh, 6},
	} {
		t.Run(fmt.Sprint(tc.q), func(t *testing.T) {
			require.Equal(t, tc.recipe, soxrQuality(tc.q))
			src := qualityTone(8000, 8000, 3400)
			r, err := newSoxr(24000, 8000, 1, tc.recipe)
			require.NoError(t, err)
			defer r.Close()
			want, err := r.Resample(nil, src)
			require.NoError(t, err)
			want, err = r.Resample(want, nil)
			require.NoError(t, err)
			got := Resample(nil, 24000, src, 8000, WithResampleQuality(tc.q))
			require.Len(t, want, 24000)
			require.Len(t, got, 24000)
			for i := range want {
				require.InDelta(t, want[i], got[i], 3, "sample %d", i)
			}
		})
	}
	require.Panics(t, func() { soxrQuality(ResampleQuality(-1)) })
}

func TestResampleQualitySignal(t *testing.T) {
	src := qualityTone(8000, 16000, 3400)
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			convert := func(opts ...ResampleOption) PCM16Sample {
				if !streaming {
					return Resample(nil, 24000, src, 8000, opts...)
				}
				var out PCM16Sample
				w := ResampleWriter(NewPCM16BufferWriter(&out, 24000), 8000, opts...)
				for i := 0; i < len(src); i += 40 {
					require.NoError(t, w.WriteSample(src[i:i+40]))
				}
				require.NoError(t, w.Close())
				require.Len(t, out, 48000)
				return out
			}
			low := qualityAmplitude(convert(WithResampleQuality(ResampleQualityLow)))
			medium := qualityAmplitude(convert(WithResampleQuality(ResampleQualityMedium)))
			def := qualityAmplitude(convert())
			require.InDelta(t, low, def, 2, "default must remain LOW")
			require.InDelta(t, 10000, medium, 1000, "MEDIUM should preserve the fundamental")
			require.Greater(t, medium, low*1.5, "MEDIUM should materially exceed LOW")
			t.Logf("fundamental amplitude: default=%.1f LOW=%.1f MEDIUM=%.1f", def, low, medium)
		})
	}
}

func TestResampleQualityStreamingTail(t *testing.T) {
	for _, q := range []ResampleQuality{
		ResampleQualityQuick, ResampleQualityLow, ResampleQualityMedium,
		ResampleQualityHigh, ResampleQualityVeryHigh,
	} {
		for _, rates := range [][2]int{{8000, 24000}, {48000, 8000}, {11025, 48000}} {
			t.Run(fmt.Sprintf("quality=%d/%d-%d", q, rates[0], rates[1]), func(t *testing.T) {
				src := qualityTone(rates[0], rates[0]/2, 1000)
				opts := []ResampleOption{WithResampleQuality(q)}
				want := Resample(nil, rates[1], src, rates[0], opts...)
				var got PCM16Sample
				w := ResampleWriter(NewPCM16BufferWriter(&got, rates[1]), rates[0], opts...)
				for i := 0; i < len(src); i += 7 {
					require.NoError(t, w.WriteSample(src[i:min(i+7, len(src))]))
				}
				before := len(got)
				require.NoError(t, w.Close())
				if q != ResampleQualityQuick {
					require.Greater(t, len(got), before, "Close must emit buffered tail")
				}
				require.Len(t, got, (len(src)*rates[1]+rates[0]/2)/rates[0])
				require.Len(t, got, len(want))
				// Dither can differ, but chunk boundaries must not reset or flush the filter.
				for i := range want {
					require.InDelta(t, want[i], got[i], 3, "sample %d", i)
				}
			})
		}
	}
}
