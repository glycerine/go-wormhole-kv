//go:build !amd64

package main

import wormhole "github.com/glycerine/wormhole/go"

func prefetchProbe(text string, future *wormhole.Key[string]) {}
