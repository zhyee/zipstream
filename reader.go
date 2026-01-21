package zipstream

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"math"
	"sync"
	"time"

	"github.com/klauspost/compress/flate"
)

const (
	rawReaderBufSize         = 16384
	headerIdentifierLen      = 4
	fileHeaderLen            = 26
	dataDescriptorLen        = 16 // four uint32: descriptor signature, crc32, compressed size, size
	zip64DataDescriptorLen   = 24 // four uint32: descriptor signature, crc32 and uint64: compressed size, size
	fileHeaderSignature      = 0x04034b50
	directoryHeaderSignature = 0x02014b50
	directoryEndSignature    = 0x06054b50
	dataDescriptorSignature  = 0x08074b50

	// Extra header IDs.
	// See http://mdfs.net/Docs/Comp/Archiving/Zip/ExtraField

	Zip64ExtraID       = 0x0001 // Zip64 extended information
	NtfsExtraID        = 0x000a // NTFS
	UnixExtraID        = 0x000d // UNIX
	ExtTimeExtraID     = 0x5455 // Extended timestamp
	InfoZipUnixExtraID = 0x5855 // Info-ZIP Unix extension

)

var (
	byteBuf [1]byte
	bufPool = newSyncPool[[]byte](func() []byte {
		return make([]byte, rawReaderBufSize)
	}, nil)
)

type Entry struct {
	zip.FileHeader
	r          io.Reader
	rawReader  byteCountReader
	dataReader io.ReadCloser // the entry file reader
	zip64      bool
	eof        bool
}

func (e *Entry) hasDataDescriptor() bool {
	return e.Flags&8 != 0
}

// IsDir just simply verify whether the filename ends with a forward slash "/".
func (e *Entry) IsDir() bool {
	return len(e.Name) > 0 && e.Name[len(e.Name)-1] == '/'
}

func (e *Entry) Open() (io.ReadCloser, error) {
	if e.eof {
		return nil, errors.New("this file has read to end")
	}
	if e.dataReader != nil {
		return nil, errors.New("repeated Open is not supported")
	}
	decomp := decompressor(e.Method)
	if decomp == nil {
		return nil, zip.ErrAlgorithm
	}

	e.dataReader = &checksumReader{
		rc:    decomp(e.rawReader),
		hash:  crc32.NewIEEE(),
		entry: e,
	}
	return e.dataReader, nil
}

// OpenRaw read the entry's original compressed(or stored) data, you must be
// aware that this method may be significantly slower than the same one in
// standard package [archive/zip], especially handle the entry with a data
// descriptor because we must decompress all the file to determine its EOF position.
// See the Unit Test case BenchmarkOpenRaw for details.
func (e *Entry) OpenRaw() (io.ReadCloser, error) {
	if e.eof {
		return nil, errors.New("this file has read to end")
	}
	if e.dataReader != nil {
		return nil, errors.New("repeated Open is not supported")
	}
	if e.Method == zip.Store {
		return e.Open()
	}
	e.dataReader = newRawReader(e)
	return e.dataReader, nil
}

func (e *Entry) Skip() error {
	if e.dataReader == nil {
		_, err := e.Open()
		if err != nil {
			return err
		}
	}
	return e.dataReader.Close()
}

type Reader struct {
	r            *bufio.Reader
	localFileEnd bool
	curEntry     *Entry
	err          error
}

func NewReader(r io.Reader) *Reader {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	return &Reader{
		r: br,
	}
}

func (z *Reader) readEntry() (*Entry, error) {

	buf := make([]byte, fileHeaderLen)
	if _, err := io.ReadFull(z.r, buf); err != nil {
		return nil, fmt.Errorf("unable to read local file header: %w", err)
	}

	lr := readBuf(buf)

	readerVersion := lr.uint16()
	flags := lr.uint16()
	method := lr.uint16()
	modifiedTime := lr.uint16()
	modifiedDate := lr.uint16()
	crc32Sum := lr.uint32()
	compressedSize := lr.uint32()
	uncompressedSize := lr.uint32()
	filenameLen := int(lr.uint16())
	extraAreaLen := int(lr.uint16())

	entry := &Entry{
		FileHeader: zip.FileHeader{
			ReaderVersion:      readerVersion,
			Flags:              flags,
			Method:             method,
			ModifiedTime:       modifiedTime,
			ModifiedDate:       modifiedDate,
			CRC32:              crc32Sum,
			CompressedSize:     compressedSize,
			UncompressedSize:   uncompressedSize,
			CompressedSize64:   uint64(compressedSize),
			UncompressedSize64: uint64(uncompressedSize),
		},
		r:   z.r,
		eof: false,
	}

	nameAndExtraBuf := make([]byte, filenameLen+extraAreaLen)
	if _, err := io.ReadFull(z.r, nameAndExtraBuf); err != nil {
		return nil, fmt.Errorf("unable to read entry name and extra area: %w", err)
	}

	entry.Name = string(nameAndExtraBuf[:filenameLen])
	entry.Extra = nameAndExtraBuf[filenameLen:]

	entry.NonUTF8 = flags&0x800 == 0
	if flags&1 == 1 {
		return nil, fmt.Errorf("encrypted ZIP entry not supported")
	}
	if flags&8 == 8 && method != zip.Deflate {
		// Generally "Store" files should not be followed by a data descriptor,
		// even though the specification doesn’t explicitly prohibit it.
		// Besides, in this case we are not able to determine the end position of file,
		// the only behavior is reporting an error.
		return nil, fmt.Errorf("only DEFLATED entries can have data descriptor")
	}

	needCSize := entry.CompressedSize == ^uint32(0)
	needUSize := entry.UncompressedSize == ^uint32(0)

	ler := readBuf(entry.Extra)
	var modified time.Time
parseExtras:
	for len(ler) >= 4 { // need at least tag and size
		fieldTag := ler.uint16()
		fieldSize := int(ler.uint16())
		if len(ler) < fieldSize {
			break
		}
		fieldBuf := ler.sub(fieldSize)

		switch fieldTag {
		case Zip64ExtraID:
			entry.zip64 = true

			// update directory values from the zip64 extra block.
			// They should only be consulted if the sizes read earlier
			// are maxed out.
			// See golang.org/issue/13367.
			if needUSize {
				needUSize = false
				if len(fieldBuf) < 8 {
					return nil, zip.ErrFormat
				}
				entry.UncompressedSize64 = fieldBuf.uint64()
			}
			if needCSize {
				needCSize = false
				if len(fieldBuf) < 8 {
					return nil, zip.ErrFormat
				}
				entry.CompressedSize64 = fieldBuf.uint64()
			}
		case NtfsExtraID:
			if len(fieldBuf) < 4 {
				continue parseExtras
			}
			fieldBuf.uint32()        // reserved (ignored)
			for len(fieldBuf) >= 4 { // need at least tag and size
				attrTag := fieldBuf.uint16()
				attrSize := int(fieldBuf.uint16())
				if len(fieldBuf) < attrSize {
					continue parseExtras
				}
				attrBuf := fieldBuf.sub(attrSize)
				if attrTag != 1 || attrSize != 24 {
					continue // Ignore irrelevant attributes
				}

				const ticksPerSecond = 1e7    // Windows timestamp resolution
				ts := int64(attrBuf.uint64()) // ModTime since Windows epoch
				secs := ts / ticksPerSecond
				nsecs := (1e9 / ticksPerSecond) * int64(ts%ticksPerSecond)
				epoch := time.Date(1601, time.January, 1, 0, 0, 0, 0, time.UTC)
				modified = time.Unix(epoch.Unix()+secs, nsecs)
			}
		case UnixExtraID, InfoZipUnixExtraID:
			if len(fieldBuf) < 8 {
				continue parseExtras
			}
			fieldBuf.uint32()              // AcTime (ignored)
			ts := int64(fieldBuf.uint32()) // ModTime since Unix epoch
			modified = time.Unix(ts, 0)
		case ExtTimeExtraID:
			if len(fieldBuf) < 5 || fieldBuf.uint8()&1 == 0 {
				continue parseExtras
			}
			ts := int64(fieldBuf.uint32()) // ModTime since Unix epoch
			modified = time.Unix(ts, 0)
		}
	}

	msDosModified := MSDosTimeToTime(entry.ModifiedDate, entry.ModifiedTime)
	entry.Modified = msDosModified

	if !modified.IsZero() {
		entry.Modified = modified.UTC()

		// If legacy MS-DOS timestamps are set, we can use the delta between
		// the legacy and extended versions to estimate timezone offset.
		//
		// A non-UTC timezone is always used (even if offset is zero).
		// Thus, FileHeader.Modified.Location() == time.UTC is useful for
		// determining whether extended timestamps are present.
		// This is necessary for users that need to do additional time
		// calculations when dealing with legacy ZIP formats.
		if entry.ModifiedTime != 0 || entry.ModifiedDate != 0 {
			entry.Modified = modified.In(timeZone(msDosModified.Sub(modified)))
		}
	}

	if needCSize {
		return nil, zip.ErrFormat
	}

	// If "general purpose bit flag" Bit 3 is set, the fields crc-32,
	// compressed size and uncompressed size are set to zero in the
	// local header.  The correct values are put in the
	// data descriptor immediately following the compressed
	// data.
	if entry.IsDir() {
		entry.rawReader = countable(bytes.NewReader(nil))
	} else if !entry.hasDataDescriptor() {
		entry.rawReader = countable(newLimitByteReader(z.r, int64(entry.CompressedSize64)))
	} else {
		entry.rawReader = countable(z.r) // use the deflate reader to determine the entry's EOF.
	}

	return entry, nil
}

// Next indicates whether there is more entry can be read,
// You can check err to judge if there is any failure when it returns false.
func (z *Reader) Next() bool {
	if z.err != nil {
		return false
	}
	if z.localFileEnd {
		return false
	}
	if z.curEntry != nil && !z.curEntry.eof {
		if err := z.curEntry.Skip(); err != nil {
			z.err = fmt.Errorf("unable to skip previos file data: %w", err)
			return false
		}
		z.curEntry.eof = true
	}
	headerSigBuf := make([]byte, headerIdentifierLen)
	if _, err := io.ReadFull(z.r, headerSigBuf); err != nil {
		z.err = fmt.Errorf("unable to read header identifier: %w", err)
		return false
	}
	headerSig := binary.LittleEndian.Uint32(headerSigBuf)
	if headerSig != fileHeaderSignature {
		if headerSig == directoryHeaderSignature || headerSig == directoryEndSignature {
			z.localFileEnd = true
			return false
		}
		z.err = zip.ErrFormat
		return false
	}
	return true
}

func (z *Reader) Err() error {
	return z.err
}

func (z *Reader) Entry() (*Entry, error) {
	entry, err := z.readEntry()
	if err != nil {
		return nil, fmt.Errorf("unable to read zip file header: %w", err)
	}
	z.curEntry = entry
	return entry, nil
}

// GetNextEntry return next entry in the zip archive
// Deprecated, together use Next and Entry instead
func (z *Reader) GetNextEntry() (*Entry, error) {
	if z.Next() {
		return z.Entry()
	}
	if z.err != nil {
		return nil, z.err
	}
	return nil, io.EOF
}

var (
	decompressors sync.Map // map[uint16]Decompressor
)

func init() {
	decompressors.Store(zip.Store, zip.Decompressor(io.NopCloser))
	decompressors.Store(zip.Deflate, zip.Decompressor(newDeflateReader))
}

func decompressor(method uint16) zip.Decompressor {
	di, ok := decompressors.Load(method)
	if !ok {
		return nil
	}
	return di.(zip.Decompressor)
}

var deflateReaderPool sync.Pool

// We use github.com/klauspost/compress/flate instead of the standard compress/flate because
// the latter’s documentation says that it may read beyond the end of the Deflate stream.
func newDeflateReader(r io.Reader) io.ReadCloser {
	fr, ok := deflateReaderPool.Get().(io.ReadCloser)
	if ok {
		fr.(flate.Resetter).Reset(r, nil)
	} else {
		fr = flate.NewReader(r)
	}
	return &pooledDeflateReader{fr: fr}
}

type pooledDeflateReader struct {
	mu sync.Mutex // guards Close and Read
	fr io.ReadCloser
}

func (r *pooledDeflateReader) Read(p []byte) (n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fr == nil {
		return 0, errors.New("Read after Close")
	}
	return r.fr.Read(p)
}

func (r *pooledDeflateReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var err error
	if r.fr != nil {
		err = r.fr.Close()
		deflateReaderPool.Put(r.fr)
		r.fr = nil
	}
	return err
}

func readDataDescriptor(r io.Reader, entry *Entry, zip64 bool) error {
	entry.zip64 = zip64
	ddLen := dataDescriptorLen
	if zip64 {
		ddLen = zip64DataDescriptorLen
	}
	buf := make([]byte, ddLen)
	// The spec says: "Although not originally assigned a
	// signature, the value 0x08074b50 has commonly been adopted
	// as a signature value for the data descriptor record.
	// Implementers should be aware that ZIP files may be
	// encountered with or without this signature marking data
	// descriptors and should account for either case when reading
	// ZIP files to ensure compatibility."
	//
	// dataDescriptorLen includes the size of the signature but
	// first read just those 4 bytes to see if it exists.
	_, err := io.ReadFull(r, buf[:4])
	if err != nil {
		return err
	}
	off := 0
	if binary.LittleEndian.Uint32(buf[:4]) != dataDescriptorSignature {
		// No data descriptor signature. Reserve these four bytes.
		off += 4
	}
	_, err = io.ReadFull(r, buf[off:ddLen-4])
	if err != nil {
		return err
	}
	entry.eof = true

	b := readBuf(buf[:ddLen-4])
	entry.CRC32 = b.uint32()

	if zip64 {
		entry.CompressedSize64 = b.uint64()
		entry.UncompressedSize64 = b.uint64()
	} else {
		entry.CompressedSize64 = uint64(b.uint32())
		entry.UncompressedSize64 = uint64(b.uint32())
	}

	return nil
}

type shadowReader struct {
	ch     chan []byte
	buf    []byte
	offset int
	size   int
	err    error
}

func (s *shadowReader) Read(p []byte) (n int, err error) {
	var ok bool
	for n < len(p) {
		if s.offset >= len(s.buf) {
			if s.err != nil {
				return n, s.err
			}
			bufPool.Put(s.buf)
			s.buf, ok = <-s.ch
			if !ok {
				return n, io.EOF
			}
			s.offset = 0
		}
		size := copy(p[n:], s.buf[s.offset:])
		s.offset += size
		n += size
	}
	if s.offset < len(s.buf) {
		return n, nil // there is unread data, do not return error
	}
	return n, s.err
}

type readerBridge struct {
	r      byteReader
	buf    []byte
	size   int
	err    error
	shadow *shadowReader
	closed bool
}

func newReaderBridge(r byteReader) *readerBridge {
	initBuf := bufPool.Get()
	return &readerBridge{
		r:   r,
		buf: bufPool.Get(),
		shadow: &shadowReader{
			ch:     make(chan []byte),
			buf:    initBuf, // avoid nil value checking
			offset: len(initBuf),
		},
	}
}

func (r *readerBridge) closeChan() {
	if !r.closed {
		close(r.shadow.ch)
		r.closed = true
	}
}

func (r *readerBridge) flush(err error) {
	if r.size > 0 {
		r.shadow.ch <- r.buf[:r.size]
		r.size = 0
	}
	r.closeChan()
	if r.err != nil {
		r.shadow.err = r.err
		return
	}
	r.err = err
	r.shadow.err = err
}

func (r *readerBridge) Read(p []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err = r.r.Read(p[:min(len(r.buf)-r.size, len(p))])
	r.size += copy(r.buf[r.size:], p[:n])
	r.err = err
	if err != nil || (r.size >= len(r.buf)) {
		r.shadow.ch <- r.buf[:r.size]
		r.buf = bufPool.Get()
		r.size = 0
		if err != nil {
			r.shadow.err = err
			r.closeChan()
		}
	}
	return n, err
}

func (r *readerBridge) ReadByte() (byte, error) {
	n, err := r.Read(byteBuf[:])
	if n > 0 {
		return byteBuf[0], err
	}
	return 0, err
}

type rawReader struct {
	r     io.Reader
	uSize uint64 // number of uncompressed bytes read so far
	entry *Entry
	err   error
}

func newRawReader(e *Entry) *rawReader {
	rr := &rawReader{
		entry: e,
	}
	if !e.hasDataDescriptor() {
		rr.r = e.rawReader
		return rr
	}
	bridge := newReaderBridge(e.rawReader)
	fr := flate.NewReader(bridge)
	go func() {
		buf := make([]byte, rawReaderBufSize)
		for {
			n, err := fr.Read(buf)
			rr.uSize += uint64(n)
			if err != nil {
				rr.err = err
				bridge.flush(err)
				break
			}
		}
		_ = fr.Close()
	}()
	rr.r = bridge.shadow
	return rr
}

func (r *rawReader) Read(p []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err = r.r.Read(p)
	if errors.Is(err, io.EOF) {
		if r.entry.hasDataDescriptor() {
			zip64 := r.entry.rawReader.NRead() > math.MaxUint32 || r.uSize > math.MaxUint32
			if err := readDataDescriptor(r.entry.r, r.entry, zip64); err != nil {
				if errors.Is(err, io.EOF) {
					r.err = io.ErrUnexpectedEOF
					return n, r.err
				} else {
					r.err = err
					return n, r.err
				}
			}
		}
		if r.entry.CompressedSize64 > 0 && r.entry.rawReader.NRead() != r.entry.CompressedSize64 {
			r.err = io.ErrUnexpectedEOF
			return n, r.err
		}

		// skip crc32 checksum verification, it's the caller's duty in raw deflate reader
		r.entry.eof = true
	}
	r.err = err
	return n, err
}

func (r *rawReader) Close() error {
	if !r.entry.eof {
		_, err := io.Copy(io.Discard, r)
		if err != nil {
			return err
		}
		r.entry.eof = true
	}
	return nil
}

type checksumReader struct {
	rc    io.ReadCloser
	hash  hash.Hash32
	uSize uint64 // number of uncompressed bytes read so far
	entry *Entry
	err   error // sticky error
}

func (r *checksumReader) Read(b []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err = r.rc.Read(b)
	r.hash.Write(b[:n])
	r.uSize += uint64(n)
	if err == nil {
		return
	}
	if errors.Is(err, io.EOF) {
		if r.entry.UncompressedSize64 > 0 && r.uSize != r.entry.UncompressedSize64 {
			r.err = io.ErrUnexpectedEOF
			return n, r.err
		}
		if r.entry.hasDataDescriptor() {
			zip64 := r.entry.rawReader.NRead() > math.MaxUint32 || r.uSize > math.MaxUint32
			if err := readDataDescriptor(r.entry.r, r.entry, zip64); err != nil {
				if errors.Is(err, io.EOF) {
					r.err = io.ErrUnexpectedEOF
					return n, r.err
				} else {
					r.err = err
					return n, r.err
				}
			}
			if r.entry.rawReader.NRead() != r.entry.CompressedSize64 {
				r.err = fmt.Errorf("invalid entry compressed size (expected %d but got %d bytes)",
					r.entry.CompressedSize64, r.entry.rawReader.NRead())
				return n, r.err
			}
			if r.uSize != r.entry.UncompressedSize64 {
				r.err = fmt.Errorf("invalid entry size (expected %d but got %d bytes)",
					r.entry.UncompressedSize64, r.uSize)
				return n, r.err
			}
		}

		r.entry.eof = true
		if r.entry.CRC32 != 0 && r.hash.Sum32() != r.entry.CRC32 {
			r.err = zip.ErrChecksum
			return n, r.err
		}
	}
	r.err = err
	return n, r.err
}

func (r *checksumReader) Close() error {
	if !r.entry.eof {
		_, err := io.Copy(io.Discard, r)
		if err != nil {
			return err
		}
		r.entry.eof = true
	}
	return r.rc.Close()
}
