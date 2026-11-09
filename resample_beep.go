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
	"errors"
	"fmt"
	"slices"
)

const beepQuality = 3

func resampleBufferBeep(dst PCM16Sample, dstSampleRate int, src PCM16Sample, srcSampleRate int, opts *resampleOptions) PCM16Sample {
	w := newResampleWriterBeep(NewPCM16BufferWriter(&dst, dstSampleRate), srcSampleRate, opts)
	err := w.WriteSample(src)
	_ = w.Close()
	if err != nil {
		panic(err)
	}
	return dst
}

func newResampleWriterBeep(w WriteCloser[PCM16Sample], sampleRate int, opts *resampleOptions) WriteCloser[PCM16Sample] {
	srcRate := sampleRate
	dstRate := w.SampleRate()
	r := &resampleWriterBeep{
		w:       w,
		srcRate: srcRate,
		dstRate: dstRate,
		out:     frameBuffer{srcRate: srcRate, dstRate: dstRate, ch: opts.Channels},
	}
	r.r = beepResample(beepQuality, srcRate, dstRate, opts.Channels, r)
	return r
}

type resampleWriterBeep struct {
	w       WriteCloser[PCM16Sample]
	r       *beepResampler
	inbuf   PCM16Sample
	srcRate int
	dstRate int
	out     frameBuffer
}

func (w *resampleWriterBeep) String() string {
	return fmt.Sprintf("Resample(%d->%d) -> %s", w.srcRate, w.dstRate, w.w.String())
}

func (w *resampleWriterBeep) SampleRate() int {
	return w.srcRate
}

func (w *resampleWriterBeep) Close() error {
	if w.r.srcEnded {
		return errResamplerClosed
	}
	w.r.srcEnded = true
	w.stream(0)
	return errors.Join(w.out.drain(w.w), partialFrameErr(len(w.inbuf)), w.w.Close())
}

func (w *resampleWriterBeep) ReadSample(data PCM16Sample) (int, error) {
	n := copy(data, w.inbuf[:len(w.inbuf)-len(w.inbuf)%w.r.ch])
	w.inbuf = w.inbuf[n:]
	return n, nil
}

func (w *resampleWriterBeep) WriteSample(data PCM16Sample) error {
	if w.r.srcEnded {
		return errResamplerClosed
	}
	// ReadSample only hands out whole frames, so a partial frame stays at the tail of inbuf.
	before := len(w.inbuf) / w.r.ch
	w.inbuf = append(w.inbuf, data...)
	w.stream(w.out.queue(len(w.inbuf)/w.r.ch - before))
	return w.out.flush(w.w)
}

func (w *resampleWriterBeep) stream(chunk int) {
	sz := max(chunk, w.r.ch)
	for {
		buf := slices.Grow(w.out.buf, sz)
		n, _ := w.r.Stream(buf[len(buf) : len(buf)+sz])
		w.out.buf = buf[:len(buf)+n]
		if n < sz {
			return
		}
	}
}
