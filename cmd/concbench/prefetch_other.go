//go:build !amd64

package main

import wormhole "github.com/glycerine/go-wormhole-kv"

func prefetchProbe(text string, future *wormhole.Key[string]) {}
