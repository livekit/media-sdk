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

	"github.com/frostbyte73/core"
	"github.com/go-logr/logr"
	"github.com/pion/rtp"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/mono"
)

type ExtPacket struct {
	ReceivedAt time.Time
	*rtp.Packet
}

type Buffer struct {
	depacketizer rtp.Depacketizer
	latency      time.Duration
	logger       logger.Logger
	onPacket     PacketFunc
	onPacketLoss func()

	mu     sync.Mutex
	closed core.Fuse

	initialized bool
	prevSN      uint16
	prevTS      uint32
	head        *packet
	tail        *packet

	stats *BufferStats
	timer *time.Timer

	pool *packet
	size int
}

type Option func(*Buffer)

type BufferStats struct {
	PacketsPushed  uint64 // total packets pushed
	PaddingPushed  uint64 // padding packets pushed
	PacketsLost    uint64 // packets lost
	PacketsDropped uint64 // packets dropped (incomplete)
	PacketsPopped  uint64 // packets sent to handler
	SamplesPopped  uint64 // samples sent to handler
}

type PacketFunc func(packets []ExtPacket)

func NewBuffer(
	depacketizer rtp.Depacketizer,
	latency time.Duration,
	fnc PacketFunc,
	opts ...Option,
) *Buffer {
	b := &Buffer{
		depacketizer: depacketizer,
		latency:      latency,
		logger:       logger.LogRLogger(logr.Discard()),
		stats:        &BufferStats{},
		timer:        time.NewTimer(latency),
		onPacket:     fnc,
	}
	for _, opt := range opts {
		opt(b)
	}

	go func() {
		for {
			select {
			case <-b.timer.C:
				b.mu.Lock()
				b.popReady()
				b.mu.Unlock()
			case <-b.closed.Watch():
				return
			}
		}
	}()

	return b
}

func WithLogger(logger logger.Logger) Option {
	return func(b *Buffer) {
		b.logger = logger
	}
}

func WithPacketLossHandler(handler func()) Option {
	return func(b *Buffer) {
		b.onPacketLoss = handler
	}
}

func (b *Buffer) WithLogger(logger logger.Logger) *Buffer {
	b.logger = logger
	return b
}

func (b *Buffer) UpdateLatency(latency time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.latency = latency
	if b.head != nil {
		b.timer.Reset(time.Until(b.head.extPacket.ReceivedAt.Add(latency)))
	}
}

func (b *Buffer) Push(pkt *rtp.Packet) {
	b.PushAt(pkt, mono.Now())
}

func (b *Buffer) PushExtPacket(extPkt ExtPacket) {
	b.PushAt(extPkt.Packet, extPkt.ReceivedAt)
}

func (b *Buffer) PushExtPacketBatch(extPktBatch []ExtPacket) {
	for _, extPkt := range extPktBatch {
		b.PushAt(extPkt.Packet, extPkt.ReceivedAt)
	}
}

func (b *Buffer) PushAt(pkt *rtp.Packet, receivedAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.push(pkt, receivedAt)
	if b.head == nil {
		return
	}

	b.popReady()
}

func (b *Buffer) Size() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.size
}

func (b *Buffer) Stats() *BufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	return &BufferStats{
		PacketsPushed:  b.stats.PacketsPushed,
		PaddingPushed:  b.stats.PaddingPushed,
		PacketsLost:    b.stats.PacketsLost,
		PacketsDropped: b.stats.PacketsDropped,
		PacketsPopped:  b.stats.PacketsPopped,
		SamplesPopped:  b.stats.SamplesPopped,
	}
}

func (s *BufferStats) PacketLoss() float64 {
	if s.PacketsPushed == 0 {
		return 0
	}

	return float64(s.PacketsDropped) / float64(s.PacketsPushed)
}

func (b *Buffer) Close() {
	b.timer.Stop()
	b.closed.Break()
}

func (b *Buffer) isLargeTimestampJump(current, prev uint32) bool {
	const MAX_TIMESTAMP_JUMP = 8000 * 30 // 30 seconds at 8kHz

	if !b.initialized {
		return false
	}

	cur := int64(current)
	prv := int64(prev)

	forwardDiff := cur - prv
	if forwardDiff < 0 {
		forwardDiff += (1 << 32) // handle 32-bit wrap-around
	}

	backwardDiff := prv - cur
	if backwardDiff < 0 {
		backwardDiff += (1 << 32) // handle 32-bit wrap-around
	}

	return min(backwardDiff, forwardDiff) > MAX_TIMESTAMP_JUMP
}

func (b *Buffer) isLargeSequenceJump(current, prev uint16) bool {
	const MAX_SEQUENCE_JUMP = 1000

	if !b.initialized {
		return false
	}

	cur := int32(current)
	prv := int32(prev)

	forwardDiff := cur - prv
	if forwardDiff < 0 {
		forwardDiff += 65536 // handle wrap-around
	}

	backwardDiff := prv - cur
	if backwardDiff < 0 {
		backwardDiff += 65536 // handle wrap-around
	}

	return min(backwardDiff, forwardDiff) > MAX_SEQUENCE_JUMP
}

func (b *Buffer) reset() {
	b.logger.Infow("resetting jitter buffer due to RTP discontinuity")

	for b.head != nil {
		next := b.head.next
		b.free(b.head)
		b.head = next
	}
	b.tail = nil

	b.initialized = false
	b.prevSN = 0
	b.prevTS = 0

	if !b.timer.Stop() {
		select {
		case <-b.timer.C:
		default:
		}
	}
	b.timer.Reset(b.latency)
}

// push adds a packet to the buffer
func (b *Buffer) push(pkt *rtp.Packet, receivedAt time.Time) {
	b.stats.PacketsPushed++
	if pkt.Padding {
		b.stats.PaddingPushed++
		if !b.initialized {
			return
		}
	}

	if b.isLargeTimestampJump(pkt.Timestamp, b.prevTS) ||
		b.isLargeSequenceJump(pkt.SequenceNumber, b.prevSN) {
		b.logger.Infow("large RTP discontinuity detected",
			"current_ts", pkt.Timestamp,
			"prev_ts", b.prevTS,
			"current_sn", pkt.SequenceNumber,
			"prev_sn", b.prevSN,
		)
		b.reset()
	}

	if b.initialized && before(pkt.SequenceNumber, b.prevSN) {
		// packet expired (not after discontinuity reset)
		if !pkt.Padding {
			b.stats.PacketsDropped++
			if b.onPacketLoss != nil {
				b.onPacketLoss()
			}
		}
		return
	}

	p := b.newPacket(pkt, receivedAt)

	discont := !b.initialized || !withinRange(pkt.SequenceNumber, b.prevSN)

	if b.head == nil {
		p.discont = discont && p.start
		b.head = p
		b.tail = p
		return
	}

	beforeHead := before(pkt.SequenceNumber, b.head.extPacket.SequenceNumber)
	afterTail := !before(pkt.SequenceNumber, b.tail.extPacket.SequenceNumber)
	withinHeadRange := withinRange(pkt.SequenceNumber, b.head.extPacket.SequenceNumber)
	withinTailRange := withinRange(pkt.SequenceNumber, b.tail.extPacket.SequenceNumber)

	switch {
	case beforeHead && withinHeadRange:
		// prepend
		p.discont = discont && p.start
		b.head.prev = p
		p.next = b.head
		b.head = p

	case afterTail && withinTailRange:
		// append
		p.prev = b.tail
		b.tail.next = p
		b.tail = p

	case withinTailRange:
		// insert, search from tail
		for c := b.tail.prev; c != nil; c = c.prev {
			discont = !withinRange(pkt.SequenceNumber, c.extPacket.SequenceNumber)
			if !before(pkt.SequenceNumber, c.extPacket.SequenceNumber) || discont {
				// insert after c
				p.discont = discont && p.start
				p.prev = c
				p.next = c.next
				c.next.prev = p
				c.next = p
				return
			}
		}

	case withinHeadRange:
		// insert, search from head
		for c := b.head.next; c != nil; c = c.next {
			discont = !withinRange(pkt.SequenceNumber, c.extPacket.SequenceNumber)
			if before(pkt.SequenceNumber, c.extPacket.SequenceNumber) || discont {
				// insert before c
				p.prev = c.prev
				p.next = c
				c.prev.next = p
				c.prev = p
				return
			}
		}

	default:
		// append (discont)
		p.discont = p.start
		p.prev = b.tail
		b.tail.next = p
		b.tail = p
	}
}

// popReady pushes all ready samples to the out channel
func (b *Buffer) popReady() {
	expiry := time.Now().Add(-b.latency)

	b.dropIncompleteExpired(expiry)

	loss := false
	for b.head != nil &&
		b.head.isComplete() {

		if b.head.extPacket.SequenceNumber == b.prevSN+1 || b.head.discont || !b.initialized {
			// normal
		} else if b.head.extPacket.ReceivedAt.Before(expiry) {
			// max latency reached
			loss = true
			b.stats.PacketsLost += uint64(b.head.extPacket.SequenceNumber - b.prevSN - 1)
		} else {
			break
		}

		if sample := b.popSample(); len(sample) > 0 {
			b.onPacket(sample)
		}
	}

	if loss && b.onPacketLoss != nil {
		b.onPacketLoss()
	}

	if b.head != nil {
		b.timer.Reset(time.Until(b.head.extPacket.ReceivedAt.Add(b.latency)))
	}
}

// dropIncompleteExpired drops incomplete expired packets
func (b *Buffer) dropIncompleteExpired(expiry time.Time) {
	dropped := false

	for b.head != nil && !b.head.isComplete() && b.head.extPacket.ReceivedAt.Before(expiry) {
		if b.initialized && !b.head.discont {
			b.stats.PacketsLost += uint64(b.head.extPacket.SequenceNumber - b.prevSN - 1)
		}

		b.free(b.popHead())

		dropped = true
		b.stats.PacketsDropped++
	}

	if dropped && b.onPacketLoss != nil {
		b.onPacketLoss()
	}
}

func (b *Buffer) popSample() []ExtPacket {
	sample := make([]ExtPacket, 0, b.size)
	end := false
	for !end {
		c := b.popHead()
		end = c.end

		if !c.extPacket.Padding {
			sample = append(sample, c.extPacket)
		}

		b.stats.PacketsPopped++
		b.free(c)
	}

	b.initialized = true
	b.stats.SamplesPopped++

	return sample
}

func (b *Buffer) popHead() *packet {
	c := b.head
	b.prevSN = c.extPacket.SequenceNumber
	b.prevTS = c.extPacket.Timestamp
	b.head = c.next
	if b.head == nil {
		b.tail = nil
	} else {
		b.head.prev = nil
	}
	return c
}

func before(a, b uint16) bool {
	return (b-a)&0x8000 == 0
}

func withinRange(a, b uint16) bool {
	return a-b < 3000 || b-a < 3000
}
