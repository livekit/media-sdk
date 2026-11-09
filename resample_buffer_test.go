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

	"github.com/stretchr/testify/require"
)

type chunkCounter struct{ chunks int }

func (c *chunkCounter) String() string                { return "chunkCounter" }
func (c *chunkCounter) SampleRate() int               { return 48000 }
func (c *chunkCounter) WriteSample(PCM16Sample) error { c.chunks++; return nil }
func (c *chunkCounter) Close() error                  { return nil }

// Every resampled stream goes through here on each write, so a per-write allocation adds up across calls.
func TestFrameBufferWriteDoesNotAllocate(t *testing.T) {
	const runs = 100
	b := &frameBuffer{srcRate: 8000, dstRate: 48000, ch: 1}
	out := make(PCM16Sample, 960)
	w := &chunkCounter{}
	allocs := testing.AllocsPerRun(runs, func() {
		b.buf = append(b.buf, out...)
		b.queue(160)
		_ = b.flush(w)
	})
	require.Zero(t, allocs)
	// AllocsPerRun makes one warm-up call before it counts.
	require.Equal(t, runs+1, w.chunks)
}

// ReadSample consumes inbuf from the front, so appending the next write must not need a new array.
func TestBeepWriterDoesNotAllocate(t *testing.T) {
	w := newResampleWriterBeep(&chunkCounter{}, 8000, &resampleOptions{Channels: 1})
	frame := make(PCM16Sample, 160)
	require.Zero(t, testing.AllocsPerRun(100, func() { _ = w.WriteSample(frame) }))
}
