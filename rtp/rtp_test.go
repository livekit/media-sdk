package rtp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRTPStreams checks if sub-streams with one SSRC correctly synchronize their timestamps.
// For example, one stream may stop and resume after some time. Timestamps must adjust accordingly.
func TestRTPStreams(t *testing.T) {
	t.Run("alternating", func(t *testing.T) {
		var buf Buffer
		w := NewSeqWriter(&buf)

		const N = 5
		s1 := w.NewStream(0, 8000)
		s2 := w.NewStream(101, 8000)

		// Both stream emit frames after each other.
		// Timestamps should be in-sync.
		for i := range N {
			s1.WritePayload([]byte{byte(i)}, false)
			s2.WritePayload([]byte{byte(i)}, i == 0)
		}

		type packet struct {
			TS   uint32
			Type byte
			Ind  byte
		}

		var got []packet
		for _, p := range buf {
			got = append(got, packet{
				TS:   p.Timestamp,
				Type: p.PayloadType,
				Ind:  p.Payload[0],
			})
		}
		var exp []packet
		for i := range N {
			exp = append(exp,
				packet{
					TS:   160 * uint32(i),
					Type: 0,
					Ind:  byte(i),
				},
				packet{
					TS:   160 * uint32(i),
					Type: 101,
					Ind:  byte(i),
				},
			)
		}
		require.Equal(t, exp, got)
	})

	t.Run("alternating batches", func(t *testing.T) {
		var buf Buffer
		w := NewSeqWriter(&buf)

		const N, batch = 5, 5
		s1 := w.NewStream(0, 8000)
		s2 := w.NewStream(101, 8000)

		// Streams emit frames in short bursts.
		// Timestamps will still be in-sync between batches.
		for i := range N {
			for j := range batch {
				ind := i*batch + j
				s1.WritePayload([]byte{byte(ind)}, false)
			}
			for j := range batch {
				ind := i*batch + j
				s2.WritePayload([]byte{byte(ind)}, i == 0 && j == 0)
			}
		}

		type packet struct {
			TS   uint32
			Type byte
			Ind  byte
		}

		var got []packet
		for _, p := range buf {
			got = append(got, packet{
				TS:   p.Timestamp,
				Type: p.PayloadType,
				Ind:  p.Payload[0],
			})
		}
		var exp []packet
		for i := range N {
			for j := range batch {
				ind := i*batch + j
				exp = append(exp, packet{
					TS:   160 * uint32(ind),
					Type: 0,
					Ind:  byte(ind),
				})
			}
			for j := range batch {
				ind := i*batch + j
				exp = append(exp, packet{
					TS:   160 * uint32(ind),
					Type: 101,
					Ind:  byte(ind),
				})
			}
		}
		require.Equal(t, exp, got)
	})

	t.Run("alternating batches dtmf", func(t *testing.T) {
		var buf Buffer
		w := NewSeqWriter(&buf)

		const N, batch = 5, 5
		s1 := w.NewStream(0, 8000)
		s2 := w.NewStream(101, 8000)

		// Streams emit frames in short bursts.
		// This is a variation of the test that uses DTMF-like API,
		// where only the first packet of a batch increments the timestamp.
		// The timestamps of the first packets should still be in-sync between streams.
		for i := range N {
			for j := range batch {
				ind := i*batch + j
				s1.WritePayload([]byte{byte(ind)}, false)
			}
			for j := range batch {
				ind := i*batch + j
				s2.WritePayloadAtCurrent([]byte{byte(ind)}, j == 0)
			}
			s2.DelayN(batch)
		}

		type packet struct {
			TS   uint32
			Type byte
			Ind  byte
		}

		var got []packet
		for _, p := range buf {
			got = append(got, packet{
				TS:   p.Timestamp,
				Type: p.PayloadType,
				Ind:  p.Payload[0],
			})
		}
		var exp []packet
		for i := range N {
			for j := range batch {
				ind := i*batch + j
				exp = append(exp, packet{
					TS:   160 * uint32(ind),
					Type: 0,
					Ind:  byte(ind),
				})
			}
			for j := range batch {
				ind := i*batch + j
				exp = append(exp, packet{
					TS:   160 * uint32(i*batch),
					Type: 101,
					Ind:  byte(ind),
				})
			}
		}
		require.Equal(t, exp, got)
	})

	t.Run("one after another", func(t *testing.T) {
		var buf Buffer
		w := NewSeqWriter(&buf)

		const N = 5 + rtpStreamTSResetFrames
		s1 := w.NewStream(0, 8000)
		s2 := w.NewStream(101, 8000)

		// One stream emits all frames, followed by another one.
		// Timestamps on a second stream should synchronize
		// with the last TS of the first stream.
		for i := range N {
			s2.WritePayload([]byte{byte(i)}, i == 0)
		}
		for i := range N {
			s1.WritePayload([]byte{byte(i)}, false)
		}

		type packet struct {
			TS   uint32
			Type byte
			Ind  byte
		}

		var got []packet
		for _, p := range buf {
			got = append(got, packet{
				TS:   p.Timestamp,
				Type: p.PayloadType,
				Ind:  p.Payload[0],
			})
		}
		var exp []packet
		for i := range N {
			exp = append(exp, packet{
				TS:   160 * uint32(i),
				Type: 101,
				Ind:  byte(i),
			})
		}
		for i := range N {
			exp = append(exp, packet{
				TS:   160*(N-1) + 160*uint32(i),
				Type: 0,
				Ind:  byte(i),
			})
		}
		require.Equal(t, exp, got)
	})

	t.Run("one after another dtmf", func(t *testing.T) {
		var buf Buffer
		w := NewSeqWriter(&buf)

		const N = 5 + rtpStreamTSResetFrames
		s1 := w.NewStream(0, 8000)
		s2 := w.NewStream(101, 8000)

		// One stream emits all frames, followed by another one.
		// Timestamps on a second stream should synchronize
		// with the last TS of the first stream.
		// Except that the test uses DTMF-like API where
		// the timestamp is frozen at start for all frames.
		for i := range N {
			s2.WritePayloadAtCurrent([]byte{byte(i)}, i == 0)
		}
		s2.DelayN(N)
		for i := range N {
			s1.WritePayload([]byte{byte(i)}, false)
		}

		type packet struct {
			TS   uint32
			Type byte
			Ind  byte
		}

		var got []packet
		for _, p := range buf {
			got = append(got, packet{
				TS:   p.Timestamp,
				Type: p.PayloadType,
				Ind:  p.Payload[0],
			})
		}
		var exp []packet
		for i := range N {
			exp = append(exp, packet{
				TS:   0,
				Type: 101,
				Ind:  byte(i),
			})
		}
		for i := range N {
			exp = append(exp, packet{
				TS:   160*N + 160*uint32(i),
				Type: 0,
				Ind:  byte(i),
			})
		}
		require.Equal(t, exp, got)
	})
}

// TestRTPStreamsWallClock checks that timestamps follow the wall clock when frames stop being written,
// e.g. audio muted while DTMF events are sent.
func TestRTPStreamsWallClock(t *testing.T) {
	const audioRate, eventsRate = 48000, 8000
	const inc = uint32(audioRate / DefFramesPerSec)

	// writeFrames writes n audio frames paced in real time and returns the last timestamp written.
	writeFrames := func(buf *Buffer, s *Stream, n int) uint32 {
		for range n {
			require.NoError(t, s.WritePayload([]byte{0}, false))
			time.Sleep(DefFrameDur)
		}
		return (*buf)[len(*buf)-1].Timestamp
	}
	// requireFramesAfter checks that ts is gap after last, less one frame of jitter slack,
	// allowing for oversleeping.
	requireFramesAfter := func(last, ts uint32, gap time.Duration) {
		frames := uint32(gap/DefFrameDur) - 1
		require.GreaterOrEqual(t, ts, last+frames*inc)
		require.LessOrEqual(t, ts, last+(frames+3)*inc)
	}

	t.Run("audio resumes after a gap", func(t *testing.T) {
		var buf Buffer
		audio := NewSeqWriter(&buf, WithWallclockAdjusted(true)).NewStream(96, audioRate)
		last := writeFrames(&buf, audio, 5)

		const gap = 30 * DefFrameDur
		time.Sleep(gap - DefFrameDur) // writeFrames already slept one frame
		require.NoError(t, audio.WritePayload([]byte{0}, false))
		requireFramesAfter(last, buf[len(buf)-1].Timestamp, gap)
	})

	t.Run("catch-up burst is not counted twice", func(t *testing.T) {
		var buf Buffer
		audio := NewSeqWriter(&buf, WithWallclockAdjusted(true)).NewStream(96, audioRate)
		last := writeFrames(&buf, audio, 5)

		// A stall shorter than the drift threshold, followed by back-to-back catch-up writes.
		time.Sleep(4 * DefFrameDur)
		for range 5 {
			require.NoError(t, audio.WritePayload([]byte{0}, false))
		}
		for i, p := range buf[len(buf)-5:] {
			require.Equal(t, last+uint32(i+1)*inc, p.Timestamp)
		}
	})

	t.Run("events without audio", func(t *testing.T) {
		var buf Buffer
		sw := NewSeqWriter(&buf, WithWallclockAdjusted(true))
		audio := sw.NewStream(96, audioRate)
		events := sw.NewStream(101, eventsRate)
		last := writeFrames(&buf, audio, 5)

		const gap = 30 * DefFrameDur
		time.Sleep(gap - DefFrameDur)
		require.NoError(t, events.WritePayloadAtCurrent([]byte{0}, true))
		evTS := buf[len(buf)-1].Timestamp
		requireFramesAfter(last, evTS, gap)

		// Audio resuming after the event must not go back behind it.
		require.NoError(t, audio.WritePayload([]byte{0}, false))
		require.GreaterOrEqual(t, buf[len(buf)-1].Timestamp, evTS)
	})
}
