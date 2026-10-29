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
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"

	"github.com/livekit/media-sdk"
)

// rtpStreamTSResetFrames is how far a timestamp may fall behind the wall clock before it jumps forward.
// It must exceed the mixer's catch-up burst after a stall (5 frames), or the burst would be counted twice,
// and stay below the spacing of DTMF digits (26 frames), so digits sent without audio get distinct timestamps.
const rtpStreamTSResetFrames = 10 // 200ms @ ptime=20ms

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

type Packet = rtp.Packet
type Header = rtp.Header

type Event struct {
	Type      byte
	Timestamp uint32
	Payload   []byte
	Marker    bool
}

type SeqWriterOpt func(*SeqWriter)

// WithClock replaces the wall clock that timestamps are kept in line with.
func WithClock(now func() time.Time) SeqWriterOpt {
	return func(s *SeqWriter) {
		s.now = now
	}
}

// NewSeqWriter creates an RTP writer for a single SSRC, with timestamps at clockRate.
func NewSeqWriter(w Writer, clockRate int, opts ...SeqWriterOpt) *SeqWriter {
	s := &SeqWriter{
		w:         w,
		clockRate: clockRate,
		frameTS:   uint32(clockRate / DefFramesPerSec),
		now:       time.Now,
	}
	s.h = rtp.Header{
		Version: 2,
		SSRC:    rand.Uint32(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SeqWriter writes the RTP packets of a single SSRC: one sequence number space and one timestamp clock,
// shared by all of its streams.
//
// The first packet written by any stream anchors timestamp 0 to the wall clock. Each stream keeps its own
// timestamp, and corrects it with CurTS before using it.
type SeqWriter struct {
	w         Writer
	clockRate int
	frameTS   uint32 // timestamp units per frame
	now       func() time.Time

	mu     sync.RWMutex
	h      Header
	auto   bool      // the automatic stream was created
	anchor time.Time // wall clock at timestamp 0
	maxTS  uint32    // latest timestamp written
}

func (s *SeqWriter) String() string {
	return s.w.String()
}

// TS returns the latest timestamp written.
func (s *SeqWriter) TS() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxTS
}

func (s *SeqWriter) ProjectWallclockTS() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.anchor.IsZero() {
		return 0
	}
	return s.projectWallclockTS()
}

func (s *SeqWriter) projectWallclockTS() uint32 {
	return uint32(s.now().Sub(s.anchor).Round(DefFrameDur)/DefFrameDur) * s.frameTS
}

// CurTS corrects a stream's timestamp: always ahead of the latest packet written,
// and not more than rtpStreamTSResetFrames behind the wall clock.
func (s *SeqWriter) CurTS(ts uint32) uint32 {
	reset := int32(rtpStreamTSResetFrames * s.frameTS)

	s.mu.RLock()
	defer s.mu.RUnlock()
	// Signed differences keep working across the 32-bit wrap.
	if int32(s.maxTS-ts) > 0 {
		ts = s.maxTS
	}
	if s.anchor.IsZero() {
		return ts
	}
	// Whole frames only: jitter within a frame must not move timestamps.
	wall := s.projectWallclockTS()
	if int32(wall-ts) > reset {
		return wall
	}
	return ts
}

// WriteEvent writes a packet with the event's timestamp, as is.
func (s *SeqWriter) WriteEvent(ev *Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.anchor.IsZero() {
		s.anchor = s.now()
	}
	s.h.PayloadType = ev.Type
	s.h.Marker = ev.Marker
	s.h.Timestamp = ev.Timestamp
	if _, err := s.w.WriteRTP(&s.h, ev.Payload); err != nil {
		return err
	}
	s.h.SequenceNumber++
	if int32(ev.Timestamp-s.maxTS) > 0 {
		s.maxTS = ev.Timestamp
	}
	return nil
}

// NewStream creates the automatic stream, which advances its timestamp by one frame per packet.
// There may be only one, and its clock rate must match the SeqWriter's.
func (s *SeqWriter) NewStream(typ byte, clockRate int) *Stream {
	if clockRate != s.clockRate {
		panic(fmt.Sprintf("rtp: automatic stream clock rate %d differs from SSRC clock rate %d", clockRate, s.clockRate))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auto {
		panic("rtp: SSRC already has an automatic stream")
	}
	s.auto = true
	return &Stream{s: s, typ: typ}
}

// NewManualStream creates a stream whose timestamp is set by the caller with SyncTimestamp,
// in SeqWriter clock units. Payloads with a clock rate other than the SeqWriter's, such as
// telephone-event, must use a manual stream.
func (s *SeqWriter) NewManualStream(typ byte) *Stream {
	return &Stream{s: s, typ: typ, manual: true}
}

var _ media.BytesWriter = (*Stream)(nil)

// Stream is a payload type within the SSRC.
type Stream struct {
	s      *SeqWriter
	typ    byte
	manual bool

	mu sync.Mutex
	ts uint32
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

// WritePayload writes the next frame of the automatic stream.
func (s *Stream) WritePayload(data []byte, marker bool) error {
	if s.manual {
		return errors.New("rtp: WritePayload on a manual stream")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.s.CurTS(s.ts)
	if err := s.s.WriteEvent(&Event{Type: s.typ, Timestamp: ts, Payload: data, Marker: marker}); err != nil {
		return err
	}
	s.ts = ts + s.s.frameTS
	return nil
}

// WritePayloadAtCurrent writes the payload of a manual stream at its current timestamp.
// This allows multiple payloads with the same timestamp (e.g. DTMF).
func (s *Stream) WritePayloadAtCurrent(data []byte, marker bool) error {
	if !s.manual {
		return errors.New("rtp: WritePayloadAtCurrent on the automatic stream")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s.WriteEvent(&Event{Type: s.typ, Timestamp: s.ts, Payload: data, Marker: marker})
}

// SyncTimestamp sets the timestamp of a manual stream to the latest packet written,
// or to the wall clock if that fell behind.
func (s *Stream) SyncTimestamp() error {
	if !s.manual {
		return errors.New("rtp: SyncTimestamp on the automatic stream")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ts = s.s.ProjectWallclockTS()
	return nil
}

// GetCurrentTimestamp returns the stream's timestamp, without corrections.
func (s *Stream) GetCurrentTimestamp() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ts
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
