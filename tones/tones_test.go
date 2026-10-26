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

package tones

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
)

type frameRecorder struct {
	limit  int
	cancel context.CancelFunc
	tone   []bool
}

func (r *frameRecorder) String() string  { return "frameRecorder" }
func (r *frameRecorder) SampleRate() int { return 8000 }

func (r *frameRecorder) WriteSample(s media.PCM16Sample) error {
	if len(r.tone) == r.limit {
		return nil // Play may keep writing until it observes cancellation
	}
	r.tone = append(r.tone, slices.ContainsFunc(s, func(v int16) bool { return v != 0 }))
	if len(r.tone) == r.limit {
		r.cancel()
	}
	return nil
}

func TestPlayMultiSegment(t *testing.T) {
	const ms = time.Millisecond
	pattern := []Tone{
		{Freq: []Hz{400, 450}, Dur: 40 * ms, Silence: 20 * ms},
		{Freq: []Hz{400, 450}, Dur: 40 * ms, Silence: 60 * ms},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &frameRecorder{limit: 16, cancel: cancel}
	err := Play(ctx, w, 1000, pattern)
	require.ErrorIs(t, err, context.Canceled)

	cycle := []bool{true, true, false, true, true, false, false, false}
	require.Equal(t, slices.Concat(cycle, cycle), w.tone)
}
