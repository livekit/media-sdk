package amrwb

/*
#include "dec_if.h"
*/
import "C"

import "unsafe"

func newDecoder() *decoder {
	return &decoder{
		p: C.D_IF_init(),
	}
}

type decoder struct {
	p unsafe.Pointer
}

func (d *decoder) Close() {
	if d.p != nil {
		C.D_IF_exit(d.p)
		d.p = nil
	}
}

func (d *decoder) Decode(dst *PCMFrame, src []byte) {
	var in [FrameSizeMax]byte
	copy(in[:], src)
	C.D_IF_decode(d.p, (*C.UWord8)(&in[0]), (*C.Word16)(&dst[0]), C._good_frame)
}
