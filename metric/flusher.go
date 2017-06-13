package metric

import (
	"sync"
	"time"
	"github.com/One-com/gone/metric/num64"
)

// This file is a shared implementation of two types of flushers: static/fixed or dynamic
// A flusher either flushes at a given interval, or when it's asked to do so by a Meter (which has filled its internal buffer)

// A flusher is either run with a fixed flushinterval with a go-routine which
// exits on stop(), or with a dynamic changeable flushinterval in a permanent go-routine.
// This is chosen by either calling run() og rundyn()
const (
	flusherTypeUndef = iota
	flusherTypeFixed
	flusherTypeDynamic // used for the defaultFlusher
)

// To make a flusher private but still make other code able to define SetFlusher()

//// Flusher is a
//type Flusher struct {
//	*flusher
//}

type flusher struct {
	// Tell the flusher to exit - or (for defaultFlusher) restart
	stopChan chan struct{}
	// Kick the flusher to reconsider interval (used for defaultFlusher)
	kickChan chan struct{}

	// The flusher interval
	interval time.Duration

	// The Meters (metrics objects) being flushed by this flusher
	mu     sync.Mutex
	meters []meter

	// only set once by the run/rundyn method to fix how the flusher is used.
	ftype int

	// The sink of data being flushed. Created from a SinkFactory.
	// The Sink is guaranteed to be called under an external lock, so it
	// doesn't need to use locking it self.
	sink Sink
}

func newFlusher(interval time.Duration) *flusher {
	f := &flusher{interval: interval, sink: &nilSink{}}
	f.stopChan = make(chan struct{})
	return f
}

func (f *flusher) setSink(sink SinkFactory) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sink = sink.Sink()
}

func (f *flusher) stop() {
	f.stopChan <- struct{}{}
}

func (f *flusher) setInterval(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ftype != flusherTypeFixed {
		f.interval = d
		select {
		case f.kickChan <- struct{}{}:
		default:
		}
	}
}

// A go-routine which will flush at adjustable intervals and doesn't
// exit if interval is zero.
// This is used for the defaultFlusher of the Client
func (f *flusher) rundyn() {
	var interval time.Duration

	f.mu.Lock()
	if f.ftype == flusherTypeFixed {
		panic("Attempt to make fixed flusher dynamic")
	} else {
		f.ftype = flusherTypeDynamic
	}

	f.kickChan = make(chan struct{})
	f.mu.Unlock()

	var ticker *time.Ticker

RUNNING: // two cases - either with a flush or not
	for {
		f.mu.Lock()
		// take any new interval into account
		interval = f.interval
		f.mu.Unlock()
		if interval == 0 {
			// sit here waiting doing nothing
			select {
			case <-f.stopChan:
				break RUNNING
			case <-f.kickChan:
			}
		} else {
			ticker = time.NewTicker(interval)
		LOOP:
			for {
				select {
				case <-f.stopChan:
					ticker.Stop()
					break RUNNING
				case <-f.kickChan:
					ticker.Stop()
					break LOOP // to test to make a new ticker
				case <-ticker.C:
					f.Flush()
				}
			}
		}
	}
	f.Flush()
}

// Run the flusher until stopchan.
// The flusher is fixed to be a flusherTypeFixed.
func (f *flusher) run(done *sync.WaitGroup) {
	defer done.Done()

	f.mu.Lock()
	if f.ftype == flusherTypeDynamic {
		panic("Attempt to make default flusher fixed")
	} else {
		f.ftype = flusherTypeFixed
	}
	f.mu.Unlock()

	if f.interval == 0 {
		// don't start a meaningless flusher
		return
	}

	ticker := time.NewTicker(f.interval)
LOOP:
	for {
		select {
		case <-f.stopChan:
			ticker.Stop()
			break LOOP
		case <-ticker.C:
			f.Flush()
		}
	}
	f.Flush()
}

// flush a single meter. Sync with the Flusher mutex
func (f *flusher) FlushMeter(m meter) {
	f.mu.Lock()
	m.Flush(f.sink)
	f.mu.Unlock()
}

// flush all meters. Sync with the Flusher mutex
func (f *flusher) Flush() {
	f.mu.Lock()
	for _, m := range f.meters {
		m.Flush(f.sink)
	}
	f.sink.Flush()
	f.mu.Unlock()
}

// Register a meter in the flusher. If the meters needs to know
// the flushe to do autoflushing, tell it.
func (f *flusher) register(m meter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meters = append(f.meters, m)
	if a, ok := m.(autoFlusher); ok {
		//a.SetFlusher(Flusher{f})
		a.SetFlusher(f)
	}
}

func (f *flusher) Record(mtype int, name string, value interface{}, flush bool) {
	f.mu.Lock()
	f.sink.Record(mtype, name, value)
	if flush {
		f.sink.Flush()
	}
	f.mu.Unlock()
}

func (f *flusher) RecordNumeric64(mtype int, name string, value num64.Numeric64, flush bool) {
	f.mu.Lock()
	f.sink.RecordNumeric64(mtype, name, value)
	if flush {
		f.sink.Flush()
	}
	f.mu.Unlock()
}

func (f *flusher) FlushSink() {
	f.mu.Lock()
	f.sink.Flush()
	f.mu.Unlock()
}
