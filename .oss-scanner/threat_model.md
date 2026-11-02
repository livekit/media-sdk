# LiveKit media SDK threat model

`github.com/livekit/media-sdk` is a Go library of audio building blocks used by
LiveKit's SIP and media services. It parses and
negotiates SDP (including SDES-SRTP crypto attributes), receives and sends RTP
and SRTP, reorders packets in a jitter buffer, decodes RFC 4733 DTMF events,
and encodes and decodes G.711, G.722, Opus, and AMR-WB. It also resamples,
mixes, and generates tones. Most of its input comes straight from remote SIP
and RTP peers on the network.

## Scope and trust boundaries

Audit the Go on `main`. Prioritise:

- `sdp/`: `Parse`, `ParseOffer`, `ParseAnswer`, `ParseMedia`, `SelectAudio`,
  `SelectCrypto`, and the `a=crypto` parsers (`parseSRTPProfile`, `parseMKI`,
  `parseLifetime`).
- `rtp/`: `NewSession` and `AcceptStream` (per-SSRC stream demux), `Mux`,
  `HandleLoop`, and the payload handlers in `codecs.go`.
- `srtp/`: the SRTP session wrapper over `pion/srtp` and key and profile
  handling.
- `jitter/`: packet reordering, sequence-number wrap, and loss accounting.
- `dtmf/`: `Decode`, `DecodeRTP`, and `DecodeRTPWithEnd`.
- Decoders that consume network payloads: `g711/`, `g722/`, `opus/`, and
  `amrwb/`.
- The cgo boundary (see below).

Lower priority: encoders, `mixer/`, `tones/`, `webm/` (output only), `res/`
(embedded prompts), and resampling, which handle locally produced audio.

Untrusted input: SDP bodies from remote SIP peers, and every RTP/RTCP packet
that reaches a listening port. The sockets from `rtp.ListenUDPPortRange` are
unconnected, so any host that can reach the port can send packets, not only
the negotiated peer. With SRTP, a packet stays untrusted until `pion/srtp`
has authenticated it.

Trusted: the embedding application, its configuration (codec set, port
ranges, local SRTP profiles and keys), and PCM audio it produces.

cgo boundary: `opus/` calls libopus directly (`opus_packet_get_nb_channels`)
and through `gopkg.in/hraban/opus.v2`; `amrwb/` decodes through
`github.com/livekit/amrwb-cgo`, which compiles bundled C; and
`resample_soxr.go` calls libsoxr when cgo is enabled. Pointers, lengths, and
channel counts derived from network packets cross into C here, so Go's memory
safety does not protect these calls. Report bugs in how this library sizes
buffers or passes untrusted data to C here. Report bugs inside libopus, libsoxr,
or the bundled AMR-WB C upstream, unless this library's inputs trigger them.

LiveKit server (`livekit/livekit`) is enrolled separately, and the protocol
library (`livekit/protocol`) has its own threat model.

## Security properties

- **Parsing:** SDP, `a=crypto` attributes, RTP headers and payloads, DTMF
  events, and codec frames must not panic, read or write out of bounds, or
  loop on malformed input.
- **Memory safety at cgo calls:** every call into C must pass a non-empty
  buffer and correct lengths, and output buffers must be large enough for the
  maximum frame the C library may produce for the given input, including
  channel counts taken from the packet.
- **SRTP:** negotiated keys and salts must have the lengths the selected
  profile requires. Only supported profiles may be selected. Unauthenticated
  packets must not be delivered as media once SRTP is in use.
- **Resource use:** one peer must not make the library allocate without bound,
  for example by spraying SSRCs into `AcceptStream`, by sequence-number jumps in
  the jitter buffer, or by oversized SDP.
- **Stream separation:** packets from one SSRC must not be delivered to
  another SSRC's stream.

## Severity guidance

- **Critical:** memory corruption at a cgo call, or remote code execution,
  reachable from an RTP packet or SDP body.
- **High:** a remote panic that crashes the embedding process from one packet
  or SDP body; SRTP downgrade, or acceptance of unauthenticated media in an
  SRTP session.
- **Medium:** unbounded memory or goroutine growth from one peer's traffic;
  injecting audio or DTMF into a call from an off-path host on a plain RTP
  session.
- **Low / not a finding:** bugs in encoders, mixing, resampling, or tones that
  need application-supplied input; audio quality or jitter tuning; packet loss
  and reordering handled as designed; plain RTP being unauthenticated by design.

## Testing

The checkout is at `/src`. Go dependencies are cached. `GOPROXY=off` and
`GOSUMDB=off` prevent downloads. The cgo libraries (libopus, libopusfile,
libsoxr) are installed. No external services are needed; the `rtp` tests bind
local UDP ports only.

```sh
cd /src
go test -count=1 ./...
go test -race -count=1 ./rtp/... ./jitter/... ./sdp/... ./dtmf/... ./opus/... ./amrwb/...
```

There are no fuzz tests. For parsers, add a `Fuzz` target in the affected
package, for example over `sdp.Parse`, `dtmf.Decode`, or the decoder
`WriteSample` methods.

## Reports

State the attacker's required access (none beyond reaching the RTP port, a SIP
peer that can send SDP, or a negotiated SRTP peer), the entry point, and the
demonstrated impact. Provide a minimal reproducer, preferably a Go test in the
affected package.
