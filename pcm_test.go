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
	"testing"
	"time"

	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/require"
)

type keptSamples struct{ data [][]byte }

func (s *keptSamples) WriteSample(sample media.Sample) error {
	s.data = append(s.data, sample.Data)
	return nil
}

// Callers treat FromSampleWriter as a black box, so a writer that keeps its samples must keep getting copies.
func TestFromSampleWriterLetsTheWriterKeepSamples(t *testing.T) {
	sink := &keptSamples{}
	w := FromSampleWriter[[]byte](sink, 48000, 20*time.Millisecond)
	frame := []byte{1, 2, 3, 4}
	require.NoError(t, w.WriteSample(frame))
	copy(frame, []byte{5, 6, 7, 8})
	require.NoError(t, w.WriteSample(frame))
	require.Equal(t, [][]byte{{1, 2, 3, 4}, {5, 6, 7, 8}}, sink.data)
}

type lastSample struct {
	data []byte
	dur  time.Duration
}

func (s *lastSample) WriteSample(sample media.Sample) error {
	s.data = append(s.data[:0], sample.Data...)
	s.dur = sample.Duration
	return nil
}

// Every encoded frame goes through here, and pion copies the payload while packetizing, so another copy is waste.
func TestFromSampleWriterWithoutCopyDoesNotAllocate(t *testing.T) {
	sink := &lastSample{}
	w := FromSampleWriter[[]byte](sink, 48000, 20*time.Millisecond, WithSampleCopy(false))
	frame := []byte{1, 2, 3, 4}
	require.Zero(t, testing.AllocsPerRun(100, func() { _ = w.WriteSample(frame) }))
	require.Equal(t, frame, sink.data)
	require.Equal(t, 20*time.Millisecond, sink.dur)
}
