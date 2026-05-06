package amrenc

/*
#include "enc_if.h"
*/
import "C"

import (
	"unsafe"
)

type Mode byte

const (
	encoder6KB  = Mode(0)
	encoder8KB  = Mode(1)
	encoder12KB = Mode(2)
	encoder14KB = Mode(3)
	encoder16KB = Mode(4)
	encoder18KB = Mode(5)
	encoder20KB = Mode(6)
	encoder23KB = Mode(7)
	encoder24KB = Mode(8)
	Best        = encoder24KB
)

func New(mode Mode) *Encoder {
	return &Encoder{
		p:    C.E_IF_init(),
		mode: mode,
	}
}

type Encoder struct {
	p    unsafe.Pointer
	mode Mode
}

func (e *Encoder) Close() {
	if e.p != nil {
		C.E_IF_exit(e.p)
		e.p = nil
	}
}

type PCMFrame [320]int16

func (e *Encoder) Encode(dst []byte, src *PCMFrame) []byte {
	var out [61]byte // max encoded frame size
	n := C.E_IF_encode(e.p, C.int(e.mode), (*C.short)(&src[0]), (*C.uchar)(&out[0]), 0)
	return append(dst, out[:n]...)
}
