package throttle_test

import (
	"testing"
	"time"

	"github.com/mc-agents/operator/pkg/throttle"
)

func TestIntervalGateSpacesSpawns(t *testing.T) {
	gate := throttle.NewIntervalGate(4 * time.Second)
	start := time.Unix(0, 0)

	if ok, _ := gate.Acquire(start); !ok {
		t.Fatal("the first spawn must go through immediately")
	}

	ok, wait := gate.Acquire(start.Add(time.Second))
	if ok {
		t.Fatal("a second spawn one second later must be held back")
	}
	if wait != 3*time.Second {
		t.Fatalf("wait is %s, want 3s", wait)
	}

	if ok, _ := gate.Acquire(start.Add(4 * time.Second)); !ok {
		t.Fatal("the gate must open once the interval has passed")
	}
}

func TestIntervalGateWithoutIntervalNeverBlocks(t *testing.T) {
	gate := throttle.NewIntervalGate(0)
	now := time.Unix(0, 0)
	for i := 0; i < 3; i++ {
		if ok, _ := gate.Acquire(now); !ok {
			t.Fatalf("acquire %d was blocked with no interval configured", i)
		}
	}
}
