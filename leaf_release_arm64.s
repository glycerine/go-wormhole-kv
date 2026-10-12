#include "textflag.h"

TEXT ·leafStoreRelease(SB),NOSPLIT,$0-8
 MOVD state+0(FP), R0
 STLRW ZR, (R0)
 RET
