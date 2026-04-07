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

package rtp

import (
	"fmt"

	"github.com/livekit/media-sdk"
	"github.com/pion/rtp"
)

var (
	codecByType [0xff]media.Codec
)

func init() {
	media.OnRegister(func(c media.Codec) {
		info := c.Info()
		if info.RTPIsStatic {
			codecByType[info.RTPDefType] = c
		}
	})
}

func CodecByPayloadType(typ byte) media.Codec {
	return codecByType[typ]
}

func HandlePayload(w media.BytesWriter, typ byte) HandlerCloser {
	return &rawHandler{w: w, typ: typ}
}

type rawHandler struct {
	w   media.BytesWriter
	typ byte
}

func (d *rawHandler) String() string {
	return fmt.Sprintf("RTP(%d) -> %s", int(d.typ), d.w.String())
}

func (d *rawHandler) Close() {
	d.w.Close()
}

func (d *rawHandler) HandleRTP(h *rtp.Header, payload []byte) error {
	return d.w.WriteRaw(payload)
}

func DecodePCM(w media.PCM16Writer, c media.AudioCodec, typ byte) HandlerCloser {
	return HandlePayload(c.DecodeBytes(w), typ)
}

func EncodePCM(w *Stream, c media.AudioCodec) media.PCM16Writer {
	return c.EncodeBytes(w)
}

func HandleAudio[S BytesFrame](w media.WriteCloser[S], c media.AudioFrameCodec[S], typ byte) HandlerCloser {
	bw := media.DecodeBytes(w)
	return HandlePayload(bw, typ)
}

func WriteAudio[S BytesFrame](w *Stream, c media.AudioFrameCodec[S]) media.WriteCloser[S] {
	return media.EncodeBytes[S](w, c.Info().SampleRate)
}
