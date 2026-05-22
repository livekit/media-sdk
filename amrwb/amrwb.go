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

package amrwb

import (
	"errors"
	"fmt"
	"io"

	"github.com/livekit/amrwb-cgo"

	"github.com/livekit/media-sdk"
)

const (
	SDPName    = "AMR-WB/16000"
	SampleRate = 16000
)

func init() {
	media.RegisterCodec(media.NewAudioCodec(media.CodecInfo{
		SDPName:     SDPName,
		SampleRate:  SampleRate,
		RTPIsStatic: false,
		Priority:    -4,
		FileExt:     "amrwb",
		Disabled:    true,
	}, Decode, Encode))
}

type Sample []byte

func (s Sample) Size() int {
	return len(s)
}

func (s Sample) CopyTo(dst []byte) (int, error) {
	if len(dst) < len(s) {
		return 0, io.ErrShortBuffer
	}
	n := copy(dst, s)
	return n, nil
}

type Writer = media.WriteCloser[Sample]

func Decode(w media.PCM16Writer) Writer {
	if w.SampleRate() != SampleRate {
		w = media.ResampleWriter(w, SampleRate)
	}
	return &Decoder{
		w: w,
		d: amrwb.NewDecoder(),
	}
}

type Decoder struct {
	w     media.PCM16Writer
	d     *amrwb.Decoder
	frame amrwb.PCMFrame
	buf   media.PCM16Sample
}

func (d *Decoder) String() string {
	return fmt.Sprintf("AMR-WB(decode) -> %s", d.w)
}

func (d *Decoder) SampleRate() int {
	return SampleRate
}

func (d *Decoder) Close() error {
	d.d.Close()
	return d.w.Close()
}

func (d *Decoder) WriteSample(in Sample) error {
	d.buf = d.buf[:0]
	var blockErr error
	for len(in) > 0 {
		n, err := d.d.Decode(&d.frame, in)
		if err != nil {
			blockErr = err
			break
		}
		in = in[n:]
		d.buf = append(d.buf, d.frame[:]...)
	}
	if len(d.buf) != 0 {
		if err := d.w.WriteSample(d.buf); err != nil {
			return err
		}
	}
	return blockErr
}

func Encode(w Writer) media.PCM16Writer {
	if w.SampleRate() != SampleRate {
		panic("unsupported sample rate")
	}
	return &Encoder{
		w: w,
		e: amrwb.NewEncoder(amrwb.Best),
	}
}

type Encoder struct {
	w    Writer
	e    *amrwb.Encoder
	buf  []byte
	done bool
}

func (e *Encoder) String() string {
	return fmt.Sprintf("AMR-WB(encode) -> %s", e.w)
}

func (e *Encoder) SampleRate() int {
	return SampleRate
}

func (e *Encoder) Close() error {
	return e.w.Close()
}

func (e *Encoder) WriteSample(in media.PCM16Sample) error {
	if len(in) == 0 {
		return nil
	}
	var blockErr error
	if e.done {
		// We zero-padded previous frame, but we still got data after that.
		// The stream must be normalized with FullFrames by the caller instead.
		blockErr = errors.New("amrwb: writing frame after a short previous frame")
	}
	e.buf = e.buf[:0]
	for len(in) > 0 {
		const n = amrwb.PCMFrameSize
		if len(in) < n {
			// Zero pad, it's okay for the last frame only.
			// We'll return the error if we get another frame after this.
			e.done = true
			var buf amrwb.PCMFrame
			copy(buf[:], in)
			in = buf[:]
		}
		frame := (*amrwb.PCMFrame)(in[:n])
		in = in[n:]
		e.buf = e.e.Encode(e.buf, frame)
	}
	if len(e.buf) != 0 {
		if err := e.w.WriteSample(e.buf); err != nil {
			return err
		}
	}
	return blockErr
}
