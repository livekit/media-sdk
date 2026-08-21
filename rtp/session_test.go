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
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/logger"
)

const (
	testStreamSSRC   = 0x11223344
	testSentinelSSRC = 0x55667788
	testPayloadType  = 8   // PCMA
	testPayloadSize  = 160 // 20ms of PCMA; a realistic size, and a wider copy() race window
	testClockPerPkt  = 160
)

func newRTPPacket(t *testing.T, ssrc uint32, sequenceNumber int) []byte {
	t.Helper()
	require.Less(t, sequenceNumber, 1<<16, "index must fit the 16-bit sequence number")
	p := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    testPayloadType,
			SequenceNumber: uint16(sequenceNumber),
			Timestamp:      uint32(sequenceNumber) * testClockPerPkt,
			SSRC:           ssrc,
		},
		Payload: payloadForSequenceNumber(sequenceNumber),
	}
	buf, err := p.Marshal()
	require.NoError(t, err)
	return buf
}

func payloadForSequenceNumber(sequenceNumber int) []byte {
	payload := make([]byte, testPayloadSize)
	for off := 0; off < len(payload); off += 2 {
		binary.BigEndian.PutUint16(payload[off:], uint16(sequenceNumber))
	}
	return payload
}

func sequenceNumberFromPayload(payload []byte) int {
	if len(payload) < 2 || len(payload)%2 != 0 {
		return -1
	}
	return int(binary.BigEndian.Uint16(payload))
}

func verifyRTPPacket(h *rtp.Header, payload []byte) error {
	// Ensure that the header is properly written to.
	if h.Version == 0 && h.PayloadType == 0 && h.SSRC == 0 && h.SequenceNumber == 0 && h.Timestamp == 0 {
		return fmt.Errorf("header never written: ReadRTP reported %d bytes carrying packet %d, but left the header zeroed",
			len(payload), sequenceNumberFromPayload(payload))
	}

	if h.Version != 2 || h.PayloadType != testPayloadType || h.SSRC != testStreamSSRC {
		return fmt.Errorf("torn header: got version=%d pt=%d ssrc=%#x, want version=2 pt=%d ssrc=%#x (seq=%d ts=%d)",
			h.Version, h.PayloadType, h.SSRC, testPayloadType, uint32(testStreamSSRC), h.SequenceNumber, h.Timestamp)
	}

	sequenceNumber := int(h.SequenceNumber)
	if exp := uint32(sequenceNumber) * testClockPerPkt; h.Timestamp != exp {
		return fmt.Errorf("torn header: seq=%d implies ts=%d, got ts=%d - header fields came from two packets",
			sequenceNumber, exp, h.Timestamp)
	}

	if len(payload) != testPayloadSize {
		return fmt.Errorf("unexpected payload length: header says seq=%d, but ReadRTP returned %d payload bytes, want %d",
			sequenceNumber, len(payload), testPayloadSize)
	}

	gotSequeneceNumber := sequenceNumberFromPayload(payload)
	if gotSequeneceNumber != sequenceNumber {
		return fmt.Errorf("mixed payload: header says seq=%d, but the buffer holds packet %d",
			sequenceNumber, gotSequeneceNumber)
	}
	return nil
}

func TestSessionZeroCopyHandoff(t *testing.T) {
	numPackets := 8000
	cli, srv := net.Pipe()
	defer cli.Close()
	require.NoError(t, srv.SetReadDeadline(time.Now().Add(30*time.Second)))

	sess := NewSession(logger.GetLogger(), srv)
	defer sess.Close()

	sent := make([][]byte, 0, numPackets+1)
	for i := range numPackets {
		sent = append(sent, newRTPPacket(t, testStreamSSRC, i))
	}
	sent = append(sent, newRTPPacket(t, testSentinelSSRC, 0))

	var wg sync.WaitGroup
	wg.Go(func() {
		for _, p := range sent {
			if _, err := cli.Write(p); err != nil {
				return
			}
		}
	})

	r, ssrc, err := sess.AcceptStream()
	require.NoError(t, err)
	require.EqualValues(t, uint32(testStreamSSRC), ssrc)

	allReceived := make(chan struct{})
	wg.Go(func() {
		defer close(allReceived)
		for {
			_, ssrc, err := sess.AcceptStream()
			if err != nil || ssrc == testSentinelSSRC {
				return
			}
		}
	})

	var delivered int
	var verifyErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		var h rtp.Header
		buf := make([]byte, MTUSize+1)
		for {
			h = rtp.Header{}
			n, err := r.ReadRTP(&h, buf)
			if err != nil {
				return
			}
			delivered++
			if verifyErr = verifyRTPPacket(&h, buf[:n]); verifyErr != nil {
				return
			}
		}
	}()

	wg.Wait()
	sess.Close() // releases the reader with io.EOF
	<-done

	require.NoError(t, verifyErr, "ReadRTP delivered a corrupted packet")
	require.NotZero(t, delivered, "no packets were delivered")
	t.Logf("delivered %d/%d packets", delivered, numPackets)
}
