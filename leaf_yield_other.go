//go:build !linux

package wormhole

import "runtime"

func leafOSYield() { runtime.Gosched() }
