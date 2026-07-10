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

package opus

import (
	"errors"
	"fmt"
	"io"
	"time"

	"gopkg.in/hraban/opus.v2"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/media-sdk/webm"
)

/*
#cgo pkg-config: opus
#include <opus.h>
*/
import "C"

type Sample []byte

func (s Sample) Size() int {
	return len(s)
}

func (s Sample) CopyTo(dst []byte) (int, error) {
	if len(dst) < len(s) {
		return 0, io.ErrShortBuffer
	}
	n := copy(dst, s)
	return n, nil
}

type Writer = media.WriteCloser[Sample]

const (
	SDPNameOnly    = "opus"
	SDPNameAndRate = SDPNameOnly + "/48000/2"
	SDPName        = SDPNameAndRate // Deprecated: use SDPNameOnly or SDPNameAndRate
	SampleRate     = 48000
)

// init registers Opus as a SIP/SDP codec so it can be offered and negotiated on
// the RTP (telephone) leg, not only the internal WebRTC leg. Mono, 48 kHz,
// dynamic payload type. The RTP media path is fully generic (rtp.EncodePCM /
// rtp.DecodePCM drive CodecInfo + AudioCodec), so registration is all that is
// needed for end-to-end use.
func init() {
	media.RegisterCodec(media.NewAudioCodec(media.CodecInfo{
		SDPName:      SDPNameAndRate,
		SampleRate:   SampleRate,
		RTPClockRate: SampleRate,
		RTPIsStatic:  false,
		Priority:     100,
		FileExt:      "opus",
	}, sipDecode, sipEncode))
}

// sipDecode decodes Opus RTP payloads to mono PCM16 for the SIP leg.
func sipDecode(w media.PCM16Writer) media.WriteCloser[Sample] {
	d, err := Decode(w, 1, logger.GetLogger())
	if err != nil {
		logger.GetLogger().Errorw("opus SIP decoder init failed", err)
		return failWriter[Sample]{err: err, sr: w.SampleRate()}
	}
	return d
}

// sipEncode encodes mono PCM16 to Opus for the SIP leg, tuned for telephony:
// a generous bitrate + max complexity for a rich voice, plus inband FEC with a
// packet-loss estimate so cellular handoffs degrade gracefully. Kept separate
// from Encode so the WebRTC leg, which runs its own bandwidth estimation, is
// untouched. Setter errors are non-fatal (encoder keeps library defaults).
func sipEncode(w Writer) media.PCM16Writer {
	enc, err := opus.NewEncoder(w.SampleRate(), 1, opus.AppVoIP)
	if err != nil {
		logger.GetLogger().Errorw("opus SIP encoder init failed", err)
		return failWriter[media.PCM16Sample]{err: err, sr: w.SampleRate()}
	}
	lg := logger.GetLogger()
	for _, set := range []struct {
		name string
		fn   func() error
	}{
		{"bitrate", func() error { return enc.SetBitrate(64000) }},
		{"complexity", func() error { return enc.SetComplexity(10) }},
		{"fec", func() error { return enc.SetInBandFEC(true) }},
		{"loss", func() error { return enc.SetPacketLossPerc(10) }},
	} {
		if err := set.fn(); err != nil {
			lg.Warnw("opus SIP encoder tuning failed", err, "param", set.name)
		}
	}
	return &encoder{
		w:      w,
		enc:    enc,
		buf:    make(Sample, w.SampleRate()/rtp.DefFramesPerSec),
		logger: lg,
	}
}

// failWriter is a fail-closed sink returned when per-call codec init fails, so a
// single bad call surfaces an error downstream instead of panicking the daemon.
type failWriter[T any] struct {
	err error
	sr  int
}

func (f failWriter[T]) String() string     { return "opus(init-failed)" }
func (f failWriter[T]) SampleRate() int     { return f.sr }
func (f failWriter[T]) WriteSample(T) error { return f.err }
func (f failWriter[T]) Close() error        { return f.err }


func Decode(w media.PCM16Writer, targetChannels int, logger logger.Logger) (Writer, error) {
	if targetChannels != 1 && targetChannels != 2 {
		return nil, fmt.Errorf("opus decoder only supports mono or stereo output")
	}

	return &decoder{
		w:              w,
		targetChannels: targetChannels,
		lastChannels:   targetChannels,
		logger:         logger,
	}, nil
}

func Encode(w Writer, channels int, logger logger.Logger) (media.PCM16Writer, error) {
	enc, err := opus.NewEncoder(w.SampleRate(), channels, opus.AppVoIP)
	if err != nil {
		return nil, err
	}
	return &encoder{
		w:      w,
		enc:    enc,
		buf:    make([]byte, w.SampleRate()/rtp.DefFramesPerSec*channels),
		logger: logger,
	}, nil
}

type decoder struct {
	w      media.PCM16Writer
	dec    *opus.Decoder
	buf    media.PCM16Sample
	buf2   media.PCM16Sample
	logger logger.Logger

	targetChannels int
	lastChannels   int

	successiveErrorCount int
}

func (d *decoder) String() string {
	return fmt.Sprintf("OPUS(decode) -> %s", d.w)
}

func (d *decoder) SampleRate() int {
	return d.w.SampleRate()
}

func (d *decoder) WriteSample(in Sample) error {
	if len(in) == 0 {
		return nil
	}
	channels, err := d.resetForSample(in)
	if err != nil {
		return err
	}

	n, err := d.dec.Decode(in, d.buf)
	if err != nil {
		// Some workflows (concatenating opus files) can cause a suprious decoding error, so ignore small amount of corruption errors
		if !errors.Is(err, opus.ErrInvalidPacket) || d.successiveErrorCount >= 5 {
			return err
		}
		d.logger.Debugw("opus decoder failed decoding a sample")
		d.successiveErrorCount++
		return nil
	}
	d.successiveErrorCount = 0

	returnData := d.buf[:n*channels]
	if channels < d.targetChannels {
		n2 := len(returnData) * 2
		if len(d.buf2) < n2 {
			d.buf2 = make(media.PCM16Sample, n2)
		}
		media.MonoToStereo(d.buf2, returnData)
		returnData = d.buf2[:n2]
	} else if channels > d.targetChannels {
		n2 := len(returnData) / 2
		if len(d.buf2) < n2 {
			d.buf2 = make(media.PCM16Sample, n2)
		}
		media.StereoToMono(d.buf2, returnData)
		returnData = d.buf2[:n2]
	}

	return d.w.WriteSample(returnData)
}

func (d *decoder) resetForSample(in Sample) (int, error) {
	channels := int(C.opus_packet_get_nb_channels((*C.uchar)(&in[0])))

	if d.dec == nil || d.lastChannels != channels {
		dec, err := opus.NewDecoder(d.w.SampleRate(), channels)
		if err != nil {
			d.logger.Errorw("opus decoder failed to reset", err)
			return 0, err
		}
		d.dec = dec

		d.buf = make([]int16, d.w.SampleRate()/rtp.DefFramesPerSec*channels)
		d.lastChannels = channels
	}

	return channels, nil
}

func (d *decoder) Close() error {
	return d.w.Close()
}

type encoder struct {
	w      Writer
	enc    *opus.Encoder
	buf    Sample
	logger logger.Logger
}

func (e *encoder) String() string {
	return fmt.Sprintf("OPUS(encode) -> %s", e.w)
}

func (e *encoder) SampleRate() int {
	return e.w.SampleRate()
}

func (e *encoder) WriteSample(in media.PCM16Sample) error {
	n, err := e.enc.Encode(in, e.buf)
	if err != nil {
		return err
	}
	return e.w.WriteSample(e.buf[:n])
}

func (e *encoder) Close() error {
	return e.w.Close()
}

func NewWebmWriter(w io.WriteCloser, sampleRate int, channels int, sampleDur time.Duration) media.WriteCloser[Sample] {
	return webm.NewWriter[Sample](w, "A_OPUS", channels, sampleRate, sampleDur)
}
