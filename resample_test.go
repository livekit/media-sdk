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

package media_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/res"
	"github.com/livekit/media-sdk/res/testdata"
	"github.com/livekit/media-sdk/webm"
)

type paceWriter struct {
	w       media.PCM16Writer
	mu      sync.Mutex
	cur     int
	frameT  []int
	frameSz []int
	samples int
}

func (p *paceWriter) Set(i int) {
	p.mu.Lock()
	p.cur = i
	p.mu.Unlock()
}

func (p *paceWriter) String() string {
	return fmt.Sprintf("paceWriter(%d) -> %s", p.cur, p.w.String())
}

func (p *paceWriter) SampleRate() int {
	return p.w.SampleRate()
}

func (p *paceWriter) WriteSample(sample media.PCM16Sample) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frameT = append(p.frameT, p.cur)
	p.frameSz = append(p.frameSz, len(sample))
	p.samples += len(sample)
	return p.w.WriteSample(sample)
}

func (p *paceWriter) Close() error {
	return nil
}

func TestResample(t *testing.T) {
	const (
		srcFile     = "resample.src.s16le"
		srcFileWebm = "resample.src.mka"
		srcRate     = res.SampleRate
	)
	srcFrames := res.ReadOggAudioFile(testdata.TestAudioOgg, res.SampleRate, 1)
	gotSrc := writePCM16s(t, srcFile, srcFrames)
	writePCM16sWebm(t, srcFileWebm, srcRate, srcFrames)
	require.True(t, gotSrc == "c774af95" || gotSrc == "b04a6a1f")

	for _, c := range []struct {
		Rate   int
		Skip   int
		Buffer int
		Bumps  []int
	}{
		{
			Rate: 16000, Skip: 1,
			Buffer: 1,
		},
		{
			Rate: 8000, Skip: 2,
			Buffer: 3,
			// TODO: figure out why resampler delays that specific frame; silence?
			Bumps: []int{211},
		},
	} {
		t.Run(strconv.Itoa(c.Rate), func(t *testing.T) {
			resPref := fmt.Sprintf("resample.out.%d", c.Rate)
			resFile := resPref + ".s16le"
			resFileWebm := resPref + ".mka"

			var dstFrames []media.PCM16Sample
			dst := media.NewPCM16FrameWriter(&dstFrames, c.Rate)
			pw := &paceWriter{w: dst}
			r := media.ResampleWriter(pw, srcRate)
			defer r.Close()
			totalSamples := 0
			for i, src := range srcFrames {
				pw.Set(i)
				err := r.WriteSample(src)
				require.NoError(t, err)
				totalSamples += len(src)
			}
			r.Close()
			gotDst := writePCM16s(t, resFile, dstFrames)
			// only change if you validated the quality!
			// require.Equal(t, "???", gotDst) // TODO: resampler is numerically unstable
			_ = gotDst

			writePCM16sWebm(t, resFileWebm, c.Rate, dstFrames)

			expSamples := totalSamples / (srcRate / c.Rate)
			require.Equal(t, expSamples, pw.samples)
			require.Equal(t, len(srcFrames), len(pw.frameT))
			// Tick at which the first frame was received.
			skipped := pw.frameT[0]
			require.Equal(t, c.Skip, skipped)
			corr := 0
			for i, num := range pw.frameT {
				if i >= len(pw.frameT)-c.Buffer {
					break
				}
				exp := skipped + i + corr
				for _, b := range c.Bumps {
					if b == exp {
						corr++
						exp++
						break
					}
				}
				if exp != num {
					corr = num - (skipped + i)
					t.Errorf("skipped frame: exp %d, got %d\n%v", exp, num, pw.frameT)
				}
			}
		})
	}

}

const (
	leftTone  = 440.0
	rightTone = 660.0
	// Fails a resampler that spins instead of hanging the whole test run.
	hangGuard = 10 * time.Second
)

func TestResampleStereoKeepsChannels(t *testing.T) {
	const frames = media.DefFramesPerSec
	for _, rates := range [][2]int{{48000, 16000}, {16000, 48000}} {
		srcRate, dstRate := rates[0], rates[1]
		frameLen := srcRate / media.DefFramesPerSec
		src := stereoTone(srcRate, frames*frameLen)
		for _, predictable := range []bool{false, true} {
			opts := []media.ResampleOption{media.WithResampleChannels(2), media.WithPredictableResample(predictable)}
			run := map[string]func() media.PCM16Sample{
				"writer": func() media.PCM16Sample {
					var out media.PCM16Sample
					w := media.ResampleWriter(media.NewPCM16BufferWriter(&out, dstRate), srcRate, opts...)
					for f := range frames {
						require.NoError(t, w.WriteSample(src[f*frameLen*2:(f+1)*frameLen*2]))
					}
					require.NoError(t, w.Close())
					return out
				},
				"buffer": func() media.PCM16Sample {
					return media.Resample(nil, dstRate, src, srcRate, opts...)
				},
			}
			for api, resample := range run {
				t.Run(fmt.Sprintf("%d-%d/predictable=%v/%s", srcRate, dstRate, predictable, api), func(t *testing.T) {
					requireChannelsKept(t, resample(), dstRate)
				})
			}
		}
	}
}

func TestResampleStereoPartialFrame(t *testing.T) {
	const (
		// Upsampling by a whole factor, so no write leaves an output remainder.
		srcRate, dstRate = 16000, 48000
		// Half a 20 ms stereo frame plus one sample, so every other write ends mid-frame.
		oddChunk = srcRate/media.DefFramesPerSec + 1
	)
	src := stereoTone(srcRate, srcRate)
	for _, predictable := range []bool{false, true} {
		opts := []media.ResampleOption{media.WithResampleChannels(2), media.WithPredictableResample(predictable)}
		t.Run(fmt.Sprintf("predictable=%v/buffer", predictable), func(t *testing.T) {
			for _, n := range []int{1, 3} {
				var out media.PCM16Sample
				requireReturns(t, func() { out = media.Resample(nil, dstRate, make(media.PCM16Sample, n), srcRate, opts...) })
				require.Zero(t, len(out)%2)
			}
			var out media.PCM16Sample
			requireReturns(t, func() { out = media.Resample(nil, dstRate, src[:len(src)-1], srcRate, opts...) })
			requireChannelsKept(t, out, dstRate)
		})
		t.Run(fmt.Sprintf("predictable=%v/writer", predictable), func(t *testing.T) {
			var (
				out media.PCM16Sample
				err error
			)
			requireReturns(t, func() {
				w := media.ResampleWriter(media.NewPCM16BufferWriter(&out, dstRate), srcRate, opts...)
				for chunk := range slices.Chunk(src, oddChunk) {
					if err = w.WriteSample(chunk); err != nil {
						return
					}
				}
				err = w.Close()
			})
			require.NoError(t, err)
			requireChannelsKept(t, out, dstRate)
		})
	}
}

func TestResampleBeepShortWrites(t *testing.T) {
	for _, ch := range []int{1, 2} {
		for _, rates := range [][2]int{{48000, 16000}, {16000, 48000}} {
			srcRate, dstRate := rates[0], rates[1]
			t.Run(fmt.Sprintf("channels=%d/%d-%d", ch, srcRate, dstRate), func(t *testing.T) {
				opts := []media.ResampleOption{media.WithResampleChannels(ch), media.WithPredictableResample(true)}
				var out media.PCM16Sample
				w := media.ResampleWriter(media.NewPCM16BufferWriter(&out, dstRate), srcRate, opts...)
				require.NoError(t, w.WriteSample(make(media.PCM16Sample, ch)))
				require.NoError(t, w.WriteSample(make(media.PCM16Sample, srcRate/media.DefFramesPerSec*ch)))
				require.NoError(t, w.Close())
			})
		}
	}
}

func TestResampleStereoChunksHoldWholeFrames(t *testing.T) {
	// An odd 441 frames at 2:1 leaves a fractional output frame on every write.
	const srcRate, dstRate, frames = 16000, 8000, 441
	src := stereoTone(srcRate, 20*frames)
	for _, predictable := range []bool{false, true} {
		t.Run(fmt.Sprintf("predictable=%v", predictable), func(t *testing.T) {
			var chunks []media.PCM16Sample
			w := media.ResampleWriter(media.NewPCM16FrameWriter(&chunks, dstRate), srcRate,
				media.WithResampleChannels(2), media.WithPredictableResample(predictable))
			for chunk := range slices.Chunk(src, 2*frames) {
				require.NoError(t, w.WriteSample(chunk))
			}
			require.NoError(t, w.Close())
			require.NotEmpty(t, chunks)
			for i, c := range chunks {
				require.Zero(t, len(c)%2, "chunk %d splits a stereo frame", i)
			}
		})
	}
}

// Two frames at 3:1 resample to zero output frames, which takes soxr's fallback output size.
func TestResampleStereoTinyWrite(t *testing.T) {
	var out media.PCM16Sample
	w := media.ResampleWriter(media.NewPCM16BufferWriter(&out, 16000), 48000, media.WithResampleChannels(2))
	require.NoError(t, w.WriteSample(make(media.PCM16Sample, 4)))
	require.NoError(t, w.Close())
}

func TestResampleOptionsKeepDefaults(t *testing.T) {
	old := media.DefaultResampleOptions
	t.Cleanup(func() { media.DefaultResampleOptions = old })
	media.DefaultResampleOptions = []media.ResampleOption{media.WithPredictableResample(true)}
	newWriter := func(opts ...media.ResampleOption) media.PCM16Writer {
		return media.ResampleWriter(media.NewPCM16BufferWriter(new(media.PCM16Sample), 16000), 48000, opts...)
	}
	require.IsType(t, newWriter(), newWriter(media.WithResampleChannels(2)), "the channels option dropped the defaults")
}

func TestResampleRejectsChannelsBelowOne(t *testing.T) {
	for _, ch := range []int{0, -1} {
		require.PanicsWithError(t, fmt.Sprintf("resample: invalid channel count: %d", ch), func() {
			media.WithResampleChannels(ch)
		})
	}
}

func stereoTone(rate, frames int) media.PCM16Sample {
	src := make(media.PCM16Sample, 0, 2*frames)
	for i := range frames {
		src = append(src, toneSample(leftTone, rate, i), toneSample(rightTone, rate, i))
	}
	return src
}

func requireChannelsKept(t *testing.T, out media.PCM16Sample, rate int) {
	t.Helper()
	require.Zero(t, len(out)%2)
	var l, r []float64
	for i := 0; i < len(out); i += 2 {
		l = append(l, float64(out[i]))
		r = append(r, float64(out[i+1]))
	}
	require.GreaterOrEqual(t, toneCorr(l, leftTone, rate), 0.99, "left")
	require.GreaterOrEqual(t, toneCorr(r, rightTone, rate), 0.99, "right")
	require.Less(t, toneCorr(l, rightTone, rate), 0.05, "right leaked into left")
	require.Less(t, toneCorr(r, leftTone, rate), 0.05, "left leaked into right")
}

func requireReturns(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(hangGuard):
		t.Fatal("resampler did not return")
	}
}

func toneSample(freq float64, rate, i int) int16 {
	return int16(0.5 * math.MaxInt16 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
}

// Phase-independent, so the resampler's delay needs no lag search.
func toneCorr(x []float64, freq float64, rate int) float64 {
	x = x[rate/media.DefFramesPerSec:]
	var s, c, xx float64
	for i, v := range x {
		ph := 2 * math.Pi * freq * float64(i) / float64(rate)
		s += v * math.Sin(ph)
		c += v * math.Cos(ph)
		xx += v * v
	}
	return math.Sqrt((s*s + c*c) / (xx * float64(len(x)) / 2))
}

func removeAfterTest(t testing.TB, path string) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			return
		}
		_ = os.Remove(path)
	})
}

func writePCM16s(t testing.TB, path string, buf []media.PCM16Sample) string {
	var out []byte
	for _, frame := range buf {
		for _, v := range frame {
			var b [2]byte
			binary.LittleEndian.PutUint16(b[:], uint16(v))
			out = append(out, b[:]...)
		}
	}
	err := os.WriteFile(path, out, 0644)
	require.NoError(t, err)
	removeAfterTest(t, path)
	h := sha256.Sum256(out)
	return hex.EncodeToString(h[:])[:8]
}

func writePCM16sWebm(t testing.TB, path string, rate int, buf []media.PCM16Sample) {
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	removeAfterTest(t, path)

	w := webm.NewPCM16Writer(f, rate, 1, media.DefFrameDur)
	defer w.Close()

	for _, frame := range buf {
		err = w.WriteSample(frame)
		require.NoError(t, err)
	}
}

func memstats(pid int) int64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		panic(err)
	}
	fields := strings.Fields(string(data))
	v, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		panic(err)
	}
	return v
}

func TestResampleLeak(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only linux supported for this test")
	}
	pid := os.Getpid()

	const (
		srcFile     = "resample.src.s16le"
		srcFileWebm = "resample.src.mka"
		srcRate     = res.SampleRate
	)
	srcFrames := res.ReadOggAudioFile(testdata.TestAudioOgg, res.SampleRate, 1)
	gotSrc := writePCM16s(t, srcFile, srcFrames)
	writePCM16sWebm(t, srcFileWebm, srcRate, srcFrames)
	require.True(t, gotSrc == "c774af95" || gotSrc == "b04a6a1f")

	const (
		runs           = 300
		pagesThreshold = 100
	)
	startMem := memstats(pid)
	captureStats := func(run int) {
		if t.Failed() {
			return
		}
		runtime.GC()

		endMem := memstats(pid)
		diff := endMem - startMem
		if diff > pagesThreshold {
			t.Fatalf("resampler memory leak detected (%d Kb / %d runs)", diff, run)
		}
	}
	defer captureStats(runs)

	for run := range runs {
		func() {
			defer captureStats(run + 1)
			var dstFrames []media.PCM16Sample
			dst := media.NewPCM16FrameWriter(&dstFrames, 16000)
			r := media.ResampleWriter(dst, srcRate)
			// This test the cleanup function, so intentionally avoid Close.
			//defer r.Close()
			totalSamples := 0
			for _, src := range srcFrames {
				err := r.WriteSample(src)
				require.NoError(t, err)
				totalSamples += len(src)
			}
		}()
	}
}
