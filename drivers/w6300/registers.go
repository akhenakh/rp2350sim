package w6300

// Common registers, addressed in the common block (BSB 0x00).
const (
	regCIDR0  = 0x0000
	regCIDR1  = 0x0001
	regCIDR2  = 0x0004
	regSYSR   = 0x2000
	regSYCR0  = 0x2004
	regSYCR1  = 0x2005
	regIR     = 0x2100
	regSIR    = 0x2101
	regIMR    = 0x2104
	regIRCLR  = 0x2108
	regSIMR   = 0x2114
	regPHYSR  = 0x3000
	regNET4MR = 0x4000
	regSHAR   = 0x4120
	regGAR    = 0x4130
	regSUBR   = 0x4134
	regSIPR   = 0x4138
)

// Socket registers, addressed in the socket-n block (BSB n<<2 | 0x01).
const (
	sockMode       = 0x0000
	sockCmd        = 0x0010
	sockInt        = 0x0020
	sockIntMask    = 0x0024
	sockIntClear   = 0x0028
	sockStatus     = 0x0030
	sockSrcPort    = 0x0114
	sockDestMAC    = 0x0118
	sockDestIP     = 0x0120
	sockDestPort   = 0x0140
	sockTXBufSize  = 0x0200
	sockTXFreeSize = 0x0204
	sockTXReadPtr  = 0x0208
	sockTXWritePtr = 0x020C
	sockRXBufSize  = 0x0220
	sockRXRecvSize = 0x0224
	sockRXReadPtr  = 0x0228
	sockRXWritePtr = 0x022C
)

// Socket commands.
const (
	sockCmdOpen       = 0x01
	sockCmdListen     = 0x02
	sockCmdConnect    = 0x04
	sockCmdDisconnect = 0x08
	sockCmdClose      = 0x10
	sockCmdSend       = 0x20
	sockCmdSendKeep   = 0x22
	sockCmdRecv       = 0x40
)

// Socket statuses.
const (
	sockStatusClosed      = 0x00
	sockStatusInit        = 0x13
	sockStatusListen      = 0x14
	sockStatusSynSent     = 0x15
	sockStatusSynRecv     = 0x16
	sockStatusEstablished = 0x17
	sockStatusFinWait     = 0x18
	sockStatusClosing     = 0x1A
	sockStatusTimeWait    = 0x1B
	sockStatusCloseWait   = 0x1C
	sockStatusLastAck     = 0x1D
	sockStatusUDP         = 0x22
	sockStatusUnknown     = 0xFF
)

// Socket interrupt bits (Sn_IR).
const (
	sockIntConnect uint8 = 1 << iota
	sockIntDisconnect
	sockIntReceive
	sockIntTimeout
	sockIntSendOK

	sockIntUnknown uint8 = 0
)

// Buffer block selects for the socket TX and RX memories.
const (
	blockTX = 0b10
	blockRX = 0b11
)

// sockAddr returns the block select for socket register access.
func sockAddr(sockn uint8) uint8 {
	return sockn<<2 | 0b0001
}

// bufferAddr returns the block select for a socket's TX or RX buffer.
func bufferAddr(sockn uint8, block uint8) uint8 {
	return sockn<<2 | block
}
