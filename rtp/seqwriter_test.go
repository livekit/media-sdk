// Copyright 2026 LiveKit, Inc.
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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	audioPT, eventsPT = 96, 101
	frame             = DefFrameDur
)

type fakeClock struct { // TODO: Ideally solved with testing/synctest
	t time.Time
}

func (c *fakeClock) Now() time.Time      { return c.t }
func (c *fakeClock) Add(d time.Duration) { c.t = c.t.Add(d) }
func (c *fakeClock) Set(t time.Time)     { c.t = t }

type testSSRC struct {
	*SeqWriter
	buf   *Buffer
	clock *fakeClock
	inc   uint32 // timestamp units per frame
}

func newTestSSRC(clockRate int, opts ...SeqWriterOpt) *testSSRC {
	buf := new(Buffer)
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	return &testSSRC{
		SeqWriter: NewSeqWriter(buf, clockRate, WithClock(clock.Now)),
		buf:       buf,
		clock:     clock,
		inc:       uint32(clockRate / DefFramesPerSec),
	}
}

// last returns the timestamp of the last packet written.
func (s *testSSRC) last() uint32 {
	b := *s.buf
	return b[len(b)-1].Timestamp
}

// write writes one packet, syncing a manual stream first, and returns its timestamp.
func (s *testSSRC) write(t *testing.T, st *Stream, marker bool, payload []byte) uint32 {
	if st.manual {
		require.NoError(t, st.SyncTimestamp())
		require.NoError(t, st.WritePayloadAtCurrent(payload, marker))
	} else {
		require.NoError(t, st.WritePayload(payload, marker))
	}
	return s.last()
}

// paced writes n packets, one per frame of wall clock, and returns their timestamps.
func (s *testSSRC) paced(t *testing.T, st *Stream, n int, marker bool, payload []byte) []uint32 {
	var ts []uint32
	if st.manual {
		require.NoError(t, st.SyncTimestamp())
	}
	for range n {
		ts = append(ts, s.write(t, st, marker, payload))
		s.clock.Add(frame)
	}
	return ts
}

// frames returns n consecutive timestamps starting at first.
func (s *testSSRC) frames(first uint32, n int) []uint32 {
	var ts []uint32
	for i := range n {
		ts = append(ts, first+uint32(i)*s.inc)
	}
	return ts
}

func TestSeqWriterPackets(t *testing.T) {
	s := newTestSSRC(8000)
	audio := s.NewStream(audioPT, 8000)
	events := s.NewManualStream(eventsPT)

	require.NoError(t, audio.WritePayload([]byte{1}, true))
	require.NoError(t, events.SyncTimestamp())
	require.NoError(t, events.WritePayloadAtCurrent([]byte{2}, true))
	require.NoError(t, events.WritePayloadAtCurrent([]byte{3}, false))
	require.NoError(t, audio.WritePayload([]byte{4}, false))

	exp := []struct {
		typ     byte
		marker  bool
		payload byte
	}{
		{audioPT, true, 1},
		{eventsPT, true, 2},
		{eventsPT, false, 3},
		{audioPT, false, 4},
	}
	b := *s.buf
	require.Len(t, b, len(exp))
	for i, p := range b {
		require.EqualValues(t, 2, p.Version)
		require.Equal(t, b[0].SSRC, p.SSRC, "one SSRC for all streams")
		require.Equal(t, b[0].SequenceNumber+uint16(i), p.SequenceNumber, "one sequence space for all streams")
		require.Equal(t, exp[i].typ, p.PayloadType)
		require.Equal(t, exp[i].marker, p.Marker)
		require.Equal(t, []byte{exp[i].payload}, p.Payload)
	}
}

// DTMF given at call creation is sent right after the call is answered, before any audio, so it is
// the first packet on the SSRC. Some receivers (e.g. Asterisk before 16.19/18.5) start their
// last-seen DTMF timestamp at 0 and accept only later events, so they drop a digit at timestamp 0
// entirely. No packet may ever be written at timestamp 0.
func TestSeqWriterTimestampOffset(t *testing.T) {
	require.Greater(t, rtpTimestampOffset, 0)
}

// TestSeqWriterFirstPackets covers which timestamp the first packets of an SSRC get, for each order
// of streams, writes and syncs. The wall clock stands still, so no catch-up applies.
func TestSeqWriterFirstPackets(t *testing.T) {
	type step int
	const (
		newAudio     step = iota // create the automatic stream
		newManual                // create a manual stream
		audioWrite               // write a frame of the automatic stream
		manualWrite              // write a manual packet without a sync
		manualSynced             // sync the manual stream, then write a packet
		wait                     // wait for the next frame
		waitReset                // wait for 11 frames, enough to reset the timeline
	)
	streamReset := (rtpStreamTSResetFrames + 1) * frame
	cases := []struct {
		name  string
		steps []step
		exp   []uint32 // in frames after rtpTimestampOffset
	}{
		{"manual_without_sync", []step{newManual, manualWrite, wait, manualWrite}, []uint32{0, 0}},
		{"manual_with_sync", []step{newManual, manualSynced, wait, manualSynced}, []uint32{0, 1}},
		{"audio", []step{newAudio, audioWrite, audioWrite}, []uint32{0, 1}},

		{"manual_without_sync_unaffected_by_audio",
			[]step{newManual, newAudio, manualWrite, audioWrite, wait, audioWrite, manualWrite},
			[]uint32{0, 0, 1, 0}},
		{"manual_with_sync_unaffected_by_audio",
			[]step{newManual, newAudio, manualSynced, audioWrite, wait, audioWrite, manualSynced},
			[]uint32{0, 0, 1, 1}},
		{"manual_without_sync_unaffected_by_new_audio",
			[]step{newManual, manualWrite, newAudio, audioWrite, wait, audioWrite, manualWrite},
			[]uint32{0, 0, 1, 0}},
		{"manual_with_sync_unaffected_by_new_audio",
			[]step{newManual, manualSynced, newAudio, audioWrite, wait, audioWrite, manualSynced},
			[]uint32{0, 0, 1, 1}},

		{"new_audio_inherits_synced_timestamp",
			[]step{newManual, manualSynced, wait, manualSynced, newAudio, audioWrite},
			[]uint32{0, 1, 1}},
		{"new_audio_inherits_anchor",
			[]step{newManual, manualSynced, waitReset, newAudio, audioWrite},
			[]uint32{0, 11}},
		{"new_audio_inherits_unsynced_timestamp",
			[]step{newManual, manualWrite, waitReset, newAudio, audioWrite},
			[]uint32{0, 11}},

		{"audio_anchors",
			[]step{newAudio, audioWrite, wait, newManual, manualSynced},
			[]uint32{0, 1}},
		{"snyched_anchors",
			[]step{newManual, manualSynced, waitReset, newAudio, audioWrite},
			[]uint32{0, 11}},
		{"unsnyched_anchors",
			[]step{newManual, manualWrite, waitReset, newAudio, audioWrite},
			[]uint32{0, 11}},

		{"initial_wait_irrelevant_audio",
			[]step{newAudio, wait, wait, audioWrite},
			[]uint32{0}},
		{"initial_wait_irrelevant_synced",
			[]step{newManual, wait, wait, manualSynced},
			[]uint32{0}},
		{"initial_wait_irrelevant_unsynced",
			[]step{newManual, wait, wait, manualWrite},
			[]uint32{0}},

		{"both_new_streams_defaults_to_offset",
			[]step{newManual, newAudio, manualWrite, audioWrite, audioWrite},
			[]uint32{0, 0, 1}},
		{"unsynced_manual_starts_at_offset",
			[]step{newManual, newAudio, manualSynced, audioWrite, audioWrite},
			[]uint32{0, 0, 1}},
		{"new_audio_unaffected_by_manual_write",
			[]step{newManual, manualWrite, newAudio, audioWrite, audioWrite},
			[]uint32{0, 0, 1}},
		{"manual_with_sync_then_audio_audio_created_after",
			[]step{newManual, manualSynced, newAudio, audioWrite, audioWrite},
			[]uint32{0, 0, 1}},

		{"permit_audio_runaway",
			[]step{newAudio, newManual, audioWrite, audioWrite, manualSynced},
			[]uint32{0, 1, 0}}, // Explicitly permit audio to outpace clock

		{"allow_manual_lag",
			[]step{newAudio, newManual, audioWrite, wait, audioWrite, manualWrite},
			[]uint32{0, 1, 0}},
		{"manual_sync_working",
			[]step{newAudio, newManual, audioWrite, wait, audioWrite, manualSynced},
			[]uint32{0, 1, 1}},
		{"new_manual_inherits_audio_timestamp",
			[]step{newAudio, audioWrite, wait, audioWrite, wait, newManual, manualWrite},
			[]uint32{0, 1, 1}},
		{"new_manual_sync_working",
			[]step{newAudio, audioWrite, wait, audioWrite, wait, newManual, manualSynced},
			[]uint32{0, 1, 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestSSRC(8000)
			var audio, manual *Stream
			var got []uint32
			for _, st := range c.steps {
				switch st {
				case newAudio:
					audio = s.NewStream(audioPT, 8000)
				case newManual:
					manual = s.NewManualStream(eventsPT)
				case audioWrite:
					got = append(got, s.write(t, audio, false, []byte{0}))
				case manualWrite:
					require.NoError(t, manual.WritePayloadAtCurrent([]byte{0}, true))
					got = append(got, s.last())
				case manualSynced:
					got = append(got, s.write(t, manual, true, []byte{0}))
				case wait:
					s.clock.Add(frame)
				case waitReset:
					s.clock.Add(streamReset)
				}
			}
			var exp []uint32
			for _, f := range c.exp {
				t.Logf("expected %d, got %d", f, got[len(exp)])
				curExp := rtpTimestampOffset + f*s.inc
				require.Greater(t, curExp, uint32(0)) // See rtpTimestampOffset comment
				exp = append(exp, curExp)
			}
			require.Equal(t, exp, got)
		})
	}
}

func TestSeqWriterAutomatic(t *testing.T) {
	t.Run("starts at offset, one frame per packet", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		require.Equal(t, s.frames(rtpTimestampOffset, 10), s.paced(t, audio, 10, false, []byte{0}))
	})

	t.Run("uses the SSRC clock rate", func(t *testing.T) {
		s := newTestSSRC(48000)
		audio := s.NewStream(audioPT, 48000)
		require.Equal(t, s.frames(rtpTimestampOffset, 3), s.paced(t, audio, 3, false, []byte{0}))
	})

	t.Run("bursts never share a timestamp", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		var ts []uint32
		for range 5 {
			ts = append(ts, s.write(t, audio, false, []byte{0}))
		}
		require.Equal(t, s.frames(rtpTimestampOffset, 5), ts)
	})

	t.Run("scheduling jitter does not move timestamps", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		start := s.clock.Now()
		var ts []uint32
		for i, j := range []time.Duration{0, 9, -9, 15, -5, 19, -1, 1, 0, 12} {
			s.clock.t = start.Add(time.Duration(i)*frame + j*time.Millisecond)
			ts = append(ts, s.write(t, audio, false, []byte{0}))
		}
		require.Equal(t, s.frames(rtpTimestampOffset, 10), ts)
	})

	t.Run("stall below the threshold is caught up by a burst", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		ts := s.paced(t, audio, 5, false, []byte{0})
		s.clock.Add(5 * frame)
		for range 6 {
			ts = append(ts, s.write(t, audio, false, []byte{0}))
		}
		ts = append(ts, s.paced(t, audio, 5, false, []byte{0})...)
		require.Equal(t, s.frames(rtpTimestampOffset, 16), ts)
	})

	t.Run("gap above the threshold jumps to the nearest wall clock frame", func(t *testing.T) {
		for _, c := range []struct {
			gap   time.Duration
			first uint32
		}{
			{30*frame + frame/2 - time.Millisecond, 35},
			{30*frame + frame/2, 36},
		} {
			s := newTestSSRC(8000)
			audio := s.NewStream(audioPT, 8000)
			s.paced(t, audio, 5, false, []byte{0}) // ends at 5 frames
			s.clock.Add(c.gap)
			ts := s.paced(t, audio, 3, false, []byte{0})
			require.Equal(t, s.frames(rtpTimestampOffset+c.first*s.inc, 3), ts, "gap %v", c.gap)
		}
	})

	t.Run("gap of exactly the threshold does not jump", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		s.paced(t, audio, 5, false, []byte{0})
		s.clock.Add(rtpStreamTSResetFrames * frame)
		require.Equal(t, rtpTimestampOffset+5*s.inc, s.write(t, audio, false, []byte{0}))
	})

	t.Run("lag lost to repeated stalls is recovered", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		start := s.clock.Now()
		wallFrames := func() uint32 { return uint32(s.clock.Now().Sub(start) / frame) }

		// Like the mixer: each stall of 8 frames is followed by a catch-up burst capped at 5,
		// so the timeline falls behind by 3 frames per stall.
		jumps := 0
		prev := s.write(t, audio, false, []byte{0})
		for range 10 {
			s.clock.Add(8 * frame)
			for range 5 {
				ts := s.write(t, audio, false, []byte{0})
				if ts != prev+s.inc {
					jumps++
					require.Equal(t, rtpTimestampOffset+wallFrames()*s.inc, ts, "jumps land on the wall clock")
				}
				lag := int(wallFrames()) - int((ts-rtpTimestampOffset)/s.inc)
				require.LessOrEqual(t, lag, rtpStreamTSResetFrames)
				prev = ts
			}
		}
		require.NotZero(t, jumps)
	})

	t.Run("current timestamp is the next frame", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		s.paced(t, audio, 2, false, []byte{0})
		require.Equal(t, rtpTimestampOffset+2*s.inc, audio.GetCurrentTimestamp())
	})

	t.Run("timeline crosses the 32-bit wrap", func(t *testing.T) {
		s := newTestSSRC(48000)
		audio := s.NewStream(audioPT, 48000)
		start := s.clock.Now()
		s.write(t, audio, false, []byte{0})
		// Steps under half the 32-bit range (12.4h at 48kHz) each, ending past the wrap (24.8h).
		for _, h := range []time.Duration{12, 24, 25} {
			s.clock.t = start.Add(h * time.Hour)
			ts := s.paced(t, audio, 2, false, []byte{0})
			first := rtpTimestampOffset + uint32(uint64(h*time.Hour/frame)*uint64(s.inc))
			require.Equal(t, s.frames(first, 2), ts)
		}
	})
}

func TestSeqWriterManual(t *testing.T) {
	t.Run("events follow audio", func(t *testing.T) {
		s := newTestSSRC(48000)
		audio := s.NewStream(audioPT, 48000)
		events := s.NewManualStream(eventsPT)
		s.paced(t, audio, 10, false, []byte{0})

		require.NoError(t, events.SyncTimestamp())
		require.Equal(t, rtpTimestampOffset+10*s.inc, events.GetCurrentTimestamp(), "the wall clock frame, which audio writes next")
		for i := range 3 {
			require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, i == 0))
			require.Equal(t, rtpTimestampOffset+10*s.inc, s.last())
			require.Equal(t, rtpTimestampOffset+10*s.inc+uint32(i)*s.inc, s.write(t, audio, false, []byte{0}), "audio is not affected")
			s.clock.Add(frame)
		}
	})

	t.Run("event syncs take the wall clock frame, not the last audio frame", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		events := s.NewManualStream(eventsPT)
		s.paced(t, audio, 10, false, []byte{0}) // last audio frame is 9, wall clock is at 10
		s.clock.Add(3 * frame)
		require.NoError(t, events.SyncTimestamp())
		require.Equal(t, rtpTimestampOffset+13*s.inc, events.GetCurrentTimestamp(), "within the threshold")

		s.clock.Add(rtpStreamTSResetFrames * frame)
		require.NoError(t, events.SyncTimestamp())
		require.Equal(t, rtpTimestampOffset+23*s.inc, events.GetCurrentTimestamp(), "past the threshold")
	})

	t.Run("events without audio", func(t *testing.T) {
		s := newTestSSRC(48000)
		events := s.NewManualStream(eventsPT)

		require.NoError(t, events.SyncTimestamp())
		require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, true))
		require.Equal(t, uint32(rtpTimestampOffset), s.last(), "the first packet anchors the timeline at the offset")

		s.clock.Add(26 * frame) // tone and pause of a digit
		require.NoError(t, events.SyncTimestamp())
		require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, true))
		require.Equal(t, rtpTimestampOffset+26*s.inc, s.last())
	})

	t.Run("first event write anchors without a sync", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		events := s.NewManualStream(eventsPT)
		require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, true))
		s.clock.Add(40 * frame)
		require.Equal(t, rtpTimestampOffset+40*s.inc, s.write(t, audio, false, []byte{0}))
	})

	t.Run("audio starting after events lines up with the anchor", func(t *testing.T) {
		for _, c := range []struct {
			phase time.Duration
			first uint32
		}{
			{0, 150},
			{frame/2 - time.Millisecond, 150},
			{frame / 2, 151}, // the nearest frame
			{frame - time.Millisecond, 151},
		} {
			s := newTestSSRC(8000)
			audio := s.NewStream(audioPT, 8000)
			events := s.NewManualStream(eventsPT)
			require.NoError(t, events.SyncTimestamp())
			require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, true))
			start := s.clock.Now()

			// Audio starts off the anchor's frame grid, and wobbles by ±1ms around it.
			var ts []uint32
			for i, j := range []time.Duration{0, 1, -1, 1, -1, 0, 1, -1} {
				s.clock.t = start.Add(150*frame + c.phase + time.Duration(i)*frame + j*time.Millisecond)
				ts = append(ts, s.write(t, audio, false, []byte{0}))
			}
			require.Equal(t, s.frames(rtpTimestampOffset+c.first*s.inc, 8), ts, "phase %v", c.phase)
		}
	})

	t.Run("audio paused during events", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		events := s.NewManualStream(eventsPT)
		s.paced(t, audio, 10, false, []byte{0}) // last audio frame is 9, wall clock is at 10

		require.NoError(t, events.SyncTimestamp())
		require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, true))
		require.Equal(t, rtpTimestampOffset+10*s.inc, s.last())

		s.clock.Add(26 * frame)
		require.NoError(t, events.SyncTimestamp())
		require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, true))
		require.Equal(t, rtpTimestampOffset+36*s.inc, s.last(), "follows the wall clock without audio")

		s.clock.Add(15 * frame)
		require.Equal(t, rtpTimestampOffset+51*s.inc, s.write(t, audio, false, []byte{0}), "audio resumes on the wall clock, after the events")
	})

	t.Run("audio resuming within the threshold of an event continues from it", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		events := s.NewManualStream(eventsPT)
		s.paced(t, audio, 10, false, []byte{0})
		s.clock.Add(26 * frame)
		require.NoError(t, events.SyncTimestamp())
		require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, true))
		require.Equal(t, rtpTimestampOffset+36*s.inc, s.last())

		s.clock.Add(5 * frame)
		require.Equal(t, rtpTimestampOffset+36*s.inc, s.write(t, audio, false, []byte{0}), "not behind the event")
		require.Equal(t, rtpTimestampOffset+37*s.inc, s.write(t, audio, false, []byte{0}))
	})

	t.Run("audio resuming right after an event sync does not go behind it", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		events := s.NewManualStream(eventsPT)
		s.paced(t, audio, 10, false, []byte{0})
		s.clock.Add(20 * frame)
		require.NoError(t, events.SyncTimestamp())
		require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, true))
		ev := s.last()
		require.Equal(t, ev, s.write(t, audio, false, []byte{0}))
		require.Equal(t, ev+s.inc, s.write(t, audio, false, []byte{0}))
	})

	t.Run("timestamps are in SSRC clock units", func(t *testing.T) {
		clockRate := 48000
		s := newTestSSRC(clockRate)
		events := s.NewManualStream(eventsPT)
		timestamps := []time.Duration{
			0,
			0,
			time.Millisecond,
			9 * time.Millisecond,
			9 * time.Millisecond,
			time.Millisecond,
			time.Millisecond,
			9 * time.Millisecond,
			9 * time.Millisecond,
			time.Millisecond,
			time.Millisecond,
			9 * time.Millisecond,
			9 * time.Millisecond,
			time.Millisecond,
			time.Second,
			time.Minute,
		}
		cumulative := time.Duration(0)
		for _, timestamp := range timestamps {
			require.NoError(t, events.SyncTimestamp())
			expectedTS := rtpTimestampOffset + uint32(cumulative.Seconds()*float64(clockRate))
			require.Equal(t, expectedTS, events.GetCurrentTimestamp())
			s.clock.Add(timestamp)
		}
	})

	t.Run("alternating audio and events", func(t *testing.T) {
		s := newTestSSRC(8000)
		audio := s.NewStream(audioPT, 8000)
		events := s.NewManualStream(eventsPT)

		// Digits of 4 frames, each followed by a pause of 4 frames. Audio keeps flowing throughout.
		var audioTS []uint32
		for d := range 3 {
			require.NoError(t, events.SyncTimestamp())
			for range 4 {
				audioTS = append(audioTS, s.write(t, audio, false, []byte{0}))
				require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, false))
				require.Equal(t, rtpTimestampOffset+uint32(d*8)*s.inc, s.last(), "digit %d stays at the frame it started on", d)
				s.clock.Add(frame)
			}
			audioTS = append(audioTS, s.paced(t, audio, 4, false, []byte{0})...)
		}
		require.Equal(t, s.frames(rtpTimestampOffset, 24), audioTS, "audio is not affected")
	})

	t.Run("alternating events", func(t *testing.T) {
		s := newTestSSRC(8000)
		ev1 := s.NewManualStream(eventsPT)
		ev2 := s.NewManualStream(eventsPT + 1)

		// The streams overlap without audio: each keeps the frame it synced on.
		require.NoError(t, ev1.SyncTimestamp())
		for i := range 6 {
			if i == 2 {
				require.NoError(t, ev2.SyncTimestamp())
			}
			require.NoError(t, ev1.WritePayloadAtCurrent([]byte{0}, false))
			require.Equal(t, uint32(rtpTimestampOffset), s.last())
			if i >= 2 {
				require.NoError(t, ev2.WritePayloadAtCurrent([]byte{0}, false))
				require.Equal(t, rtpTimestampOffset+2*s.inc, s.last(), "not pulled to the other stream")
			}
			s.clock.Add(frame)
		}

		// Taking turns, each sync lands on the wall clock.
		for i := range 4 {
			st := ev1
			if i%2 == 1 {
				st = ev2
			}
			require.Equal(t, rtpTimestampOffset+uint32(6+i*3)*s.inc, s.write(t, st, true, []byte{0}))
			s.clock.Add(3 * frame)
		}
	})

	t.Run("write at current repeats the timestamp", func(t *testing.T) {
		s := newTestSSRC(8000)
		events := s.NewManualStream(eventsPT)
		s.clock.Add(time.Second)
		require.NoError(t, events.SyncTimestamp())
		for range 4 {
			require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, false))
			s.clock.Add(frame)
		}
		for _, p := range *s.buf {
			require.Equal(t, uint32(rtpTimestampOffset), p.Timestamp)
		}
	})
}

func TestSeqWriterMisuse(t *testing.T) {
	s := newTestSSRC(48000)
	require.Panics(t, func() { s.NewStream(audioPT, 8000) }, "clock rate mismatch")
	audio := s.NewStream(audioPT, 48000)
	require.Panics(t, func() { s.NewStream(audioPT+1, 48000) }, "second automatic stream")
	require.Error(t, audio.SyncTimestamp())
	require.Error(t, audio.WritePayloadAtCurrent([]byte{0}, false))
	events := s.NewManualStream(eventsPT)
	require.Error(t, events.WritePayload([]byte{0}, false))
	require.Empty(t, *s.buf)
}

func TestSeqWriterDefaultClock(t *testing.T) {
	var buf Buffer
	s := NewSeqWriter(&buf, 8000)
	audio := s.NewStream(audioPT, 8000)
	require.NoError(t, audio.WritePayload([]byte{0}, false))
	time.Sleep(15 * frame)
	require.NoError(t, audio.WritePayload([]byte{0}, false))
	require.GreaterOrEqual(t, buf[1].Timestamp, uint32(15*160))
}

func TestSeqWriterConcurrent(t *testing.T) {
	var buf Buffer
	s := NewSeqWriter(&buf, 8000)
	audio := s.NewStream(audioPT, 8000)
	events := s.NewManualStream(eventsPT)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 500 {
			_ = audio.WritePayload([]byte{0}, false)
		}
	}()
	go func() {
		defer wg.Done()
		for range 100 {
			require.NoError(t, events.SyncTimestamp())
			_ = events.WritePayloadAtCurrent([]byte{0}, true)
			_ = events.WritePayloadAtCurrent([]byte{0}, false)
		}
	}()
	wg.Wait()

	require.Len(t, buf, 700)
	var audioTS, eventTS []uint32
	for i, p := range buf {
		require.Equal(t, buf[0].SequenceNumber+uint16(i), p.SequenceNumber)
		switch p.PayloadType {
		case audioPT:
			audioTS = append(audioTS, p.Timestamp)
		case eventsPT:
			eventTS = append(eventTS, p.Timestamp)
		}
	}
	require.IsIncreasing(t, audioTS)
	require.IsNonDecreasing(t, eventTS)
}
