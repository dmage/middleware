package maxconnections

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

type counter struct {
	mu sync.Mutex
	m  map[int]int
}

func newCounter() *counter {
	return &counter{
		m: make(map[int]int),
	}
}

func (c *counter) Add(key int, delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] += delta
}

func (c *counter) Values() map[int]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := make(map[int]int)
	for k, v := range c.m {
		m[k] = v
	}
	return m
}

func (c *counter) Equal(m map[int]int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range m {
		if c.m[k] != v {
			return false
		}
	}
	for k, v := range c.m {
		if _, ok := m[k]; !ok && v != 0 {
			return false
		}
	}
	return true
}

func TestCoutner(t *testing.T) {
	c := newCounter()
	c.Add(100, 1)
	c.Add(200, 2)
	c.Add(300, 3)
	if expected := map[int]int{100: 1, 200: 2, 300: 3}; !reflect.DeepEqual(c.m, expected) {
		t.Fatalf("c.m = %v, want %v", c.m, expected)
	}
	if expected := map[int]int{100: 1, 200: 2, 300: 3}; !c.Equal(expected) {
		t.Fatalf("counter(%v).Equal(%v) is false, want true", c.m, expected)
	}

	c.Add(200, -2)
	if expected := map[int]int{100: 1, 200: 0, 300: 3}; !c.Equal(expected) {
		t.Fatalf("counter(%v).Equal(%v) is false, want true", c.m, expected)
	}
	if expected := map[int]int{100: 1, 300: 3}; !c.Equal(expected) {
		t.Fatalf("counter(%v).Equal(%v) is false, want true", c.m, expected)
	}
	if expected := map[int]int{100: 1, 300: 3, 400: 0}; !c.Equal(expected) {
		t.Fatalf("counter(%v).Equal(%v) is false, want true", c.m, expected)
	}

	if expected := map[int]int{100: 1}; c.Equal(expected) {
		t.Fatalf("counter(%v).Equal(%v) is true, want false", c.m, expected)
	}
	if expected := map[int]int{100: 1, 300: 3, 400: 4}; c.Equal(expected) {
		t.Fatalf("counter(%v).Equal(%v) is true, want false", c.m, expected)
	}
}

func TestMaxConnections(t *testing.T) {
	const timeout = 1 * time.Second

	maxRunning := 2
	maxInQueue := 3
	handlerBarrier := make(chan struct{}, maxRunning+maxInQueue+1)
	h := New(maxRunning, maxInQueue, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-handlerBarrier
		http.Error(w, "OK", http.StatusOK)
	}))

	deadline := make(chan time.Time)
	h.newTimer = func(d time.Duration) *time.Timer {
		t := time.NewTimer(d)
		t.C = deadline
		return t
	}
	h.MaxWaitInQueue = 1 // all clients in the queue will be rejected when the channel deadline is closed.

	ts := httptest.NewServer(h)
	defer ts.Close()
	defer func() {
		// Finish all pending requests in case of an error.
		// This prevents ts.Close() from being stuck.
		close(handlerBarrier)
	}()

	c := newCounter()
	done := make(chan struct{})
	wait := func(reason string) {
		select {
		case <-done:
		case <-time.After(timeout):
			t.Fatal(reason)
		}
	}
	for i := 0; i < maxRunning+maxInQueue+1; i++ {
		go func() {
			res, err := http.Get(ts.URL)
			if err != nil {
				t.Errorf("failed to get %s: %s", ts.URL, err)
			}
			c.Add(res.StatusCode, 1)
			done <- struct{}{}
		}()
	}

	wait("timeout while waiting one failed client")

	// expected state: 2 running, 3 in queue, 1 failed
	if expected := map[int]int{503: 1}; !c.Equal(expected) {
		t.Errorf("c = %v, want %v", c.Values(), expected)
	}

	handlerBarrier <- struct{}{}
	wait("timeout while waiting one succeed client")

	// expected state: 2 running, 2 in queue, 1 failed, 1 succeed
	if expected := map[int]int{200: 1, 503: 1}; !c.Equal(expected) {
		t.Errorf("c = %v, want %v", c.Values(), expected)
	}

	close(deadline)
	wait("timeout while waiting the first failed client from the queue")
	wait("timeout while waiting the second failed client from the queue")

	// expected state: 2 running, 0 in queue, 3 failed, 1 succeed
	if expected := map[int]int{200: 1, 503: 3}; !c.Equal(expected) {
		t.Errorf("c = %v, want %v", c.Values(), expected)
	}

	handlerBarrier <- struct{}{}
	handlerBarrier <- struct{}{}
	wait("timeout while waiting the first succeed client")
	wait("timeout while waiting the second succeed client")

	// expected state: 0 running, 0 in queue, 3 failed, 3 succeed
	if expected := map[int]int{200: 3, 503: 3}; !c.Equal(expected) {
		t.Errorf("c = %v, want %v", c.Values(), expected)
	}
}
