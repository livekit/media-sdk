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

package rtp

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"

	"github.com/livekit/media-sdk"
)

const rtpStreamTSResetFrames = 25 // 500ms @ ptime=20ms

// After this many frames without a frame-advancing write, writes follow the wall clock.
// Must exceed the longest burst of catch-up writes after a scheduler stall (mixer input buffer, 5 frames),
// or the burst would be counted twice. Event syncs follow the wall clock without this threshold.
const rtpStreamTSDriftFrames = 10 // 200ms @ ptime=20ms

type BytesFrame interface {
	media.BytesFrame
}

type Writer interface {
	String() string
	WriteRTP(h *rtp.Header, payload []byte) (int, error)
}

type Reader interface {
	ReadRTP() (*rtp.Packet, interceptor.Attributes, error)
}

type Handler interface {
	String() string
	HandleRTP(h *rtp.Header, payload []byte) error
}

type HandlerCloser interface {
	Handler
	Close()
}

type HandlerFunc func(h *rtp.Header, payload []byte) error

func (fnc HandlerFunc) String() string {
	return "HandlerFunc"
}

func (fnc HandlerFunc) HandleRTP(h *rtp.Header, payload []byte) error {
	if fnc == nil {
		return nil
	}
	return fnc(h, payload)
}

func HandleLoop(r Reader, h HandlerCloser) error {
	defer h.Close()

	for {
		p, _, err := r.ReadRTP()
		if err != nil {
			return err
		}
		err = h.HandleRTP(&p.Header, p.Payload)
		if err != nil {
			return err
		}
	}
}

type nopCloser struct {
	Handler
}

func NewNopCloser(h Handler) HandlerCloser {
	return nopCloser{h}
}

func (nopCloser) Close() {}

// Buffer is a Writer that clones and appends RTP packets into a slice.
type Buffer []*Packet

func (b *Buffer) String() string {
	return "Buffer"
}

func (b *Buffer) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	*b = append(*b, &rtp.Packet{
		Header:  *h,
		Payload: slices.Clone(payload),
	})
	return len(payload), nil
}

// NewSeqWriter creates an RTP writer that automatically increments the sequence number.
func NewSeqWriter(w Writer, opts ...SeqWriterOpt) *SeqWriter {
	s := &SeqWriter{w: w}
	s.h = rtp.Header{
		Version:        2,
		SSRC:           rand.Uint32(),
		SequenceNumber: 0,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type Packet = rtp.Packet
type Header = rtp.Header

type Event struct {
	Type      byte
	Timestamp uint32
	Payload   []byte
	Marker    bool
}

type SeqWriterOpt func(*SeqWriter)

func WithWallclockAdjusted(adjusted bool) SeqWriterOpt {
	return func(s *SeqWriter) {
		s.wallclockAdjusted = adjusted
	}
}

type SeqWriter struct {
	maxTS             atomic.Uint32
	maxTime           atomic.Value // time.Time
	mainClockRate     atomic.Uint32
	mu                sync.Mutex
	w                 Writer
	h                 Header
	wallclockAdjusted bool
}

func (s *SeqWriter) String() string {
	return s.w.String()
}

// Return the number of frames that have passed since the last packet by wall-clock time.
func (s *SeqWriter) wallclockFrameDelta() uint32 {
	// In the absence of original headers, or some metadata, there's no knowing
	// the original timestamp (or timestamps for mixed audio) of these samples.
	if !s.wallclockAdjusted {
		return 0
	}
	now := time.Now()
	last, ok := s.maxTime.Load().(time.Time)
	s.maxTime.Store(now)
	if !ok {
		return 0
	}
	// Calculate now many "frames" do we need to add to the timestamp based on time since last packet
	framesSince := now.Sub(last).Round(DefFrameDur) / DefFrameDur
	framesSince -= 1 // The expected bump
	if framesSince <= 0 {
		framesSince = 0
	}
	return uint32(framesSince)
}

// CurTS requests a timestamp from the stream, using ts as a reference.
// The function may return timestamp as-is or may adjust it to catch up with other active streams.
func (s *SeqWriter) CurTS(ts, inc uint32) uint32 {
	for {
		cur := s.maxTS.Load()
		// TODO: Handle wrap-around. Not a concern for now, because all streams start at TS=0.
		if cur > ts+rtpStreamTSResetFrames*inc {
			// Previous timestamp on the stream was too long ago.
			// Force a timestamp reset to the one from a more recent stream.
			return cur
		}
		if cur >= ts {
			// Timestamp is within allowed range.
			return ts
		}
		// Adjust the max timestamp, make sure other stream didn't update it before us.
		if s.maxTS.CompareAndSwap(cur, ts) {
			return ts
		}
	}
}

func (s *SeqWriter) WriteEvent(ev *Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.h.PayloadType = ev.Type
	s.h.Marker = ev.Marker
	s.h.Timestamp = ev.Timestamp
	if _, err := s.w.WriteRTP(&s.h, ev.Payload); err != nil {
		return err
	}
	s.h.SequenceNumber++
	return nil
}

// NewStream creates a new media stream in RTP and tracks timestamps associated with it.
func (s *SeqWriter) NewStream(typ byte, clockRate int) *Stream {
	return s.NewStreamWithDur(typ, uint32(clockRate/DefFramesPerSec))
}

func (s *SeqWriter) NewStreamWithDur(typ byte, packetDur uint32) *Stream {
	st := &Stream{s: s, packetDur: packetDur}
	st.ev.Type = typ
	return st
}

var _ media.BytesWriter = (*Stream)(nil)

type Stream struct {
	s         *SeqWriter
	packetDur uint32
	mu        sync.Mutex
	ev        Event
	maxTime   atomic.Value // time.Time
}

func (s *Stream) String() string {
	return s.s.String()
}

func (s *Stream) Close() error {
	return nil
}

func (s *Stream) WriteRaw(data []byte) error {
	return s.WritePayload(data, false)
}

// Return the number of samples, rounded to full frames, that have passed since the last packet by wall-clock time.
func (s *Stream) AdjustWallclockDelta() {
	dur := s.s.mainClockRate.Load()
	if dur == 0 {
		dur = s.packetDur
	}
	framesSince := s.s.wallclockFrameDelta()
	s.ev.Timestamp += framesSince * dur
	for {
		cur := s.s.maxTS.Load()
		if cur >= s.ev.Timestamp {
			return
		}
		if s.s.maxTS.CompareAndSwap(cur, s.ev.Timestamp) {
			return
		}
	}
}

func (s *Stream) writePayload(preserveLastTS bool, data []byte, marker bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ev.Payload = data
	s.ev.Marker = marker
	if !preserveLastTS {
		s.s.mainClockRate.Store(s.packetDur)
		s.AdjustWallclockDelta()
		s.ev.Timestamp = s.s.CurTS(s.ev.Timestamp, s.packetDur)
	}
	if err := s.s.WriteEvent(&s.ev); err != nil {
		return err
	}
	if !preserveLastTS {
		s.ev.Timestamp += s.packetDur
	}
	return nil
}

// WritePayload writes the payload to RTP and increments the timestamp.
func (s *Stream) WritePayload(data []byte, marker bool) error {
	return s.writePayload(false, data, marker)
}

// WritePayloadAtCurrent writes the payload to RTP at the current timestamp.
// This allows to emit multiple different payloads with the same timestamp in this stream (e.g. DTMF).
// The caller is expected to call Delay or DelayN at some point to advances the timestamp.
func (s *Stream) WritePayloadAtCurrent(data []byte, marker bool) error {
	return s.writePayload(true, data, marker)
}

// Delay advances the timestamp of the next frame. Typically used in combination with WritePayloadAtCurrent.
func (s *Stream) Delay(dur uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ev.Timestamp += dur
	s.ev.Timestamp = s.s.CurTS(s.ev.Timestamp, s.packetDur)
}

// DelayN is similar to Delay, but it increments time in multiples of the frame durations.
func (s *Stream) DelayN(n int) {
	if n < 0 {
		panic("rtp: negative delay")
	}
	s.Delay(uint32(n) * s.packetDur)
}

// make next event have an in-sync timestamp to audio
// Relies on non-stop audio traffic.
func (s *Stream) SyncEventTimestamp() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ev.Timestamp = s.s.maxTS.Load() // TODO: Consider sending the next timestamp instead
}

func (s *Stream) GetCurrentTimestamp() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.ev.Timestamp
}

func NewMediaStreamOut[T BytesFrame](s *Stream, sampleRate int) *MediaStreamOut[T] {
	return &MediaStreamOut[T]{s: s, sampleRate: sampleRate}
}

type MediaStreamOut[T BytesFrame] struct {
	s          *Stream
	sampleRate int
}

func (s *MediaStreamOut[T]) String() string {
	return fmt.Sprintf("RTP(%d)", s.sampleRate)
}

func (s *MediaStreamOut[T]) SampleRate() int {
	return s.sampleRate
}

func (s *MediaStreamOut[T]) Close() error {
	return nil
}

func (s *MediaStreamOut[T]) WriteSample(sample T) error {
	return s.s.WritePayload([]byte(sample), false)
}

func NewMediaStreamIn[T BytesFrame](w media.Writer[T]) *MediaStreamIn[T] {
	return &MediaStreamIn[T]{Writer: w}
}

type MediaStreamIn[T BytesFrame] struct {
	Writer media.Writer[T]
}

func (s *MediaStreamIn[T]) String() string {
	return fmt.Sprintf("RTP(%d) -> %s", s.Writer.SampleRate(), s.Writer)
}

func (s *MediaStreamIn[T]) HandleRTP(_ *rtp.Header, payload []byte) error {
	return s.Writer.WriteSample(T(payload))
}
