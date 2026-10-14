//go:build !amd64 || race

package wormhole

func (l *leaf[K, V]) prefetchStringSplit() {}
