package ui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

var braille = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// Spinner displays an animated progress indicator on stderr.
type Spinner struct {
	mu      sync.Mutex
	msg     string
	running bool
	done    chan struct{}
	w       io.Writer
}

// NewSpinner creates a spinner that writes to stderr.
func NewSpinner() *Spinner {
	return &Spinner{w: os.Stderr}
}

// Start begins the animation with the given message.
// Falls back to a plain print when color is disabled.
func (s *Spinner) Start(msg string) {
	if !Enabled() {
		fmt.Fprintf(s.w, "%s\n", msg)
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.msg = msg
	s.running = true
	s.done = make(chan struct{})
	s.mu.Unlock()

	go s.animate()
}

// Update changes the spinner message mid-flight.
func (s *Spinner) Update(msg string) {
	s.mu.Lock()
	s.msg = msg
	s.mu.Unlock()
}

// Stop halts the animation and prints a final message.
func (s *Spinner) Stop(msg string) {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		if Enabled() {
			fmt.Fprintf(s.w, "%s\n", msg)
		}
		return
	}
	s.running = false
	close(s.done)
	s.mu.Unlock()

	// Clear the spinner line and print final message.
	fmt.Fprintf(s.w, "\r\033[K%s\n", msg)
}

func (s *Spinner) animate() {
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()

	i := 0
	for {
		select {
		case <-s.done:
			return
		case <-tick.C:
			s.mu.Lock()
			msg := s.msg
			s.mu.Unlock()
			fmt.Fprintf(s.w, "\r\033[K%c %s", braille[i%len(braille)], msg)
			i++
		}
	}
}
