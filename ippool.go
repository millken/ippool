package ippool

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"
)

var ErrNoResult = errors.New("empty result")

var defaultInterval = time.Duration(time.Minute * 3)

// Target
type Target struct {
	Host string
	Port int

	Counter  int
	Interval time.Duration
	Timeout  time.Duration
}

func (target Target) String() string {
	return fmt.Sprintf("%s:%d", target.Host, target.Port)
}

type Result struct {
	Counter        int
	SuccessCounter int
	Target         *Target

	MinDuration   time.Duration
	MaxDuration   time.Duration
	TotalDuration time.Duration
}

// Avg return the average time of ping
func (result *Result) Avg() time.Duration {
	if result.SuccessCounter == 0 {
		return 0
	}
	return result.TotalDuration / time.Duration(result.SuccessCounter)
}

func (result *Result) String() string {
	return `
--- ` + result.Target.String() + ` ping statistics ---
` + strconv.Itoa(result.Counter) + ` probes sent, ` + strconv.Itoa(result.SuccessCounter) + ` successful, ` + strconv.Itoa(result.Failed()) + ` failed.
rtt min/avg/max = ` + result.MinDuration.String() + `/` + result.Avg().String() + `/` + result.MaxDuration.String()
}

// Failed return failed counter
func (result Result) Failed() int {
	return result.Counter - result.SuccessCounter
}

type Options struct {
	Interval time.Duration
}

type Pool struct {
	opt Options
	sync.RWMutex
	stop     chan bool
	targets  []*Target
	results  []*Result
	interval time.Duration
}

func New(opts ...Options) *Pool {
	p := &Pool{
		stop: make(chan bool, 1),
	}
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	p.opt = o

	p.prepareOptions()
	return p
}

func (p *Pool) prepareOptions() {
	if p.opt.Interval == 0 {
		p.opt.Interval = defaultInterval
	}
}

func (p *Pool) AddTarget(target *Target) {
	p.Lock()
	defer p.Unlock()
	p.targets = append(p.targets, target)
}

func (p *Pool) Reset() {
	p.Lock()
	defer p.Unlock()
	p.targets = nil
}

func (p *Pool) Start(ctx context.Context) error {
	go func() {
		t := time.NewTicker(1)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				p.Pinger()
				t.Reset(p.interval)
			}
		}
	}()

	return nil
}

func (p *Pool) Results() []*Result {
	p.RLock()
	defer p.RUnlock()
	results := p.results
	return results
}

func (p *Pool) Targets() []*Target {
	p.RLock()
	defer p.RUnlock()
	targets := p.targets
	return targets
}

func (p *Pool) Pinger() {
	resultChan := make(chan *Result)
	for _, target := range p.Targets() {
		go p.ping(target, resultChan)
	}
	var results []*Result
	for range p.targets {
		result := <-resultChan
		results = append(results, result)
	}
	p.Lock()
	p.results = results
	p.Unlock()
}

func (p *Pool) ping(target *Target, ch chan<- *Result) {
	result := &Result{
		Target: target,
	}
	t := time.NewTicker(target.Interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if result.Counter >= target.Counter && target.Counter != 0 {
				ch <- result
				return
			}
			dur, err := timeIt(func() error {
				conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", target.Host, target.Port), target.Timeout)
				if err != nil {
					return err
				}
				conn.Close()
				return nil
			})
			result.Counter++
			duration := time.Duration(dur)
			if err == nil {
				if result.MinDuration == 0 {
					result.MinDuration = duration
				}
				if result.MaxDuration == 0 {
					result.MaxDuration = duration
				}
				result.SuccessCounter++
				if duration > result.MaxDuration {
					result.MaxDuration = duration
				} else if duration < result.MinDuration {
					result.MinDuration = duration
				}
				result.TotalDuration += duration
			}
		}
	}

}

func (p *Pool) Stop(ctx context.Context) error {
	p.stop <- true
	return nil
}

func (p *Pool) GetFastestTarget() (*Target, error) {
	results := p.Results()
	if len(results) == 0 {
		return nil, ErrNoResult
	}
	res := results[0]
	for _, result := range results {
		if result.SuccessCounter > result.Failed() && res.Avg() <= result.Avg() {
			res = result
		}
	}
	return res.Target, nil
}

func (p *Pool) GetRandomTarget() (*Target, error) {
	results := make([]*Result, 0)

	for _, result := range p.Results() {
		if result.SuccessCounter > result.Failed() {
			results = append(results, result)
		}
	}
	if len(results) == 0 {
		return nil, ErrNoResult
	}
	return results[rand.Intn(len(results))].Target, nil
}
