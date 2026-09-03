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

//go:build cgo

package opus

import (
	"github.com/livekit/protocol/logger"

	media "github.com/livekit/media-sdk"
)

const (
	SDPNameOnly = "opus"
	SDPName     = SDPNameOnly + "/48000/2"
)

func init() {
	info := media.CodecTypeInfo{
		Name:        SDPNameOnly,
		RTPIsStatic: false,
		Priority:    10,
		FileExt:     "opus",
	}
	media.RegisterCodec(media.NewCodec(info, nil, func(c media.CodecConfig) (media.CodecInfo, media.CreateFunc, bool) {
		switch c.Channels {
		default:
			return media.CodecInfo{}, nil, false
		case 0, 1, 2:
		}
		const defRate = 48000
		if c.SampleRate == 0 {
			c.SampleRate = 48000
		}
		if c.SampleRate != defRate {
			return media.CodecInfo{}, nil, false
		}
		// TODO: support codec parameters

		info := media.CodecInfo{CodecTypeInfo: info, CodecConfig: c}
		info.Params = nil

		create := media.NewAudioCodecFunc(info, func(w media.PCM16Writer) media.WriteCloser[Sample] {
			dec, err := Decode(w, 1, logger.GetLogger())
			if err != nil {
				return nil
			}
			return dec
		}, func(w media.WriteCloser[Sample]) media.PCM16Writer {
			enc, err := Encode(w, 1, logger.GetLogger())
			if err != nil {
				return nil
			}
			return enc
		})
		return info, create, true
	}))
}
