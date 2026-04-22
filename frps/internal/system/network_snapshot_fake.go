package system

import (
	"errors"
	"sync"
)

type StaticSnapshotReader struct {
	Snapshot Snapshot
}

type MutableSnapshotReader struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

type CollectResult struct {
	Snapshot Snapshot
	Err      error
}

type ScriptedCollector struct {
	mu       sync.Mutex
	platform string
	results  []CollectResult
}

func NewStaticSnapshotReader(snapshot Snapshot) StaticSnapshotReader {
	return StaticSnapshotReader{Snapshot: cloneSnapshot(snapshot)}
}

func NewMutableSnapshotReader(snapshot Snapshot) *MutableSnapshotReader {
	return &MutableSnapshotReader{snapshot: cloneSnapshot(snapshot)}
}

func NewScriptedCollector(platform string, results ...CollectResult) *ScriptedCollector {
	cloned := make([]CollectResult, len(results))
	for index, result := range results {
		cloned[index] = CollectResult{
			Snapshot: cloneSnapshot(result.Snapshot),
			Err:      result.Err,
		}
	}
	return &ScriptedCollector{
		platform: platform,
		results:  cloned,
	}
}

func (r StaticSnapshotReader) Current() Snapshot {
	return cloneSnapshot(r.Snapshot)
}

func (r *MutableSnapshotReader) Current() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneSnapshot(r.snapshot)
}

func (r *MutableSnapshotReader) Set(snapshot Snapshot) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.snapshot = cloneSnapshot(snapshot)
	r.mu.Unlock()
}

func (c *ScriptedCollector) Platform() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.platform
}

func (c *ScriptedCollector) Collect() (Snapshot, error) {
	if c == nil {
		return Snapshot{}, errors.New("collector is nil")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.results) == 0 {
		return Snapshot{}, errors.New("unexpected extra collect")
	}

	result := c.results[0]
	c.results = c.results[1:]
	if result.Err != nil {
		return Snapshot{}, result.Err
	}
	return cloneSnapshot(result.Snapshot), nil
}
