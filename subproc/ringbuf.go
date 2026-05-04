package subproc

import (
	"sync/atomic"
)

// paddedSlot isolates each atomic pointer on its own cache line so the writer's
// Store to one slot doesn't invalidate readers' Load from adjacent slots.
type paddedSlot struct {
	ptr atomic.Pointer[[]byte]
	_   [64 - 8]byte
}

// RingBuffer is a lock-free, single-writer, multiple-reader ring buffer.
// The writer calls Store to publish new frames; any number of concurrent
// readers call Load to retrieve the latest frame. Load uses a seqlock-style
// check to detect if the writer has cycled back to the reader's slot.
//
// Fields are padded to separate cache lines: count (written every frame by
// the writer) is isolated from size/slots (read-only after construction).
type RingBuffer struct {
	count atomic.Uint64
	_     [64 - 8]byte
	slots []paddedSlot
	size  uint64 // power of 2
	mask  uint64 // size - 1
}

// NewRingBuffer creates a ring buffer with at least `size` slots, rounded up
// to the next power of 2. A minimum of 4 slots is enforced.
func NewRingBuffer(size int) *RingBuffer {
	if size < 4 {
		size = 4
	}
	// round up to next power of 2
	p := 1
	for p < size {
		p <<= 1
	}
	size = p
	return &RingBuffer{
		slots: make([]paddedSlot, size),
		size:  uint64(size),
		mask:  uint64(size - 1),
	}
}

// Store publishes data into the next slot, then advances the counter.
// Must be called from a single goroutine (the writer).
func (rb *RingBuffer) Store(data []byte) {
	next := (rb.count.Load() + 1) & rb.mask
	rb.slots[next].ptr.Store(&data)
	rb.count.Add(1)
}

// Load returns the most recent frame, or nil if no frame has been stored yet.
// Safe for concurrent use by multiple readers.
func (rb *RingBuffer) Load() []byte {
	for {
		n := rb.count.Load()
		if n == 0 {
			return nil
		}
		p := rb.slots[n&rb.mask].ptr.Load()
		// If the writer has advanced size+ times since we read the counter,
		// this slot may have been recycled. Retry with the current counter.
		if rb.count.Load()-n < rb.size {
			if p == nil {
				return nil
			}
			return *p
		}
	}
}
