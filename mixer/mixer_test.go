// Copyright 2023 LiveKit, Inc.
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

package mixer

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	msdk "github.com/livekit/media-sdk"
	"github.com/stretchr/testify/require"
)

const (
	inputBufferMin = DefaultInputBufferFrames/2 + 1
)

func newTestWriter(buf *msdk.PCM16Sample, sampleRate int) msdk.PCM16Writer {
	return &testWriter{
		buf:        buf,
		sampleRate: sampleRate,
	}
}

type testWriter struct {
	buf        *msdk.PCM16Sample
	sampleRate int
}

func (b *testWriter) String() string {
	return fmt.Sprintf("testWriter(%d)", b.sampleRate)
}

func (b *testWriter) SampleRate() int {
	return b.sampleRate
}

func (b *testWriter) Close() error {
	return nil
}

func (b *testWriter) WriteSample(data msdk.PCM16Sample) error {
	*b.buf = data
	return nil
}

func newSyncWriter(sampleRate int, validate bool) *syncWriter {
	mu := &sync.Mutex{}
	mu.Lock()
	return &syncWriter{
		validate:       validate,
		cond:           sync.NewCond(mu),
		sampleRate:     sampleRate,
		expectations:   make([]writerExpectation, 0),
		writtenSamples: make([]msdk.PCM16Sample, 0),
	}
}

type writerExpectation struct {
	sample   msdk.PCM16Sample
	optional bool
}

type syncWriter struct {
	validate       bool
	running        atomic.Bool
	cond           *sync.Cond
	sampleRate     int
	expectations   []writerExpectation
	writtenSamples []msdk.PCM16Sample
}

func (b *syncWriter) Unblock() {
	b.running.Store(true)
	b.cond.Signal()
}

func (b *syncWriter) Expect(sample msdk.PCM16Sample, optional bool) {
	logSample("Expecting", sample)
	b.expectations = append(b.expectations, writerExpectation{sample: sample, optional: optional})
}

func (b *syncWriter) verifySample(sample msdk.PCM16Sample) {
	if !b.validate {
		return
	}
	for len(b.expectations) > 0 {
		exp := b.expectations[0]
		b.expectations = b.expectations[1:]
		if exp.sample[0] != sample[0] && exp.optional {
			continue
		}

		// Skip full verification, we just need to know the start and end of smaple, since the whole range is the same.
		if exp.sample[0] != sample[0] {
			panic(fmt.Sprintf("received sample %d does not match expectation %d", sample[0], exp.sample[0]))
		}
		if exp.sample[0] != sample[len(sample)-1] {
			panic(fmt.Sprintf("received sample %d does not match expectation %d", sample[len(sample)-1], exp.sample[len(exp.sample)-1]))
		}
		return
	}

	panic("no matching expectation found")
}

func (b *syncWriter) String() string {
	return fmt.Sprintf("syncWriter(%d)", b.sampleRate)
}

func (b *syncWriter) SampleRate() int {
	return b.sampleRate
}
func (b *syncWriter) Close() error {
	return nil
}

func (b *syncWriter) WriteSample(data msdk.PCM16Sample) error {
	logSample("Mixed", data)
	if !b.running.Load() {
		b.cond.Wait()
	}
	b.writtenSamples = append(b.writtenSamples, data)
	if b.validate {
		b.verifySample(data)
	}
	return nil
}

func logSample(msg string, sample msdk.PCM16Sample) {
	timefmt := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Printf("%s: %-9s sample %d\n", timefmt, msg, sample[0])
}

func genSample(frameSize int, value int16) msdk.PCM16Sample {
	sample := make(msdk.PCM16Sample, frameSize)
	for i := 0; i < frameSize; i++ {
		sample[i] = value
	}
	return sample
}

func TestBlockingWrites(t *testing.T) {
	frameDur := 20 * time.Millisecond
	frameSize := int(time.Duration(8000) * frameDur / time.Second)
	samples := []msdk.PCM16Sample{
		genSample(frameSize, 1),
		genSample(frameSize, 2),
		genSample(frameSize, 3),
	}

	t.Run("no channel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			writer := newSyncWriter(8000, false)
			mixer, err := NewMixer(writer, 20*time.Millisecond, nil, 1)
			require.NoError(t, err)
			defer mixer.Stop()

			input := mixer.NewInput()
			defer input.Close()

			input.WriteSample(samples[0])
			input.WriteSample(samples[1])
			input.WriteSample(samples[2])

			time.Sleep(61 * time.Millisecond)
			require.Equal(t, 0, len(writer.writtenSamples))
			writer.Unblock()
			time.Sleep(time.Millisecond)
			require.Equal(t, 3, len(writer.writtenSamples))
		})
	})

	t.Run("len 1 channel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			writer := newSyncWriter(8000, false)
			mixer, err := NewMixer(writer, 20*time.Millisecond, nil, 1, WithOutputChannel())
			require.NoError(t, err)
			defer mixer.Stop()

			input := mixer.NewInput()
			defer input.Close()

			input.WriteSample(samples[0])
			input.WriteSample(samples[1])
			input.WriteSample(samples[2])

			time.Sleep(61 * time.Millisecond)
			require.Equal(t, 0, len(writer.writtenSamples))
			writer.Unblock()
			time.Sleep(time.Millisecond)
			require.Equal(t, 3, len(writer.writtenSamples))
		})
	})

	t.Run("len 5 channel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			writer := newSyncWriter(8000, false)
			mixer, err := NewMixer(writer, 20*time.Millisecond, nil, 1, WithOutputChannelSize(5))
			require.NoError(t, err)
			defer mixer.Stop()

			input := mixer.NewInput()
			defer input.Close()

			input.WriteSample(samples[0])
			input.WriteSample(samples[1])
			input.WriteSample(samples[2])

			time.Sleep(61 * time.Millisecond)
			require.Equal(t, 0, len(writer.writtenSamples))
			writer.Unblock()
			time.Sleep(time.Millisecond)
			require.Equal(t, 3, len(writer.writtenSamples))
		})
	})
}

// Produce a steady stream of samples, offset by jitter.
func steadyStream(in *Input, out *syncWriter, frameSize int, frameDur time.Duration, jitter time.Duration, start int, count int) {
	next := time.Now()
	end := start + count
	halfJitter := jitter / 2
	for i := start; i < end; i++ {
		next = next.Add(frameDur) // Clean of jitter
		jitter := time.Duration((rand.Float64() * float64(jitter)) - float64(halfJitter))
		time.Sleep(time.Until(next.Add(jitter)))
		frame := genSample(frameSize, int16(i))
		out.Expect(frame, false)
		logSample("Writing", frame)
		in.WriteSample(frame)

	}
}

// Produce a burst of samples
func burst(in *Input, out *syncWriter, frameMax int, frameSize int, frameDur time.Duration, start int, count int) {
	end := start + count
	zeroFrame := genSample(frameSize, 0)
	for i := start; i < end; i++ {
		out.Expect(zeroFrame, true)
	}
	skip := count - frameMax
	for i := start; i < end; i++ {
		skip--
		if skip >= 0 {
			continue
		}
		out.Expect(genSample(frameSize, int16(i)), skip == -1) // First non-skipped frame may still be overwritten due to jitter.
	}
	time.Sleep((time.Duration(count) * frameDur))
	for i := start; i < end; i++ {
		frame := genSample(frameSize, int16(i))
		logSample("Writing", frame)
		in.WriteSample(frame)
	}
}

func testBurstSize(t *testing.T, sampleRate int, frameDur time.Duration, burstCount int) {
	frameSize := int(time.Duration(sampleRate) * frameDur / time.Second)
	zeroFrame := genSample(frameSize, 0)
	jitter := 3 * time.Millisecond

	fmt.Printf("\nTesting burst size: %d\n", burstCount)
	synctest.Test(t, func(t *testing.T) {
		output := newSyncWriter(sampleRate, true)
		output.Unblock()
		mixer, err := NewMixer(output, frameDur, nil, 1, WithOutputChannel()) // Starts a timer goroutine.
		require.NoError(t, err)
		defer mixer.Stop()
		input := mixer.NewInput()
		defer input.Close()

		for i := 0; i < mixer.inputBufferMin; i++ {
			output.Expect(zeroFrame, true)
		}

		next := 1
		fmt.Printf("steadyStream: %d\n", mixer.inputBufferFrames+1)
		steadyStream(input, output, frameSize, frameDur, jitter, next, mixer.inputBufferFrames+1)
		next += mixer.inputBufferFrames + 1
		fmt.Printf("burst: %d\n", burstCount)
		burst(input, output, mixer.inputBufferFrames, frameSize, frameDur, next, burstCount)
		next += burstCount
		fmt.Printf("steadyStream: %d\n", mixer.inputBufferFrames+1)
		steadyStream(input, output, frameSize, frameDur, jitter, next, mixer.inputBufferFrames+1)
		next += mixer.inputBufferFrames + 1
	})
}

func TestSyncBursts(t *testing.T) {
	sampleRate := 8000
	frameDur := 20 * time.Millisecond
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("burst size %d", i)
		t.Run(name, func(t *testing.T) {
			testBurstSize(t, sampleRate, frameDur, i)
		})
	}
}

type testMixer struct {
	t      testing.TB
	sample msdk.PCM16Sample
	*Mixer
}

func newTestMixer(t testing.TB) *testMixer {
	m := &testMixer{t: t}

	mixer := newMixer(newTestWriter(&m.sample, 8000), 5, nil)
	m.Mixer = mixer
	return m
}

func (m *testMixer) Expect(exp msdk.PCM16Sample, msgAndArgs ...any) {
	m.t.Helper()
	m.mixOnce()
	require.Equal(m.t, exp, m.sample, msgAndArgs...)
}

func WriteSampleN(inp *Input, i int) {
	v := int16(i) * 5
	inp.WriteSample(msdk.PCM16Sample{v + 0, v + 1, v + 2, v + 3, v + 4})
}

func (m *testMixer) ExpectSampleN(i int, msgAndArgs ...any) {
	m.t.Helper()
	m.mixOnce()
	m.CheckSampleN(i, msgAndArgs...)
}

func (m *testMixer) CheckSampleN(i int, msgAndArgs ...any) {
	m.t.Helper()
	v := int16(i) * 5
	require.Equal(m.t, msdk.PCM16Sample{v + 0, v + 1, v + 2, v + 3, v + 4}, m.sample, msgAndArgs...)
}

func TestMixer(t *testing.T) {
	t.Run("no input produces silence", func(t *testing.T) {
		m := newTestMixer(t)
		m.mixOnce()
		m.Expect(msdk.PCM16Sample{0, 0, 0, 0, 0})
	})

	t.Run("one input mixing correctly", func(t *testing.T) {
		m := newTestMixer(t)
		inp := m.NewInput()
		defer inp.Close()
		inp.buffering = false

		WriteSampleN(inp, 1)
		m.ExpectSampleN(1)
	})

	t.Run("two inputs mixing correctly", func(t *testing.T) {
		m := newTestMixer(t)
		one := m.NewInput()
		defer one.Close()
		one.buffering = false
		one.WriteSample([]int16{0xE, 0xD, 0xC, 0xB, 0xA})

		two := m.NewInput()
		defer two.Close()
		two.buffering = false
		two.WriteSample([]int16{0xA, 0xB, 0xC, 0xD, 0xE})

		m.Expect(msdk.PCM16Sample{24, 24, 24, 24, 24})

		one.WriteSample([]int16{0x7FFF, 0x1, -0x7FFF, -0x1, 0x0})
		two.WriteSample([]int16{0x1, 0x7FFF, -0x1, -0x7FFF, 0x0})

		m.Expect(msdk.PCM16Sample{0x7FFF, 0x7FFF, -0x7FFF, -0x7FFF, 0x0})
	})

	t.Run("draining produces silence afterwards", func(t *testing.T) {
		m := newTestMixer(t)
		inp := m.NewInput()
		defer inp.Close()

		for i := 0; i < DefaultInputBufferFrames; i++ {
			inp.WriteSample([]int16{0, 1, 2, 3, 4})
		}

		for i := 0; i < DefaultInputBufferFrames+3; i++ {
			expected := msdk.PCM16Sample{0, 1, 2, 3, 4}
			if i >= DefaultInputBufferFrames {
				expected = msdk.PCM16Sample{0, 0, 0, 0, 0}
			}
			m.Expect(expected, "i=%d", i)
		}
	})

	t.Run("drops frames on overflow", func(t *testing.T) {
		m := newTestMixer(t)
		input := m.NewInput()
		defer input.Close()

		for i := 0; i < DefaultInputBufferFrames+3; i++ {
			input.WriteSample([]int16{0, 1, 2, 3, 4})
		}

		m.mixOnce()
		require.Equal(t, (DefaultInputBufferFrames-1)*5, input.buf.Len())
	})

	t.Run("buffered initially and after starving", func(t *testing.T) {
		m := newTestMixer(t)
		inp := m.NewInput()
		defer inp.Close()

		inp.WriteSample([]int16{10, 11, 12, 13, 14})

		for i := 0; i < inputBufferMin-1; i++ {
			// Mixing produces nothing, because we are buffering the input initially.
			m.Expect(msdk.PCM16Sample{0, 0, 0, 0, 0})
			WriteSampleN(inp, i)
		}

		// Now we should finally receive all our samples, even if no new data is available.
		m.Expect(msdk.PCM16Sample{10, 11, 12, 13, 14})
		for i := 0; i < inputBufferMin-1; i++ {
			m.ExpectSampleN(i)
		}

		// Input is starving, should produce silence again until we buffer enough samples.
		m.Expect(msdk.PCM16Sample{0, 0, 0, 0, 0})
		for i := 0; i < inputBufferMin; i++ {
			m.Expect(msdk.PCM16Sample{0, 0, 0, 0, 0})
			WriteSampleN(inp, i)
		}
		// Data is flowing again after we buffered enough.
		for i := 0; i < inputBufferMin; i++ {
			m.ExpectSampleN(i)
			// Keep writing to see if we can get this data later without gaps.
			WriteSampleN(inp, i*10)
		}
		// Check data that we were writing above.
		for i := 0; i < inputBufferMin; i++ {
			m.ExpectSampleN(i * 10)
		}
	})

	t.Run("catches up after not running for long", func(t *testing.T) {
		step := 20 * time.Millisecond
		m := newTestMixer(t)
		m.tickerDur = step

		inp := m.NewInput()
		defer inp.Close()

		for i := 0; i < DefaultInputBufferFrames; i++ {
			WriteSampleN(inp, i)
		}

		m.mixUpdate()
		require.EqualValues(t, 1, m.mixCnt)
		m.CheckSampleN(0)

		const steps = DefaultInputBufferFrames/2 + 1
		time.Sleep(step*steps + step/2)
		m.mixUpdate()
		require.EqualValues(t, 1+steps, m.mixCnt)
		m.CheckSampleN(steps)
	})
}
