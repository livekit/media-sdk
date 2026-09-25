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

package sdp_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/amrwb"
	. "github.com/livekit/media-sdk/sdp"
)

type amrPacketCapture struct{ packets [][]byte }

func (w *amrPacketCapture) String() string { return "AMRPacketCapture" }
func (w *amrPacketCapture) Close() error   { return nil }
func (w *amrPacketCapture) WriteRaw(p []byte) error {
	w.packets = append(w.packets, append([]byte(nil), p...))
	return nil
}

func TestAMRWBAnswerFmtpWhitespace(t *testing.T) {
	for _, c := range []struct {
		name string
		fmtp string
	}{
		{"compact", "octet-align=0;mode-set=0,1,2;max-red=0;mode-change-capability=2"},
		// Some carriers put a space after each semicolon.
		{"spaces", "octet-align=0; mode-set=0,1,2; max-red=0; mode-change-capability=2"},
		{"tabs", "octet-align=0;\tmode-set=0,1,2;\tmax-red=0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			codecs := media.NewCodecSet()
			codecs.SetEnabled(amrwb.SDPNameOnly, true)
			data := strings.Join([]string{
				"v=0", "o=- 1 1 IN IP4 192.0.2.1", "s=-", "c=IN IP4 192.0.2.1", "t=0 0",
				"m=audio 5004 RTP/AVP 102", "a=rtpmap:102 AMR-WB/16000",
				"a=fmtp:102 " + c.fmtp, "a=ptime:20", "a=sendrecv", "",
			}, "\r\n")
			offer, err := ParseOfferWith(codecs, []byte(data))
			require.NoError(t, err)
			answer, config, err := offer.Answer(netip.MustParseAddr("192.0.2.2"), 5006, EncryptionNone)
			require.NoError(t, err)
			answerData, err := answer.SDP.Marshal()
			require.NoError(t, err)
			// RFC 4867 8.3.1: the answer returns the offered mode-set unmodified.
			require.Contains(t, string(answerData), "a=fmtp:102 octet-align=0;mode-set=0,1,2\r\n")

			_, create, ok := config.Audio.Codec.Supports(config.Audio.Info.CodecConfig)
			require.True(t, ok)
			codec := create().(media.AudioCodec)
			capture := new(amrPacketCapture)
			enc := codec.EncodeBytes(capture)
			defer enc.Close()
			require.NoError(t, enc.WriteSample(make(media.PCM16Sample, 320)))
			require.Len(t, capture.packets, 1)
			packet := capture.packets[0]
			require.GreaterOrEqual(t, len(packet), 2)
			// Bandwidth-efficient payload header: CMR(4) F(1) FT(4) Q(1).
			frameType := int((packet[0]&7)<<1 | packet[1]>>7)
			require.Equal(t, 2, frameType, "encoded frame type must be within the offered mode-set, got packet %x", packet)
		})
	}
}
