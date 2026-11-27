package ble

import "encoding/binary"

// ATT opcodes used by the scripted environmental-sensing peer.
const (
	attErrorResponse          byte = 0x01
	attExchangeMTUReq         byte = 0x02
	attExchangeMTURsp         byte = 0x03
	attFindInfoReq            byte = 0x04
	attFindInfoRsp            byte = 0x05
	attFindByTypeValueReq     byte = 0x06
	attFindByTypeValueRsp     byte = 0x07
	attReadByTypeReq          byte = 0x08
	attReadByTypeRsp          byte = 0x09
	attReadReq                byte = 0x0A
	attReadRsp                byte = 0x0B
	attReadByGroupReq         byte = 0x10
	attReadByGroupRsp         byte = 0x11
	attWriteReq               byte = 0x12
	attWriteRsp               byte = 0x13
	attHandleValueNotify      byte = 0x1B
	attErrAttributeNotFound   byte = 0x0A
	attErrRequestNotSupported byte = 0x06
)

// The peer exposes the Environmental Sensing service (0x181A) with a
// Temperature characteristic (0x2A6E), matching the stock Pico W
// ble_temp_reader example.
const (
	serviceStart    uint16 = 0x0010
	serviceEnd      uint16 = 0x0014
	charDeclHandle  uint16 = 0x0011
	charValueHandle uint16 = 0x0012
	cccdHandle      uint16 = 0x0013
	envSensingUUID  uint16 = 0x181A
	temperatureUUID uint16 = 0x2A6E
)

// peer is the scripted GATT server on the other side of the air.
type peer struct {
	mtu          uint16
	temperature  uint16
	notify       bool
	notifyHandle uint16
}

func newPeer() *peer {
	return &peer{mtu: 23, temperature: 2345}
}

// handleATT processes one ATT request and returns the response PDU. It also
// reports whether the request enabled notifications.
func (p *peer) handleATT(req []byte) ([]byte, bool) {
	if len(req) == 0 {
		return nil, false
	}
	switch req[0] {
	case attExchangeMTUReq:
		if len(req) < 3 {
			return p.errorRsp(attExchangeMTUReq, 0x0000, 0x01), false
		}
		p.mtu = max(min(binary.LittleEndian.Uint16(req[1:]), 247), 23)
		return []byte{attExchangeMTURsp, byte(p.mtu), byte(p.mtu >> 8)}, false

	case attFindByTypeValueReq:
		if len(req) < 9 {
			return p.errorRsp(attFindByTypeValueReq, 0x0000, 0x01), false
		}
		start := binary.LittleEndian.Uint16(req[1:])
		end := binary.LittleEndian.Uint16(req[3:])
		typ := binary.LittleEndian.Uint16(req[5:])
		value := binary.LittleEndian.Uint16(req[7:])
		if typ != 0x2800 || value != envSensingUUID ||
			start > serviceStart || end < serviceEnd {
			return p.errorRsp(attFindByTypeValueReq, start, attErrAttributeNotFound), false
		}
		// Find By Type Value Response has no length byte: the pairs start
		// immediately after the opcode.
		return []byte{attFindByTypeValueRsp,
			byte(serviceStart), byte(serviceStart >> 8),
			byte(serviceEnd), byte(serviceEnd >> 8)}, false

	case attReadByGroupReq:
		if len(req) < 7 {
			return p.errorRsp(attReadByGroupReq, 0x0000, 0x01), false
		}
		start := binary.LittleEndian.Uint16(req[1:])
		end := binary.LittleEndian.Uint16(req[3:])
		typ := binary.LittleEndian.Uint16(req[5:])
		if typ != 0x2800 || start > serviceStart || end < serviceEnd {
			return p.errorRsp(attReadByGroupReq, start, attErrAttributeNotFound), false
		}
		rsp := []byte{attReadByGroupRsp, 6,
			byte(serviceStart), byte(serviceStart >> 8),
			byte(serviceEnd), byte(serviceEnd >> 8),
			byte(envSensingUUID & 0xff), byte(envSensingUUID >> 8)}
		return rsp, false

	case attReadByTypeReq:
		if len(req) < 7 {
			return p.errorRsp(attReadByTypeReq, 0x0000, 0x01), false
		}
		start := binary.LittleEndian.Uint16(req[1:])
		end := binary.LittleEndian.Uint16(req[3:])
		typ := binary.LittleEndian.Uint16(req[5:])
		if typ != 0x2803 || start > charDeclHandle || end < charDeclHandle {
			return p.errorRsp(attReadByTypeReq, start, attErrAttributeNotFound), false
		}
		rsp := []byte{attReadByTypeRsp, 7,
			byte(charDeclHandle), byte(charDeclHandle >> 8),
			0x12, // READ | NOTIFY
			byte(charValueHandle), byte(charValueHandle >> 8),
			byte(temperatureUUID & 0xff), byte(temperatureUUID >> 8)}
		return rsp, false

	case attFindInfoReq:
		if len(req) < 5 {
			return p.errorRsp(attFindInfoReq, 0x0000, 0x01), false
		}
		start := binary.LittleEndian.Uint16(req[1:])
		end := binary.LittleEndian.Uint16(req[3:])
		if start > cccdHandle || end < cccdHandle {
			return p.errorRsp(attFindInfoReq, start, attErrAttributeNotFound), false
		}
		return []byte{attFindInfoRsp, 0x01,
			byte(cccdHandle), byte(cccdHandle >> 8),
			0x02, 0x29}, false

	case attReadReq:
		if len(req) < 3 {
			return p.errorRsp(attReadReq, 0x0000, 0x01), false
		}
		handle := binary.LittleEndian.Uint16(req[1:])
		switch handle {
		case charValueHandle:
			return []byte{attReadRsp,
				byte(p.temperature), byte(p.temperature >> 8)}, false
		case cccdHandle:
			return []byte{attReadRsp, 0x00, 0x00}, false
		default:
			return p.errorRsp(attReadReq, handle, attErrAttributeNotFound), false
		}

	case attWriteReq:
		if len(req) < 3 {
			return p.errorRsp(attWriteReq, 0x0000, 0x01), false
		}
		handle := binary.LittleEndian.Uint16(req[1:])
		if handle != cccdHandle || len(req) < 5 {
			return p.errorRsp(attWriteReq, handle, attErrAttributeNotFound), false
		}
		enabled := binary.LittleEndian.Uint16(req[3:]) == 1
		p.notify = enabled
		p.notifyHandle = charValueHandle
		return []byte{attWriteRsp}, enabled

	default:
		return p.errorRsp(req[0], 0x0000, attErrRequestNotSupported), false
	}
}

func (p *peer) errorRsp(op byte, handle uint16, reason byte) []byte {
	return []byte{attErrorResponse, op, byte(handle), byte(handle >> 8), reason}
}

// notification builds a Handle Value Notification carrying the temperature.
func (p *peer) notification() []byte {
	if !p.notify {
		return nil
	}
	return []byte{attHandleValueNotify,
		byte(p.notifyHandle), byte(p.notifyHandle >> 8),
		byte(p.temperature), byte(p.temperature >> 8)}
}
