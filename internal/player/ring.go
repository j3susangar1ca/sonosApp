package player

import "errors"

// ErrEmptyBuffer is returned when performing an operation on an empty RingBuffer.
var ErrEmptyBuffer = errors.New("ring buffer is empty")

// RingBuffer represents a circular queue of capacity C_Q.
type RingBuffer[T any] struct {
	data     []T
	head     int
	tail     int
	size     int
	capacity int
}

// NewRingBuffer initializes a RingBuffer with the given initial capacity.
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	if capacity <= 0 {
		capacity = 16
	}
	return &RingBuffer[T]{
		data:     make([]T, capacity),
		capacity: capacity,
	}
}

// Enqueue adds an item to the tail of the buffer.
// If the buffer is full, it dynamically doubles its capacity.
func (r *RingBuffer[T]) Enqueue(item T) {
	if r.size == r.capacity {
		r.resize(r.capacity * 2)
	}
	r.data[r.tail] = item
	r.tail = (r.tail + 1) % r.capacity
	r.size++
}

// Dequeue removes and returns the item at the head of the buffer.
// The slot is cleared to avoid memory leaks.
func (r *RingBuffer[T]) Dequeue() (T, error) {
	var zero T
	if r.size == 0 {
		return zero, ErrEmptyBuffer
	}
	item := r.data[r.head]
	r.data[r.head] = zero // Clear reference for GC
	r.head = (r.head + 1) % r.capacity
	r.size--
	return item, nil
}

// Head returns the item at the head of the buffer without removing it.
func (r *RingBuffer[T]) Head() (T, error) {
	var zero T
	if r.size == 0 {
		return zero, ErrEmptyBuffer
	}
	return r.data[r.head], nil
}

// Size returns the current number of elements in the buffer.
func (r *RingBuffer[T]) Size() int {
	return r.size
}

// All returns a slice containing all elements in queue order.
func (r *RingBuffer[T]) All() []T {
	if r.size == 0 {
		return nil
	}
	items := make([]T, r.size)
	for i := 0; i < r.size; i++ {
		items[i] = r.data[(r.head+i)%r.capacity]
	}
	return items
}

// Clear resets the buffer and clears references.
func (r *RingBuffer[T]) Clear() {
	var zero T
	for i := 0; i < r.capacity; i++ {
		r.data[i] = zero
	}
	r.head = 0
	r.tail = 0
	r.size = 0
}

// resize doubles the capacity of the buffer and reorganizes elements starting at index 0.
func (r *RingBuffer[T]) resize(newCapacity int) {
	newData := make([]T, newCapacity)
	for i := 0; i < r.size; i++ {
		newData[i] = r.data[(r.head+i)%r.capacity]
	}
	r.data = newData
	r.head = 0
	r.tail = r.size
	r.capacity = newCapacity
}
