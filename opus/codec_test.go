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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
)

func TestSDPName(t *testing.T) {
	// RFC 7587 requires "opus/48000/2", even for mono streams.
	var codec media.CodecType
	for _, c := range media.Codecs() {
		if c.SDPName() == SDPNameOnly {
			codec = c
		}
	}
	require.NotNil(t, codec)
	for _, ch := range []int{0, 1, 2} {
		info, _, ok := codec.Supports(media.CodecConfig{Channels: ch})
		require.True(t, ok)
		exp := SDPName
		if ch == 1 {
			exp = SDPNameOnly + "/48000/1"
		}
		require.Equal(t, exp, info.SDPFullName(), "channels: %d", ch)
	}
}
