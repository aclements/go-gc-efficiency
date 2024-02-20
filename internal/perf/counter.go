// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perf

import (
	"encoding/binary"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

type Event interface {
	setAttrs(*unix.PerfEventAttr) error
	String() string
}

type eventBasic struct {
	name   string
	typ    uint32
	config uint64
}

func (e eventBasic) setAttrs(a *unix.PerfEventAttr) error {
	a.Type = e.typ
	a.Config = e.config
	return nil
}

func (e eventBasic) String() string {
	return e.name
}

var (
	EventCPUCycles       = eventBasic{"cpu-cycles", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_CPU_CYCLES}
	EventInstructions    = eventBasic{"instructions", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_INSTRUCTIONS}
	EventCacheReferences = eventBasic{"cache-references", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_CACHE_REFERENCES}
	EventCacheMisses     = eventBasic{"cache-misses", unix.PERF_TYPE_HARDWARE, unix.PERF_COUNT_HW_CACHE_MISSES}
)

type Counter struct {
	f *os.File
}

// OpenCounter returns a new [Counter] that reads values for the given [Event]
// on the current goroutine. It calls [runtime.LockOSThread] to tie this
// goroutine to a thread because perf is a thread-oriented API. Callers are
// expected to call [Counter.Close] to unlock the thread.
func OpenCounter(event Event) (*Counter, error) {
	attr := unix.PerfEventAttr{}
	attr.Size = uint32(unsafe.Sizeof(attr))
	if err := event.setAttrs(&attr); err != nil {
		return nil, err
	}
	attr.Read_format = unix.PERF_FORMAT_TOTAL_TIME_ENABLED | unix.PERF_FORMAT_TOTAL_TIME_RUNNING
	attr.Bits = unix.PerfBitDisabled

	// XXX
	attr.Bits |= unix.PerfBitExcludeKernel

	runtime.LockOSThread()
	fd, err := unix.PerfEventOpen(&attr, 0, -1, -1, unix.PERF_FLAG_FD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "<perf-event>")

	return &Counter{f}, nil
}

func (c *Counter) Close() {
	if c == nil {
		return
	}
	// TODO: Ignore double close
	runtime.UnlockOSThread()
}

func (c *Counter) Start() {
	// TODO: Ignore double start
	if c == nil {
		return
	}
	unix.IoctlGetInt(int(c.f.Fd()), unix.PERF_EVENT_IOC_ENABLE)
}

func (c *Counter) Stop() {
	if c == nil {
		return
	}
	unix.IoctlGetInt(int(c.f.Fd()), unix.PERF_EVENT_IOC_DISABLE)
}

type Count struct {
	RawValue    uint64
	TimeEnabled uint64
	TimeRunning uint64
}

func (c *Counter) Read() (Count, error) {
	if c == nil {
		return Count{}, nil
	}

	// Kernel's read layout, for reference
	type raw struct {
		Value       uint64 // Event counter value
		TimeEnabled uint64 // if ReadFormatTotalTimeEnabled
		TimeRunning uint64 // if ReadFormatTotalTimeRunning
		ID          uint64 // if ReadFormatID
	}
	_ = raw{}

	var out Count
	var rec [3 * 8]byte
	_, err := c.f.Read(rec[:])
	if err != nil {
		return out, err
	}

	out.RawValue = binary.NativeEndian.Uint64(rec[0:])
	out.TimeEnabled = binary.NativeEndian.Uint64(rec[8:])
	out.TimeRunning = binary.NativeEndian.Uint64(rec[16:])
	return out, nil
}

// Value returns the value of Count, scaled to account for time the counter was
// descheduled.
func (c Count) Value() uint64 {
	if c.TimeEnabled == c.TimeRunning {
		// Common case: it was running the whole time.
		return c.RawValue
	}
	if c.TimeRunning == 0 {
		// Avoid divide by zero.
		return 0
	}
	return uint64(float64(c.RawValue) * (float64(c.TimeEnabled) / float64(c.TimeRunning)))
}
