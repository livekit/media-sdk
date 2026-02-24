// Copyright 2024 LiveKit, Inc.
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

package dtmf

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk/rtp"
)

func TestDTMF(t *testing.T) {
	cases := []struct {
		name string
		data string
		exp  Event
	}{
		{
			name: "star end",
			data: `0a8a0820`,
			exp: Event{
				Code:   10,
				Digit:  '*',
				Volume: 10,
				End:    true,
				Dur:    2080,
			},
		},
		{
			name: "four",
			data: `040a0140`,
			exp: Event{
				Code:   4,
				Digit:  '4',
				Volume: 10,
				End:    false,
				Dur:    320,
			},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			data, err := hex.DecodeString(c.data)
			require.NoError(t, err)

			got, err := Decode(data)
			require.NoError(t, err)
			require.Equal(t, c.exp, got)

			var buf [4]byte
			n, err := Encode(buf[:], got)
			require.NoError(t, err)
			require.Equal(t, len(data), n)
			require.Equal(t, c.data, hex.EncodeToString(buf[:n]))
		})
	}
}

func TestDecodeRTPWithEnd(t *testing.T) {
	// star end: event=10, end=true, volume=10, dur=2080
	endPayload, _ := hex.DecodeString("0a8a0820")
	// four: event=4, end=false, volume=10, dur=320
	noEndPayload, _ := hex.DecodeString("040a0140")

	t.Run("end bit set, no marker - should accept", func(t *testing.T) {
		h := &rtp.Header{Marker: false, SequenceNumber: 1, Timestamp: 100}
		ev, ok := DecodeRTPWithEnd(h, endPayload)
		require.True(t, ok)
		require.Equal(t, byte('*'), ev.Digit)
		require.Equal(t, byte(10), ev.Code)
		require.True(t, ev.End)
	})

	t.Run("marker set, no end bit - should accept", func(t *testing.T) {
		h := &rtp.Header{Marker: true, SequenceNumber: 2, Timestamp: 200}
		ev, ok := DecodeRTPWithEnd(h, noEndPayload)
		require.True(t, ok)
		require.Equal(t, byte('4'), ev.Digit)
		require.Equal(t, byte(4), ev.Code)
		require.False(t, ev.End)
	})

	t.Run("both end bit and marker set - should accept", func(t *testing.T) {
		h := &rtp.Header{Marker: true, SequenceNumber: 3, Timestamp: 300}
		ev, ok := DecodeRTPWithEnd(h, endPayload)
		require.True(t, ok)
		require.Equal(t, byte('*'), ev.Digit)
		require.True(t, ev.End)
	})

	t.Run("neither end bit nor marker - should reject", func(t *testing.T) {
		h := &rtp.Header{Marker: false, SequenceNumber: 4, Timestamp: 400}
		_, ok := DecodeRTPWithEnd(h, noEndPayload)
		require.False(t, ok)
	})

	t.Run("short payload - should reject", func(t *testing.T) {
		h := &rtp.Header{Marker: false, SequenceNumber: 5, Timestamp: 500}
		_, ok := DecodeRTPWithEnd(h, []byte{0x01})
		require.False(t, ok)
	})

	t.Run("compare with DecodeRTP - no marker", func(t *testing.T) {
		h := &rtp.Header{Marker: false, SequenceNumber: 6, Timestamp: 600}
		// DecodeRTP should reject (no marker)
		_, ok := DecodeRTP(h, endPayload)
		require.False(t, ok)
		// DecodeRTPWithEnd should accept (end bit is set)
		ev, ok := DecodeRTPWithEnd(h, endPayload)
		require.True(t, ok)
		require.Equal(t, byte('*'), ev.Digit)
	})
}

func TestDTMFDelay(t *testing.T) {
	const startTime = 1242

	var buf rtp.Buffer
	w := rtp.NewSeqWriter(&buf).NewStream(101, SampleRate)
	err := Write(context.Background(), nil, w, startTime, "1w23")
	require.NoError(t, err)

	type packet struct {
		SequenceNumber uint16
		Timestamp      uint32
		Marker         bool
		Event
	}
	var (
		exp []packet
		seq uint16
		ts  uint32
	)
	const (
		packetDur = uint32(SampleRate / int(time.Second/rtp.DefFrameDur))
	)

	ts = startTime

	expectDigit := func(code byte, digit byte) {
		start := ts
		const n = 13
		for i := 0; i < n-1; i++ {
			exp = append(exp, packet{
				SequenceNumber: seq,
				Timestamp:      start, // should be the same for all events
				Marker:         i == 0,
				Event: Event{
					Code:   code,
					Digit:  digit,
					Volume: eventVolume,
					Dur:    uint16(i+1) * uint16(packetDur),
					End:    false,
				},
			})
			ts += packetDur
			seq++
		}
		// end event must be sent 3 times with the same duration
		for i := 0; i < 3; i++ {
			exp = append(exp, packet{
				SequenceNumber: seq,
				Timestamp:      start, // should be the same for all events
				Marker:         false,
				Event: Event{
					Code:   code,
					Digit:  digit,
					Volume: eventVolume,
					Dur:    uint16(n) * uint16(packetDur),
					End:    true,
				},
			})
			seq++
		}
		ts += packetDur
		// delay between digits
		ts += uint32(eventDur / (time.Second / SampleRate))
		// rounding error (12.5 events in a sec)
		ts -= packetDur / 2
	}
	expectDigit(1, '1')
	ts += SampleRate / 2 // 500ms delay
	expectDigit(2, '2')
	expectDigit(3, '3')
	var got []packet
	for _, p := range buf {
		e, err := Decode(p.Payload)
		require.NoError(t, err)
		got = append(got, packet{
			SequenceNumber: p.SequenceNumber,
			Timestamp:      p.Timestamp,
			Marker:         p.Marker,
			Event:          e,
		})
	}
	require.Equal(t, exp, got)
}
