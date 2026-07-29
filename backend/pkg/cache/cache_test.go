package cache

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestGetServesAStoredValueAndExpiresIt(t *testing.T) {
	store := New[string](40 * time.Millisecond)

	store.Put("cluster:1", "key", "answer")
	if got, ok := store.Get("key"); !ok || got != "answer" {
		t.Fatalf("Get = %q, %v; want the stored value", got, ok)
	}

	time.Sleep(60 * time.Millisecond)

	if _, ok := store.Get("key"); ok {
		t.Fatal("expected an expired entry to miss")
	}
	// An expired entry is dropped on the way past rather than left holding its
	// value until something else needs the room.
	if store.Len() != 0 {
		t.Fatalf("Len = %d after an expired read; want 0", store.Len())
	}
}

func TestPutRefusesNoTTL(t *testing.T) {
	// A cache built with a nonsensical TTL takes the documented default rather
	// than becoming one that never hits.
	if got := New[string](0).TTL(); got != DefaultTTL {
		t.Fatalf("TTL = %s, want %s", got, DefaultTTL)
	}
}

func TestInvalidateScopeDropsOnlyThatScope(t *testing.T) {
	store := New[string](time.Minute)

	store.Put("cluster:1", "a", "one")
	store.Put("cluster:1", "b", "two")
	store.Put("cluster:2", "c", "three")

	store.InvalidateScope("cluster:1")

	if _, ok := store.Get("a"); ok {
		t.Fatal("expected the invalidated scope to be gone")
	}
	if _, ok := store.Get("b"); ok {
		t.Fatal("expected the whole invalidated scope to be gone")
	}
	// A write to one cluster says nothing about another one's reads.
	if got, ok := store.Get("c"); !ok || got != "three" {
		t.Fatalf("Get on another scope = %q, %v; want it untouched", got, ok)
	}
}

// The bound is what keeps a cache from becoming a leak on a large fleet: keys
// carry the caller and the question, so the set of distinct keys is unbounded
// even though each entry lives seconds.
func TestPutStaysWithinItsBound(t *testing.T) {
	store := New[int](time.Minute)
	store.max = 8

	for i := 0; i < 200; i++ {
		store.Put("cluster:1", strconv.Itoa(i), i)
	}

	if store.Len() > 8 {
		t.Fatalf("Len = %d, want at most the 8-entry bound", store.Len())
	}
	// Eviction has to leave a usable cache behind, not an empty one.
	if store.Len() == 0 {
		t.Fatal("eviction emptied the cache instead of making room")
	}
}

// Two questions that differ only in where a boundary falls have to be different
// keys; if they were not, one caller would be served another's answer.
func TestKeyDistinguishesPartBoundaries(t *testing.T) {
	if Key("a", "bc") == Key("ab", "c") {
		t.Fatal("keys collide across part boundaries")
	}
	if Key("1", "pods") == Key("1", "pods", "extra") {
		t.Fatal("an extra part did not change the key")
	}
	if Key("1", "pods") != Key("1", "pods") {
		t.Fatal("the same question produced two keys")
	}
}

func TestSortedQueryIsOrderIndependent(t *testing.T) {
	first := SortedQuery(map[string][]string{
		"namespace": {"team-a"}, "all_namespaces": {"true"},
	})
	second := SortedQuery(map[string][]string{
		"all_namespaces": {"true"}, "namespace": {"team-a"},
	})
	if first != second {
		t.Fatalf("SortedQuery depends on map order: %q vs %q", first, second)
	}

	// Different values still have to read as different questions.
	other := SortedQuery(map[string][]string{"namespace": {"team-b"}})
	if other == SortedQuery(map[string][]string{"namespace": {"team-a"}}) {
		t.Fatal("two different namespaces rendered to the same query")
	}
}

func TestCacheIsSafeUnderConcurrentUse(t *testing.T) {
	store := New[int](time.Minute)

	var group sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for i := 0; i < 100; i++ {
				key := strconv.Itoa((worker + i) % 32)
				store.Put("cluster:1", key, i)
				store.Get(key)
				if i%25 == 0 {
					store.InvalidateScope("cluster:1")
				}
			}
		}(worker)
	}
	group.Wait()
}
