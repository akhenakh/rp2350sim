module w6300test

go 1.25

require (
	github.com/akhenakh/rp2350sim/drivers/w6300 v0.0.0
	tinygo.org/x/drivers v0.37.0
)

require github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510 // indirect

replace github.com/akhenakh/rp2350sim/drivers/w6300 => ../../drivers/w6300
