//go:build !race

#include "textflag.h"

TEXT ·matchTagMask(SB),NOSPLIT,$0-20
	MOVQ tags+0(FP), SI
	MOVWLZX tag+8(FP), AX
	MOVD AX, X0
	PSHUFLW $0, X0, X0
	PSHUFD $0, X0, X0
	MOVOU (SI), X1
	PCMPEQW X1, X0
	PMOVMSKB X0, AX
	MOVL AX, ret+16(FP)
	RET
