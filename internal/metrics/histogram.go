// Package metrics collects per-command counters and latency histograms in the
// live process and persists them to <root>/.lino/stats.json.
package metrics

import (
	"math"
	"time"
)

// Buckets grow by growth from minBucket; the last one is open-ended. With
// 1µs and 1.2 the upper bounds reach about 2 minutes at bucket 102, and a
// percentile is off by at most 20%.
const (
	minBucket = time.Microsecond
	growth    = 1.2
	nBuckets  = 104
)

// Histogram is a log-bucketed latency histogram. The zero value is ready.
type Histogram struct {
	Counts []uint64      `json:"counts"` // per bucket, trailing zeros trimmed
	Count  uint64        `json:"count"`
	Sum    time.Duration `json:"sum_ns"`
	Max    time.Duration `json:"max_ns"`
}

func bucketOf(d time.Duration) int {
	if d <= minBucket {
		return 0
	}
	i := int(math.Ceil(math.Log(float64(d)/float64(minBucket)) / math.Log(growth)))
	return min(i, nBuckets-1)
}

// upper is the upper bound of bucket i.
func upper(i int) time.Duration {
	return time.Duration(float64(minBucket) * math.Pow(growth, float64(i)))
}

// Observe records one duration.
func (h *Histogram) Observe(d time.Duration) {
	if d < 0 {
		d = 0
	}
	i := bucketOf(d)
	if i >= len(h.Counts) {
		h.Counts = append(h.Counts, make([]uint64, i+1-len(h.Counts))...)
	}
	h.Counts[i]++
	h.Count++
	h.Sum += d
	h.Max = max(h.Max, d)
}

// Merge adds o into h.
func (h *Histogram) Merge(o *Histogram) {
	if o == nil {
		return
	}
	if len(o.Counts) > len(h.Counts) {
		h.Counts = append(h.Counts, make([]uint64, len(o.Counts)-len(h.Counts))...)
	}
	for i, c := range o.Counts {
		h.Counts[i] += c
	}
	h.Count += o.Count
	h.Sum += o.Sum
	h.Max = max(h.Max, o.Max)
}

// Quantile returns the q-quantile (0 < q <= 1): the upper bound of the bucket
// holding it, capped at Max. It is 0 for an empty histogram.
func (h *Histogram) Quantile(q float64) time.Duration {
	if h == nil || h.Count == 0 {
		return 0
	}
	rank := uint64(math.Ceil(q * float64(h.Count)))
	rank = max(rank, 1)
	var seen uint64
	for i, c := range h.Counts {
		seen += c
		if seen >= rank {
			if i == nBuckets-1 {
				return h.Max
			}
			return min(upper(i), h.Max)
		}
	}
	return h.Max
}

// Mean returns the average duration.
func (h *Histogram) Mean() time.Duration {
	if h == nil || h.Count == 0 {
		return 0
	}
	return h.Sum / time.Duration(h.Count)
}

func (h *Histogram) clone() *Histogram {
	c := *h
	c.Counts = append([]uint64(nil), h.Counts...)
	return &c
}
