package web

import "sync"

// StepEvent says "run R just finished step N". Subscribers look the rest up
// in the run log, so events stay tiny.
type StepEvent struct {
	RunID int64
	Step  int
}

// Broker fans step events out to every open dashboard tab. It is the
// classic pub/sub shape: one publisher (the harness loop), many subscribers
// (SSE connections), each with its own buffered channel.
type Broker struct {
	mu   sync.Mutex
	subs map[chan StepEvent]struct{}
}

func NewBroker() *Broker {
	return &Broker{subs: make(map[chan StepEvent]struct{})}
}

func (b *Broker) Subscribe() chan StepEvent {
	ch := make(chan StepEvent, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) Unsubscribe(ch chan StepEvent) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// Publish never blocks: a slow browser tab drops events rather than
// stalling the game loop. It only ever misses intermediate frames, since
// each event carries the latest step.
func (b *Broker) Publish(ev StepEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
