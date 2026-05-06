package amrwb

/*
#include "enc_if.h"
*/
import "C"

import "unsafe"

type encoderMode byte

const (
	encoder6KB  = encoderMode(0)
	encoder8KB  = encoderMode(1)
	encoder12KB = encoderMode(2)
	encoder14KB = encoderMode(3)
	encoder16KB = encoderMode(4)
	encoder18KB = encoderMode(5)
	encoder20KB = encoderMode(6)
	encoder23KB = encoderMode(7)
	encoder24KB = encoderMode(8)
)

func newEncoder(mode encoderMode) *encoder {
	return &encoder{
		p:    C.E_IF_init(),
		mode: mode,
	}
}

type encoder struct {
	p    unsafe.Pointer
	mode encoderMode
}

func (e *encoder) Close() {
	if e.p != nil {
		C.E_IF_exit(e.p)
		e.p = nil
	}
}

func (e *encoder) Encode(dst []byte, src *PCMFrame) []byte {
	var out [FrameSizeMax]byte
	n := C.E_IF_encode(e.p, C.Word16(e.mode), (*C.Word16)(&src[0]), (*C.UWord8)(&out[0]), 0)
	return append(dst, out[:n]...)
}
