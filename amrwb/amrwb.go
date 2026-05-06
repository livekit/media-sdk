package amrwb

import (
	"errors"
	"fmt"
	"io"

	"github.com/livekit/media-sdk"
)

const (
	SDPName    = "AMR-WB/16000"
	SampleRate = 16000
)

func init() {
	media.RegisterCodec(media.NewAudioCodec(media.CodecInfo{
		SDPName:     SDPName,
		SampleRate:  SampleRate,
		RTPIsStatic: false,
		Priority:    -4,
		FileExt:     "amrwb",
		Disabled:    true,
	}, Decode, Encode))
}

const (
	PCMFrameSize = 320 // PCM frame size at 16kHz
	FrameSizeMax = 61  // max encoded frame size
)

type PCMFrame [PCMFrameSize]int16

var blockSizes = []byte{18, 24, 33, 37, 41, 47, 51, 59, 61, 6, 6, 0, 0, 0, 1, 1}

// BlockSize returns size of the first AMR-WB block in data.
func BlockSize(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	mode := int((data[0] >> 3) & 0x0f)
	if mode >= len(blockSizes) {
		return -1
	}
	n := int(blockSizes[mode])
	if n > len(data) {
		return -1
	}
	return n
}

type Sample []byte

func (s Sample) Size() int {
	return len(s)
}

// Blocks counts the number of AMR-WB blocks in the frame.
// It also returns the size of all valid blocks.
func (s Sample) Blocks() (sz, cnt int) {
	for len(s) > 0 {
		n := BlockSize(s)
		if n <= 0 {
			break
		}
		sz += n
		cnt++
		s = s[sz:]
	}
	return
}

func (s Sample) CopyTo(dst []byte) (int, error) {
	if len(dst) < len(s) {
		return 0, io.ErrShortBuffer
	}
	n := copy(dst, s)
	return n, nil
}

type Writer = media.WriteCloser[Sample]

func Decode(w media.PCM16Writer) Writer {
	return &Decoder{
		w: w,
		d: newDecoder(),
	}
}

type Decoder struct {
	w     media.PCM16Writer
	d     *decoder
	frame PCMFrame
	buf   media.PCM16Sample
}

func (d *Decoder) String() string {
	return fmt.Sprintf("AMR-WB(decode) -> %s", d.w)
}

func (d *Decoder) SampleRate() int {
	return SampleRate
}

func (d *Decoder) Close() error {
	d.d.Close()
	return d.w.Close()
}

func (d *Decoder) WriteSample(in Sample) error {
	d.buf = d.buf[:0]
	var blockErr error
	for len(in) > 0 {
		n := BlockSize(in)
		if n <= 0 {
			blockErr = errors.New("invalid amr-wb block")
			break
		}
		block := in[:n]
		in = in[n:]
		d.d.Decode(&d.frame, block)
		d.buf = append(d.buf, d.frame[:]...)
	}
	if len(d.buf) != 0 {
		if err := d.w.WriteSample(d.buf); err != nil {
			return err
		}
	}
	return blockErr
}

func Encode(w Writer) media.PCM16Writer {
	return &Encoder{
		w: w,
		e: newEncoder(encoder24KB), // best
	}
}

type Encoder struct {
	w    Writer
	e    *encoder
	buf  []byte
	done bool
}

func (e *Encoder) String() string {
	return fmt.Sprintf("AMR-WB(encode) -> %s", e.w)
}

func (e *Encoder) SampleRate() int {
	return SampleRate
}

func (e *Encoder) Close() error {
	return e.w.Close()
}

func (e *Encoder) WriteSample(in media.PCM16Sample) error {
	if len(in) == 0 {
		return nil
	}
	var blockErr error
	if e.done {
		// We zero-padded previous frame, but we still got data after that.
		// The stream must be normalized with FullFrames by the caller instead.
		blockErr = errors.New("amrwb: writing frame after a short previous frame")
	}
	e.buf = e.buf[:0]
	for len(in) > 0 {
		const n = PCMFrameSize
		if len(in) < n {
			// Zero pad, it's okay for the last frame only.
			// We'll return the error if we get another frame after this.
			e.done = true
			var buf PCMFrame
			copy(buf[:], in)
			in = buf[:]
		}
		frame := (*PCMFrame)(in[:n])
		in = in[n:]
		e.buf = e.e.Encode(e.buf, frame)
	}
	if len(e.buf) != 0 {
		if err := e.w.WriteSample(e.buf); err != nil {
			return err
		}
	}
	return blockErr
}
