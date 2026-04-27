package zipstream

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	deflateStoredBlock  = 0
	deflateFixedBlock   = 1
	deflateDynamicBlock = 2

	maxDeflateCodeBits    = 15
	maxDeflateCodeLenBits = 7

	maxDeflateLitLenSymbols = 286
	maxDeflateDistSymbols   = 32
	maxDeflateCodeLenSymbol = 19
	maxDeflateLitLenNodes   = 1 + maxDeflateLitLenSymbols*maxDeflateCodeBits
	maxDeflateDistNodes     = 1 + maxDeflateDistSymbols*maxDeflateCodeBits
	maxDeflateCodeLenNodes  = 1 + maxDeflateCodeLenSymbol*maxDeflateCodeLenBits
)

var (
	fixedDeflateLitLen *deflateHuffman
	fixedDeflateDist   *deflateHuffman

	deflateScanWorkspacePool = sync.Pool{
		New: func() any {
			return new(deflateScanWorkspace)
		},
	}
)

func init() {
	fixedDeflateLitLen = mustDeflateHuffman(fixedDeflateLitLenLengths(), maxDeflateCodeBits)
	fixedDeflateDist = mustDeflateHuffman(fixedDeflateDistLengths(), maxDeflateCodeBits)
}

// scanDeflateEOF consumes a raw DEFLATE stream until the final block's
// end-of-block marker. It parses Huffman tokens and counts the bytes that would
// be produced, but it does not materialize output or perform back-reference
// copies.
func scanDeflateEOF(r byteReader) (compressedSize uint64, uncompressedSize uint64, err error) {
	br := &deflateBitReader{r: r}
	ws := deflateScanWorkspacePool.Get().(*deflateScanWorkspace)
	defer deflateScanWorkspacePool.Put(ws)

	for {
		final, err := br.readBits(1)
		if err != nil {
			return br.nread, uncompressedSize, err
		}
		blockType, err := br.readBits(2)
		if err != nil {
			return br.nread, uncompressedSize, err
		}

		switch blockType {
		case deflateStoredBlock:
			uncompressedSize, err = scanDeflateStoredBlock(br, uncompressedSize)
		case deflateFixedBlock:
			uncompressedSize, err = scanDeflateCompressedBlock(br, fixedDeflateLitLen, fixedDeflateDist, uncompressedSize)
		case deflateDynamicBlock:
			var litLen, dist deflateHuffman
			if err := readDynamicDeflateHuffman(br, &litLen, &dist, ws); err != nil {
				return br.nread, uncompressedSize, err
			}
			uncompressedSize, err = scanDeflateCompressedBlock(br, &litLen, &dist, uncompressedSize)
		default:
			err = errors.New("invalid deflate block type")
		}
		if err != nil {
			return br.nread, uncompressedSize, err
		}
		if final == 1 {
			return br.nread, uncompressedSize, nil
		}
	}
}

type deflateScanWorkspace struct {
	litLenNodes    [maxDeflateLitLenNodes]deflateHuffmanNode
	distNodes      [maxDeflateDistNodes]deflateHuffmanNode
	codeLenNodes   [maxDeflateCodeLenNodes]deflateHuffmanNode
	codeLenLengths [maxDeflateCodeLenSymbol]uint8
	lengths        [maxDeflateLitLenSymbols + maxDeflateDistSymbols]uint8
}

type deflateBitReader struct {
	r     byteReader
	bits  uint64
	nbits uint8
	nread uint64
}

func (r *deflateBitReader) readBits(n uint8) (uint32, error) {
	for r.nbits < n {
		b, err := r.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		r.bits |= uint64(b) << r.nbits
		r.nbits += 8
		r.nread++
	}

	v := uint32(r.bits & ((uint64(1) << n) - 1))
	r.bits >>= n
	r.nbits -= n
	return v, nil
}

func (r *deflateBitReader) alignByte() {
	r.bits = 0
	r.nbits = 0
}

func (r *deflateBitReader) readAlignedByte() (byte, error) {
	b, err := r.r.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	r.nread++
	return b, nil
}

func (r *deflateBitReader) readAlignedUint16() (uint16, error) {
	lo, err := r.readAlignedByte()
	if err != nil {
		return 0, err
	}
	hi, err := r.readAlignedByte()
	if err != nil {
		return 0, err
	}
	return uint16(lo) | uint16(hi)<<8, nil
}

func (r *deflateBitReader) skipAlignedBytes(n uint64) error {
	var buf [4096]byte
	for n > 0 {
		size := len(buf)
		if uint64(size) > n {
			size = int(n)
		}
		nr, err := r.r.Read(buf[:size])
		r.nread += uint64(nr)
		n -= uint64(nr)
		if n == 0 {
			return nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		if nr == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

func scanDeflateStoredBlock(r *deflateBitReader, out uint64) (uint64, error) {
	r.alignByte()
	length, err := r.readAlignedUint16()
	if err != nil {
		return out, err
	}
	nlength, err := r.readAlignedUint16()
	if err != nil {
		return out, err
	}
	if length^nlength != 0xffff {
		return out, errors.New("invalid deflate stored block length")
	}
	out, err = addDeflateOutput(out, uint64(length))
	if err != nil {
		return out, err
	}
	return out, r.skipAlignedBytes(uint64(length))
}

func scanDeflateCompressedBlock(r *deflateBitReader, litLen, dist *deflateHuffman, out uint64) (uint64, error) {
	for {
		sym, err := litLen.decode(r)
		if err != nil {
			return out, err
		}

		switch {
		case sym < 256:
			out, err = addDeflateOutput(out, 1)
		case sym == 256:
			return out, nil
		case sym <= 285:
			length, err := readDeflateLength(r, sym)
			if err != nil {
				return out, err
			}
			distance, err := readDeflateDistance(r, dist)
			if err != nil {
				return out, err
			}
			if uint64(distance) > out {
				return out, errors.New("invalid deflate distance")
			}
			out, err = addDeflateOutput(out, uint64(length))
		default:
			return out, fmt.Errorf("invalid deflate literal/length symbol %d", sym)
		}
		if err != nil {
			return out, err
		}
	}
}

func readDynamicDeflateHuffman(r *deflateBitReader, litLen, dist *deflateHuffman, ws *deflateScanWorkspace) error {
	hlitBits, err := r.readBits(5)
	if err != nil {
		return err
	}
	hdistBits, err := r.readBits(5)
	if err != nil {
		return err
	}
	hclenBits, err := r.readBits(4)
	if err != nil {
		return err
	}

	hlit := int(hlitBits) + 257
	hdist := int(hdistBits) + 1
	hclen := int(hclenBits) + 4

	codeLenLengths := ws.codeLenLengths[:]
	clear(codeLenLengths)
	for i := 0; i < hclen; i++ {
		length, err := r.readBits(3)
		if err != nil {
			return err
		}
		codeLenLengths[deflateCodeLenOrder[i]] = uint8(length)
	}

	var codeLen deflateHuffman
	if err := initDeflateHuffman(&codeLen, codeLenLengths, maxDeflateCodeLenBits, ws.codeLenNodes[:]); err != nil {
		return err
	}
	if codeLen.empty() {
		return errors.New("empty deflate code length tree")
	}

	lengths := ws.lengths[:hlit+hdist]
	clear(lengths)
	for i := 0; i < len(lengths); {
		sym, err := codeLen.decode(r)
		if err != nil {
			return err
		}

		switch {
		case sym <= 15:
			lengths[i] = uint8(sym)
			i++
		case sym == 16:
			if i == 0 {
				return errors.New("invalid deflate repeat length")
			}
			extra, err := r.readBits(2)
			if err != nil {
				return err
			}
			repeat := int(extra) + 3
			if i+repeat > len(lengths) {
				return errors.New("deflate repeat length overflows alphabet")
			}
			for j := 0; j < repeat; j++ {
				lengths[i+j] = lengths[i-1]
			}
			i += repeat
		case sym == 17:
			extra, err := r.readBits(3)
			if err != nil {
				return err
			}
			repeat := int(extra) + 3
			if i+repeat > len(lengths) {
				return errors.New("deflate zero repeat overflows alphabet")
			}
			i += repeat
		case sym == 18:
			extra, err := r.readBits(7)
			if err != nil {
				return err
			}
			repeat := int(extra) + 11
			if i+repeat > len(lengths) {
				return errors.New("deflate long zero repeat overflows alphabet")
			}
			i += repeat
		default:
			return fmt.Errorf("invalid deflate code length symbol %d", sym)
		}
	}

	litLenLengths := lengths[:hlit]
	if litLenLengths[256] == 0 {
		return errors.New("deflate literal/length tree missing end code")
	}
	if err := initDeflateHuffman(litLen, litLenLengths, maxDeflateCodeBits, ws.litLenNodes[:]); err != nil {
		return err
	}
	if err := initDeflateHuffman(dist, lengths[hlit:], maxDeflateCodeBits, ws.distNodes[:]); err != nil {
		return err
	}
	return nil
}

type deflateHuffman struct {
	nodes []deflateHuffmanNode
}

type deflateHuffmanNode struct {
	child  [2]int
	symbol int
}

func newDeflateHuffman(lengths []uint8, maxBits int) (*deflateHuffman, error) {
	h := new(deflateHuffman)
	nodes := make([]deflateHuffmanNode, 1+len(lengths)*maxBits)
	if err := initDeflateHuffman(h, lengths, maxBits, nodes); err != nil {
		return nil, err
	}
	return h, nil
}

func initDeflateHuffman(h *deflateHuffman, lengths []uint8, maxBits int, nodes []deflateHuffmanNode) error {
	var counts [maxDeflateCodeBits + 1]int
	for _, length := range lengths {
		if int(length) > maxBits {
			return fmt.Errorf("invalid deflate huffman code length %d", length)
		}
		if length > 0 {
			counts[length]++
		}
	}

	code := 0
	var nextCode [maxDeflateCodeBits + 1]int
	for bits := 1; bits <= maxBits; bits++ {
		code = (code + counts[bits-1]) << 1
		if code > 1<<bits {
			return errors.New("oversubscribed deflate huffman tree")
		}
		nextCode[bits] = code
	}

	if len(nodes) == 0 {
		return errors.New("missing deflate huffman node storage")
	}
	nodes[0] = newDeflateHuffmanNode()
	h.nodes = nodes[:1]

	for sym, length := range lengths {
		if length == 0 {
			continue
		}
		code := nextCode[length]
		nextCode[length]++
		if err := h.insert(sym, reverseDeflateBits(code, length), length); err != nil {
			return err
		}
	}
	return nil
}

func mustDeflateHuffman(lengths []uint8, maxBits int) *deflateHuffman {
	h, err := newDeflateHuffman(lengths, maxBits)
	if err != nil {
		panic(err)
	}
	return h
}

func newDeflateHuffmanNode() deflateHuffmanNode {
	return deflateHuffmanNode{
		child:  [2]int{-1, -1},
		symbol: -1,
	}
}

func (h *deflateHuffman) empty() bool {
	return len(h.nodes) == 1 && h.nodes[0].symbol < 0 &&
		h.nodes[0].child[0] < 0 && h.nodes[0].child[1] < 0
}

func (h *deflateHuffman) insert(sym, code int, length uint8) error {
	node := 0
	for i := uint8(0); i < length; i++ {
		if h.nodes[node].symbol >= 0 {
			return errors.New("invalid deflate huffman prefix")
		}
		bit := (code >> i) & 1
		next := h.nodes[node].child[bit]
		if next < 0 {
			if len(h.nodes) == cap(h.nodes) {
				return errors.New("deflate huffman node storage exhausted")
			}
			next = len(h.nodes)
			h.nodes = append(h.nodes, newDeflateHuffmanNode())
			h.nodes[node].child[bit] = next
		}
		node = next
	}
	if h.nodes[node].symbol >= 0 || h.nodes[node].child[0] >= 0 || h.nodes[node].child[1] >= 0 {
		return errors.New("invalid deflate huffman code")
	}
	h.nodes[node].symbol = sym
	return nil
}

func (h *deflateHuffman) decode(r *deflateBitReader) (int, error) {
	node := 0
	for {
		if h.nodes[node].symbol >= 0 {
			return h.nodes[node].symbol, nil
		}
		for r.nbits == 0 {
			b, err := r.r.ReadByte()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return 0, io.ErrUnexpectedEOF
				}
				return 0, err
			}
			r.bits = uint64(b)
			r.nbits = 8
			r.nread++
		}
		bit := int(r.bits & 1)
		r.bits >>= 1
		r.nbits--

		next := h.nodes[node].child[bit]
		if next < 0 {
			return 0, errors.New("invalid deflate huffman code")
		}
		node = next
	}
}

func reverseDeflateBits(code int, length uint8) int {
	reversed := 0
	for i := uint8(0); i < length; i++ {
		reversed = reversed<<1 | code&1
		code >>= 1
	}
	return reversed
}

func readDeflateLength(r *deflateBitReader, sym int) (int, error) {
	idx := sym - 257
	if idx < 0 || idx >= len(deflateLengthBase) {
		return 0, fmt.Errorf("invalid deflate length symbol %d", sym)
	}
	extra, err := r.readBits(deflateLengthExtra[idx])
	if err != nil {
		return 0, err
	}
	return deflateLengthBase[idx] + int(extra), nil
}

func readDeflateDistance(r *deflateBitReader, dist *deflateHuffman) (int, error) {
	sym, err := dist.decode(r)
	if err != nil {
		return 0, err
	}
	if sym < 0 || sym >= len(deflateDistBase) {
		return 0, fmt.Errorf("invalid deflate distance symbol %d", sym)
	}
	extra, err := r.readBits(deflateDistExtra[sym])
	if err != nil {
		return 0, err
	}
	return deflateDistBase[sym] + int(extra), nil
}

func addDeflateOutput(out, n uint64) (uint64, error) {
	if out > ^uint64(0)-n {
		return out, errors.New("deflate output size overflow")
	}
	return out + n, nil
}

func fixedDeflateLitLenLengths() []uint8 {
	lengths := make([]uint8, 288)
	for i := 0; i <= 143; i++ {
		lengths[i] = 8
	}
	for i := 144; i <= 255; i++ {
		lengths[i] = 9
	}
	for i := 256; i <= 279; i++ {
		lengths[i] = 7
	}
	for i := 280; i <= 287; i++ {
		lengths[i] = 8
	}
	return lengths
}

func fixedDeflateDistLengths() []uint8 {
	lengths := make([]uint8, 32)
	for i := range lengths {
		lengths[i] = 5
	}
	return lengths
}

var deflateCodeLenOrder = [...]int{
	16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15,
}

var deflateLengthBase = [...]int{
	3, 4, 5, 6, 7, 8, 9, 10,
	11, 13, 15, 17,
	19, 23, 27, 31,
	35, 43, 51, 59,
	67, 83, 99, 115,
	131, 163, 195, 227,
	258,
}

var deflateLengthExtra = [...]uint8{
	0, 0, 0, 0, 0, 0, 0, 0,
	1, 1, 1, 1,
	2, 2, 2, 2,
	3, 3, 3, 3,
	4, 4, 4, 4,
	5, 5, 5, 5,
	0,
}

var deflateDistBase = [...]int{
	1, 2, 3, 4,
	5, 7,
	9, 13,
	17, 25,
	33, 49,
	65, 97,
	129, 193,
	257, 385,
	513, 769,
	1025, 1537,
	2049, 3073,
	4097, 6145,
	8193, 12289,
	16385, 24577,
}

var deflateDistExtra = [...]uint8{
	0, 0, 0, 0,
	1, 1,
	2, 2,
	3, 3,
	4, 4,
	5, 5,
	6, 6,
	7, 7,
	8, 8,
	9, 9,
	10, 10,
	11, 11,
	12, 12,
	13, 13,
}
