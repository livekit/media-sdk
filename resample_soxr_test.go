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
	"testing"
	"time"
)

// The writer only passes whole frames, so this calls soxr directly with less than one.
func TestSoxrReturnsOnPartialFrame(t *testing.T) {
	const hangGuard = 10 * time.Second
	var out PCM16Sample
	w := newResampleWriter(NewPCM16BufferWriter(&out, 16000), 48000, &resampleOptions{Channels: 2}).(*resampleWriter)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = w.r.Resample(nil, PCM16Sample{0})
	}()
	select {
	case <-done:
	case <-time.After(hangGuard):
		t.Fatal("soxr spun on a partial frame")
	}
}
