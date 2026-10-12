package wormhole_test

import (
	"fmt"

	"github.com/glycerine/go-wormhole-kv"
)

func ExampleMap() {
	m := wormhole.New[string, string]()
	r := m.Ref() // use a separate handle for each concurrent caller
	defer r.Close()
	if err := r.Put("user:42", "Ada"); err != nil {
		panic(err)
	}
	value, found := r.Get("user:42")
	fmt.Println(value, found, m.Len())
	fmt.Println(r.Delete("user:42"), r.Probe("user:42"))
	// Output:
	// Ada true 1
	// true false
}

func ExampleNewWithOptions() {
	m := wormhole.NewWithOptions[uint64, string](wormhole.Options{MetaCache: true})
	r := m.Ref()
	defer r.Close()
	if err := r.Put(42, "Ada"); err != nil {
		panic(err)
	}
	value, found := r.Get(42)
	fmt.Println(value, found)
	// Output: Ada true
}

func ExampleIterator() {
	m := wormhole.New[string, string]()
	r := m.Ref()
	defer r.Close()
	for _, key := range []string{"c", "a", "b"} {
		if err := r.Put(key, "value"); err != nil {
			panic(err)
		}
	}
	it := r.Iterator()
	it.Seek("b")
	for it.Valid() {
		key, _, _ := it.Next()
		fmt.Println(key)
	}
	it.Close() // return the handle before deleting a range
	fmt.Println("removed:", r.DeleteRange("a", "c"))
	// Output:
	// b
	// c
	// removed: 2
}

func ExampleIterator_Inspect() {
	m := wormhole.New[string, string]()
	r := m.Ref()
	defer r.Close()
	if err := r.Put("user:42", "Ada"); err != nil {
		panic(err)
	}
	it := r.Iterator()
	defer it.Close()
	it.Seek("user:")
	it.Inspect(func(key, value string, exists bool) {
		fmt.Println(key, value, exists)
	})
	key, _, _ := it.Peek() // inspection leaves the position unchanged
	fmt.Println("still at", key)
	it.Park()
	it.Inspect(func(_, _ string, exists bool) {
		fmt.Println("parked:", exists) // called even without a current record
	})
	// Output:
	// user:42 Ada true
	// still at user:42
	// parked: false
}
