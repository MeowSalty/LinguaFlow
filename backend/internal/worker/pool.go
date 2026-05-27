package worker

import "sync"

type Pool struct {
	sem      chan struct{}
	wg       sync.WaitGroup
	errMu    sync.Mutex
	firstErr error
}

func NewPool(concurrency int) *Pool {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Pool{sem: make(chan struct{}, concurrency)}
}

func (p *Pool) Go(fn func() error) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.sem <- struct{}{}
		defer func() { <-p.sem }()
		if err := fn(); err != nil {
			p.errMu.Lock()
			if p.firstErr == nil {
				p.firstErr = err
			}
			p.errMu.Unlock()
		}
	}()
}

func (p *Pool) Wait() error {
	p.wg.Wait()
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.firstErr
}
