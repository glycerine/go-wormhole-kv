#include "textflag.h"

// SSE4.2 is optional on amd64; execute CRC32 only after checking CPUID.
TEXT ·hasCRC32(SB),NOSPLIT,$0-1
	MOVL $1, AX
	CPUID
	SHRL $20, CX
	ANDL $1, CX
	MOVB CX, ret+0(FP)
	RET

// Raw CRC32C, with the same unaligned 8/4/2/1-byte decomposition as lib.c.
// Every memory operand is bounded by the string length, including tails.
TEXT ·crcShort(SB),NOSPLIT,$0-28
	MOVL seed+0(FP), AX
	MOVQ key_base+8(FP), SI
	MOVQ key_len+16(FP), CX
loop8:
	CMPQ CX, $8
	JB tail4
	CRC32Q (SI), AX
	ADDQ $8, SI
	SUBQ $8, CX
	JMP loop8
tail4:
	TESTQ $4, CX
	JZ tail2
	CRC32L (SI), AX
	ADDQ $4, SI
tail2:
	TESTQ $2, CX
	JZ tail1
	CRC32W (SI), AX
	ADDQ $2, SI
tail1:
	TESTQ $1, CX
	JZ done
	CRC32B (SI), AX
done:
	MOVL AX, ret+24(FP)
	RET
