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

package opus

import (
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/rtp"
)

const (
	testRate   = 48000
	testFrames = 50
	// Above half scale, so an int16 sum of two equal channels wraps.
	loudLevel   = 0.95
	minPeak     = 0.8 * math.MaxInt16
	minWaveCorr = 0.95
	// One frame, which covers the 312-sample encoder lookahead that delays decoded output.
	maxDelay = testRate / rtp.DefFramesPerSec
)

func loudTone() []media.PCM16Sample {
	n := testRate / rtp.DefFramesPerSec
	frames := make([]media.PCM16Sample, testFrames)
	for f := range frames {
		frames[f] = make(media.PCM16Sample, n)
		for i := range n {
			t := float64(f*n+i) / testRate
			frames[f][i] = int16(loudLevel * math.MaxInt16 * math.Sin(2*math.Pi*440*t))
		}
	}
	return frames
}

// The mono source goes on both channels, as a 2-channel encoder fed mono content would see it.
func encodePackets(t *testing.T, frames []media.PCM16Sample, channels int) []Sample {
	var packets []Sample
	enc, err := Encode(media.NewFrameWriter[Sample](&packets, testRate), channels, logger.GetLogger())
	require.NoError(t, err)
	for _, f := range frames {
		in := make(media.PCM16Sample, 0, len(f)*channels)
		for _, v := range f {
			for range channels {
				in = append(in, v)
			}
		}
		require.NoError(t, enc.WriteSample(in))
	}
	return packets
}

func decodePackets(t *testing.T, packets []Sample, channels int) media.PCM16Sample {
	var out media.PCM16Sample
	dec, err := Decode(media.NewPCM16BufferWriter(&out, testRate), channels, logger.GetLogger())
	require.NoError(t, err)
	for _, p := range packets {
		require.NoError(t, dec.WriteSample(p))
	}
	return out
}

func peak(x media.PCM16Sample) float64 {
	var m float64
	for _, v := range x {
		m = max(m, math.Abs(float64(v)))
	}
	return m
}

func correlation(a, b media.PCM16Sample) float64 {
	n := min(len(a), len(b))
	var sa, sb, sab, saa, sbb float64
	for i := range n {
		x, y := float64(a[i]), float64(b[i])
		sa += x
		sb += y
		sab += x * y
		saa += x * x
		sbb += y * y
	}
	fn := float64(n)
	return (sab - sa*sb/fn) / math.Sqrt((saa-sa*sa/fn)*(sbb-sb*sb/fn))
}

func delayedCorrelation(got, src media.PCM16Sample) float64 {
	best := -1.0
	for lag := range maxDelay + 1 {
		best = max(best, correlation(got[lag:], src))
	}
	return best
}

func TestDecodeLoudStereoToMono(t *testing.T) {
	frames := loudTone()
	src := slices.Concat(frames...)
	got := decodePackets(t, encodePackets(t, frames, 2), 1)
	require.Len(t, got, len(src))
	require.GreaterOrEqual(t, peak(got), minPeak)
	require.GreaterOrEqual(t, delayedCorrelation(got, src), minWaveCorr)
}

func TestDecodeChannelSwitchingStream(t *testing.T) {
	frames := loudTone()
	src := slices.Concat(frames...)
	mono := encodePackets(t, frames, 1)
	stereo := encodePackets(t, frames, 2)
	mixed := make([]Sample, len(mono))
	for i := range mixed {
		mixed[i] = mono[i]
		if i%2 == 1 {
			mixed[i] = stereo[i]
		}
	}
	got := decodePackets(t, mixed, 1)
	require.Len(t, got, len(src))
	require.GreaterOrEqual(t, delayedCorrelation(got, src), minWaveCorr)
}

func TestDecodeMonoToStereo(t *testing.T) {
	got := decodePackets(t, encodePackets(t, loudTone(), 1), 2)
	require.Len(t, got, testFrames*testRate/rtp.DefFramesPerSec*2)
	left := make(media.PCM16Sample, len(got)/2)
	right := make(media.PCM16Sample, len(got)/2)
	for i := range left {
		left[i], right[i] = got[2*i], got[2*i+1]
	}
	require.Equal(t, left, right)
}

func TestDecodeLongPackets(t *testing.T) {
	for _, dur := range []time.Duration{40 * time.Millisecond, 60 * time.Millisecond, 120 * time.Millisecond} {
		samples := int(time.Duration(testRate) * dur / time.Second)
		var packets []Sample
		enc, err := Encode(media.NewFrameWriter[Sample](&packets, testRate), 1, logger.GetLogger())
		require.NoError(t, err)
		require.NoError(t, enc.WriteSample(make(media.PCM16Sample, samples)))
		for _, channels := range []int{1, 2} {
			t.Run(fmt.Sprintf("%v/%dch", dur, channels), func(t *testing.T) {
				var out media.PCM16Sample
				dec, err := Decode(media.NewPCM16BufferWriter(&out, testRate), channels, logger.GetLogger())
				require.NoError(t, err)
				require.NoError(t, dec.WriteSample(packets[0]))
				require.Len(t, out, samples*channels)
			})
		}
	}
}

// nil input should return an error instead of segfaulting
func TestDecodeNil(t *testing.T) {
	_, err := Decode(nil, 1, logger.GetLogger())
	require.Error(t, err)
}
