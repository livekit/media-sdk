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

package media_test

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
)

func TestResampleWriterKeepsAllInput(t *testing.T) {
	cases := []struct {
		srcRate, dstRate, chunkFrames int
	}{
		{16000, 8000, 441},
		{48000, 16000, 961},
	}
	const writes = 200
	for _, c := range cases {
		for _, ch := range []int{1, 2} {
			for _, predictable := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d-%d/chunk=%d/channels=%d/predictable=%v", c.srcRate, c.dstRate, c.chunkFrames, ch, predictable), func(t *testing.T) {
					src := make(media.PCM16Sample, writes*c.chunkFrames*ch)
					var out media.PCM16Sample
					w := media.ResampleWriter(media.NewPCM16BufferWriter(&out, c.dstRate), c.srcRate,
						media.WithResampleChannels(ch), media.WithPredictableResample(predictable))
					for chunk := range slices.Chunk(src, c.chunkFrames*ch) {
						require.NoError(t, w.WriteSample(chunk))
					}
					require.NoError(t, w.Close())
					want := len(src) / ch * c.dstRate / c.srcRate
					require.InDelta(t, want, len(out)/ch, 2, "output frames")
				})
			}
		}
	}
}

func TestResampleBufferNonIntegerRatio(t *testing.T) {
	const srcRate, dstRate = 44100, 48000
	for _, ch := range []int{1, 2} {
		for _, predictable := range []bool{false, true} {
			t.Run(fmt.Sprintf("channels=%d/predictable=%v", ch, predictable), func(t *testing.T) {
				src := make(media.PCM16Sample, srcRate*ch)
				out := media.Resample(nil, dstRate, src, srcRate,
					media.WithResampleChannels(ch), media.WithPredictableResample(predictable))
				require.InDelta(t, dstRate, len(out)/ch, dstRate/100, "output frames")
			})
		}
	}
}

// Streaming in 20 ms writes must give the same audio as one-shot resampling, apart from the stream's tail.
func TestResampleBeepWriterMatchesBuffer(t *testing.T) {
	for _, ch := range []int{1, 2} {
		for _, rates := range [][2]int{{16000, 48000}, {48000, 16000}} {
			srcRate, dstRate := rates[0], rates[1]
			t.Run(fmt.Sprintf("channels=%d/%d-%d", ch, srcRate, dstRate), func(t *testing.T) {
				opts := []media.ResampleOption{media.WithResampleChannels(ch), media.WithPredictableResample(true)}
				src := make(media.PCM16Sample, 0, srcRate*ch)
				for i := range srcRate {
					for c := range ch {
						src = append(src, toneSample(leftTone*float64(c+1), srcRate, i)/2)
					}
				}
				want := media.Resample(nil, dstRate, src, srcRate, opts...)

				var got media.PCM16Sample
				w := media.ResampleWriter(media.NewPCM16BufferWriter(&got, dstRate), srcRate, opts...)
				for chunk := range slices.Chunk(src, srcRate/media.DefFramesPerSec*ch) {
					require.NoError(t, w.WriteSample(chunk))
				}
				require.NoError(t, w.Close())

				n := min(len(want), len(got)) - dstRate/media.DefFramesPerSec*ch
				require.Positive(t, n)
				bad, worst := 0, 0
				for i := range n {
					if d := absDiff(want[i], got[i]); d > 1 {
						bad++
						worst = max(worst, d)
					}
				}
				require.Zero(t, bad, "%d samples differ from one-shot output, worst by %d", bad, worst)
			})
		}
	}
}

// Interpolation overshoot near full scale must clip, not wrap to the opposite sign.
func TestResampleBeepClipsOvershoot(t *testing.T) {
	const (
		srcRate, dstRate = 16000, 48000
		halfPeriod       = 40
	)
	for _, ch := range []int{1, 2} {
		t.Run(fmt.Sprintf("channels=%d", ch), func(t *testing.T) {
			src := make(media.PCM16Sample, 0, srcRate*ch)
			for i := range srcRate {
				v := int16(math.MaxInt16)
				if i/halfPeriod%2 == 1 {
					v = -math.MaxInt16
				}
				for range ch {
					src = append(src, v)
				}
			}
			edges := srcRate/halfPeriod - 1
			out := media.Resample(nil, dstRate, src, srcRate, media.WithResampleChannels(ch), media.WithPredictableResample(true))
			for c := range ch {
				flips := 0
				for i := c + ch; i < len(out); i += ch {
					if (out[i] < 0) != (out[i-ch] < 0) {
						flips++
					}
				}
				require.LessOrEqual(t, flips, edges+2, "channel %d: sign flips beyond the square wave's edges", c)
			}
		})
	}
}

func TestResampleWriterCloseReportsPartialFrame(t *testing.T) {
	for _, predictable := range []bool{false, true} {
		t.Run(fmt.Sprintf("predictable=%v", predictable), func(t *testing.T) {
			var out media.PCM16Sample
			w := media.ResampleWriter(media.NewPCM16BufferWriter(&out, 16000), 48000,
				media.WithResampleChannels(2), media.WithPredictableResample(predictable))
			require.NoError(t, w.WriteSample(make(media.PCM16Sample, 2*960+1)))
			require.Error(t, w.Close(), "a trailing half frame was dropped silently")
		})
	}
}

func absDiff(a, b int16) int {
	d := int(a) - int(b)
	if d < 0 {
		return -d
	}
	return d
}
