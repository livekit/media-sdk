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

package sdp

import (
	"strings"

	"github.com/livekit/media-sdk"
)

var (
	codecByName = make(map[string]media.Codec)
)

func init() {
	media.OnRegister(func(c media.Codec) {
		name := c.Info().SDPName
		if name != "" {
			name = strings.ToLower(name)
			codecByName[name] = c
			if strings.Count(name, "/") == 1 {
				codecByName[name+"/1"] = c
			}
		}
	})
}

// CodecByNameWith finds the codec with a given SDP name.
// If the codec is not found or disabled in the codec set, it returns nil.
func CodecByNameWith(s *media.CodecSet, name string) media.Codec {
	if s == nil {
		s = media.GlobalCodecs()
	}
	c := codecByName[strings.ToLower(name)]
	if !s.IsEnabled(c) {
		return nil
	}
	return c
}

// CodecByName finds the codec with a given SDP name.
//
// Deprecated: use CodecByNameWith
func CodecByName(name string) media.Codec {
	return CodecByNameWith(media.GlobalCodecs(), name)
}
