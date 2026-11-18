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
// hold-back peaks late on some rates, so each stream runs 30 s.
func TestSteadyResampleEmitsEveryWrite(t *testing.T) {
	cases := []struct {
		src, dst, ms, ch int
		predictable      bool
	}{
		{8000, 48000, 20, 1, false},
		{48000, 8000, 20, 1, false},
		{16000, 48000, 20, 2, false},
		{48000, 16000, 20, 1, false},
		{8000, 48000, 10, 2, false},
		{48000, 8000, 10, 1, false},
		{11025, 48000, 10, 1, false},
		{44100, 48000, 10, 2, false},
		{8000, 48000, 20, 1, true},
		{11025, 48000, 10, 1, true},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d-%d/%dms/ch%d/predictable=%v", c.src, c.dst, c.ms, c.ch, c.predictable), func(t *testing.T) {
			var chunks []media.PCM16Sample
			w := media.ResampleWriter(media.NewPCM16FrameWriter(&chunks, c.dst), c.src,
				media.WithResampleChannels(c.ch), media.WithPredictableResample(c.predictable), media.WithSteadyResample())
			frames := c.src * c.ms / 1000
			in := make(media.PCM16Sample, frames*c.ch)
			for i := range 30_000 / c.ms {
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
