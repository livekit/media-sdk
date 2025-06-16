package opus

import (
	"time"

	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/media-sdk/rtp"
	"github.com/livekit/protocol/logger"
)

const (
	opusJitterMaxLatency = 60 * time.Millisecond
	opusDTXFrameLength   = 1
)

func HandleOpusJitter(h rtp.Handler, pcmWriter media.PCM16Writer, targetChannels int) rtp.Handler {
	handler := &opusJitterHandler{
		h:      h,
		err:    make(chan error, 1),
		logger: logger.GetLogger(),
	}

	dec, err := Decode(pcmWriter, targetChannels, handler.logger)
	if err != nil {
		handler.err <- err
		return handler
	}
	handler.decoder = dec.(*decoder)

	handler.buf = jitter.NewBuffer(
		rtp.AudioDepacketizer{},
		opusJitterMaxLatency,
		func(packets []*rtp.Packet) {
			for _, p := range packets {
				handler.handleRTP(p)
			}
		},
		jitter.WithPacketLossHandler(func() {
			handler.pendingLoss = true
		}),
	)

	return handler
}

type opusJitterHandler struct {
	h           rtp.Handler
	buf         *jitter.Buffer
	decoder     *decoder
	err         chan error
	logger      logger.Logger
	nextPacket  *rtp.Packet
	lastPacket  *rtp.Packet
	pendingLoss bool
}

func (r *opusJitterHandler) String() string {
	return "OpusJitter -> " + r.h.String()
}

func (r *opusJitterHandler) HandleRTP(h *rtp.Header, payload []byte) error {
	r.buf.Push(&rtp.Packet{Header: *h, Payload: payload})
	select {
	case err := <-r.err:
		return err
	default:
		return nil
	}
}

func (r *opusJitterHandler) handleRTP(p *rtp.Packet) {
	isDtx := len(p.Payload) == opusDTXFrameLength

	// Not sure what to do if we have a pending loss and the packet is DTX.
	if r.pendingLoss && !isDtx {
		// Store the next packet for FEC
		r.nextPacket = p
		r.handlePacketLoss()
		r.pendingLoss = false
	}

	if r.lastPacket != nil && (isDtx || len(r.lastPacket.Payload) == opusDTXFrameLength) {
		silenceSamples := int(p.Timestamp - r.lastPacket.Timestamp)
		if silenceSamples > 0 {
			silenceBuf := make([]int16, silenceSamples*r.decoder.targetChannels)
			if err := r.decoder.w.WriteSample(silenceBuf); err != nil {
				r.logger.Warnw("failed to write silence", err)
			}
		}

		if isDtx {
			r.lastPacket = p
			return
		}
	}

	if err := r.decoder.WriteSample(p.Payload); err != nil {
		r.logger.Warnw("failed to decode packet", err)
	}

	r.lastPacket = p
}

func (r *opusJitterHandler) handlePacketLoss() {
	if r.decoder == nil || r.nextPacket == nil {
		return
	}

	lostPackets := int(r.nextPacket.SequenceNumber - r.buf.LastSequenceNumber() - 1)
	if lostPackets <= 0 {
		return
	}

	lastTs := r.buf.LastTimestamp()
	nextTs := r.nextPacket.Timestamp

	totalSamples := int(nextTs - lastTs)
	if totalSamples <= 0 {
		return
	}

	samplesPerPacket := totalSamples / lostPackets

	if lostPackets > 1 {
		// For mono audio, if we call DecodePLC right after a
		// SFU generated mono silence, the concealment might not be proper.
		// But, we need to pass the buffer for the exact duration of the lost audio.
		plcSamples := samplesPerPacket * (lostPackets - 1) * r.decoder.lastChannels
		buf := make([]int16, plcSamples)

		err := r.decoder.DecodePLC(buf)
		if err != nil {
			r.logger.Warnw("failed to recover lost packets with PLC", err)
			return
		}
		_ = r.decoder.w.WriteSample(buf)
	}

	// Should we reset for the next packet before calling DecodeFEC?
	// This will update the decoder's state for the next packet so it might help.
	// But, it might also cause some issues if the next packet is SFU generated silence.
	channels, err := r.decoder.resetForSample(r.nextPacket.Payload)
	if err != nil {
		r.logger.Warnw("failed to reset decoder for FEC", err)
		return
	}

	buf := make([]int16, samplesPerPacket*channels)
	err = r.decoder.DecodeFEC(r.nextPacket.Payload, buf)
	if err != nil {
		r.logger.Warnw("failed to recover last lost packet with FEC", err)
		return
	}
	_ = r.decoder.w.WriteSample(buf)
}

func (r *opusJitterHandler) Close() error {
	if r.decoder != nil {
		return r.decoder.Close()
	}
	return nil
}
