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

func TestSequenceRestart(t *testing.T) {
	delivered := 0
	b := NewBuffer(&testDepacketizer{}, testBufferLatency,
		func(pkts []ExtPacket) { delivered += len(pkts) })
	defer b.Close()

	push := func(seq uint16) {
		s := &stream{ssrc: 0xc1a417, seq: seq}
		b.Push(s.gen(true, true))
	}
	for i := 0; i <= 349; i++ { // partial first loop
		push(uint16(i))
	}
	for loop := 0; loop < 2; loop++ { // two full loops
		for i := 0; i <= 801; i++ {
			push(uint16(i))
		}
	}
	for i := 0; i <= 999; i++ { // final segment, runs past the loop point
		push(uint16(i))
	}

	// each restart costs the packets seen before the run is recognised
	lost := uint64(3 * (sequenceRestartRun - 1))
	checkStats(t, b, &BufferStats{
		PacketsPushed:    2954,
		PacketsDropped:   lost,
		PacketsPopped:    2954 - lost,
		SamplesPopped:    2954 - lost,
		SequenceRestarts: 3,
	})
	require.Equal(t, int(2954-lost), delivered)
}

// A false positive rewinds prevSN into the stale burst and replays the rest.
func TestSequenceRestartFalsePositive(t *testing.T) {
	var got []uint16
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, func(p []ExtPacket) {
		for _, x := range p {
			got = append(got, x.SequenceNumber)
		}
	})
	defer b.Close()
	push := func(seq uint16) { s := &stream{ssrc: 7, seq: seq}; b.Push(s.gen(true, true)) }

	for i := 100; i <= 300; i++ { // healthy stream, prevSN = 300
		push(uint16(i))
	}
	n := len(got)
	for i := 150; i <= 250; i++ { // one long ascending run of late packets
		push(uint16(i))
	}

	first := uint16(150 + sequenceRestartRun - 1) // trips the run
	require.Equal(t, uint64(1), b.Stats().SequenceRestarts)
	require.Equal(t, uint64(sequenceRestartRun-1), b.Stats().PacketsDropped)
	require.Equal(t, first, got[n], "first replayed packet")
	require.Len(t, got[n:], 250-int(first)+1, "rest of the burst is replayed")
}

// A transfer can re-anchor the same SSRC far behind prevSN, beyond the reorder
// window. That cannot be a late packet, so it resyncs after a short run instead
// of the full sequenceRestartRun. Captured call: 21294 -> 12676.
func TestSequenceRestartFar(t *testing.T) {
	var got []uint16
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, func(p []ExtPacket) {
		for _, x := range p {
			got = append(got, x.SequenceNumber)
		}
	})
	defer b.Close()
	push := func(seq uint16) { s := &stream{ssrc: 7, seq: seq}; b.Push(s.gen(true, true)) }

	for i := 21245; i <= 21294; i++ {
		push(uint16(i))
	}
	for i := 12676; i < 12676+50; i++ {
		push(uint16(i))
	}

	lost := uint64(farSequenceRestartRun - 1)
	checkStats(t, b, &BufferStats{
		PacketsPushed:    100,
		PacketsDropped:   lost,
		PacketsPopped:    100 - lost,
		SamplesPopped:    100 - lost,
		SequenceRestarts: 1,
	})
	require.Equal(t, uint16(12676+lost), got[50], "new stream resumes after the short run")
}

// A lone stale packet from beyond the window must not rewind the stream.
func TestSequenceRestartFarStray(t *testing.T) {
	b := NewBuffer(&testDepacketizer{}, testBufferLatency, func([]ExtPacket) {})
	defer b.Close()
	push := func(seq uint16) { s := &stream{ssrc: 7, seq: seq}; b.Push(s.gen(true, true)) }

	for i := 21245; i <= 21294; i++ {
		push(uint16(i))
	}
	push(12676)
	for i := 21295; i < 21295+50; i++ {
		push(uint16(i))
	}

	checkStats(t, b, &BufferStats{
		PacketsPushed:  101,
		PacketsDropped: 1,
		PacketsPopped:  100,
		SamplesPopped:  100,
	})
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
