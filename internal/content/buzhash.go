package content

// BuzHash is a simple rolling hash used for splitting byte streams into blocks.
type BuzHash struct {
	hash   uint32
	window []byte
	pos    int
	table  [256]uint32
}

func NewBuzHash(windowSize int) *BuzHash {
	b := &BuzHash{
		window: make([]byte, windowSize),
	}
	// Initialize table with some pseudo-random values
	for i := range 256 {
		b.table[i] = uint32(i) * 0x5bd1e995
	}
	return b
}

func (b *BuzHash) WriteB(c byte) uint32 {
	oldNum := b.window[b.pos]
	b.window[b.pos] = c
	b.pos = (b.pos + 1) % len(b.window)

	b.hash = (b.hash<<1 | b.hash>>31) ^ b.table[c] ^ (b.table[oldNum]<<1 | b.table[oldNum]>>31)
	return b.hash
}
