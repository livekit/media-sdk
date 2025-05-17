// Copyright 2025 LiveKit, Inc.
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
	"time"

	"github.com/pion/rtp"

	"github.com/livekit/media-sdk/jitter"
)

const (
	jitterMaxLatency = 60 * time.Millisecond // should match mixer's target buffer size
)

func HandleJitter(h Handler) Handler {
	out := make(chan []*rtp.Packet, 10)
	handler := &jitterHandler{
		h:   h,
		buf: jitter.NewBuffer(audioDepacketizer{}, jitterMaxLatency, out),
		out: out,
		err: make(chan error, 1),
	}
	go handler.run()
	return handler
}

type jitterHandler struct {
	h   Handler
	buf *jitter.Buffer
	out chan []*rtp.Packet
	err chan error
}

func (r *jitterHandler) String() string {
	return "Jitter -> " + r.h.String()
}

func (r *jitterHandler) HandleRTP(h *rtp.Header, payload []byte) error {
	r.buf.Push(&rtp.Packet{Header: *h, Payload: payload})

	select {
	case err := <-r.err:
		return err
	default:
		return nil
	}
}

func (r *jitterHandler) run() {
	for sample := range r.out {
		for _, pkt := range sample {
			if err := r.h.HandleRTP(&pkt.Header, pkt.Payload); err != nil {
				select {
				case r.err <- err:
					// error pushed
				default:
					// error channel is full, don't block
				}
			}
		}
	}
}

type audioDepacketizer struct{}

func (d audioDepacketizer) Unmarshal(packet []byte) ([]byte, error) {
	return packet, nil
}

func (d audioDepacketizer) IsPartitionHead(payload []byte) bool {
	return true
}

func (d audioDepacketizer) IsPartitionTail(marker bool, payload []byte) bool {
	return true
}
