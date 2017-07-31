package maxconnections

import (
	"context"
	"net/http"
	"time"
)

func defaultOverloadHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "503 service is overloaded, please try again later", http.StatusServiceUnavailable)
}

// OverloadHandler is a default OverloadHandler for Middleware.
var OverloadHandler http.Handler = http.HandlerFunc(defaultOverloadHandler)

// Middleware implements the http.Handler interface.
type Middleware struct {
	// running is a buffered channel. Before invoking the handler, an empty
	// struct is sent to the channel. When the handler is finished, one element
	// is received back from the channel. If the channel's buffer is full, the
	// request is enqueued.
	running chan struct{}

	// queue is a buffered channel. An empty struct is placed into the channel
	// while a request is waiting for a spot in the running channel's buffer.
	// If the queue channel's buffer is full, the request is proccess by
	// OverloadHandler.
	queue chan struct{}

	// handler to invoke.
	handler http.Handler

	// MaxWaitInQueue is a maximum wait time in the queue.
	MaxWaitInQueue time.Duration

	// OverloadHandler is called if there is no space in running and queue
	// channels.
	OverloadHandler http.Handler

	// newTimer allows to override the function newTimer for tests.
	newTimer func(d time.Duration) *time.Timer
}

// New returns an http.Handler that runs no more than maxRunning h at the same
// time. It can enqueue up to maxInQueue requests awaiting to be run, for other
// requests OverloadHandler will be invoked.
func New(maxRunning, maxInQueue int, h http.Handler) *Middleware {
	return &Middleware{
		running: make(chan struct{}, maxRunning),
		queue:   make(chan struct{}, maxInQueue),
		handler: h,

		OverloadHandler: OverloadHandler,
		newTimer:        time.NewTimer,
	}
}

func (m *Middleware) enqueueRunning(ctx context.Context) bool {
	select {
	case m.running <- struct{}{}:
		return true
	default:
	}

	// Slow-path.
	select {
	case m.queue <- struct{}{}:
		defer func() {
			<-m.queue
		}()
	default:
		return false
	}

	var timer *time.Timer
	var timeout <-chan time.Time
	if m.MaxWaitInQueue > 0 {
		timer = m.newTimer(m.MaxWaitInQueue)
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case m.running <- struct{}{}:
		return true
	case <-timeout:
	case <-ctx.Done():
	}
	return false
}

func (m *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.enqueueRunning(r.Context()) {
		defer func() {
			<-m.running
		}()
		m.handler.ServeHTTP(w, r)
		return
	}

	m.OverloadHandler.ServeHTTP(w, r)
}
