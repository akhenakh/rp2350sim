package devices

import "github.com/akhenakh/rp2350sim/internal/rp2350"

// CANFrame is one classic CAN 2.0B frame.
type CANFrame struct {
	ID   uint32
	Ext  bool
	RTR  bool
	Data []byte
	Time uint64
}

// CANNode is a device attached to a CAN bus.
type CANNode interface {
	Receive(CANFrame)
}

// CANBus connects controllers and simulated peers.
type CANBus struct {
	chip  *rp2350.Chip
	nodes []CANNode
}

// NewCANBus creates a bus on the emulator clock.
func NewCANBus(c *rp2350.Chip) *CANBus { return &CANBus{chip: c} }

// Attach adds a node.
func (b *CANBus) Attach(n CANNode) { b.nodes = append(b.nodes, n) }

// Send delivers a frame to every node except the sender.
func (b *CANBus) Send(frame CANFrame, from CANNode) {
	frame.Time = b.chip.Cycles
	for _, n := range b.nodes {
		if n != from {
			n.Receive(frame)
		}
	}
}

// CANPeer is a small simulated ECU: it answers matching request IDs with a
// response frame carrying the request byte plus one.
type CANPeer struct {
	bus     *CANBus
	id      uint32
	ext     bool
	replyID uint32
}

// NewCANPeer attaches a responder for id. Responses use replyID.
func NewCANPeer(bus *CANBus, id, replyID uint32, ext bool) *CANPeer {
	p := &CANPeer{bus: bus, id: id, ext: ext, replyID: replyID}
	bus.Attach(p)
	return p
}

// Receive answers a matching data frame.
func (p *CANPeer) Receive(f CANFrame) {
	if f.ID != p.id || f.RTR {
		return
	}
	data := append([]byte(nil), f.Data...)
	for i := range data {
		data[i]++
	}
	p.bus.Send(CANFrame{ID: p.replyID, Ext: p.ext, Data: data}, p)
}
