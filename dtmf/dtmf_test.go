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
	"fmt"
	"math"
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
	for _, c := range []struct {
		audioRate  int
		eventsRate int
	}{
		{8000, 8000},
		{8000, 16000},
		{16000, 8000},
		{16000, 16000},
	} {
		t.Run(fmt.Sprintf("audio-%d-events-%d", c.audioRate, c.eventsRate), func(t *testing.T) {
			const audioPT, eventsPT = 96, 101
			var buf rtp.Buffer
			sw := rtp.NewSeqWriter(&buf)
			audio := sw.NewStream(audioPT, c.audioRate)
			events := sw.NewStream(eventsPT, c.eventsRate)

			t.Log("sending continuous audio stream")
			ctx, cancel := context.WithCancel(t.Context())
			audioDone := make(chan struct{})
			go func() {
				defer close(audioDone)
				ticker := time.NewTicker(rtp.DefFrameDur)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						_ = audio.WritePayload([]byte{0}, false)
					}
				}
			}()

			type packet struct {
				SequenceNumber uint16
				Timestamp      uint32
				Marker         bool
				Event
			}
			var exp []Event
			frameSamples := func(rate int, frameDur time.Duration) uint32 {
				return uint32(time.Duration(rate) * frameDur / time.Second)
			}
			durationToSamples := func(dur time.Duration, rate int) uint32 {
				return uint32(time.Duration(rate) * dur / time.Second)
			}
			packetDur := frameSamples(c.eventsRate, rtp.DefFrameDur)
			audioPacketDur := frameSamples(c.audioRate, rtp.DefFrameDur)

			expectDigit := func(code byte, digit byte) []Event {
				var generated []Event
				const n = 13
				for i := 0; i < n-1; i++ {
					generated = append(generated, Event{
						Code:   code,
						Digit:  digit,
						Volume: eventVolume,
						Dur:    uint16(i+1) * uint16(packetDur),
						End:    false,
					})
				}
				// end event must be sent 3 times with the same duration
				for i := 0; i < 3; i++ {
					generated = append(generated, Event{
						Code:   code,
						Digit:  digit,
						Volume: eventVolume,
						Dur:    uint16(n) * uint16(packetDur),
						End:    true,
					})
				}
				return generated
			}

			exp = append(exp, expectDigit(1, '1')...)
			exp = append(exp, expectDigit(2, '2')...)
			exp = append(exp, expectDigit(3, '3')...)
			exp = append(exp, expectDigit(4, '4')...)
			delayBetweenDigits := eventDur
			expectedDelay := []time.Duration{
				-1, // Don't care how long until the first event
				eventDur + delayBetweenDigits,
				eventDur + delayBetweenDigits + delayDur,
				eventDur + delayBetweenDigits,
			}

			time.Sleep(5 * rtp.DefFrameDur)

			t.Log("sending DTMF events")
			err := Write(t.Context(), nil, events, c.eventsRate, "12w34")
			cancel()
			<-audioDone
			require.NoError(t, err)

			t.Log("verifying DTMF events")
			var got []Event
			lastAudioTS := uint32(math.MaxUint32)
			lastEventTS := uint32(math.MaxUint32)
			for i, p := range buf {
				require.Equal(t, buf[0].SequenceNumber+uint16(i), p.SequenceNumber)
				require.Equal(t, buf[0].SSRC, p.SSRC)
				switch p.PayloadType {
				case audioPT:
					if i > 0 {
						require.Equal(t, audioPacketDur, p.Timestamp-lastAudioTS)
					}
					lastAudioTS = p.Timestamp
				case eventsPT:
					if p.Timestamp != lastEventTS {
						t.Logf("received new event timestamp, timestamp: %d, last event timestamp: %d", p.Timestamp, lastEventTS)
						require.Equal(t, lastAudioTS, p.Timestamp)
						require.True(t, p.Marker)
						expectedDurDelay := expectedDelay[0]
						expectedDelay = expectedDelay[1:]
						if expectedDurDelay >= 0 {
							expectedSampleDelay := durationToSamples(expectedDurDelay, c.audioRate)
							sampleDelay := p.Timestamp - lastEventTS
							t.Logf("expected delay: %v", expectedDurDelay)
							t.Logf("expected sample delay: %d", expectedSampleDelay)
							t.Logf("sample delay: %d", sampleDelay)
							require.GreaterOrEqual(t, expectedSampleDelay+(2*audioPacketDur), sampleDelay)
							require.LessOrEqual(t, expectedSampleDelay-audioPacketDur, sampleDelay)
						}
					} else {
						require.Equal(t, lastEventTS, p.Timestamp)
						require.False(t, p.Marker)
					}
					e, err := Decode(p.Payload)
					require.NoError(t, err)
					got = append(got, e)
					lastEventTS = p.Timestamp
				default:
					t.Fatalf("unexpected payload type: %d", p.PayloadType)
				}
			}
			require.Equal(t, exp, got)
		})
	}
}
