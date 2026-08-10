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

package media

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// DefFrameDur is a default duration of an audio frame.
	DefFrameDur = 20 * time.Millisecond
	// DefFramesPerSec is a default number of audio frames per second.
	DefFramesPerSec = int(time.Second / DefFrameDur)
)

type Frame interface {
	// Size of the frame in bytes.
	Size() int
	// CopyTo copies the frame content to the destination bytes slice.
	// It returns io.ErrShortBuffer is the buffer size is less than frame's Size.
	CopyTo(dst []byte) (int, error)
}

type Reader[T any] interface {
	ReadSample(buf T) (int, error)
}

type ReadCloser[T any] interface {
	Reader[T]
	Close() error
}

type Writer[T any] interface {
	String() string
	SampleRate() int
	WriteSample(sample T) error
}

type WriteCloser[T any] interface {
	Writer[T]
	Close() error
}

type writeCloser[T any] struct {
	Writer[T]
}

func (*writeCloser[T]) Close() error {
	return nil
}

func NopCloser[T any](w Writer[T]) WriteCloser[T] {
	return &writeCloser[T]{w}
}

func NewSwitchWriter(sampleRate int) *SwitchWriter {
	// This protects from a case when sample rate is not initialized,
	// but still allows passing -1 to delay initialization.
	// If sample rate is still uninitialized when another writer is attached,
	// the SampleRate method will panic instead of this check.
	if sampleRate == 0 {
		panic("no sample rate specified")
	}
	if sampleRate < 0 {
		sampleRate = -1 // checked by SetSampleRate
	}
	w := &SwitchWriter{}
	w.sampleRate.Store(int32(sampleRate))
	return w
}

type SwitchWriter struct {
	WriteCloserSwitch[PCM16Sample]
	disabled atomic.Bool
}

func (s *SwitchWriter) Enable() {
	s.disabled.Store(false)
}

func (s *SwitchWriter) Disable() {
	s.disabled.Store(true)
}

func (s *SwitchWriter) Get() PCM16Writer {
	ptr := s.WriteCloserSwitch.Get()
	if ptr == nil {
		return nil // Untyped nil
	}
	return ptr
}

// Swap sets an underlying writer and returns the old one.
// Caller is responsible for closing the old writer.
func (s *SwitchWriter) Swap(w PCM16Writer) PCM16Writer {
	if w != nil {
		if rate := s.SampleRate(); rate != w.SampleRate() {
			w = ResampleWriter(w, rate)
		}
	}
	old := s.WriteCloserSwitch.Swap(w)
	if old == nil {
		return nil // Untyped nil
	}
	return old
}

func (s *SwitchWriter) String() string {
	w := s.Get()
	return fmt.Sprintf("Switch(%d) -> %v", s.sampleRate.Load(), w)
}

// SetSampleRate sets a new sample rate for the switch. For this to work, NewSwitchWriter(-1) must be called.
// The code will panic if sample rate is unset when a writer is attached, or if this method is called twice.
func (s *SwitchWriter) SetSampleRate(rate int) {
	if rate <= 0 {
		panic("invalid sample rate")
	}
	if !s.WriteCloserSwitch.sampleRate.CompareAndSwap(-1, int32(rate)) {
		panic("sample rate can only be changed once")
	}
}

// SampleRate returns an expected sample rate for this writer. It panics if the sample rate is not specified.
func (s *SwitchWriter) SampleRate() int {
	rate := s.WriteCloserSwitch.SampleRate()
	if rate == 0 {
		panic("switch writer not initialized")
	} else if rate < 0 {
		panic("sample rate is unset on a switch writer")
	}
	return rate
}

func (s *SwitchWriter) WriteSample(sample PCM16Sample) error {
	if s.disabled.Load() {
		return nil
	}
	return s.WriteCloserSwitch.WriteSample(sample)
}

type WriteCloserSwitch[T any] struct { // msdk.WriteCloser[T]
	sampleRate atomic.Int32 // Prevents changing sample rate after the switch is created
	w          atomic.Pointer[WriteCloser[T]]
}

func (s *WriteCloserSwitch[T]) String() string {
	w := s.w.Load()
	if w == nil {
		return "Switch(nil)"
	}
	return fmt.Sprintf("Switch(%d) -> %v", s.SampleRate(), *w)
}

func (s *WriteCloserSwitch[T]) SampleRate() int {
	if rate := s.sampleRate.Load(); rate > 0 {
		return int(rate)
	}
	return -1
}

func (s *WriteCloserSwitch[T]) WriteSample(sample T) error {
	w := s.w.Load()
	if w == nil {
		return nil
	}
	return (*w).WriteSample(sample)
}

func (s *WriteCloserSwitch[T]) Close() error {
	w := s.w.Load()
	if w == nil {
		return nil
	}
	return (*w).Close()
}

func (s *WriteCloserSwitch[T]) Get() WriteCloser[T] {
	ptr := s.w.Load()
	if ptr == nil {
		return nil
	}
	return *ptr
}

func (s *WriteCloserSwitch[T]) Swap(w WriteCloser[T]) WriteCloser[T] {
	var old *WriteCloser[T]
	if w != nil {
		newRate := int32(w.SampleRate())
		oldRate := s.sampleRate.Swap(newRate)
		if oldRate > 0 && oldRate != newRate {
			panic(fmt.Sprintf("sample rate mismatch: expected %d, actual %d", newRate, oldRate))
		}
		old = s.w.Swap(&w)
	} else {
		old = s.w.Swap(nil)
	}
	if old == nil {
		return nil
	}
	return *old
}

type MultiWriter[T any] []WriteCloser[T]

func (s MultiWriter[T]) String() string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "MultiWriter(%d,%d)", len(s), s.SampleRate())
	for i, w := range s {
		fmt.Fprintf(&buf, "; $%d-> %s", i+1, w.String())
	}
	return buf.String()
}

func (s MultiWriter[T]) SampleRate() int {
	if len(s) == 0 {
		return 0
	}
	return s[0].SampleRate()
}

func (s MultiWriter[T]) WriteSample(sample T) error {
	var last error
	for _, w := range s {
		if err := w.WriteSample(sample); err != nil {
			last = err
		}
	}
	return last
}

func (s MultiWriter[T]) Close() error {
	var last error
	for _, w := range s {
		if err := w.Close(); err != nil {
			last = err
		}
	}
	return last
}
