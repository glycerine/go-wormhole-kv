#include "textflag.h"

TEXT ·leafStoreRelease(SB),NOSPLIT,$0-8
 MOVQ state+0(FP), AX
 MOVL $0, (AX)
 RET
