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

// Share the tag broadcast and ABI crossing across two candidate buckets.
// Do not load b before testing a: peek retains its short-circuit behavior.
TEXT ·matchEitherTag(SB),NOSPLIT,$0-25
	MOVQ a+0(FP), SI
	MOVWLZX tag+16(FP), AX
	MOVD AX, X0
	PSHUFLW $0, X0, X0
	PSHUFD $0, X0, X0
	MOVOU (SI), X1
	PCMPEQW X0, X1
	PMOVMSKB X1, AX
	TESTL AX, AX
	JNZ eitherMatch
	MOVQ b+8(FP), SI
	MOVOU (SI), X1
	PCMPEQW X0, X1
	PMOVMSKB X1, AX
	TESTL AX, AX
	SETNE ret+24(FP)
	RET
eitherMatch:
	MOVB $1, ret+24(FP)
	RET
