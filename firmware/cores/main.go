// cores checks the multicore scheduler: it builds with -scheduler=cores, runs a
// goroutine on the second core and reports which core each task ran on.
//
//	tinygo build -target=pico2 -scheduler=cores -o cores.elf .
package main

import (
	"fmt"
	"machine"
	"runtime"
	"time"
)

const iterations = 10

type report struct {
	id    int
	cores [2]bool
}

func main() {
	machine.GPIO25.Configure(machine.PinConfig{Mode: machine.PinOutput})
	machine.GPIO15.Configure(machine.PinConfig{Mode: machine.PinOutput})

	fmt.Printf("cores: cpus=%d main=%d\n", runtime.NumCPU(), machine.CurrentCore())

	ch := make(chan report, 2)
	go blink(0, machine.GPIO15, ch)
	// Let the second core pick up the goroutine before this task sleeps.
	time.Sleep(time.Millisecond)
	go blink(1, machine.GPIO25, ch)

	first := <-ch
	second := <-ch
	for _, r := range []report{first, second} {
		fmt.Printf("worker %d cores=%v\n", r.id, r.cores)
	}
	fmt.Println("cores: done")
}

func blink(id int, pin machine.Pin, ch chan<- report) {
	var r report
	r.id = id
	for i := 0; i < iterations; i++ {
		r.cores[machine.CurrentCore()] = true
		pin.Set(i%2 == 0)
		time.Sleep(20 * time.Millisecond)
	}
	pin.Set(false)
	ch <- r
}
