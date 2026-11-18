//go:build cgo

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

/*
#cgo pkg-config: soxr
#include <stdlib.h>
#include <soxr.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

func resampleBuffer(dst PCM16Sample, dstSampleRate int, src PCM16Sample, srcSampleRate int, opts *resampleOptions) PCM16Sample {
	w := newSoxrWriter(NewPCM16BufferWriter(&dst, dstSampleRate), srcSampleRate, opts)
	err := w.WriteSample(src)
	_ = w.Close()
	if err != nil {
		panic(err)
	}
	return dst
}

func newResampleWriter(w WriteCloser[PCM16Sample], sampleRate int, opts *resampleOptions) WriteCloser[PCM16Sample] {
	r := newSoxrWriter(w, sampleRate, opts)
	if opts.Steady {
		r.out.steady = true
		r.lead = soxrLead(r.srcRate, r.dstRate, opts.Channels)
		r.pendingLead = r.lead
	}
	return r
}

func newSoxrWriter(w WriteCloser[PCM16Sample], sampleRate int, opts *resampleOptions) *resampleWriter {
	srcRate := sampleRate
	dstRate := w.SampleRate()
	r := &resampleWriter{
		w:       w,
		srcRate: srcRate,
		dstRate: dstRate,
		out:     frameBuffer{srcRate: srcRate, dstRate: dstRate, ch: opts.Channels},
	}
	var err error
	r.r, err = newSoxr(dstRate, srcRate, opts.Channels, soxrQuality)
	if err != nil {
		panic(err)
	}
	return r
}

const soxrQuality = int(C.SOXR_LQ)

// The hold-back creeps up for about a second of input on low-rate sources. Against 30 s of audio on nine
// common rates, 12,000 calls were never short and at most 2 ms over.
const leadProbeCalls = 12000

// Later peaks ran at most 1 frame higher (11.025k->48k with 1 ms writes, at 38 s).
const leadMargin = 250 * time.Microsecond

var soxrLeads sync.Map // [3]int{src, dst, channels} -> *soxrLeadEntry

// Streams that start together on a new rate pair share one probe.
type soxrLeadEntry struct {
	once sync.Once
	lead int
}

func soxrLead(srcRate, dstRate, channels int) int {
	v, _ := soxrLeads.LoadOrStore([3]int{srcRate, dstRate, channels}, new(soxrLeadEntry))
	e := v.(*soxrLeadEntry)
	e.once.Do(func() { e.lead = probeSoxrLead(srcRate, dstRate, channels) })
	return e.lead
}

// soxr returns output a block at a time, so what it holds back swings by up to about 20 ms within a
// stream. A stream that starts behind by the most it ever holds can emit every write's chunk on time.
func probeSoxrLead(srcRate, dstRate, channels int) int {
	r, err := newSoxr(dstRate, srcRate, channels, soxrQuality)
	if err != nil {
		return 0
	}
	defer r.Close()
	step := max(1, srcRate/leadProbeCalls)
	in := make(PCM16Sample, step*channels)
	var out PCM16Sample
	var most C.double
	for range leadProbeCalls {
		if out, err = r.Resample(out[:0], in); err != nil {
			return 0
		}
		most = max(most, C.soxr_delay(r.ptr))
	}
	// The peak sits just before soxr returns a block, which a step of several frames can step over.
	hidden := float64(step-1) * float64(dstRate) / float64(srcRate)
	return int(math.Ceil(float64(most) + hidden + float64(dstRate)*leadMargin.Seconds()))
}

type resampleWriter struct {
	mu      sync.Mutex
	w       WriteCloser[PCM16Sample]
	r       *soxrResampler
	srcRate int
	dstRate int
	out     frameBuffer
	// Frames of silence a steady stream opens with, and how many are still to go in; see soxrLead.
	lead, pendingLead int
	// soxr takes whole frames, so a write ending mid-frame leaves its tail here for the next write.
	partial PCM16Sample
}

func (w *resampleWriter) String() string {
	return fmt.Sprintf("Resample(%d->%d) -> %s", w.srcRate, w.dstRate, w.w.String())
}

// Output frames of leading silence with WithSteadyResample, and zero without it. The silence goes out with
// the first write, so a writer closed before any write emits none.
func (w *resampleWriter) Lead() int {
	return w.lead
}

func (w *resampleWriter) SampleRate() int {
	return w.srcRate
}

func (w *resampleWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.r.done.Load() {
		return errResamplerClosed
	}
	var err error
	w.out.buf, err = w.r.Resample(w.out.buf, nil)
	_ = w.r.Close()
	return errors.Join(err, w.out.drain(w.w), partialFrameErr(len(w.partial)), w.w.Close())
}

func (w *resampleWriter) WriteSample(data PCM16Sample) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.r.done.Load() {
		return errResamplerClosed
	}
	if len(w.partial) != 0 {
		data = append(slices.Clip(w.partial), data...)
	}
	ch := w.r.channels
	whole := len(data) - len(data)%ch
	w.partial = append(w.partial[:0], data[whole:]...)
	data = data[:whole]
	if len(data) == 0 {
		return nil
	}
	if w.pendingLead > 0 {
		w.out.buf = append(w.out.buf, make(PCM16Sample, w.pendingLead*ch)...)
		w.pendingLead = 0
	}
	var err error
	// Write input to the resampler buffer.
	w.out.buf, err = w.r.Resample(w.out.buf, data)
	if err != nil {
		return err
	}
	// Resampler will likely return a short buffer in the first run. In that case, we emit no samples on the first call.
	// This will cause a one frame delay for each resampler. Flushing the sampler, however will lead to frame
	// discontinuity, and thus - distortions on the frame boundaries.
	w.out.queue(len(data) / ch)
	return w.out.flush(w.w)
}

type soxrResampler struct {
	ptr      C.soxr_t
	srcRate  int
	dstRate  int
	channels int
	maxIn    int
	done     *atomic.Bool
	// soxr_process writes these through pointers; locals would move to the heap on every call.
	read, written C.size_t
}

func soxrErr(e C.soxr_error_t) error {
	if e == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(e))
	estr := C.GoString(e)
	switch estr {
	case "", "0":
		return nil
	}
	return errors.New(estr)
}

func newSoxr(dstRate, srcRate, channels int, quality int) (*soxrResampler, error) {
	ic := C.soxr_io_spec(C.SOXR_INT16_I, C.SOXR_INT16_I)
	qc := C.soxr_quality_spec(C.ulong(quality), 0)
	rc := C.soxr_runtime_spec(1) // 1 thread
	// The default 1024-point minimum DFT holds back about 55 ms at 8k->48k on 20 ms writes;
	// 512 points halves that with the same filter.
	rc.log2_min_dft_size = 9
	var e C.soxr_error_t
	p := C.soxr_create(C.double(srcRate), C.double(dstRate), C.uint(channels), &e, &ic, &qc, &rc)
	err := soxrErr(e)
	if err != nil {
		return nil, err
	}
	// p.seed = 1234567890 // TODO: set seed somehow, otherwise results are random
	// This variable helps avoid double-free on the soxr resampler ptr. See soxrCleanup.
	done := new(atomic.Bool)
	r := &soxrResampler{
		ptr:      p,
		dstRate:  dstRate,
		srcRate:  srcRate,
		channels: channels,
		done:     done,
	}
	runtime.AddCleanup(r, func(p C.soxr_t) {
		soxrCleanup(done, p)
	}, p)
	return r, nil
}

func soxrCleanup(done *atomic.Bool, p C.soxr_t) {
	if done.CompareAndSwap(false, true) {
		C.soxr_delete(p)
	}
}

func (r *soxrResampler) Close() error {
	if r.ptr == nil {
		return nil
	}
	soxrCleanup(r.done, r.ptr)
	r.ptr = nil
	return nil
}

func (r *soxrResampler) Resample(out PCM16Sample, in PCM16Sample) (PCM16Sample, error) {
	if r.ptr == nil || r.done.Load() {
		return out, errResamplerClosed
	}
	// soxr counts interleaved frames, not samples.
	ch := r.channels
	frames := len(in) / ch
	var src C.soxr_in_t
	var dstN int
	switch {
	case len(in) == 0:
		dstN = max(r.maxIn*r.dstRate/r.srcRate, (cap(out)-len(out))/ch, 1024)
	case frames == 0:
		return out, nil
	default:
		r.maxIn = max(r.maxIn, frames)
		src = C.soxr_in_t(unsafe.Pointer(&in[0]))
		// soxr reads at most ceil(olen*srcRate/dstRate) frames per call, so round up to take them all.
		dstN = (frames*r.dstRate + r.srcRate - 1) / r.srcRate
	}
	for {
		out = slices.Grow(out, dstN*ch)
		dst := out[len(out) : len(out)+dstN*ch]
		e := C.soxr_process(r.ptr, src, C.size_t(frames), &r.read, C.soxr_out_t(unsafe.Pointer(&dst[0])), C.size_t(dstN), &r.written)
		if err := soxrErr(e); err != nil {
			return out, err
		}
		out = out[:len(out)+int(r.written)*ch]
		// A flush can hold more output than one call takes, so it repeats until soxr comes up short.
		if src != nil || int(r.written) < dstN {
			return out, nil
		}
	}
}
