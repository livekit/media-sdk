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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
)

// G.711 audio entering a room crosses 8k->48k; soxr's default 1024-point minimum DFT held three
// 20 ms frames there before any output, where the same filter on smaller blocks holds two.
func TestResampleWriter8kTo48kLatency(t *testing.T) {
	const srcRate, dstRate, maxLagFrames = 8000, 48000, 2
	frame, dstFrame := srcRate/media.DefFramesPerSec, dstRate/media.DefFramesPerSec
	var out media.PCM16Sample
	w := media.ResampleWriter(media.NewPCM16BufferWriter(&out, dstRate), srcRate)
	for i := range media.DefFramesPerSec {
		require.NoError(t, w.WriteSample(make(media.PCM16Sample, frame)))
		require.LessOrEqual(t, (i+1)*dstFrame-len(out), maxLagFrames*dstFrame, "write %d", i)
	}
	require.NoError(t, w.Close())
}
