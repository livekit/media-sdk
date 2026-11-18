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
	steady               bool
	frame                int // the latest chunk's size, for splitting what is left at close
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
		b.frame = sz
	}
	return sz
}

func (b *frameBuffer) flush(w PCM16Writer) error {
	if b.steady {
		return b.flushOne(w)
	}
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

// At most one chunk per write. Catching up after a short write would leave no margin for the next one.
func (b *frameBuffer) flushOne(w PCM16Writer) error {
	if len(b.chunks) == 0 || len(b.buf) < b.chunks[0] {
		return nil
	}
	sz := b.chunks[0]
	err := w.WriteSample(b.buf[:sz])
	b.buf = b.buf[:copy(b.buf, b.buf[sz:])]
	b.chunks = b.chunks[:copy(b.chunks, b.chunks[1:])]
	return err
}

// soxr rounds its total to nearest, so the last chunk absorbs output the queue did not account for.
// With no chunk left, as when beep rounds up, the remainder goes out alone.
func (b *frameBuffer) drain(w PCM16Writer) error {
	if b.steady {
		return b.drainWhole(w)
	}
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

// Every write is a whole chunk, since encoders take fixed frames (G.722 panics on an odd count). A lead
// can leave more than a chunk at close, and the last piece is filled out with silence.
func (b *frameBuffer) drainWhole(w PCM16Writer) error {
	var last error
	rest := b.buf
	for len(rest) > 0 {
		sz := b.frame
		if len(b.chunks) > 0 {
			sz = b.chunks[0]
			b.chunks = b.chunks[1:]
		}
		if sz <= 0 {
			sz = len(rest)
		}
		if len(rest) < sz {
			rest = append(rest, make(PCM16Sample, sz-len(rest))...)
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

// Makes every write emit exactly its own chunk, with none skipped or doubled. The output starts with
// silence covering the most soxr can hold back (the writer's Lead, in output frames), and the last chunk
// at close is filled out with silence.
func WithSteadyResample() ResampleOption {
	return func(opts *resampleOptions) {
		opts.Steady = true
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
	Steady      bool
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
