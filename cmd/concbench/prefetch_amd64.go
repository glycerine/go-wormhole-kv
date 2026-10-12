package main

import wormhole "github.com/glycerine/go-wormhole-kv"

// prefetchProbe mirrors concbench.c's upcoming-sample prefetches. Go query
// bytes live separately from their string headers, so prefetch both the next
// key's bytes and the future Key's header/tag. It never dereferences records.
//
//go:noescape
func prefetchProbe(text string, future *wormhole.Key[string])
