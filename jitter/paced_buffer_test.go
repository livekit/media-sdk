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
	"testing"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

func TestPacerBuffer_StartBlocksUntilPlaying(t *testing.T) {
	pb := NewPacedBuffer(
		&shapeShiftDepacketizer{},
		0,
		90000,
		500*time.Millisecond,
		1*time.Second,
		webrtc.RTPCodecTypeVideo,
		nil,
		logger.NewTestLogger(t),
		nil,
	)
	defer pb.Close()

	pkt := &rtp.Packet{Header: rtp.Header{Timestamp: 1000}, Payload: []byte{0x01}}
	pb.Push(pkt)

	select {
	case <-pb.Samples():
		t.Fatalf("got samples before Start was called")
	case <-time.After(50 * time.Millisecond):
		// expected no samples yet
	}

	pb.Start()

	select {
	case sample := <-pb.Samples():
		if len(sample) != 1 {
			t.Fatalf("expected 1 packet, got %d", len(sample))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for paced sample after Start")
	}
}

type dropCounter struct {
	count int
}

func (d *dropCounter) add(n int) {
	d.count += n
}

func TestPacedBuffer_DropsOnQueueFull(t *testing.T) {
	drop := &dropCounter{}

	pb := NewPacedBuffer(
		&shapeShiftDepacketizer{},
		0,
		90000,
		0,
		1*time.Second,
		webrtc.RTPCodecTypeAudio,
		nil,
		logger.NewTestLogger(t),
		drop.add,
	)
	defer pb.Close()

	for i := 0; i < incomingSamplesBuffer; i++ {
		pb.Push(&rtp.Packet{
			Header: rtp.Header{
				Timestamp:      uint32(i),
				SequenceNumber: uint16(i),
			},
			Payload: []byte{0x01},
		})
	}

	pb.handleSample([]*rtp.Packet{{
		Header: rtp.Header{
			Timestamp:      999,
			SequenceNumber: 1000,
		},
		Payload: []byte{0x01},
	}})

	if drop.count == 0 {
		t.Fatalf("expected drop when incoming queue full")
	}

	pb.Start()
}

func TestPacedBuffer_AllowLeadThenPaced(t *testing.T) {
	clockRate := uint32(48000)
	frameDuration := 20 * time.Millisecond
	allowLead := frameDuration * 4
	maxLag := 500 * time.Millisecond
	total := 12
	leadCount := int(allowLead / frameDuration)
	if leadCount <= 0 || leadCount >= total {
		t.Fatalf("invalid test setup: leadCount=%d total=%d", leadCount, total)
	}

	pb := NewPacedBuffer(
		&shapeShiftDepacketizer{},
		0,
		clockRate,
		allowLead,
		maxLag,
		webrtc.RTPCodecTypeAudio,
		nil,
		logger.NewTestLogger(t),
		nil,
	)
	t.Cleanup(pb.Close)
	pb.Start()

	tsStep := uint32((frameDuration * time.Duration(clockRate)) / time.Second)
	pushStart := time.Now()
	for i := 0; i < total; i++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Timestamp:      uint32(i) * tsStep,
				SequenceNumber: uint16(i),
			},
			Payload: []byte{0x01},
		}
		pb.Push(pkt)
	}

	arrivals := make([]time.Time, total)
	deadline := frameDuration * 2
	for i := 0; i < total; i++ {
		select {
		case sample := <-pb.Samples():
			if len(sample) != 1 {
				t.Fatalf("expected 1 packet, got %d", len(sample))
			}
			arrivals[i] = time.Now()
		case <-time.After(deadline):
			t.Fatalf("timed out waiting for paced sample %d", i)
		}
	}

	for i := 0; i < leadCount; i++ {
		leadDelay := arrivals[i].Sub(pushStart)
		if leadDelay > frameDuration {
			t.Fatalf("expected lead sample %d within %v, got %v", i, frameDuration, leadDelay)
		}
	}

	for i := leadCount + 1; i < total; i++ {
		delta := arrivals[i].Sub(arrivals[i-1])
		require.InDeltaf(
			t,
			float64(frameDuration),
			float64(delta),
			float64(10*time.Millisecond),
			"expected paced gap near %v between sample %d and %d, got %v",
			frameDuration,
			i-1,
			i,
			delta,
		)
	}
}

func TestPacedBuffer_ClampResetsLag(t *testing.T) {
	clockRate := uint32(48000)
	frameDuration := 60 * time.Millisecond
	allowLead := time.Duration(0)
	maxLag := 20 * time.Millisecond
	backlog := 3
	total := 8

	pb := NewPacedBuffer(
		&shapeShiftDepacketizer{},
		0,
		clockRate,
		allowLead,
		maxLag,
		webrtc.RTPCodecTypeAudio,
		nil,
		logger.NewTestLogger(t),
		nil,
	)
	t.Cleanup(pb.Close)
	pb.Start()

	tsStep := uint32((frameDuration * time.Duration(clockRate)) / time.Second)

	deadline := frameDuration + maxLag + 20*time.Millisecond
	waitSample := func(idx int) time.Time {
		select {
		case sample := <-pb.Samples():
			require.Lenf(t, sample, 1, "expected 1 packet for sample %d", idx)
			return time.Now()
		case <-time.After(deadline):
			t.Fatalf("timed out waiting for paced sample %d", idx)
			return time.Time{}
		}
	}

	// Introduce an initial burst to exceed maxLag and force a clamp.
	for i := 0; i < backlog; i++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Timestamp:      uint32(i) * tsStep,
				SequenceNumber: uint16(i),
			},
			Payload: []byte{0x01},
		}
		pb.Push(pkt)
	}

	arrivals := make([]time.Time, total)
	for i := 0; i < backlog; i++ {
		arrivals[i] = waitSample(i)
	}

	// Feed the remaining samples at real-time cadence so the pacer can recover.
	for i := backlog; i < total; i++ {
		time.Sleep(frameDuration)
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Timestamp:      uint32(i) * tsStep,
				SequenceNumber: uint16(i),
			},
			Payload: []byte{0x01},
		}
		pb.Push(pkt)
		arrivals[i] = waitSample(i)
	}

	clampIndex := -1
	for i := 1; i < backlog; i++ {
		delta := arrivals[i].Sub(arrivals[i-1])
		if delta < 5*time.Millisecond {
			clampIndex = i
			break
		}
	}
	if clampIndex == -1 {
		t.Fatalf("expected clamp during initial burst")
	}

	for i := backlog + 1; i < total; i++ {
		delta := arrivals[i].Sub(arrivals[i-1])
		require.InDeltaf(
			t,
			float64(frameDuration),
			float64(delta),
			float64(10*time.Millisecond),
			"expected pacing recovery near %v between sample %d and %d, got %v",
			frameDuration,
			i-1,
			i,
			delta,
		)
	}
}

type shapeShiftDepacketizer struct{}

func (shapeShiftDepacketizer) Unmarshal([]byte) ([]byte, error)  { return nil, nil }
func (shapeShiftDepacketizer) IsPartitionHead([]byte) bool       { return true }
func (shapeShiftDepacketizer) IsPartitionTail(bool, []byte) bool { return true }

var _ rtp.Depacketizer = (*shapeShiftDepacketizer)(nil)
