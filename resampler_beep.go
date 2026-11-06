// MIT License
//
// Copyright (c) 2017 Michal Štrba
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package media

// Code below was taken from https://github.com/gopxl/beep/blob/main/resample.go
// and adapted for our use case (interleaved int16 samples).

import (
	"fmt"
	"math"
)

// Resample takes a Streamer which is assumed to stream at the old sample rate and returns a
// Streamer, which streams the data from the original Streamer resampled to the new sample rate.
//
// This is, for example, useful when mixing multiple Streamer with different sample rates, either
// through a beep.Mixer, or through a speaker. Speaker has a constant sample rate. Thus, playing
// Streamer which stream at a different sample rate will lead to a changed speed and pitch of the
// playback.
//
//	sr := beep.SampleRate(48000)
//	speaker.Init(sr, sr.N(time.Second/2))
//	speaker.Play(beep.Resample(3, format.SampleRate, sr, s))
//
// In the example above, the original sample rate of the source is format.SampleRate. We want to play
// it at the speaker's native sample rate and thus we need to resample.
//
// The quality argument specifies the quality of the resampling process. Higher quality implies
// worse performance. Values below 1 or above 64 are invalid and Resample will panic. Here's a table
// for deciding which quality to pick.
//
//	quality | use case
//	--------|---------
//	1       | very high performance, on-the-fly resampling, low quality
//	3-4     | good performance, on-the-fly resampling, good quality
//	6       | higher CPU usage, usually not suitable for on-the-fly resampling, very good quality
//	>6      | even higher CPU usage, for offline resampling, very good quality
//
// Sane quality values are usually below 16. Higher values will consume too much CPU, giving
// negligible quality improvements.
//
// Resample propagates errors from s.
func beepResample(quality int, old, new int, channels int, s Reader[PCM16Sample]) *beepResampler {
	return beepResampleRatio(quality, float64(old)/float64(new), channels, s)
}

// ResampleRatio is same as Resample, except it takes the ratio of the old and the new sample rate,
// specifically, the old sample rate divided by the new sample rate. Aside from correcting the
// sample rate, this can be used to change the speed of the audio. For example, resampling at the
// ratio of 2 and playing at the original sample rate will cause doubled speed in playback.
func beepResampleRatio(quality int, ratio float64, channels int, s Reader[PCM16Sample]) *beepResampler {
	if quality < 1 || 64 < quality {
		panic(fmt.Errorf("resample: invalid quality: %d", quality))
	}
	if math.IsInf(ratio, 0) || math.IsNaN(ratio) {
		panic(fmt.Errorf("resample: invalid ratio: %f", ratio))
	}
	return &beepResampler{
		s:       s,
		ch:      channels,
		ratio:   ratio,
		first:   true,
		buf1:    make(PCM16Sample, 512*channels),
		buf2:    make(PCM16Sample, 512*channels),
		weights: make([]float64, quality*2),
		off:     0,
		pos:     0,
	}
}

// Resampler is a Streamer created by Resample and ResampleRatio functions. It allows dynamic
// changing of the resampling ratio, which can be useful for dynamically changing the speed of
// streaming.
type beepResampler struct {
	s          Reader[PCM16Sample] // the orignal streamer
	ch         int                 // samples per interleaved frame; off, pos and the indexes below count frames
	ratio      float64             // old sample rate / new sample rate
	first      bool                // true when Stream was not called before
	buf1, buf2 PCM16Sample         // buf1 contains previous buf2, new data goes into buf2, buf1 is because interpolation might require old samples
	srcEnded   bool                // set by the owner once s will return no more data
	off        int                 // off is the position of the start of buf2 in the original data
	pos        int                 // pos is the current position in the resampled data
	weights    []float64
}

// Stream streams the original audio resampled according to the current ratio.
// Until srcEnded is set, it stops short rather than read missing input as silence.
func (r *beepResampler) Stream(samples PCM16Sample) (n int, ok bool) {
	// if it's the first time, we need to fill buf2 with initial data, buf1 remains zeroed
	if r.first {
		sn, _ := r.s.ReadSample(r.buf2)
		r.buf2 = r.buf2[:sn]
		r.first = false
	}
	ch := r.ch
	for len(samples) >= ch {
		// calculate the current position in the original data
		j := float64(r.pos) * r.ratio
		lo := int(j) - len(r.weights)/2 + 1
		hi := lo + len(r.weights) - 1
		for hi >= r.end() && r.load() {
		}
		if hi >= r.end() && !r.srcEnded {
			return n, n > 0
		}
		if int(j) >= r.end() {
			return n, n > 0
		}
		for i := range r.weights {
			l := 1.0
			for m := range r.weights {
				if i == m {
					continue
				}
				l *= (j - float64(lo+m)) / float64(i-m)
			}
			r.weights[i] = l
		}
		for c := range ch {
			y := 0.0
			for i, l := range r.weights {
				y += r.at(lo+i, c) * l
			}
			samples[c] = int16(min(max(y*0x7fff, math.MinInt16), math.MaxInt16))
		}
		samples = samples[ch:]
		n += ch
		r.pos++
	}
	return n, true
}

func (r *beepResampler) end() int {
	return r.off + len(r.buf2)/r.ch
}

func (r *beepResampler) at(k, c int) float64 {
	var y int16
	switch {
	case k < r.off:
		y = r.buf1[len(r.buf1)+(k-r.off)*r.ch+c]
	case k < r.end():
		y = r.buf2[(k-r.off)*r.ch+c]
	}
	return float64(y) / 0x7fff
}

// Swapping in a short buf2 would leave buf1 too few frames of history, so buf2 fills to capacity
// first.
func (r *beepResampler) load() bool {
	if len(r.buf2) < cap(r.buf2) {
		sn, _ := r.s.ReadSample(r.buf2[len(r.buf2):cap(r.buf2)])
		r.buf2 = r.buf2[:len(r.buf2)+sn]
		return sn > 0
	}
	sn, _ := r.s.ReadSample(r.buf1[:cap(r.buf1)])
	if sn == 0 {
		return false
	}
	r.off += len(r.buf2) / r.ch
	r.buf1, r.buf2 = r.buf2, r.buf1[:sn]
	return true
}

// Ratio returns the current resampling ratio.
func (r *beepResampler) Ratio() float64 {
	return r.ratio
}

// SetRatio sets the resampling ratio. This does not cause any glitches in the stream.
func (r *beepResampler) SetRatio(ratio float64) {
	if math.IsInf(ratio, 0) || math.IsNaN(ratio) {
		panic(fmt.Errorf("resample: invalid ratio: %f", ratio))
	}
	r.pos = int(float64(r.pos) * r.ratio / ratio)
	r.ratio = ratio
}
