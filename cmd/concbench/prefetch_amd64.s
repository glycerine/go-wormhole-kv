#include "textflag.h"

// SSE PREFETCHNTA matches native __builtin_prefetch(..., 0, 0).
// Both string addresses are inside text; both sample addresses are inside Key.
TEXT ·prefetchProbe(SB),NOSPLIT,$0-24
	MOVQ text_base+0(FP), SI
	MOVQ text_len+8(FP), CX
	TESTQ CX, CX
	JZ sample
	PREFETCHNTA (SI)
	CMPQ CX, $64
	JBE sample
	PREFETCHNTA 64(SI)
sample:
	MOVQ future+16(FP), SI
	PREFETCHNTA (SI)
	PREFETCHNTA 16(SI)
	RET
