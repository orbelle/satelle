package remotechan

import "time"

type Clock interface {
	Now() time.Time
	Tick(d time.Duration) (c <-chan time.Time, stop func())
}

type realClock struct{}

func RealClock() Clock { return realClock{} }

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Tick(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}
