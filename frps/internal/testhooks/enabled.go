package testhooks

import (
	"context"
	"sync"
)

type Controller struct {
	mu       sync.Mutex
	indices  map[string]int
	hits     map[string][]Hit
	waiters  map[barrierKey][]chan Hit
	barriers map[barrierKey]*barrierState
}

type barrierKey struct {
	point string
	index int
}

type barrierState struct {
	arrived chan Hit
	release chan struct{}
}

var (
	globalMu         sync.RWMutex
	globalController *Controller
)

func NewController() *Controller {
	return &Controller{
		indices:  make(map[string]int),
		hits:     make(map[string][]Hit),
		waiters:  make(map[barrierKey][]chan Hit),
		barriers: make(map[barrierKey]*barrierState),
	}
}

func Install(controller *Controller) func() {
	globalMu.Lock()
	previous := globalController
	globalController = controller
	globalMu.Unlock()

	return func() {
		globalMu.Lock()
		globalController = previous
		globalMu.Unlock()
	}
}

func Point(point string, fields ...Field) {
	globalMu.RLock()
	controller := globalController
	globalMu.RUnlock()
	if controller == nil {
		return
	}
	controller.record(point, fields...)
}

func (c *Controller) AddBarrier(point string, hitIndex int) {
	if c == nil || hitIndex <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	key := barrierKey{point: point, index: hitIndex}
	if _, ok := c.barriers[key]; ok {
		return
	}
	c.barriers[key] = &barrierState{
		arrived: make(chan Hit, 1),
		release: make(chan struct{}),
	}
}

func (c *Controller) WaitUntilHit(ctx context.Context, point string, hitIndex int) (Hit, error) {
	if c == nil {
		return Hit{}, context.Canceled
	}
	if hitIndex <= 0 {
		return Hit{}, context.Canceled
	}

	key := barrierKey{point: point, index: hitIndex}

	c.mu.Lock()
	if hits := c.hits[point]; len(hits) >= hitIndex {
		hit := cloneHit(hits[hitIndex-1])
		c.mu.Unlock()
		return hit, nil
	}

	waiter := make(chan Hit, 1)
	c.waiters[key] = append(c.waiters[key], waiter)
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return Hit{}, ctx.Err()
	case hit := <-waiter:
		return cloneHit(hit), nil
	}
}

func (c *Controller) Release(point string, hitIndex int) bool {
	if c == nil || hitIndex <= 0 {
		return false
	}

	c.mu.Lock()
	barrier := c.barriers[barrierKey{point: point, index: hitIndex}]
	c.mu.Unlock()
	if barrier == nil {
		return false
	}

	select {
	case <-barrier.release:
		return false
	default:
		close(barrier.release)
		return true
	}
}

func (c *Controller) Hits(point string) []Hit {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	source := c.hits[point]
	if len(source) == 0 {
		return nil
	}

	cloned := make([]Hit, len(source))
	for index, hit := range source {
		cloned[index] = cloneHit(hit)
	}
	return cloned
}

func (c *Controller) record(point string, fields ...Field) {
	c.mu.Lock()

	index := c.indices[point] + 1
	c.indices[point] = index

	hit := Hit{
		Point:  point,
		Index:  index,
		Fields: fieldsMap(fields),
	}
	c.hits[point] = append(c.hits[point], hit)

	key := barrierKey{point: point, index: index}
	waiters := append([]chan Hit(nil), c.waiters[key]...)
	delete(c.waiters, key)
	barrier := c.barriers[key]

	c.mu.Unlock()

	for _, waiter := range waiters {
		waiter <- cloneHit(hit)
	}

	if barrier == nil {
		return
	}

	barrier.arrived <- cloneHit(hit)
	<-barrier.release
}

func fieldsMap(fields []Field) map[string]any {
	if len(fields) == 0 {
		return nil
	}

	values := make(map[string]any, len(fields))
	for _, field := range fields {
		values[field.Key] = field.Value
	}
	return values
}

func cloneHit(hit Hit) Hit {
	cloned := Hit{
		Point: hit.Point,
		Index: hit.Index,
	}
	if len(hit.Fields) != 0 {
		cloned.Fields = make(map[string]any, len(hit.Fields))
		for key, value := range hit.Fields {
			cloned.Fields[key] = value
		}
	}
	return cloned
}
