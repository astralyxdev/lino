package metrics

import (
	"testing"
	"time"
)

func within(got, want time.Duration, tol float64) bool {
	return float64(got) >= float64(want)*(1-tol) && float64(got) <= float64(want)*(1+tol)
}

func TestQuantile(t *testing.T) {
	uniform := func() *Histogram {
		h := &Histogram{}
		for i := 1; i <= 1000; i++ {
			h.Observe(time.Duration(i) * time.Millisecond)
		}
		return h
	}
	tests := []struct {
		name string
		h    *Histogram
		q    float64
		want time.Duration
	}{
		{"empty", &Histogram{}, 0.5, 0},
		{"p50 uniform", uniform(), 0.50, 500 * time.Millisecond},
		{"p95 uniform", uniform(), 0.95, 950 * time.Millisecond},
		{"p99 uniform", uniform(), 0.99, 990 * time.Millisecond},
		{"p100 is max", uniform(), 1, time.Second},
		{"single", func() *Histogram { h := &Histogram{}; h.Observe(3 * time.Millisecond); return h }(), 0.5, 3 * time.Millisecond},
		{"sub-microsecond", func() *Histogram { h := &Histogram{}; h.Observe(10); return h }(), 0.5, 10},
		{"huge", func() *Histogram { h := &Histogram{}; h.Observe(time.Hour); return h }(), 0.99, time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.h.Quantile(tt.q)
			if tt.want == 0 && got != 0 || !within(got, tt.want, 0.2) {
				t.Fatalf("Quantile(%v) = %v, want ~%v", tt.q, got, tt.want)
			}
		})
	}
}

func TestHistogramMergeAndMean(t *testing.T) {
	a, b := &Histogram{}, &Histogram{}
	for i := 0; i < 90; i++ {
		a.Observe(time.Millisecond)
	}
	for i := 0; i < 10; i++ {
		b.Observe(100 * time.Millisecond)
	}
	a.Merge(b)
	if a.Count != 100 || a.Max != 100*time.Millisecond {
		t.Fatalf("merged %+v", a)
	}
	if got := a.Mean(); got != 10900*time.Microsecond {
		t.Fatalf("mean %v", got)
	}
	if p50, p95 := a.Quantile(0.5), a.Quantile(0.95); !within(p50, time.Millisecond, 0.2) || !within(p95, 100*time.Millisecond, 0.2) {
		t.Fatalf("p50 %v p95 %v", p50, p95)
	}
}

func TestBucketBounds(t *testing.T) {
	for d := time.Microsecond; d < 2*time.Minute; d = d*3/2 + 1 {
		i := bucketOf(d)
		if upper(i) < d || (i > 0 && upper(i-1) >= d) {
			t.Fatalf("%v in bucket %d (%v..%v]", d, i, upper(i-1), upper(i))
		}
	}
}
