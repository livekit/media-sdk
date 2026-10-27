// Copyright 2025 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package jitter

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

const testBufferLatency = 800 * time.Millisecond

func chanFunc(t testing.TB, out chan<- []ExtPacket) PacketFunc {
	return func(packets []ExtPacket) {
		select {
		case out <- packets:
		default:
			t.Error("buffer is full")
		}
	}
}

func TestJitterBuffer(t *testing.T) {
	out := make(chan []ExtPacket, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out))
	s := newTestStream()

	i := 0
	for ; i < 100; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 1)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  100,
		PacketsLost:    0,
		PacketsDropped: 0,
		PacketsPopped:  100,
		SamplesPopped:  100,
	})
}

func TestSamples(t *testing.T) {
	out := make(chan []ExtPacket, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out))
	s := newTestStream()

	i := 0
	for ; i < 50; i++ {
		b.Push(s.gen(true, false))
		checkSample(t, out, 0)

		b.Push(s.gen(false, true))
		checkSample(t, out, 2)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  100,
		PacketsLost:    0,
		PacketsDropped: 0,
		PacketsPopped:  100,
		SamplesPopped:  50,
	})
}

func TestJitter(t *testing.T) {
	out := make(chan []ExtPacket, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out))
	s := newTestStream()

	i := 0
	for ; i < 17; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 1)
	}

	ooo := []*rtp.Packet{
		s.gen(true, true),
		s.gen(true, true),
		s.gen(true, true),
	}
	b.Push(ooo[1])
	b.Push(ooo[2])
	checkSample(t, out, 0)

	b.Push(ooo[0])
	checkSample(t, out, 1)
	checkSample(t, out, 1)
	checkSample(t, out, 1)

	checkStats(t, b, &BufferStats{
		PacketsPushed:  20,
		PacketsLost:    0,
		PacketsDropped: 0,
		PacketsPopped:  20,
		SamplesPopped:  20,
		// only ooo[0] actually arrived out of sequence; ooo[2] followed ooo[1]
		PacketsReordered: 1,
	})
}

func TestDiscontinuity(t *testing.T) {
	out := make(chan []ExtPacket, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out))
	s := newTestStream()

	i := 0
	for ; i < 50; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 1)
	}
	s.discont()
	for ; i < 100; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 1)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  100,
		PacketsLost:    0,
		PacketsDropped: 0,
		PacketsPopped:  100,
		SamplesPopped:  100,
	})
}

func TestLostPackets(t *testing.T) {
	out := make(chan []ExtPacket, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out))
	s := newTestStream()

	i := 0
	for ; i < 10; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 1)
	}

	// packet loss
	_ = s.gen(true, true)

	for ; i < 20; i++ {
		b.Push(s.gen(true, true))
		checkSample(t, out, 0)
	}

	// latency
	time.Sleep(time.Second)
	for range 10 {
		checkSample(t, out, 1)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  20,
		PacketsLost:    1,
		PacketsDropped: 0,
		PacketsPopped:  20,
		SamplesPopped:  20,
	})
}

func TestDroppedPackets(t *testing.T) {
	out := make(chan []ExtPacket, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out))
	s := newTestStream()

	i := 0
	for ; i < 10; i++ {
		b.Push(s.gen(true, false))
		b.Push(s.gen(false, true))
		checkSample(t, out, 2)
	}

	// packet loss - missing head
	_ = s.gen(true, false)
	b.Push(s.gen(false, true))
	checkSample(t, out, 0)

	for ; i < 20; i++ {
		b.Push(s.gen(true, false))
		b.Push(s.gen(false, true))
		checkSample(t, out, 0)
	}

	time.Sleep(time.Millisecond * 500)

	// packet loss - missing tail
	b.Push(s.gen(true, false))
	_ = s.gen(false, true)
	checkSample(t, out, 0)

	for ; i < 30; i++ {
		b.Push(s.gen(true, false))
		b.Push(s.gen(false, true))
		checkSample(t, out, 0)
	}

	time.Sleep(time.Millisecond * 500)

	// first incomplete sample expired
	for range 10 {
		checkSample(t, out, 2)
	}
	checkSample(t, out, 0)

	time.Sleep(time.Millisecond * 500)

	// second incomplete sample expired
	for range 10 {
		checkSample(t, out, 2)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  62,
		PacketsLost:    2,
		PacketsDropped: 2,
		PacketsPopped:  60,
		SamplesPopped:  30,
	})
}

func TestFlushDropsIncomplete(t *testing.T) {
	out := make(chan []ExtPacket, 10)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out))
	s := newTestStream()

	// incomplete sample at the head blocks later complete samples
	b.Push(s.gen(true, false))
	_ = s.gen(true, true) // simulate lost packet (seq gap), keeps head incomplete
	b.Push(s.gen(true, false))
	b.Push(s.gen(false, true))
	checkSample(t, out, 0)

	b.Flush()

	checkSample(t, out, 2)
	checkStats(t, b, &BufferStats{
		PacketsPushed:  3,
		PacketsLost:    0,
		PacketsDropped: 1,
		PacketsPopped:  2,
		SamplesPopped:  1,
	})
}

func TestFlushReportsLoss(t *testing.T) {
	out := make(chan []ExtPacket, 10)
	losses := 0
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out), WithPacketLossHandler(func(_, _ uint64) {
		losses++
	}))
	s := newTestStream()

	// first sample passes through normally
	b.Push(s.gen(true, false))
	b.Push(s.gen(false, true))
	checkSample(t, out, 2)

	// simulate a missing packet between samples
	_ = s.gen(true, true)

	// next sample should be released on flush and counted as loss
	b.Push(s.gen(true, false))
	b.Push(s.gen(false, true))
	checkSample(t, out, 0)

	b.Flush()

	checkSample(t, out, 2)
	require.Equal(t, 1, losses)
	checkStats(t, b, &BufferStats{
		PacketsPushed:  4,
		PacketsLost:    1,
		PacketsDropped: 0,
		PacketsPopped:  4,
		SamplesPopped:  2,
	})
}

// An out-of-order packet put back in sequence must be counted, and reported.
func TestPacketsReordered(t *testing.T) {
	out := make(chan []ExtPacket, 10)
	notified := 0
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out),
		WithStatsHandler(func(*BufferStats) { notified++ }))
	defer b.Close()
	s := &stream{ssrc: 1, seq: 100}

	b.Push(s.gen(true, true)) // 100
	checkSample(t, out, 1)
	p101 := s.gen(true, true) // hold 101 back
	b.Push(s.gen(true, true)) // 102 arrives first, waits
	checkSample(t, out, 0)
	b.Push(p101) // 101 arrives late and is put back in sequence
	checkSample(t, out, 1)
	checkSample(t, out, 1)

	checkStats(t, b, &BufferStats{
		PacketsPushed:    3,
		PacketsPopped:    3,
		SamplesPopped:    3,
		PacketsReordered: 1,
	})
	require.Equal(t, 1, notified, "stats handler should report the reorder")
}

// lineTest pushes packets on one SSRC with arrival times, as an audio sender
// would, and counts delivery per source. The source is carried in the RTP
// timestamp.
type lineTest struct {
	b    *Buffer
	t0   time.Time
	n    int // next slot for run
	got  map[uint32]int
	seqs []uint16
}

const (
	srcA = 'A'
	srcB = 'B'
)

func newLineTest(opts ...Option) *lineTest {
	l := &lineTest{t0: time.Now().Add(-time.Hour), got: map[uint32]int{}}
	l.b = NewBuffer(&testDepacketizer{}, testBufferLatency, func(p []ExtPacket) {
		for _, x := range p {
			l.got[x.Timestamp]++
			l.seqs = append(l.seqs, x.SequenceNumber)
		}
	}, append(opts, WithSequenceRestartDetection())...)
	return l
}

func (l *lineTest) push(at time.Duration, seq int, src uint32) {
	pkt := (&stream{ssrc: 7, seq: uint16(seq)}).gen(true, true)
	pkt.Timestamp = src
	l.b.PushAt(pkt, l.t0.Add(at))
}

// run pushes sequence numbers first..last from srcA, one per 20ms slot.
func (l *lineTest) run(first, last int) {
	for i := first; i <= last; i++ {
		l.push(slot(l.n), i, srcA)
		l.n++
	}
}

func slot(i int) time.Duration { return time.Duration(i) * 20 * time.Millisecond }

// A media file looping back to sequence 0 on the same SSRC
// (SCL_mwhYqDjdFj2w). The old line stops, so the new one takes over once it
// has been quiet for lineQuiet: two packets per restart.
func TestSequenceRestart(t *testing.T) {
	l := newLineTest()
	defer l.b.Close()

	l.run(0, 349)
	l.run(0, 801)
	l.run(0, 801)
	l.run(0, 999)

	checkStats(t, l.b, &BufferStats{
		PacketsPushed:    2954,
		PacketsDropped:   6,
		PacketsPopped:    2948,
		SamplesPopped:    2948,
		SequenceRestarts: 3,
		PacketsStray:     6,
	})
}

// A transfer can re-anchor the same SSRC far behind prevSN, beyond the reorder
// window. Captured call: 21294 -> 12676.
func TestSequenceRestartFar(t *testing.T) {
	l := newLineTest()
	defer l.b.Close()

	l.run(21245, 21294)
	l.run(12676, 12725)

	checkStats(t, l.b, &BufferStats{
		PacketsPushed:    100,
		PacketsDropped:   2,
		PacketsPopped:    98,
		SamplesPopped:    98,
		SequenceRestarts: 1,
		PacketsStray:     2,
	})
	require.Equal(t, uint16(12678), l.seqs[50])
}

// A lone stale packet from beyond the window must not rewind the stream.
func TestSequenceRestartFarStray(t *testing.T) {
	l := newLineTest()
	defer l.b.Close()

	l.run(21245, 21294)
	l.push(slot(l.n)-10*time.Millisecond, 12676, srcA)
	l.run(21295, 21344)

	checkStats(t, l.b, &BufferStats{
		PacketsPushed:  101,
		PacketsDropped: 1,
		PacketsPopped:  100,
		SamplesPopped:  100,
		PacketsStray:   1,
	})
}

// Each stray must be reported to the stats handler. A call can drop thousands
// of strays with no switch, reorder or restart to carry the count.
func TestSequenceStrayNotified(t *testing.T) {
	var last BufferStats
	l := newLineTest(WithStatsHandler(func(st *BufferStats) { last = *st }))
	defer l.b.Close()

	l.run(21245, 21294)
	l.push(slot(l.n)-10*time.Millisecond, 12676, srcA)
	l.run(21295, 21344)

	require.Equal(t, uint64(1), last.PacketsStray)
	require.Equal(t, uint64(1), last.PacketsDropped)
}

// Late packets close behind the line are expired as usual, not a new line.
func TestSequenceLateBurst(t *testing.T) {
	l := newLineTest()
	defer l.b.Close()

	l.run(100, 300)
	for i := 250; i <= 299; i++ { // all at once
		l.push(slot(l.n), i, srcA)
	}
	l.run(301, 350)

	checkStats(t, l.b, &BufferStats{
		PacketsPushed:  301,
		PacketsDropped: 50,
		PacketsPopped:  251,
		SamplesPopped:  251,
	})
}

// A burst further behind than lineBehind, arriving back-to-back, out-delivers
// the live line and is taken for a new one. Not seen on audio, which is why
// detection is opt-in: on video this is a NACK burst for a lost keyframe.
func TestSequenceLateBurstFalsePositive(t *testing.T) {
	l := newLineTest()
	defer l.b.Close()

	l.run(100, 300)
	n := len(l.seqs)
	for i := 150; i <= 250; i++ { // all at once
		l.push(slot(l.n), i, srcA)
	}

	// the live line has 15 packets in lineWindow, so the burst wins at 30
	require.Equal(t, uint64(1), l.b.Stats().SequenceRestarts)
	require.Equal(t, uint64(29), l.b.Stats().PacketsStray)
	require.Equal(t, []uint16{179, 250}, []uint16{l.seqs[n], l.seqs[len(l.seqs)-1]})
}

// Restart detection is opt-in: by default a restart is discarded as expired
// until the new sequence catches up, near or far behind.
func TestSequenceRestartDisabled(t *testing.T) {
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, func([]ExtPacket) {})
	defer b.Close()
	push := func(seq uint16) { s := &stream{ssrc: 7, seq: seq}; b.Push(s.gen(true, true)) }

	for i := 21245; i <= 21294; i++ {
		push(uint16(i))
	}
	for i := 21000; i < 21000+20; i++ { // near run
		push(uint16(i))
	}
	for i := 12676; i < 12676+2; i++ { // far run
		push(uint16(i))
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  72,
		PacketsDropped: 22,
		PacketsPopped:  50,
		SamplesPopped:  50,
	})
}

// Silence suppression stops packets without a new line, and real loss is a
// gap on the same line.
func TestLineSilenceAndLoss(t *testing.T) {
	l := newLineTest()
	defer l.b.Close()

	for i := 0; i < 10; i++ { // 1s talk, 1s silence
		l.run(1000+50*i, 1049+50*i)
		l.n += 50
	}
	l.run(1892, 2000) // 392 lost

	checkStats(t, l.b, &BufferStats{
		PacketsPushed: 609,
		PacketsLost:   392,
		PacketsPopped: 609,
		SamplesPopped: 609,
	})
}

// Two sources on one SSRC, each slot 20ms. gen returns the sequence numbers A
// and B send in slot i, or -1; B arrives delay after A.
func TestTwoLines(t *testing.T) {
	pause := func(i, from, n int) bool { return i >= from && i < from+n }
	sparse := func(i int) bool { return i%9 == 4 || i%9 == 5 } // 2 of 9 slots
	or := func(ok bool, seq int) int {
		if ok {
			return seq
		}
		return -1
	}
	silentSeq := 999
	for _, c := range []struct {
		name     string
		slots    int
		delay    time.Duration
		gen      func(i int) (a, b int)
		a, b     int // packets delivered
		restarts uint64
	}{
		// B2BUA interleaving a sparse second source 392 ahead (SCL_n6cVRuyyFN2M)
		{"sparse ahead", 1500, 4 * time.Millisecond, func(i int) (int, int) {
			return or(i < 500 || i%9 != 4, 1000+i), or(i >= 500 && sparse(i), 1392+i)
		}, 1389, 0, 0},
		// two full-rate lines: nothing to prefer, so the playing one stays
		{"dense ahead", 1500, 4 * time.Millisecond, func(i int) (int, int) {
			return 1000 + i, or(i >= 500, 1392+i)
		}, 1500, 0, 0},
		// the playing line trickles: the full-rate line takes over, ahead in the
		// first quiet gap as loss, behind on rate as a restart
		{"trickle, B ahead", 1500, 4 * time.Millisecond, func(i int) (int, int) {
			return or(i < 500 || i%9 < 2, 1000+i), or(i >= 500, 1392+i)
		}, 500, 998, 0},
		{"trickle, B behind", 1500, 4 * time.Millisecond, func(i int) (int, int) {
			return or(i < 500 || i%9 < 2, 1000+i), or(i >= 500, 608+i)
		}, 500, 998, 1},
		// new source on the same SSRC, the old one trailing for 55ms
		{"handover", 1500, 25 * time.Millisecond, func(i int) (int, int) {
			return or(i <= 504, 1000+i), or(i >= 500, 1500+i)
		}, 505, 994, 0},
		// a 500ms pause on A is too short for the parallel sparse line to take over
		{"sparse ahead, A silent", 1500, 4 * time.Millisecond, func(i int) (int, int) {
			a := -1
			if (i < 500 || i%9 != 4) && !pause(i, 900, 25) {
				silentSeq++
				a = silentSeq
			}
			return a, or(i >= 500 && sparse(i), 1392+i)
		}, 1367, 0, 0},
		// two full-rate lines for the whole call (SCL_sskecEKAj9eR): a loss
		// burst on A does not hand over the call, a 1s outage does
		{"parallel, A loses 120ms", 600, 10 * time.Millisecond, func(i int) (int, int) {
			return or(!pause(i, 200, 6), 1630+i), 1000 + i
		}, 594, 0, 0},
		{"parallel, A loses 300ms", 600, 10 * time.Millisecond, func(i int) (int, int) {
			return or(!pause(i, 200, 15), 1630+i), 1000 + i
		}, 585, 0, 0},
		{"parallel, A loses 1s", 600, 10 * time.Millisecond, func(i int) (int, int) {
			return or(!pause(i, 200, 50), 1630+i), 1000 + i
		}, 200, 376, 1},
		// a sparse line at one packet per second, ahead of A until A catches up
		// with it. Near the crossing it is within lineAhead of A but continues
		// its own line, so it stays stray.
		{"sparse crossing", 200, 4 * time.Millisecond, func(i int) (int, int) {
			return 1000 + i, or(i%50 == 0, 1120+i/50)
		}, 200, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLineTest()
			defer l.b.Close()
			for i := 0; i < c.slots; i++ {
				a, b := c.gen(i)
				if a >= 0 {
					l.push(slot(i), a, srcA)
				}
				if b >= 0 {
					l.push(slot(i)+c.delay, b, srcB)
				}
			}
			require.Equal(t, []int{c.a, c.b}, []int{l.got[srcA], l.got[srcB]}, "delivered A, B")
			require.Equal(t, c.restarts, l.b.Stats().SequenceRestarts)
		})
	}
}

// A finished stream sending late packets must not stall the active one.
func TestSSRCSwitch(t *testing.T) {
	out := make(chan []ExtPacket, 100)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out))
	live := &stream{ssrc: 0x5b4617a5, seq: 20250}
	stale := &stream{ssrc: 0x4b721f38, seq: 47786}

	for i := 0; i < 10; i++ {
		b.Push(live.gen(true, true))
		checkSample(t, out, 1)
	}
	for i := 0; i < 5; i++ { // stale stream, far ahead in its own sequence space
		b.Push(stale.gen(true, true))
		checkSample(t, out, 1)
	}
	for i := 0; i < 10; i++ { // live stream must keep flowing
		b.Push(live.gen(true, true))
		checkSample(t, out, 1)
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed: 25,
		PacketsPopped: 25,
		SamplesPopped: 25,
		SSRCSwitches:  2,
	})
}

func TestSSRCSwitchFlushes(t *testing.T) {
	out := make(chan []ExtPacket, 10)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out))
	s := &stream{ssrc: 1, seq: 100}

	b.Push(s.gen(true, true))
	checkSample(t, out, 1)
	_ = s.gen(true, true) // simulate lost packet (seq gap), next sample waits
	b.Push(s.gen(true, true))
	checkSample(t, out, 0)

	// The switch flushes 102 before clearing initialized.
	b.Push((&stream{ssrc: 2, seq: 50}).gen(true, true))
	checkSample(t, out, 1)
	checkSample(t, out, 1)

	checkStats(t, b, &BufferStats{
		PacketsPushed: 3,
		PacketsLost:   1,
		PacketsPopped: 3,
		SamplesPopped: 3,
		SSRCSwitches:  1,
	})
}

// Close must emit what is still buffered, not discard it.
func TestCloseFlushes(t *testing.T) {
	out := make(chan []ExtPacket, 10)
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, chanFunc(t, out))
	s := &stream{ssrc: 1, seq: 100}

	b.Push(s.gen(true, true))
	checkSample(t, out, 1)
	_ = s.gen(true, true) // lost packet, so the next one has to wait
	b.Push(s.gen(true, true))
	checkSample(t, out, 0)

	b.Close()
	checkSample(t, out, 1)
}

func checkSample(t *testing.T, out chan []ExtPacket, expected int) {
	select {
	case sample := <-out:
		if expected == 0 {
			t.Fatal("received unexpected sample")
		} else {
			require.Equal(t, expected, len(sample))
		}
	default:
		if expected > 0 {
			t.Fatal("expected to receive sample")
		}
	}
}

func checkStats(t *testing.T, b *Buffer, expected *BufferStats) {
	stats := b.Stats()
	require.Equal(t, expected.PacketsPushed, stats.PacketsPushed)
	require.Equal(t, expected.PacketsLost, stats.PacketsLost)
	require.Equal(t, expected.PacketsDropped, stats.PacketsDropped)
	require.Equal(t, expected.PacketsPopped, stats.PacketsPopped)
	require.Equal(t, expected.SamplesPopped, stats.SamplesPopped)
	require.Equal(t, expected.SSRCSwitches, stats.SSRCSwitches)
	require.Equal(t, expected.PacketsReordered, stats.PacketsReordered)
	require.Equal(t, expected.SequenceRestarts, stats.SequenceRestarts)
	require.Equal(t, expected.PacketsStray, stats.PacketsStray)
}

type stream struct {
	ssrc uint32
	seq  uint16
}

func newTestStream() *stream {
	return &stream{
		seq: uint16(rand.Uint32()),
	}
}

func (s *stream) gen(head, tail bool) *rtp.Packet {
	p := &rtp.Packet{
		Header: rtp.Header{
			Marker:         tail,
			SequenceNumber: s.seq,
			SSRC:           s.ssrc,
		},
		Payload: make([]byte, defaultPacketSize),
	}
	if head {
		copy(p.Payload, headerBytes)
	}
	s.seq++
	return p
}

func (s *stream) discont() {
	s.seq += math.MaxUint16 / 2
}

const defaultPacketSize = 200

var headerBytes = []byte{0xaa, 0xaa}

type testDepacketizer struct{}

func (d *testDepacketizer) Unmarshal(r []byte) ([]byte, error) {
	return r, nil
}

func (d *testDepacketizer) IsPartitionHead(payload []byte) bool {
	if headerBytes == nil || len(payload) < len(headerBytes) {
		return false
	}
	for i, b := range headerBytes {
		if payload[i] != b {
			return false
		}
	}
	return true
}

func (d *testDepacketizer) IsPartitionTail(marker bool, _ []byte) bool {
	return marker
}
