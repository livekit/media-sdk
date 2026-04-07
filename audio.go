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

// AudioCodec is an audio codec implementation that can encode/decode bytes<->PCM.
//
// Each audio codec also implements AudioFrameCodec that can encode/decode bytes to a native Frame format of the codec.
type AudioCodec interface {
	Codec
	// EncodeBytes creates a PCM->bytes encoder using the codec.
	EncodeBytes(w BytesWriter) PCM16Writer
	// DecodeBytes creates a bytes->PCM decoder using the codec.
	DecodeBytes(w PCM16Writer) BytesWriter
}

// AudioFrameCodec is an audio codec implementation that can encode/decode bytes to/from a native Frame format of the codec.
type AudioFrameCodec[S BytesFrame] interface {
	AudioCodec
	// Encode creates a Frame->bytes encoder using the codec.
	// The frame is in a native format of the codec.
	Encode(w WriteCloser[S]) PCM16Writer
	// Decode creates a bytes->Frame decoder using the codec.
	// The frame is in a native format of the codec.
	Decode(w PCM16Writer) WriteCloser[S]
}

type AudioDecodeFunc[S Frame] func(w PCM16Writer) WriteCloser[S]
type AudioEncodeFunc[S Frame] func(w WriteCloser[S]) PCM16Writer

// NewAudioCodec creates an audio codec with a given encode and decode implementations.
func NewAudioCodec[S BytesFrame](
	info CodecInfo,
	decode AudioDecodeFunc[S],
	encode AudioEncodeFunc[S],
) AudioFrameCodec[S] {
	if info.SampleRate <= 0 {
		panic("invalid sample rate")
	}
	if info.RTPClockRate == 0 {
		info.RTPClockRate = info.SampleRate
	}
	return &audioCodec[S]{
		info:   info,
		encode: encode,
		decode: decode,
	}
}

type audioCodec[S BytesFrame] struct {
	info   CodecInfo
	decode AudioDecodeFunc[S]
	encode AudioEncodeFunc[S]
}

func (c *audioCodec[S]) Info() CodecInfo {
	return c.info
}

func (c *audioCodec[S]) Encode(w WriteCloser[S]) PCM16Writer {
	return c.encode(w)
}

func (c *audioCodec[S]) Decode(w PCM16Writer) WriteCloser[S] {
	return c.decode(w)
}

func (c *audioCodec[S]) EncodeBytes(w BytesWriter) PCM16Writer {
	bw := EncodeBytes[S](w, c.info.SampleRate)
	return c.encode(bw)
}

func (c *audioCodec[S]) DecodeBytes(w PCM16Writer) BytesWriter {
	pw := c.decode(w)
	return DecodeBytes[S](pw)
}
