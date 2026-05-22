//go:build !cgo

package media

func resampleBuffer(dst PCM16Sample, dstSampleRate int, src PCM16Sample, srcSampleRate int, opts *resampleOptions) PCM16Sample {
	return resampleBufferBeep(dst, dstSampleRate, src, srcSampleRate, opts)
}

func newResampleWriter(w WriteCloser[PCM16Sample], sampleRate int, opts *resampleOptions) WriteCloser[PCM16Sample] {
	return newResampleWriterBeep(w, sampleRate, opts)
}
