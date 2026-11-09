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

package g711

import (
	"cmp"
	"encoding/binary"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
)

func TestG711(t *testing.T) {
	const path = "testdata/sweep"
	src := readPCM16(t, path+".s16le")
	testLaw[ULawSample](t, path, "ulaw", src)
	testLaw[ALawSample](t, path, "alaw", src)
}

func testLaw[T ~[]byte, P interface {
	*T
	Decode() media.PCM16Sample
	Encode(data media.PCM16Sample)
}](t *testing.T, path, name string, src media.PCM16Sample) {
	t.Run(name, func(t *testing.T) {
		lpath := path + "." + name

		data, err := os.ReadFile(lpath)
		require.NoError(t, err)
		log := T(data)

		dec := readPCM16(t, lpath+".s16le")
		t.Run("encode", func(t *testing.T) {
			src := slices.Clone(src)
			var dst T
			P(&dst).Encode(src)
			require.Equal(t, log, dst)
		})
		t.Run("decode", func(t *testing.T) {
			src := slices.Clone(log)
			dst := P(&src).Decode()
			require.Equal(t, dec, dst)
		})
	})
}

// A call sends same-size frames, so a writer that reallocates when a frame exactly fills its buffer allocates on every frame.
func TestWritersReuseBuffers(t *testing.T) {
	pcm := readPCM16(t, "testdata/sweep.s16le")
	var frames []media.PCM16Sample
	for _, n := range []int{80, 160, 160, 80} {
		frames = append(frames, pcm[:n])
		pcm = pcm[n:]
	}
	var ulaw []ULawSample
	var alaw []ALawSample
	for _, f := range frames {
		var u ULawSample
		u.Encode(f)
		ulaw = append(ulaw, u)
		var a ALawSample
		a.Encode(f)
		alaw = append(alaw, a)
	}
	t.Run("ulaw encode", func(t *testing.T) {
		out := &frameSink[ULawSample, byte]{record: true}
		testWriter(t, EncodeULaw(out), out, frames, ulaw)
	})
	t.Run("ulaw decode", func(t *testing.T) {
		out := &frameSink[media.PCM16Sample, int16]{record: true}
		testWriter(t, DecodeULaw(out), out, ulaw, decodeAll(ulaw))
	})
	t.Run("alaw encode", func(t *testing.T) {
		out := &frameSink[ALawSample, byte]{record: true}
		testWriter(t, EncodeALaw(out), out, frames, alaw)
	})
	t.Run("alaw decode", func(t *testing.T) {
		out := &frameSink[media.PCM16Sample, int16]{record: true}
		testWriter(t, DecodeALaw(out), out, alaw, decodeAll(alaw))
	})
}

func decodeAll[T interface{ Decode() media.PCM16Sample }](frames []T) []media.PCM16Sample {
	var out []media.PCM16Sample
	for _, f := range frames {
		out = append(out, f.Decode())
	}
	return out
}

func testWriter[In ~[]EI, EI any, Out ~[]EO, EO any](t *testing.T, w media.WriteCloser[In], out *frameSink[Out, EO], in []In, want []Out) {
	for _, f := range in {
		require.NoError(t, w.WriteSample(f))
	}
	require.Equal(t, want, out.frames)
	out.record = false
	largest := slices.MaxFunc(in, func(a, b In) int { return cmp.Compare(len(a), len(b)) })
	require.Zero(t, testing.AllocsPerRun(100, func() { _ = w.WriteSample(largest) }))
}

type frameSink[T ~[]E, E any] struct {
	record bool
	frames []T
}

func (s *frameSink[T, E]) String() string  { return "frameSink" }
func (s *frameSink[T, E]) SampleRate() int { return 8000 }
func (s *frameSink[T, E]) Close() error    { return nil }
func (s *frameSink[T, E]) WriteSample(f T) error {
	if s.record {
		s.frames = append(s.frames, slices.Clone(f))
	}
	return nil
}

func readPCM16(t testing.TB, path string) media.PCM16Sample {
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := make([]int16, len(data)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return out
}
