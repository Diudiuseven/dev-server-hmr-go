package coalescer

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestDbgRapid2(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New[string](40 * time.Millisecond)
	go c.Run(ctx)
	// first cycle
	c.Signal("init")
	fmt.Println("first", <-c.Out())
	// rapid burst, consuming emissions as they arrive like the engine
	go func() {
		for i := 0; i < 50; i++ {
			c.Signal("c")
		}
	}()
	deadline := time.After(800 * time.Millisecond)
	got := 0
loop:
	for {
		select {
		case b := <-c.Out():
			got += len(b)
			fmt.Println("batch", b)
		case <-deadline:
			break loop
		}
	}
	fmt.Println("total payloads:", got)
}
