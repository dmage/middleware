package maxconnections

import (
	"net/http"
	"time"
)

func defaultOverloadHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "503 service is overloaded, please try again later", http.StatusServiceUnavailable)
}

// OverloadHandler is a default OverloadHandler for Middleware.
var OverloadHandler http.Handler = http.HandlerFunc(defaultOverloadHandler)

// openTimeChannel is a channel receiving from which blocks forever.
var openTimeChannel <-chan time.Time = make(chan time.Time)

// Middleware implements the http.Handler interface.
type Middleware struct {
	// running is a buffered channel. Before running a handler, an empty struct
	// is sent to the channel. When the handler is finished, one element is
	// received back from the channel. If the channel's buffer is full, the
	// request is enqueued.
	running chan struct{}

	// queue is a buffered channel. An empty struct is placed into the channel
	// while the request is waiting for a spot in the buffer of the channel
	// running. If queue's buffer is full, the request is proccess by
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
	}
}

func (m *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case m.running <- struct{}{}:
		defer func() {
			<-m.running
		}()
		m.handler.ServeHTTP(w, r)
		return
	default:
	}

	// Slow-path.
	overloadHandler := m.OverloadHandler
	if overloadHandler == nil {
		overloadHandler = OverloadHandler
	}

	select {
	case m.queue <- struct{}{}:
	default:
		overloadHandler.ServeHTTP(w, r)
		return
	}

	timeout := openTimeChannel
	var timer *time.Timer
	if m.MaxWaitInQueue > 0 {
		newTimer := m.newTimer
		if newTimer == nil {
			newTimer = time.NewTimer
		}
		timer = newTimer(m.MaxWaitInQueue)
		timeout = timer.C
	}

	select {
	case m.running <- struct{}{}:
		<-m.queue
		if timer != nil {
			timer.Stop()
		}

		defer func() {
			<-m.running
		}()
		m.handler.ServeHTTP(w, r)
		return
	case <-timeout:
		<-m.queue
		overloadHandler.ServeHTTP(w, r)
		return
	case <-r.Context().Done():
		<-m.queue
		if timer != nil {
			timer.Stop()
		}
		// Did the client go away? Do we need to send a response?
	}
}
