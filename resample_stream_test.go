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
		{8000, 48000, 160},
		{11025, 48000, 220},
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

// Lag after warm-up may exceed its warm-up peak by at most one write's output.
func TestResampleWriterKeepsUp(t *testing.T) {
	const srcRate, dstRate, writeFrames, writes = 11025, 8000, 221, 2000
	dstFrame := writeFrames * dstRate / srcRate
	for _, predictable := range []bool{false, true} {
		t.Run(fmt.Sprintf("predictable=%v", predictable), func(t *testing.T) {
			var out media.PCM16Sample
			w := media.ResampleWriter(media.NewPCM16BufferWriter(&out, dstRate), srcRate, media.WithPredictableResample(predictable))
			warmupLag := 0
			for i := range writes {
				require.NoError(t, w.WriteSample(make(media.PCM16Sample, writeFrames)))
				lag := (i+1)*writeFrames*dstRate/srcRate - len(out)
				if i < media.DefFramesPerSec {
					warmupLag = max(warmupLag, lag)
					continue
				}
				require.LessOrEqual(t, lag, warmupLag+dstFrame, "write %d", i)
			}
			require.NoError(t, w.Close())
		})
	}
}

// Encoders downstream take exact frame sizes, so every chunk but the last must match the write's duration.
func TestResampleWriterChunksMatchWrites(t *testing.T) {
	cases := []struct {
		srcRate, dstRate, writeFrames int
	}{
		{44100, 48000, 441},
		{16000, 48000, 320},
	}
	const writes = 100
	for _, c := range cases {
		for _, predictable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d-%d/predictable=%v", c.srcRate, c.dstRate, predictable), func(t *testing.T) {
				var chunks []media.PCM16Sample
				w := media.ResampleWriter(media.NewPCM16FrameWriter(&chunks, c.dstRate), c.srcRate,
					media.WithPredictableResample(predictable))
				for range writes {
					require.NoError(t, w.WriteSample(make(media.PCM16Sample, c.writeFrames)))
				}
				require.NoError(t, w.Close())
				require.Greater(t, len(chunks), 1)
				dstFrame := c.writeFrames * c.dstRate / c.srcRate
				for i, chunk := range chunks[:len(chunks)-1] {
					require.Equal(t, dstFrame, len(chunk), "chunk %d of %d", i, len(chunks))
				}
			})
		}
	}
}

// A long write, such as a long-ptime packet, or a short one must not change the chunking of the writes around it.
func TestResampleWriterChunksMirrorWrites(t *testing.T) {
	const srcRate, dstRate = 48000, 8000
	frame := srcRate / media.DefFramesPerSec
	dstFrame := frame * dstRate / srcRate
	repeat := func(n, size int) []int { return slices.Repeat([]int{size}, n) }
	cases := []struct {
		name   string
		writes []int
		chunks [][]int
	}{
		{
			name:   "long write first",
			writes: append([]int{srcRate}, repeat(50, frame)...),
			chunks: [][]int{append([]int{dstRate}, repeat(50, dstFrame)...)},
		},
		{
			// The short write adds under one sample. It joins the last chunk if soxr still holds it at Close,
			// but beep runs a sample ahead and has already flushed it.
			name:   "short write mid-stream",
			writes: slices.Concat(repeat(25, frame), []int{5}, repeat(25, frame)),
			chunks: [][]int{
				append(repeat(49, dstFrame), dstFrame+1),
				append(repeat(50, dstFrame), 1),
			},
		},
	}
	for _, c := range cases {
		for _, predictable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/predictable=%v", c.name, predictable), func(t *testing.T) {
				require.Contains(t, c.chunks, chunkSizes(t, srcRate, dstRate, c.writes, 1, predictable))
			})
		}
	}

	// Stereo writes ending mid-frame carry a sample to the next write; chunks still follow whole frames.
	stereoWrites := make([]int, 50)
	for i := range stereoWrites {
		stereoWrites[i] = 2*frame + 1 - 2*(i%2)
	}
	for _, predictable := range []bool{false, true} {
		t.Run(fmt.Sprintf("stereo writes ending mid-frame/predictable=%v", predictable), func(t *testing.T) {
			require.Equal(t, repeat(50, 2*dstFrame), chunkSizes(t, srcRate, dstRate, stereoWrites, 2, predictable))
		})
	}

	// 11025 Hz writes of 221 or 220 frames resample to a fractional 160.36 or 159.64 samples at 8 kHz.
	const oddRate, oddWrites = 11025, 200
	sizes := make([]int, oddWrites)
	for i := range sizes {
		sizes[i] = 221 - i%2
	}
	for _, predictable := range []bool{false, true} {
		t.Run(fmt.Sprintf("fractional output sizes/predictable=%v", predictable), func(t *testing.T) {
			chunks := chunkSizes(t, oddRate, dstRate, sizes, 1, predictable)
			require.Len(t, chunks, oddWrites)
			total := 0
			for i, n := range chunks {
				require.InDelta(t, float64(sizes[i])*dstRate/oddRate, n, 1, "chunk %d", i)
				total += n
			}
			require.Equal(t, oddWrites/2*441*dstRate/oddRate, total)
		})
	}
}

func chunkSizes(t *testing.T, srcRate, dstRate int, writes []int, ch int, predictable bool) []int {
	t.Helper()
	var chunks []media.PCM16Sample
	w := media.ResampleWriter(media.NewPCM16FrameWriter(&chunks, dstRate), srcRate,
		media.WithResampleChannels(ch), media.WithPredictableResample(predictable))
	for _, n := range writes {
		require.NoError(t, w.WriteSample(make(media.PCM16Sample, n)))
	}
	require.NoError(t, w.Close())
	sizes := make([]int, len(chunks))
	for i, c := range chunks {
		sizes[i] = len(c)
	}
	return sizes
}

type closeCounter struct {
	media.PCM16Writer
	closes int
}

func (c *closeCounter) Close() error {
	c.closes++
	return c.PCM16Writer.Close()
}

func TestResampleWriterRejectsUseAfterClose(t *testing.T) {
	for _, predictable := range []bool{false, true} {
		t.Run(fmt.Sprintf("predictable=%v", predictable), func(t *testing.T) {
			down := &closeCounter{PCM16Writer: media.NewPCM16BufferWriter(new(media.PCM16Sample), 16000)}
			w := media.ResampleWriter(down, 48000, media.WithPredictableResample(predictable))
			require.NoError(t, w.Close())
			require.ErrorContains(t, w.WriteSample(make(media.PCM16Sample, 960)), "closed")
			require.ErrorContains(t, w.WriteSample(nil), "closed")
			require.ErrorContains(t, w.Close(), "closed")
			require.Equal(t, 1, down.closes, "the downstream writer was closed more than once")
		})
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
