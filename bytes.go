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

package media

import (
	"bufio"
	"fmt"
	"io"
)

type BytesFrame interface {
	~[]byte
	Frame
}

// BytesWriter is similar to io.WriteCloser, but intentionally breaks API compatibility.
//
// This is done to emphasize that BytesWriter implementations are aware of the frame boundaries,
// and will only use buffer sizes that match the frame size.
type BytesWriter interface {
	String() string
	WriteRaw(frame []byte) error
	Close() error
}

// NewBytesWriter creates a BytesWriter that writes to a standard io.WriteCloser.
//
// This process will erase the frame boundaries. Implement BytesWriter directly to preserve frame boundaries.
func NewBytesWriter(w io.WriteCloser) BytesWriter {
	return &fileWriter{
		w:  w,
		bw: bufio.NewWriter(w),
	}
}

type fileWriter struct {
	w  io.WriteCloser
	bw *bufio.Writer
}

func (w *fileWriter) String() string {
	return "FileWriter"
}

func (w *fileWriter) WriteRaw(data []byte) error {
	_, err := w.bw.Write(data)
	return err
}

func (w *fileWriter) Close() error {
	if err := w.bw.Flush(); err != nil {
		_ = w.w.Close()
		return err
	}
	return w.w.Close()
}

// NewFileWriter creates a new frame writer that encodes frame to a binary stream.
//
// This process will erase the frame boundaries. Use EncodeBytes to preserve frame boundaries.
func NewFileWriter[T Frame](w io.WriteCloser, sampleRate int) WriteCloser[T] {
	bw := NewBytesWriter(w)
	return EncodeBytes[T](bw, sampleRate)
}

// EncodeBytes creates a writer that converts every frame write to a single binary Write call on a standard io.WriteCloser.
//
// If preserving frame boundaries is not required, using NewFileWriter would be more efficient.
func EncodeBytes[S Frame](w BytesWriter, sampleRate int) WriteCloser[S] {
	return &byteEncoder[S]{w: w, sampleRate: sampleRate}
}

type byteEncoder[S Frame] struct {
	w          BytesWriter
	sampleRate int
	buf        []byte
}

func (w *byteEncoder[S]) String() string {
	return fmt.Sprintf("ByteEncoder(%d) -> %s", w.sampleRate, w.w.String())
}

func (w *byteEncoder[S]) SampleRate() int {
	return w.sampleRate
}

func (w *byteEncoder[S]) WriteSample(sample S) error {
	if sz := sample.Size(); cap(w.buf) < sz {
		w.buf = make([]byte, sz)
	} else {
		w.buf = w.buf[:sz]
	}
	n, err := sample.CopyTo(w.buf)
	if err != nil {
		return err
	}
	return w.w.WriteRaw(w.buf[:n])
}

func (w *byteEncoder[T]) Close() error {
	w.buf = nil
	return w.w.Close()
}

// DecodeBytes creates a writer that converts every binary write from a standard io.WriteCloser to a frame write.
//
// Note that directly reading from a file is not possible in this case, as the frame boundaries are unknown.
func DecodeBytes[S BytesFrame](w WriteCloser[S]) BytesWriter {
	return &byteDecoder[S]{w: w}
}

type byteDecoder[S BytesFrame] struct {
	w WriteCloser[S]
}

func (w *byteDecoder[S]) String() string {
	return fmt.Sprintf("ByteDecoder -> %s", w.w.String())
}

func (w *byteDecoder[S]) SampleRate() int {
	return w.w.SampleRate()
}

func (w *byteDecoder[S]) WriteRaw(sample []byte) error {
	return w.w.WriteSample(S(sample))
}

func (w *byteDecoder[T]) Close() error {
	return w.w.Close()
}
