package media

import (
	"errors"
	"fmt"
	"slices"
	"sync"
)

type sample interface {
	byte | int8 | int16 | int32 | int64 | float32 | float64
}

// FullFrames creates a writer that only writes full frames of a given size to the underlying writer (except the last one).
func FullFrames[T ~[]S, S sample](w WriteCloser[T], frameSize int) WriteCloser[T] {
	if frameSize <= 0 {
		panic("invalid frame size")
	}
	return &fullFrameBuffer[T, S]{
		w:         w,
		frameSize: frameSize,
		buf:       make([]S, 0, frameSize),
	}
}

type fullFrameBuffer[T ~[]S, S sample] struct {
	frameSize int
	mu        sync.Mutex
	w         WriteCloser[T]
	buf       []S
}

func (b *fullFrameBuffer[T, S]) String() string {
	return fmt.Sprintf("FullFrameBuf(%d) -> %s", b.frameSize, b.w)
}
func (b *fullFrameBuffer[T, S]) SampleRate() int {
	return b.w.SampleRate()
}

func (b *fullFrameBuffer[T, S]) WriteSample(in T) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, in...)
	return b.flush(false)
}

func (b *fullFrameBuffer[T, S]) flush(force bool) error {
	it := b.buf
	defer func() {
		if len(it) == 0 {
			b.buf = b.buf[:0]
		} else if dn := len(b.buf) - len(it); dn > 0 {
			b.buf = slices.Delete(b.buf, 0, dn)
		}
	}()
	for len(it)/b.frameSize > 0 {
		frame := it[:b.frameSize]
		it = it[len(frame):]
		if err := b.w.WriteSample(frame); err != nil {
			return err
		}
	}
	if force && len(it) > 0 {
		if err := b.w.WriteSample(it); err != nil {
			return err
		}
		it = nil
	}
	return nil
}

func (b *fullFrameBuffer[T, S]) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	err := b.flush(true)
	err2 := b.w.Close()
	return errors.Join(err, err2)
}

// NewFrameWriter creates a writer that appends a copy of all written frames to a slice.
func NewFrameWriter[T ~[]S, S sample](buf *[]T, sampleRate int) WriteCloser[T] {
	return &frameWriter[T, S]{
		buf:        buf,
		sampleRate: sampleRate,
	}
}

type frameWriter[T ~[]S, S sample] struct {
	buf        *[]T
	sampleRate int
}

func (b *frameWriter[T, S]) String() string {
	return fmt.Sprintf("Frames(%d)", b.sampleRate)
}

func (b *frameWriter[T, S]) SampleRate() int {
	return b.sampleRate
}

func (b *frameWriter[T, S]) Close() error {
	return nil
}

func (b *frameWriter[T, S]) WriteSample(data T) error {
	*b.buf = append(*b.buf, slices.Clone(data))
	return nil
}
