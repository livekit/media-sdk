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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// DumpFramesPCM16 writes PCM16 frames in a little-endian format (s16le).
// It is compatible with the following ffmpeg options: -f s16le -ar <rate> -ac 1
func DumpFramesPCM16(w io.WriteCloser, sampleRate int, frames []PCM16Sample) error {
	defer w.Close()

	fw := NewFileWriter[PCM16Sample](w, sampleRate)
	defer fw.Close()

	for _, frame := range frames {
		if err := fw.WriteSample(frame); err != nil {
			return err
		}
	}
	if err := fw.Close(); err != nil {
		return err
	}
	if err := w.Close(); err != nil && !errors.Is(err, fs.ErrClosed) {
		return err
	}
	return nil
}

// DumpFile writes frames in its native byte format, without any container to a file '<name>_ar<rate>.<ext>'
//
// This is probably not the way you want to save the audio, unless you know what you are doing.
// Consider using webm package instead if you want to play the audio file directly.
func DumpFile[T Frame](name, ext string, sampleRate int) WriteCloser[T] {
	nameOut := fmt.Sprintf("%s_ar%d.%s", name, sampleRate, ext)
	f, err := os.Create(nameOut)
	if err != nil {
		panic(err)
	}
	return NewFileWriter[T](f, sampleRate)
}

// DumpFilePCM16 writes PCM16 frames in a little-endian format (s16le) to a file '<name>_ar<rate>.s16le'.
// It is compatible with the following ffmpeg options: -f s16le -ar <rate> -ac 1
func DumpFilePCM16(name string, sampleRate int) PCM16Writer {
	return DumpFile[PCM16Sample](name, "s16le", sampleRate)
}

// DumpWriterPCM16 wraps the provided audio writer and dumps all processed frames to a file with DumpFilePCM16.
// It is useful when debugging different audio processing stages.
func DumpWriterPCM16(name string, w PCM16Writer) PCM16Writer {
	return DumpWriter[PCM16Sample]("s16le", name, w)
}

// DumpWriter wraps the provided audio writer and dumps all processed frames to a file with DumpFile.
// It is useful when debugging different audio processing stages.
func DumpWriter[T Frame](ext string, name string, w WriteCloser[T]) WriteCloser[T] {
	rate := w.SampleRate()
	return MultiWriter[T]{
		w,
		DumpFile[T](name, ext, rate),
	}
}
