package clock

import "time"

type Clock interface {
	Now() time.Time
}

type Task interface {
	Done() <-chan struct{}
}
