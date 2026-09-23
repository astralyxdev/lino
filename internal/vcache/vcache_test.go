package vcache

import (
	"strings"
	"sync"
	"testing"
)

func TestCache(t *testing.T) {
	type op struct {
		put      bool
		path, v  string
		data     string
		wantHit  bool
		wantData string
	}
	tests := []struct {
		name string
		max  int64
		ops  []op
		len  int
		size int64
	}{
		{"miss", 10, []op{{path: "a", v: "1"}}, 0, 0},
		{"hit", 10, []op{{put: true, path: "a", v: "1", data: "abc"}, {path: "a", v: "1", wantHit: true, wantData: "abc"}}, 1, 3},
		{"other version", 10, []op{{put: true, path: "a", v: "1", data: "abc"}, {path: "a", v: "2"}}, 1, 3},
		{"replace", 10, []op{{put: true, path: "a", v: "1", data: "abc"}, {put: true, path: "a", v: "1", data: "abcdef"}, {path: "a", v: "1", wantHit: true, wantData: "abcdef"}}, 1, 6},
		{"evict lru", 6, []op{
			{put: true, path: "a", v: "1", data: "aaa"},
			{put: true, path: "b", v: "1", data: "bbb"},
			{path: "a", v: "1", wantHit: true, wantData: "aaa"},
			{put: true, path: "c", v: "1", data: "ccc"},
			{path: "b", v: "1"},
			{path: "a", v: "1", wantHit: true, wantData: "aaa"},
			{path: "c", v: "1", wantHit: true, wantData: "ccc"},
		}, 2, 6},
		{"too large ignored", 2, []op{{put: true, path: "a", v: "1", data: "abc"}, {path: "a", v: "1"}}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(tt.max)
			for i, o := range tt.ops {
				if o.put {
					c.Put(o.path, o.v, []byte(o.data))
					continue
				}
				got, ok := c.Get(o.path, o.v)
				if ok != o.wantHit || string(got) != o.wantData {
					t.Fatalf("op %d: Get(%s,%s) = %q,%v", i, o.path, o.v, got, ok)
				}
			}
			if c.Len() != tt.len || c.Size() != tt.size {
				t.Fatalf("len %d size %d, want %d %d", c.Len(), c.Size(), tt.len, tt.size)
			}
		})
	}
}

func TestForget(t *testing.T) {
	c := New(100)
	c.Put("a", "1", []byte("x"))
	c.Put("a", "2", []byte("y"))
	c.Put("b", "1", []byte("z"))
	c.Forget("a")
	if c.Len() != 1 || c.Size() != 1 {
		t.Fatalf("len %d size %d", c.Len(), c.Size())
	}
	if _, ok := c.Get("b", "1"); !ok {
		t.Fatal("b evicted")
	}
}

func TestConcurrent(t *testing.T) {
	c := New(1000)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				p := string(rune('a' + (i+j)%5))
				c.Put(p, "v", []byte(strings.Repeat("x", j%50)))
				c.Get(p, "v")
			}
		}(i)
	}
	wg.Wait()
	if c.Size() > 1000 {
		t.Fatalf("size %d over max", c.Size())
	}
}

func TestMetaRemoveRegister(t *testing.T) {
	c := New(100)
	m := Meta{Path: "a", Next: 7, AsOf: 9}
	c.PutMeta("a", "1", []byte("xy"), m)
	if d, got, ok := c.GetMeta("a", "1"); !ok || string(d) != "xy" || got != m {
		t.Fatalf("GetMeta = %q %+v %v", d, got, ok)
	}
	c.Put("a", "1", []byte("xyz"))
	if _, got, _ := c.GetMeta("a", "1"); got != (Meta{}) || c.Size() != 3 {
		t.Fatalf("Put did not replace meta: %+v size %d", got, c.Size())
	}
	c.Remove("a", "1")
	c.Remove("a", "missing")
	if c.Len() != 0 || c.Size() != 0 {
		t.Fatalf("len %d size %d after Remove", c.Len(), c.Size())
	}

	if For("/r") != nil {
		t.Fatal("unregistered root has a cache")
	}
	Register("/r", c)
	if For("/r") != c {
		t.Fatal("For after Register")
	}
	Register("/r", nil)
	if For("/r") != nil {
		t.Fatal("For after unregister")
	}
}

func TestMemoryBound(t *testing.T) {
	const max = 64 << 10
	c := New(max)
	for i := range 2000 {
		c.PutMeta(string(rune('a'+i%26)), strings.Repeat("v", 1+i%7), make([]byte, 1+i%3000), Meta{Next: int64(i)})
		if c.Size() > max {
			t.Fatalf("size %d over %d after %d puts", c.Size(), max, i+1)
		}
	}
	var sum int64
	for el := c.order.Front(); el != nil; el = el.Next() {
		sum += int64(len(el.Value.(*entry).data))
	}
	if sum != c.Size() || c.Len() != c.order.Len() {
		t.Fatalf("accounting: sum %d size %d len %d/%d", sum, c.Size(), c.Len(), c.order.Len())
	}
}
