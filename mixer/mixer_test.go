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

func newTestWriter2(sampleRate int, strict bool) *testWriter2 {
	return &testWriter2{
		realizations: make([]msdk.PCM16Sample, 0),
		expectations: make([]msdk.PCM16Sample, 0),
		sampleRate:   sampleRate,
		strict:       strict,
	}
}

type testWriter2 struct {
	realizations []msdk.PCM16Sample
	expectations []msdk.PCM16Sample
	sampleRate   int
	strict       bool // If true, will panic if we're receiving a smaple without a queued expectation
	skippedZero  bool // Whether the first zero frame has been ignored. Needed due to random jitter.
}

func (b *testWriter2) String() string {
	return fmt.Sprintf("testWriter2(%d)", b.sampleRate)
}

func (b *testWriter2) SampleRate() int {
	return b.sampleRate
}

func (b *testWriter2) Close() error {
	return nil
}

func logSample(msg string, sample msdk.PCM16Sample) {
	timefmt := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Printf("%s: %-9s sample %d\n", timefmt, msg, sample[0])
}

func (b *testWriter2) Expect(exp msdk.PCM16Sample) {
	logSample("Expecting", exp)
	b.expectations = append(b.expectations, exp)
}

func (b *testWriter2) WriteSample(data msdk.PCM16Sample) error {
	logSample("Received", data)
	if len(b.expectations) > 0 {
		expected := b.expectations[0]
		b.expectations = b.expectations[1:]

		if !b.skippedZero && expected[0] == 0 && data[0] != 0 {
			expected = b.expectations[0]
			b.expectations = b.expectations[1:]
			b.skippedZero = true
		}

		if len(data) != len(expected) {
			panic(fmt.Sprintf("received sample length does not match expectation: %d != %d", len(data), len(expected)))
		}
		for i := range data {

			if data[i] != expected[i] {
				panic(fmt.Sprintf("received sample %d does not match expectation %d", data[i], expected[i]))
			}
		}
		return nil
	}
	if b.strict {
		panic("received sample without expectation")
	}
	b.realizations = append(b.realizations, data)
	return nil
}

func (b *testWriter2) ReadFrame() msdk.PCM16Sample {
	if len(b.realizations) == 0 {
		return nil
	}
	frame := b.realizations[0]
	b.realizations = b.realizations[1:]
	return frame
}

func genSample(frameSize int, value int16) msdk.PCM16Sample {
	sample := make(msdk.PCM16Sample, frameSize)
	for i := 0; i < frameSize; i++ {
		sample[i] = value
	}
	return sample
}

// Produce a steady stream of samples, offset by jitter.
func steadyStream(in *Input, out *testWriter2, frameSize int, frameDur time.Duration, jitter time.Duration, start int, count int) {
	next := time.Now()
	end := start + count
	halfJitter := jitter / 2
	for i := start; i < end; i++ {
		next = next.Add(frameDur) // Clean of jitter
		jitter := time.Duration((rand.Float64() * float64(jitter)) - float64(halfJitter))
		time.Sleep(time.Until(next.Add(jitter)))
		frame := genSample(frameSize, int16(i))
		out.Expect(frame)
		logSample("Writing", frame)
		in.WriteSample(frame)

	}
}

// Produce a burst of samples
func burst(in *Input, out *testWriter2, frameMax int, frameSize int, frameDur time.Duration, start int, count int) {
	end := start + count
	// We may be asked to produce more samples than we can fit in the buffer, so we need to skip some expectations.
	skipExpect := 0
	zeroFrame := genSample(frameSize, 0)
	if count > frameMax {
		skipExpect = count - frameMax
	}
	for i := start; i < end; i++ {
		if skipExpect > 0 {
			skipExpect--
			out.Expect(zeroFrame)
		} else {
			out.Expect(genSample(frameSize, int16(i)))
		}
	}
	time.Sleep(time.Duration(count) * frameDur)
	for i := start; i < end; i++ {
		frame := genSample(frameSize, int16(i))
		logSample("Writing", frame)
		in.WriteSample(frame)
	}
}

func testBurstSize(t *testing.T, sampleRate int, frameDur time.Duration, burstCount int) {
	frameSize := int(time.Duration(sampleRate) * frameDur / time.Second)
	zeroFrame := genSample(frameSize, 0)
	output := newTestWriter2(sampleRate, true)
	stats := Stats{}
	jitter := 3 * time.Millisecond

	fmt.Printf("\nTesting burst size: %d\n", burstCount)
	synctest.Test(t, func(t *testing.T) {
		defer func() {
			fmt.Printf("stats: %+v\n", stats)
		}()

		mixer, err := NewMixer(output, frameDur, &stats, 1) // Starts a timer goroutine.
		require.NoError(t, err)
		defer mixer.Stop()
		input := mixer.NewInput()
		defer input.Close()

		for i := 0; i < mixer.inputBufferMin; i++ {
			output.Expect(zeroFrame)
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

	mixer, err := newMixer(newTestWriter(&m.sample, 8000), 5, nil)
	require.NoError(t, err)
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
