package workspace

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestExecExitNotificationRegistrationRace(t *testing.T) {
	for i := 0; i < 1000; i++ {
		stream := &execStream{}
		var notifications atomic.Int32
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); stream.OnExit(func() { notifications.Add(1) }) }()
		go func() { defer wg.Done(); stream.notifyExit() }()
		wg.Wait()
		stream.notifyExit()
		if notifications.Load() != 1 {
			t.Fatalf("iteration %d: exit notifications=%d", i, notifications.Load())
		}
	}
}
