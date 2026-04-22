package clock

import (
	"context"
	"sort"
	"sync"
	"time"
)

type Manual struct {
	mu     sync.Mutex
	cond   *sync.Cond
	now    time.Time
	active int
	nextID int
	jobs   map[int]*manualJob
}

type manualJob struct {
	id       int
	name     string
	ctx      context.Context
	interval time.Duration
	next     time.Time
	fn       func(context.Context, time.Time)
	task     *task
}

func NewManual(start time.Time) *Manual {
	manual := &Manual{
		now:  start.UTC(),
		jobs: make(map[int]*manualJob),
	}
	manual.cond = sync.NewCond(&manual.mu)
	return manual
}

func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *Manual) Go(ctx context.Context, _ string, fn func(context.Context)) Task {
	task := newTask()

	m.mu.Lock()
	m.active++
	m.mu.Unlock()

	go func() {
		defer close(task.done)
		defer m.finishWork()
		fn(ctx)
	}()

	return task
}

func (m *Manual) Every(ctx context.Context, name string, interval time.Duration, fn func(context.Context, time.Time)) Task {
	if interval <= 0 {
		panic("manual scheduler interval must be positive")
	}

	task := newTask()

	m.mu.Lock()
	m.nextID++
	jobID := m.nextID
	m.jobs[jobID] = &manualJob{
		id:       jobID,
		name:     name,
		ctx:      ctx,
		interval: interval,
		next:     m.now.Add(interval),
		fn:       fn,
		task:     task,
	}
	m.mu.Unlock()

	go func() {
		<-ctx.Done()
		m.mu.Lock()
		delete(m.jobs, jobID)
		m.mu.Unlock()
		close(task.done)
	}()

	return task
}

func (m *Manual) Advance(d time.Duration) {
	if d < 0 {
		panic("manual clock cannot move backwards")
	}

	m.mu.Lock()
	m.now = m.now.Add(d)

	for {
		job := m.nextDueJobLocked()
		if job == nil {
			m.mu.Unlock()
			return
		}

		firedAt := job.next
		job.next = m.now.Add(job.interval)
		m.active++
		m.mu.Unlock()

		job.fn(job.ctx, firedAt)

		m.finishWork()
		m.mu.Lock()
	}
}

func (m *Manual) WaitIdle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for m.active > 0 {
		m.cond.Wait()
	}
}

func (m *Manual) PendingJobNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	names := make([]string, 0, len(m.jobs))
	for _, job := range m.jobs {
		names = append(names, job.name)
	}
	sort.Strings(names)
	return names
}

func (m *Manual) finishWork() {
	m.mu.Lock()
	m.active--
	m.cond.Broadcast()
	m.mu.Unlock()
}

func (m *Manual) nextDueJobLocked() *manualJob {
	var due *manualJob
	for _, job := range m.jobs {
		if job.ctx.Err() != nil {
			continue
		}
		if job.next.After(m.now) {
			continue
		}
		if due == nil || job.next.Before(due.next) || (job.next.Equal(due.next) && job.id < due.id) {
			due = job
		}
	}
	return due
}
