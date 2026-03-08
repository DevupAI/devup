package ringbuffer

import (
	"sync"
)

// RingBuffer is a fixed-capacity buffer that drops oldest data when full.
// Thread-safe.
type RingBuffer struct {
	mu       sync.RWMutex
	buf      []byte
	capacity int
	size     int
	start    int // index of first byte
}

// New creates a ring buffer with the given capacity in bytes.
func New(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 4096
	}
	return &RingBuffer{
		buf:      make([]byte, capacity),
		capacity: capacity,
	}
}

// Write appends p to the buffer. Drops oldest bytes if over capacity.
// Returns len(p), nil always.
func (r *RingBuffer) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n = len(p)
	if n >= r.capacity {
		// Keep only the last capacity bytes
		r.buf = make([]byte, r.capacity)
		copy(r.buf, p[n-r.capacity:])
		r.start = 0
		r.size = r.capacity
		return n, nil
	}
	for _, b := range p {
		if r.size < r.capacity {
			idx := (r.start + r.size) % r.capacity
			r.buf[idx] = b
			r.size++
		} else {
			r.start = (r.start + 1) % r.capacity
			idx := (r.start + r.size - 1) % r.capacity
			r.buf[idx] = b
		}
	}
	return n, nil
}

// Bytes returns a copy of the current buffer contents.
func (r *RingBuffer) Bytes() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.size == 0 {
		return nil
	}
	out := make([]byte, r.size)
	for i := 0; i < r.size; i++ {
		out[i] = r.buf[(r.start+i)%r.capacity]
	}
	return out
}
