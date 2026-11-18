module wifi

go 1.25

require (
	github.com/akhenakh/rp2350sim/drivers/ch9120 v0.0.0
	tinygo.org/x/drivers v0.37.0
)

replace github.com/akhenakh/rp2350sim/drivers/ch9120 => ../../drivers/ch9120
