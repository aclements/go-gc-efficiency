// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

// set xlabel "words"; set log x 2; set ylabel "ns/byte"; plot '/tmp/x' index 0 using 1:5 with lp title "Alloc ptrs", '/tmp/x' index 1 using 1:5 with lp title "Alloc scalars", '/tmp/x' index 2 using 1:5 with lp title "Zero"

import (
	"fmt"
	"runtime"
	"testing"
	"unsafe"

	"github.com/aclements/go-gc-efficiency/internal/perf"
)

const wordBytes = int(unsafe.Sizeof((*int)(nil)))
const llcBytes = 16 << 20 // LLC size or larger

type word [wordBytes]byte

var ballast []byte

func BenchmarkAllocPtr(b *testing.B) {
	ballast = make([]byte, llcBytes)
	defer func() { ballast = nil }()

	bench[[1]*byte](b)
	bench[[2]*byte](b)
	bench[[4]*byte](b)
	bench[[8]*byte](b)
	bench[[16]*byte](b)
	bench[[32]*byte](b)
	bench[[64]*byte](b)
	bench[[128]*byte](b)
	bench[[256]*byte](b)
	bench[[512]*byte](b)
	bench[[1024]*byte](b)
	bench[[2048]*byte](b)
	bench[[4096]*byte](b)
	bench[[8192]*byte](b)
	bench[[16536]*byte](b)
	bench[[32768]*byte](b)
}

func BenchmarkAllocScalar(b *testing.B) {
	ballast = make([]byte, llcBytes)
	defer func() { ballast = nil }()

	bench[[1]word](b)
	bench[[2]word](b)
	bench[[4]word](b)
	bench[[8]word](b)
	bench[[16]word](b)
	bench[[32]word](b)
	bench[[64]word](b)
	bench[[128]word](b)
	bench[[256]word](b)
	bench[[512]word](b)
	bench[[1024]word](b)
	bench[[2048]word](b)
	bench[[4096]word](b)
	bench[[8192]word](b)
	bench[[16536]word](b)
	bench[[32768]word](b)
}

var sink any
var alwaysFalse bool

func bench[T any](b *testing.B) {
	sizeofT := unsafe.Sizeof(*new(T))

	b.Run(fmt.Sprintf("bytes=%d", sizeofT), func(b *testing.B) {
		cs := openCounters(b)
		defer cs.Close()

		var mstats runtime.MemStats
		runtime.ReadMemStats(&mstats)
		startGCs := mstats.NumGC
		b.ResetTimer()
		cs.Start()
		// XXX Subtract paused time. Maybe "Read" is too low-level. What do you
		// ever do with the raw values? I should instead have a start/stop API.

		var total uintptr
		for range b.N {
			x := new(T)
			if alwaysFalse {
				sink = x
			}
			total += sizeofT
			if total > uintptr(len(ballast)/2) {
				// Run GC manually so we can exclude GC time from the benchmark results.
				cs.Stop()
				b.StopTimer()
				total = 0
				runtime.GC()
				startGCs++
				b.StartTimer()
				cs.Start()
			}
		}

		cs.Stop()
		b.StopTimer()

		duration := b.Elapsed()
		b.ReportMetric(float64(duration.Nanoseconds())/float64(int(sizeofT)*b.N), "ns/byte")

		// Confirm that no automatic GCs happened during the benchmark.
		runtime.ReadMemStats(&mstats)
		endGCs := mstats.NumGC
		if endGCs != startGCs {
			b.Fatalf("%d unaccounted GCs", endGCs-startGCs)
		}
	})
}

func BenchmarkZeroLLCMiss(b *testing.B) {
	// Ensure we have a backing store that doesn't fit in L3.
	store := make([]byte, llcBytes*2)
	sink = store
	clear(store) // Page in

	for bytes := wordBytes; bytes <= 32768*wordBytes; bytes *= 2 {
		b.Run(fmt.Sprintf("bytes=%d", bytes), func(b *testing.B) {
			cs := openCounters(b)
			defer cs.Close()
			cs.Start()

			var x []byte
			for range b.N {
				if len(x) < bytes {
					x = store
				}
				clear(x[:bytes])
				x = x[bytes:]
			}
			cs.Stop()
			b.StopTimer()

			duration := b.Elapsed()
			b.ReportMetric(float64(duration.Nanoseconds())/float64(bytes*b.N), "ns/byte")
		})
	}
}

var events = [...]perf.Event{
	perf.EventCPUCycles,
	perf.EventInstructions,
	perf.EventCacheMisses,
	perf.EventCacheReferences,
}

type Counters struct {
	b *testing.B

	counters [len(events)]*perf.Counter
}

func openCounters(b *testing.B) *Counters {
	cs := Counters{b: b}

	for i, event := range events {
		var err error
		cs.counters[i], err = perf.OpenCounter(event)
		if err != nil {
			b.Logf("error opening counter %s: %v", event, err)
		}
	}

	return &cs
}

func (cs *Counters) Start() {
	for _, c := range cs.counters {
		c.Start()
	}
}

func (cs *Counters) Stop() {
	for _, c := range cs.counters {
		c.Stop()
	}
}

func (cs *Counters) Close() {
	if cs.b == nil {
		return
	}

	for i, c := range cs.counters {
		val, err := c.Read()
		if err != nil {
			cs.b.Logf("error reading %s: %v", events[i], err)
		} else if val.TimeRunning > 0 {
			cs.b.ReportMetric(float64(val.Value())/float64(cs.b.N), events[i].String()+"/op")
		}
		c.Close()
	}
	cs.b = nil
}
