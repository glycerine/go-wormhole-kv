//go:build !race

#include "textflag.h"

TEXT ·shiftIndices(SB),NOSPLIT,$0-40
	MOVQ ss+0(FP), SI
	MOVQ n+8(FP), CX
	TESTQ CX, CX
	JE done
	// Broadcast uint8(128 - from - count).
	MOVQ $128, AX
	SUBQ from+16(FP), AX
	SUBQ count+24(FP), AX
	ANDL $255, AX
	IMULL $0x01010101, AX
	MOVD AX, X1
	PSHUFD $0, X1, X1
	// Broadcast int8(127 - count).
	MOVQ $127, AX
	SUBQ count+24(FP), AX
	ANDL $255, AX
	IMULL $0x01010101, AX
	MOVD AX, X2
	PSHUFD $0, X2, X2
	// A matching mask becomes +1 or -1 after AND with delta.
	MOVQ delta+32(FP), AX
	ANDL $255, AX
	IMULL $0x01010101, AX
	MOVD AX, X3
	PSHUFD $0, X3, X3
loop:
	MOVOU (SI), X0
	MOVO X0, X4
	PADDB X1, X4
	PCMPGTB X2, X4
	PAND X3, X4
	PADDB X4, X0
	MOVOU X0, (SI)
	ADDQ $16, SI
	SUBQ $16, CX
	JG loop
done:
	RET
