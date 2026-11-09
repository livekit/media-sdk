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
	"os"
	"sync/atomic"
)

var (
	resampleID         atomic.Uint32
	resampleDumpToFile = os.Getenv("LK_DUMP_RESAMPLE") == "true"
)

var DefaultResampleOptions []ResampleOption

// Resample the source sample into the destination sample rate.
// It appends resulting samples to dst and returns the result.
func Resample(dst PCM16Sample, dstSampleRate int, src PCM16Sample, srcSampleRate int, opts ...ResampleOption) PCM16Sample {
	if dstSampleRate == srcSampleRate {
		return append(dst, src...)
	}
	opt := newResampleOptions(opts)
	if opt.Predictable {
		return resampleBufferBeep(dst, dstSampleRate, src, srcSampleRate, &opt)
	}
	return resampleBuffer(dst, dstSampleRate, src, srcSampleRate, &opt)
}

func partialFrameErr(samples int) error {
	if samples == 0 {
		return nil
	}
	return fmt.Errorf("resample: input ended mid-frame, dropping %d samples", samples)
}

var errResamplerClosed = errors.New("resampler is closed")

// One chunk per write, so resampling keeps the caller's framing. Chunk ends come from the running
// input total, so rounding never piles up across writes.
type frameBuffer struct {
	srcRate, dstRate, ch int
	buf                  PCM16Sample
	chunks               []int
	inFrames             int64
	outEnd               int64
}

// Zero means the write's output joins the next chunk.
func (b *frameBuffer) queue(frames int) int {
	b.inFrames += int64(frames)
	end := b.inFrames * int64(b.dstRate) / int64(b.srcRate) * int64(b.ch)
	sz := int(end - b.outEnd)
	b.outEnd = end
	if sz > 0 {
		b.chunks = append(b.chunks, sz)
	}
	return sz
}

func (b *frameBuffer) flush(w PCM16Writer) error {
	var last error
	rest, n := b.buf, 0
	for n < len(b.chunks) && len(rest) >= b.chunks[n] {
		if err := w.WriteSample(rest[:b.chunks[n]]); err != nil {
			last = err
		}
		rest = rest[b.chunks[n]:]
		n++
	}
	b.buf = b.buf[:copy(b.buf, rest)]
	b.chunks = b.chunks[:copy(b.chunks, b.chunks[n:])]
	return last
}

// soxr rounds its total to nearest, so the last chunk absorbs output the queue did not account for.
// With no chunk left, as when beep rounds up, the remainder goes out alone.
func (b *frameBuffer) drain(w PCM16Writer) error {
	var last error
	rest := b.buf
	for len(rest) > 0 {
		sz := len(rest)
		if len(b.chunks) > 1 {
			sz = min(sz, b.chunks[0])
		}
		if len(b.chunks) > 0 {
			b.chunks = b.chunks[1:]
		}
		if err := w.WriteSample(rest[:sz]); err != nil {
			last = err
		}
		rest = rest[sz:]
	}
	b.buf, b.chunks = b.buf[:0], nil
	return last
}

type ResampleOption func(opts *resampleOptions)

func newResampleOptions(opts []ResampleOption) resampleOptions {
	opt := resampleOptions{Channels: 1}
	for _, o := range DefaultResampleOptions {
		o(&opt)
	}
	for _, o := range opts {
		o(&opt)
	}
	return opt
}

// WithResampleChannels sets how many channels the samples interleave. The default is mono.
func WithResampleChannels(channels int) ResampleOption {
	if channels < 1 {
		panic(fmt.Errorf("resample: invalid channel count: %d", channels))
	}
	return func(opts *resampleOptions) {
		opts.Channels = channels
	}
}

func WithPredictableResample(enable bool) ResampleOption {
	return func(opts *resampleOptions) {
		opts.Predictable = enable
	}
}

func WithResampleDump(inputName, outputName string) ResampleOption {
	return func(opts *resampleOptions) {
		opts.DumpInput = inputName
		opts.DumpOutput = outputName
	}
}

type resampleOptions struct {
	Channels    int
	Predictable bool
	DumpInput   string
	DumpOutput  string
}

// ResampleWriter returns a new writer that expects samples of a given sample rate
// and resamples then for the destination writer.
func ResampleWriter(w PCM16Writer, sampleRate int, opts ...ResampleOption) (w2 PCM16Writer) {
	srcRate := sampleRate
	dstRate := w.SampleRate()
	if dstRate == srcRate {
		return w
	}
	opt := newResampleOptions(opts)

	if resampleDumpToFile {
		id := resampleID.Add(1)
		pref := fmt.Sprintf("sip_resample_%d", id)
		opt.DumpInput = pref + "_in"
		opt.DumpOutput = pref + "_out"
	}
	if file := opt.DumpOutput; file != "" {
		w = DumpWriterPCM16(file, w)
	}
	if file := opt.DumpInput; file != "" {
		defer func() {
			w2 = DumpWriterPCM16(file, w2)
		}()
	}
	if opt.Predictable {
		return newResampleWriterBeep(w, sampleRate, &opt)
	}
	return newResampleWriter(w, sampleRate, &opt)
}
