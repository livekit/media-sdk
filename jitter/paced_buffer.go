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
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/livekit/protocol/logger"
)

const (
	pacedSamplesBuffer    = 50
	incomingSamplesBuffer = 150
)

type pacerState struct {
	clockRate   uint32        // RTP clock rate of the stream
	maxLag      time.Duration // maximum delay tolerated behind real time
	allowLead   time.Duration // maximum lead permitted ahead of real time
	timer       *time.Timer   // shared timer reused between waits
	lastTS      uint32        // RTP timestamp of the previously paced packet
	releaseAt   time.Time     // wall-clock timestamp when the next packet should be sent
	lastForward time.Time     // wall-clock timestamp when we last forwarded a packet
}

type pacerSnapshot struct {
	lastTS    uint32
	releaseAt time.Time
}

func newPacerState(clockRate uint32, maxLag, allowLead time.Duration) *pacerState {
	t := time.NewTimer(time.Hour)
	if !t.Stop() {
		<-t.C
	}
	return &pacerState{
		clockRate: clockRate,
		maxLag:    maxLag,
		allowLead: allowLead,
		timer:     t,
	}
}

func (p *pacerState) snapshot() pacerSnapshot {
	return pacerSnapshot{
		lastTS:    p.lastTS,
		releaseAt: p.releaseAt,
	}
}

func (p *pacerState) restore(s pacerSnapshot) {
	p.lastTS = s.lastTS
	p.releaseAt = s.releaseAt
}

// prepare updates pacing deadlines based on the incoming RTP timestamp.
// It returns the time to wait before forwarding and whether we clamped lag.
func (p *pacerState) prepare(now time.Time, ts uint32) (time.Duration, bool) {
	if p.releaseAt.IsZero() || p.lastForward.IsZero() || now.Sub(p.lastForward) > p.maxLag {
		p.releaseAt = now.Add(-p.allowLead)
	} else {
		p.releaseAt = p.releaseAt.Add(durationFromTimestampDiff(ts-p.lastTS, p.clockRate))
	}

	if p.allowLead > 0 {
		maxRelease := now.Add(p.allowLead)
		if p.releaseAt.After(maxRelease) {
			p.releaseAt = maxRelease
		}
	}

	p.lastTS = ts

	wait := time.Until(p.releaseAt)
	if wait > p.maxLag {
		p.releaseAt = now
		return 0, true
	}
	return wait, false
}

// wait sleeps until the scheduled release time or until stop is triggered.
func (p *pacerState) wait(wait time.Duration, stop <-chan struct{}) bool {
	if wait <= 0 {
		p.stopTimer()
		return true
	}

	p.stopTimer()
	p.timer.Reset(wait)
	select {
	case <-p.timer.C:
		return true
	case <-stop:
		p.stopTimer()
		return false
	}
}

func (p *pacerState) stopTimer() {
	if !p.timer.Stop() {
		select {
		case <-p.timer.C:
		default:
		}
	}
}

func (p *pacerState) markForward() {
	p.lastForward = time.Now()
}

func (p *pacerState) close() {
	p.timer.Stop()
}

// PacedBuffer wraps the jitter buffer with pacing logic so we consume bursts at a controlled rate.
type PacedBuffer struct {
	buffer    *Buffer            // underlying jitter buffer collecting RTP packets
	incoming  chan []*rtp.Packet // samples awaiting pacing
	samples   chan []*rtp.Packet // paced sample output
	state     *pacerState        // pacing state machine
	logger    logger.Logger
	sendPLI   func()              // optional PLI callback when lag overflows on video
	trackKind webrtc.RTPCodecType // track kind for conditional behavior
	allowLead time.Duration       // maximum lead allowed when pre-warming
	maxLag    time.Duration       // maximum lag permitted before clamping

	stop      chan struct{} // closed to signal shutdown
	start     chan struct{} // closed when pacing should begin
	done      sync.WaitGroup
	startOnce sync.Once

	onDrop func(int) // invoked when a sample is dropped due to backpressure
}

// NewPacedBuffer constructs a jitter buffer wrapped with pacing logic.
func NewPacedBuffer(
	depacketizer rtp.Depacketizer,
	latency time.Duration,
	clockRate uint32,
	allowLead time.Duration,
	maxLag time.Duration,
	trackKind webrtc.RTPCodecType,
	sendPLI func(),
	logger logger.Logger,
	onDrop func(int),
) *PacedBuffer {
	if maxLag <= 0 {
		maxLag = time.Second
	}
	if maxLag > time.Second {
		maxLag = time.Second
	}

	pb := &PacedBuffer{
		incoming:  make(chan []*rtp.Packet, incomingSamplesBuffer),
		samples:   make(chan []*rtp.Packet, pacedSamplesBuffer),
		logger:    logger,
		sendPLI:   sendPLI,
		trackKind: trackKind,
		allowLead: allowLead,
		maxLag:    maxLag,
		stop:      make(chan struct{}),
		start:     make(chan struct{}),
		onDrop:    onDrop,
	}

	pb.state = newPacerState(clockRate, pb.maxLag, pb.allowLead)

	opts := []Option{WithLogger(logger)}
	if sendPLI != nil {
		opts = append(opts, WithPacketLossHandler(sendPLI))
	}

	pb.buffer = NewBuffer(
		depacketizer,
		latency,
		pb.handleSample,
		opts...,
	)

	pb.done.Add(1)
	go pb.run()

	return pb
}

func (pb *PacedBuffer) Samples() <-chan []*rtp.Packet {
	return pb.samples
}

// Start allows pacing to begin; until called, incoming samples are queued.
func (pb *PacedBuffer) Start() {
	pb.startOnce.Do(func() {
		close(pb.start)
	})
}

func (pb *PacedBuffer) Push(pkt *rtp.Packet) {
	pb.buffer.Push(pkt)
}

func (pb *PacedBuffer) Stats() *BufferStats {
	return pb.buffer.Stats()
}

func (pb *PacedBuffer) UpdateLatency(latency time.Duration) {
	pb.buffer.UpdateLatency(latency)
}

func (pb *PacedBuffer) Close() {
	select {
	case <-pb.stop:
		// already closed
	default:
		close(pb.stop)
		pb.buffer.Close()
	}
	pb.Start()
	pb.done.Wait()
	pb.state.close()
	close(pb.samples)
}

func (pb *PacedBuffer) handleSample(sample []*rtp.Packet) {
	select {
	case <-pb.stop:
		return
	default:
	}

	select {
	case pb.incoming <- sample:
	default:
		if pb.onDrop != nil {
			pb.onDrop(len(sample))
		}
		pb.logger.Warnw("pacer queue full, dropping sample", nil)
	}
}

func (pb *PacedBuffer) run() {
	defer pb.done.Done()

	select {
	case <-pb.start:
	case <-pb.stop:
		return
	}

	for {
		select {
		case <-pb.stop:
			return
		case sample, ok := <-pb.incoming:
			if !ok {
				return
			}
			if len(sample) == 0 {
				continue
			}

			snapshot := pb.state.snapshot()
			wait, clamped := pb.state.prepare(time.Now(), sample[0].Timestamp)
			if clamped {
				pb.logger.Warnw(
					"pacer lag exceeded, clamping", nil,
					"packetTimestamp", sample[0].Timestamp,
					"wait", wait,
					"maxLag", pb.maxLag,
				)
			}

			if !pb.state.wait(wait, pb.stop) {
				pb.state.restore(snapshot)
				return
			}

			select {
			case <-pb.stop:
				pb.state.restore(snapshot)
				return
			case pb.samples <- sample:
				pb.state.markForward()
			default:
				pb.state.restore(snapshot)
				if pb.onDrop != nil {
					pb.onDrop(len(sample))
				}
				pb.logger.Warnw("output queue full, dropping sample", nil)

			}
		}
	}
}

func durationFromTimestampDiff(diff uint32, clockRate uint32) time.Duration {
	if clockRate == 0 || diff == 0 {
		return 0
	}

	return time.Duration(diff) * time.Second / time.Duration(clockRate)
}
