//go:build !race

#include "textflag.h"

TEXT ·prefetchMetaBuckets(SB),NOSPLIT,$0-16
 MOVQ a+0(FP), AX
 MOVQ b+8(FP), CX
 PREFETCHT0 (AX)
 PREFETCHT0 (CX)
 RET

TEXT ·prefetchLeafSlot(SB),NOSPLIT,$0-16
 MOVQ tag+0(FP), AX
 MOVQ slot+8(FP), CX
 PREFETCHT0 (AX)
 PREFETCHT0 (CX)
 RET

TEXT ·prefetchWriteKey(SB),NOSPLIT,$0-8
 MOVQ key+0(FP), AX
 PREFETCHT0 (AX)
 RET

TEXT ·prefetchScanLeaf(SB),NOSPLIT,$0-32
 MOVQ header+0(FP), AX
 PREFETCHT0 (AX)
 MOVQ slots+8(FP), AX
 PREFETCHT0 (AX)
 PREFETCHT0 64(AX)
 MOVQ data+16(FP), AX
 MOVQ size+24(FP), CX
 TESTQ CX, CX
 JE scan_done
scan_loop:
 CMPQ CX, $256
 JL scan_tail
 PREFETCHT0 (AX)
 PREFETCHT0 64(AX)
 PREFETCHT0 128(AX)
 PREFETCHT0 192(AX)
 ADDQ $256, AX
 SUBQ $256, CX
 JG scan_loop
 RET
scan_tail:
 PREFETCHT0 (AX)
 CMPQ CX, $64
 JLE scan_done
 ADDQ $64, AX
 SUBQ $64, CX
 JMP scan_tail
scan_done:
 RET
