// assigncore checks the cores scheduler's machine.AssignCore: two goroutines
// are pinned to core 0 and core 1 and must stay there across sleeps and
// channel wakeups.
//
//	tinygo build -target=pico2 -scheduler=cores -o assigncore.elf .
package main

import (
	"fmt"
	"machine"
	"runtime"
	"time"
)

const rounds = 4

func main() {
	fmt.Printf("assigncore: cpus=%d\n", runtime.NumCPU())

	tick0 := make(chan struct{})
	tick1 := make(chan struct{})
	out := make(chan string, 2)

	go worker(0, tick0, out)
	go worker(1, tick1, out)

	for i := 0; i < rounds; i++ {
		tick0 <- struct{}{}
		tick1 <- struct{}{}
	}
	for i := 0; i < 2; i++ {
		fmt.Println(<-out)
	}
	fmt.Println("assigncore: done")
}

func worker(core int, tick <-chan struct{}, out chan<- string) {
	machine.AssignCore(core)
	ok := true
	for i := 0; i < rounds; i++ {
		if machine.CurrentCore() != core {
			ok = false
		}
		time.Sleep(10 * time.Millisecond)
		if machine.CurrentCore() != core {
			ok = false
		}
		<-tick
		if machine.CurrentCore() != core {
			ok = false
		}
	}
	out <- fmt.Sprintf("assigncore: core=%d ok=%v", core, ok)
}
