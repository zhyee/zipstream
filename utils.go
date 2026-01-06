package zipstream

import (
	"encoding/binary"
	"io"
	"sync"
	"time"
)

func MSDosTimeToTime(dosDate, dosTime uint16) time.Time {
	return time.Date(
		// date bits 0-4: day of month; 5-8: month; 9-15: years since 1980
		int(dosDate>>9+1980),
		time.Month(dosDate>>5&0xf),
		int(dosDate&0x1f),

		// time bits 0-4: second/2; 5-10: minute; 11-15: hour
		int(dosTime>>11),
		int(dosTime>>5&0x3f),
		int(dosTime&0x1f*2),
		0, // nanoseconds

		time.UTC,
	)
}

// timeZone returns a *time.Location based on the provided offset.
// If the offset is non-sensible, then this uses an offset of zero.
func timeZone(offset time.Duration) *time.Location {
	const (
		minOffset   = -12 * time.Hour  // E.g., Baker island at -12:00
		maxOffset   = +14 * time.Hour  // E.g., Line island at +14:00
		offsetAlias = 15 * time.Minute // E.g., Nepal at +5:45
	)
	offset = offset.Round(offsetAlias)
	if offset < minOffset || maxOffset < offset {
		offset = 0
	}
	return time.FixedZone("", int(offset/time.Second))
}

type readBuf []byte

func (b *readBuf) uint8() uint8 {
	v := (*b)[0]
	*b = (*b)[1:]
	return v
}

func (b *readBuf) uint16() uint16 {
	v := binary.LittleEndian.Uint16(*b)
	*b = (*b)[2:]
	return v
}

func (b *readBuf) uint32() uint32 {
	v := binary.LittleEndian.Uint32(*b)
	*b = (*b)[4:]
	return v
}

func (b *readBuf) uint64() uint64 {
	v := binary.LittleEndian.Uint64(*b)
	*b = (*b)[8:]
	return v
}

func (b *readBuf) sub(n int) readBuf {
	b2 := (*b)[:n]
	*b = (*b)[n:]
	return b2
}

type byteReader interface {
	io.Reader
	io.ByteReader
}

type byteCountReader interface {
	byteReader
	NRead() uint64
}

type countableReader struct {
	byteReader
	nRead uint64
}

func (r *countableReader) Read(p []byte) (int, error) {
	n, err := r.byteReader.Read(p)
	r.nRead += uint64(n)
	return n, err
}

func (r *countableReader) ReadByte() (byte, error) {
	n, err := r.byteReader.ReadByte()
	if err == nil {
		r.nRead++
	}
	return n, err
}

func (r *countableReader) NRead() uint64 {
	return r.nRead
}

func countable(r byteReader) byteCountReader {
	return &countableReader{
		byteReader: r,
	}
}

type limitByteReader struct {
	*io.LimitedReader
}

func newLimitByteReader(r byteReader, n int64) *limitByteReader {
	return &limitByteReader{
		LimitedReader: &io.LimitedReader{R: r, N: n},
	}
}

func (r *limitByteReader) ReadByte() (byte, error) {
	if r.N <= 0 {
		return 0, io.EOF
	}
	b, err := r.LimitedReader.R.(io.ByteReader).ReadByte()
	if err == nil {
		r.N--
	}
	return b, err
}

type syncPool[T any] struct {
	pool  *sync.Pool
	reset func(t T) T
}

func newSyncPool[T any](new func() T, reset func(T) T) *syncPool[T] {
	return &syncPool[T]{
		pool: &sync.Pool{
			New: func() any {
				return new()
			},
		},
		reset: reset,
	}
}

func (p *syncPool[T]) Get() T {
	t := p.pool.Get().(T)
	if p.reset != nil {
		return p.reset(t)
	}
	return t
}

func (p *syncPool[T]) Put(t T) {
	p.pool.Put(t)
}
