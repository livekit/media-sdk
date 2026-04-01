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
	t.Run("ulaw", func(t *testing.T) {
		testLaw(t, path, "ulaw", src, EncodeULawTo, DecodeULawTo)
	})
	t.Run("alaw", func(t *testing.T) {
		testLaw(t, path, "alaw", src, EncodeALawTo, DecodeALawTo)
	})
}

func testLaw(t *testing.T, path, name string, src media.PCM16Sample,
	encodeFn func(dst []byte, src []int16),
	decodeFn func(dst []int16, src []byte),
) {
	lpath := path + "." + name

	data, err := os.ReadFile(lpath)
	require.NoError(t, err)

	dec := readPCM16(t, lpath+".s16le")
	t.Run("encode", func(t *testing.T) {
		src := slices.Clone(src)
		dst := make([]byte, len(src))
		encodeFn(dst, src)
		require.Equal(t, data, dst)
	})
	t.Run("decode", func(t *testing.T) {
		src := slices.Clone(data)
		dst := make(media.PCM16Sample, len(src))
		decodeFn(dst, src)
		require.Equal(t, dec, dst)
	})
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
