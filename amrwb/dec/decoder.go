package amrdec

/*
#include "dec_if.h"
*/
import "C"

import "unsafe"

func New() *Decoder {
	return &Decoder{
		p: C.D_IF_init(),
	}
}

type Decoder struct {
	p unsafe.Pointer
}

func (d *Decoder) Close() {
	if d.p != nil {
		C.D_IF_exit(d.p)
		d.p = nil
	}
}

type PCMFrame [320]int16

func (d *Decoder) Decode(dst *PCMFrame, src []byte) {
	var in [61]byte // max encoded frame size
	copy(in[:], src)
	C.D_IF_decode(d.p, (*C.uchar)(&in[0]), (*C.short)(&dst[0]), C._good_frame)
}
