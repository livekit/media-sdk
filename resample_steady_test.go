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

//go:build cgo

package media_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
)

type leader interface{ Lead() int }

// A skipped write is a 20 ms hole in an RTP stream and a doubled one is a burst (issue #109). soxr's
// hold-back peaks late on some rates, so each stream runs at least 30 s.
func TestSteadyResampleEmitsEveryWrite(t *testing.T) {
	cases := []struct {
		src, dst, ms, ch int
		predictable      bool
		secs             int
	}{
		// soxr's hold-back last peaked 1 frame higher at 38 s here, which the lead's margin covers.
		{src: 11025, dst: 48000, ms: 1, ch: 1, secs: 45},
		{src: 8000, dst: 48000, ms: 20, ch: 1},
		{src: 48000, dst: 8000, ms: 20, ch: 1},
		{src: 16000, dst: 48000, ms: 20, ch: 2},
		{src: 48000, dst: 16000, ms: 20, ch: 1},
		{src: 8000, dst: 48000, ms: 10, ch: 2},
		{src: 48000, dst: 8000, ms: 10, ch: 1},
		{src: 11025, dst: 48000, ms: 10, ch: 1},
		{src: 44100, dst: 48000, ms: 10, ch: 2},
		{src: 8000, dst: 48000, ms: 20, ch: 1, predictable: true},
		{src: 11025, dst: 48000, ms: 10, ch: 1, predictable: true},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d-%d/%dms/ch%d/predictable=%v", c.src, c.dst, c.ms, c.ch, c.predictable), func(t *testing.T) {
			var chunks []media.PCM16Sample
			w := media.ResampleWriter(media.NewPCM16FrameWriter(&chunks, c.dst), c.src,
				media.WithResampleChannels(c.ch), media.WithPredictableResample(c.predictable), media.WithSteadyResample())
			frames := c.src * c.ms / 1000
			in := make(media.PCM16Sample, frames*c.ch)
			secs := c.secs
			if secs == 0 {
				secs = 30
			}
			for i := range secs * 1000 / c.ms {
				before := len(chunks)
				require.NoError(t, w.WriteSample(in))
				// beep's look-ahead holds the first write back, so its chunks run one write late.
				if c.predictable {
					require.LessOrEqual(t, len(chunks), before+1, "write %d", i)
					require.True(t, i == 0 || len(chunks) == before+1, "write %d emitted nothing", i)
					continue
				}
				require.Len(t, chunks, before+1, "write %d", i)
				own := (i+1)*frames*c.dst/c.src - i*frames*c.dst/c.src
				require.Len(t, chunks[before], own*c.ch, "write %d", i)
			}
			require.NoError(t, w.Close())
		})
	}
}

// Steady output is the default output behind Lead frames of silence, so a consumer that subtracts Lead
// keeps exact playout positions. Every write stays a whole chunk, since encoders take fixed frames.
func TestSteadyResampleOnlyDelaysOutput(t *testing.T) {
	cases := []struct{ src, dst, ms, ch int }{
		{8000, 48000, 20, 1},
		{48000, 16000, 20, 1},
		{11025, 48000, 10, 2},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d-%d/%dms/ch%d", c.src, c.dst, c.ms, c.ch), func(t *testing.T) {
			frames := c.src * c.ms / 1000
			// streamed counts the samples written before Close.
			run := func(opts ...media.ResampleOption) (chunks []media.PCM16Sample, lead, streamed int) {
				w := media.ResampleWriter(media.NewPCM16FrameWriter(&chunks, c.dst), c.src,
					append(opts, media.WithResampleChannels(c.ch))...)
				in := make(media.PCM16Sample, frames*c.ch)
				for i := range 100 {
					for j := range in {
						in[j] = int16((i*len(in) + j) * 7919 % 20000)
					}
					require.NoError(t, w.WriteSample(in))
				}
				streamed = len(slices.Concat(chunks...))
				require.NoError(t, w.Close())
				return chunks, w.(leader).Lead(), streamed
			}
			plain, plainLead, plainStreamed := run()
			steady, lead, _ := run(media.WithSteadyResample())
			require.Zero(t, plainLead)
			require.Positive(t, lead)

			out := slices.Concat(steady...)
			want := slices.Concat(make(media.PCM16Sample, lead*c.ch), slices.Concat(plain...))
			require.GreaterOrEqual(t, len(out), len(want))
			streamedEnd := lead*c.ch + plainStreamed
			require.Equal(t, want[:streamedEnd], out[:streamedEnd])
			// soxr's flush at close rounds a non-integer ratio's tail up to 2 apart when drained in other sizes.
			const flushRounding = 2
			for i := streamedEnd; i < len(want); i++ {
				require.InDelta(t, want[i], out[i], flushRounding, "flushed sample %d of %d", i, len(want))
			}
			require.Equal(t, make(media.PCM16Sample, len(out)-len(want)), out[len(want):], "close fills the last chunk with silence")

			nominal := frames * c.dst / c.src
			for i, chunk := range steady {
				require.Zero(t, len(chunk)%c.ch, "chunk %d", i)
				require.InDelta(t, nominal, len(chunk)/c.ch, 1, "chunk %d", i)
			}
		})
	}
}

// A short last write must not set the frame size for what close drains, or a file ending in a partial
// frame leaves hundreds of tiny RTP packets at hangup.
func TestSteadyResampleCloseKeepsFrameAfterShortWrite(t *testing.T) {
	const srcRate, dstRate = 8000, 48000
	var chunks []media.PCM16Sample
	w := media.ResampleWriter(media.NewPCM16FrameWriter(&chunks, dstRate), srcRate, media.WithSteadyResample())
	for range 50 {
		require.NoError(t, w.WriteSample(make(media.PCM16Sample, srcRate/media.DefFramesPerSec)))
	}
	require.NoError(t, w.WriteSample(make(media.PCM16Sample, 1)))
	before := len(chunks)
	require.NoError(t, w.Close())
	for i, chunk := range chunks[before:] {
		require.Len(t, chunk, dstRate/media.DefFramesPerSec, "close chunk %d", i)
	}
}

// With no whole chunk ever queued there is no frame to keep, and a lone odd-sized write would crash
// G.722, so close emits nothing.
func TestSteadyResampleCloseWithoutAChunkEmitsNothing(t *testing.T) {
	var chunks []media.PCM16Sample
	w := media.ResampleWriter(media.NewPCM16FrameWriter(&chunks, 16000), 48000, media.WithSteadyResample())
	require.NoError(t, w.WriteSample(make(media.PCM16Sample, 1)))
	require.NoError(t, w.Close())
	require.Empty(t, chunks)
}

// Debug dumps must not change what a consumer subtracts for playout.
func TestSteadyResampleLeadSurvivesInputDump(t *testing.T) {
	w := media.ResampleWriter(media.NewPCM16BufferWriter(new(media.PCM16Sample), 48000), 8000,
		media.WithSteadyResample(), media.WithResampleDump(t.TempDir()+"/in", ""))
	l, ok := w.(leader)
	require.True(t, ok, "%T has no Lead", w)
	require.Positive(t, l.Lead())
	require.NoError(t, w.Close())
}

// One-shot output has no stream to keep steady, so the option must not pad it.
func TestSteadyResampleLeavesOneShotAlone(t *testing.T) {
	src := make(media.PCM16Sample, 110)
	for i := range src {
		src[i] = int16(i * 300)
	}
	for _, predictable := range []bool{false, true} {
		plain := media.Resample(nil, 48000, src, 11025, media.WithPredictableResample(predictable))
		steady := media.Resample(nil, 48000, src, 11025, media.WithPredictableResample(predictable), media.WithSteadyResample())
		require.Equal(t, plain, steady, "predictable=%v", predictable)
	}
}
