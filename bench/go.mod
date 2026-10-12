module github.com/glycerine/go-wormhole-kv/bench

go 1.26.4

require (
	github.com/glycerine/bufftree v0.0.3
	github.com/glycerine/insdict v0.14.6
	github.com/glycerine/rbtree v0.2.2
	github.com/glycerine/turtle/go v0.0.5
	github.com/glycerine/go-wormhole-kv v0.0.0
	github.com/tidwall/btree v1.8.1
)

require github.com/cespare/xxhash/v2 v2.3.0 // indirect

replace github.com/glycerine/go-wormhole-kv => ..
