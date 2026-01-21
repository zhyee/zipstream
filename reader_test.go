package zipstream

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/rand"
	"fmt"
	"hash/crc32"
	"io"
	rand2 "math/rand"
	"net/http"
	"os"
	"testing"
)

func init() {
	generateBigZip()
}

func TestStreamReader(t *testing.T) {
	resp, err := http.Get("https://github.com/golang/go/archive/refs/tags/go1.16.10.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	zr := NewReader(resp.Body)

	for zr.Next() {
		e, err := zr.Entry()
		if err != nil {
			t.Fatalf("unable to read next entry: %s", err)
		}

		t.Log("---------------entry------------------")
		t.Log("entry name: ", e.Name)
		t.Log("entry comment: ", e.Comment)
		t.Log("entry reader version: ", e.ReaderVersion)
		t.Log("entry modify time: ", e.Modified)
		t.Log("entry compressed size: ", e.CompressedSize64)
		t.Log("entry uncompressed size: ", e.UncompressedSize64)
		t.Log("entry is a dir: ", e.IsDir())
		t.Log("entry has data descriptor: ", e.hasDataDescriptor())

		if !e.IsDir() {
			rc, err := e.Open()
			if err != nil {
				t.Fatalf("unable to open zip file: %s", err)
			}
			content, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read zip file content fail: %s", err)
			}

			if uint64(len(content)) != e.UncompressedSize64 {
				t.Fatalf("read zip file's length doest not equal with UncompressedSize64")
			}
			if err := rc.Close(); err != nil {
				t.Fatalf("close zip entry reader fail: %s", err)
			}
		}
	}

	if err = zr.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRawReader(t *testing.T) {
	f, err := os.Open("testdata/example.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	fInfo, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}

	az, err := zip.NewReader(f, fInfo.Size())
	if err != nil {
		t.Fatal(err)
	}

	fileMap := make(map[string]*zip.File, len(az.File))
	for _, zf := range az.File {
		fileMap[zf.Name] = zf
	}

	f2, err := os.Open("testdata/example.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()

	z := NewReader(io.Reader(f2))

	c32Sum := crc32.NewIEEE()

	for z.Next() {
		entry, err := z.Entry()
		if err != nil {
			t.Fatal(err)
		}

		zf, ok := fileMap[entry.Name]
		if !ok {
			t.Fatalf("unexpected file: %s", entry.Name)
		}
		delete(fileMap, entry.Name)

		if entry.Comment != zf.Comment ||
			entry.ReaderVersion != zf.ReaderVersion ||
			entry.IsDir() != zf.Mode().IsDir() ||
			entry.Flags != zf.Flags ||
			entry.Method != zf.Method ||
			!entry.Modified.Equal(zf.Modified) ||
			entry.CRC32 != zf.CRC32 ||
			//bytes.Compare(entry.Extra, zf.Extra) != 0 || // local file header's extra data may not same as central directory header's extra data
			entry.CompressedSize64 != zf.CompressedSize64 ||
			entry.UncompressedSize64 != zf.UncompressedSize64 {
			t.Fatal("some local file header attr is incorrect")
		}

		if !entry.IsDir() {
			rc, err := entry.OpenRaw()
			if err != nil {
				t.Fatalf("open zip file entry err: %s", err)
			}

			deflate, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read entry file contents fail: %s", err)
			}

			if len(deflate) != int(entry.CompressedSize64) {
				t.Fatalf("read entry raw stream content doest not equal with the compressed size")
			}

			ziprc, err := zf.Open()
			if err != nil {
				t.Fatal(err)
			}
			zipFileContents, err := io.ReadAll(ziprc)
			if err != nil {
				t.Fatal(err)
			}

			inflate := deflate
			if entry.Method == zip.Deflate {
				fr := flate.NewReader(bytes.NewReader(deflate))
				inflate, err = io.ReadAll(fr)
				if err != nil {
					t.Fatalf("unable to inflate the stream: %v", err)
				}
				_ = fr.Close()
			}

			t.Log("CompressedSize64: ", entry.CompressedSize64,
				"UncompressedSize64: ", entry.UncompressedSize64,
				"deflate: ", len(deflate), "inflate: ", len(inflate))

			if bytes.Compare(inflate, zipFileContents) != 0 {
				t.Fatal("the zip entry file contents is incorrect")
			}
			c32Sum.Reset()
			_, _ = c32Sum.Write(inflate)
			if entry.CRC32 != c32Sum.Sum32() {
				t.Fatal("the crc32 sum not same")
			}

			if err := rc.Close(); err != nil {
				t.Fatalf("close zip file entry reader err: %s", err)
			}
			_ = ziprc.Close()
		}
	}
	if err = z.Err(); err != nil {
		t.Fatal("unexpected error: ", err)
	}

	if len(fileMap) != 0 {
		t.Fatal("the resolved entry count is incorrect")
	}
}

func TestEmptyZip(t *testing.T) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	t.Log("zip archive size:", buf.Len())

	r := NewReader(buf)

	for r.Next() {
		e, err := r.Entry()
		if err != nil {
			t.Fatal(err)
		}

		t.Log("file name: ", e.Name)
		t.Log("compressed size: ", e.CompressedSize64)
		t.Log("uncompressed size: ", e.UncompressedSize64)
		t.Logf("crc32: 0x%x", e.CRC32)
		t.Log("compress method: ", e.Method)
		t.Log("is a dir: ", e.IsDir())
		t.Log("--------------------------------")
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyEntry(t *testing.T) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	_, err := zw.Create("foo/")
	if err != nil {
		t.Fatal(err)
	}

	_, err = zw.Create("foo/1.txt")
	if err != nil {
		t.Fatal(err)
	}

	_, err = zw.Create("bar/")
	if err != nil {
		t.Fatal(err)
	}

	fw, err := zw.Create("bar/2.txt")
	if err != nil {
		t.Fatal(err)
	}

	_, err = io.CopyN(fw, rand.Reader, int64(rand2.Intn(1024*1024)))
	if err != nil {
		t.Fatal(err)
	}

	_, err = zw.Create("bar/3.txt")
	if err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	t.Log("zip archive size:", buf.Len())

	r := NewReader(buf)

	for r.Next() {
		e, err := r.Entry()
		if err != nil {
			t.Fatal(err)
		}

		t.Log("--------------------------------")
		if rand2.Intn(10000)&1 > 0 {
			rc, err := e.OpenRaw()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("file compressed size: %d", len(b))
			if err := rc.Close(); err != nil {
				t.Fatal(err)
			}
		} else {
			rc, err := e.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("file uncompressed size: %d", len(b))
			if err := rc.Close(); err != nil {
				t.Fatal(err)
			}
		}
		t.Log("file name: ", e.Name)
		t.Log("compressed size: ", e.CompressedSize64)
		t.Log("uncompressed size: ", e.UncompressedSize64)
		t.Log("compress method: ", e.Method)
		t.Log("is a dir: ", e.IsDir())
		t.Logf("crc32: 0x%x", e.CRC32)
		t.Log("has data descriptor: ", e.hasDataDescriptor())
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}

}

func TestDataDescriptor(t *testing.T) {
	f, err := os.Open("testdata/data-descriptor.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	r := NewReader(f)

	for r.Next() {
		e, err := r.Entry()
		if err != nil {
			t.Fatal(err)
		}

		if rand2.Intn(10000)&1 > 0 {
			rc, err := e.OpenRaw()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("file compressed size: %d", len(b))
			if err := rc.Close(); err != nil {
				t.Fatal(err)
			}
		} else {
			rc, err := e.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("file uncompressed size: %d", len(b))
			if err := rc.Close(); err != nil {
				t.Fatal(err)
			}
		}

		t.Log("--------------------------------")
		t.Log("file name: ", e.Name)
		t.Log("compressed size: ", e.CompressedSize64)
		t.Log("uncompressed size: ", e.UncompressedSize64)
		t.Log("method: ", e.Method)
		t.Logf("crc32: 0x%x", e.CRC32)
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestZip64DataDescriptor(t *testing.T) {
	f, err := os.Open("testdata/zip64-data-descriptor.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("zip archive size: %d", info.Size())

	r := NewReader(f)
	for r.Next() {
		e, err := r.Entry()
		if err != nil {
			t.Fatal(err)
		}
		t.Log("--------------------------------")

		var rc io.ReadCloser
		if rand2.Intn(10000)&1 > 0 {
			rc, err = e.OpenRaw()
			t.Log("call OpenRaw()")
		} else {
			rc, err = e.Open()
			t.Log("call Open()")
		}
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, rc)
		if err != nil {
			t.Fatal(err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("unable to close zip entry: %v", err)
		}
		t.Log("file name: ", e.Name)
		t.Log("compressed size: ", e.CompressedSize64)
		t.Logf("uncompressed size: 0x%x", e.UncompressedSize64)
		t.Log("compress method: ", e.Method)
		t.Logf("crc32: 0x%x", e.CRC32)
		t.Log("is a dir: ", e.IsDir())
		t.Log("has data descriptor: ", e.hasDataDescriptor())
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestNewReader(t *testing.T) {

	f, err := os.Open("testdata/data-descriptor.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}

	f2, err := os.Open("testdata/data-descriptor.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()

	az, err := zip.NewReader(f, info.Size())
	if err != nil {
		t.Fatal(err)
	}

	fileMap := make(map[string]*zip.File, len(az.File))

	for _, zf := range az.File {
		fileMap[zf.Name] = zf
	}

	z := NewReader(f)

	for z.Next() {
		entry, err := z.Entry()
		if err != nil {
			t.Fatal(err)
		}

		zf, ok := fileMap[entry.Name]
		if !ok {
			t.Fatalf("unexpected file: %s", entry.Name)
		}
		delete(fileMap, entry.Name)

		if !entry.IsDir() {
			rc, err := entry.Open()
			if err != nil {
				t.Fatalf("open zip file entry err: %s", err)
			}

			entryFileContents, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read entry file contents fail: %s", err)
			}

			ziprc, err := zf.Open()
			if err != nil {
				t.Fatal(err)
			}
			zipFileContents, err := io.ReadAll(ziprc)
			if err != nil {
				t.Fatal(err)
			}

			if bytes.Compare(entryFileContents, zipFileContents) != 0 {
				t.Fatal("the zip entry file contents is incorrect")
			}

			if err := rc.Close(); err != nil {
				t.Fatalf("close zip file entry reader err: %s", err)
			}

			t.Log("------------------------------------")
			t.Log("file: ", entry.Name)
			t.Log("comment: ", entry.Comment)
			t.Log("reader version: ", entry.ReaderVersion)
			t.Log("CompressedSize64: ", entry.CompressedSize64)
			t.Log("UncompressedSize64: ", entry.UncompressedSize64)
			t.Log("flags: ", entry.Flags)
			t.Log("compress method: ", entry.Method)
			t.Log("modified: ", entry.Modified)
			t.Logf("crc32: 0x%x", entry.CRC32)
			t.Log("----")
			t.Log("file: ", zf.Name)
			t.Log("comment: ", zf.Comment)
			t.Log("reader version: ", zf.ReaderVersion)
			t.Log("CompressedSize64: ", zf.CompressedSize64)
			t.Log("UncompressedSize64: ", zf.UncompressedSize64)
			t.Log("flags: ", zf.Flags)
			t.Log("compress method: ", zf.Method)
			t.Log("modified: ", zf.Modified)
			t.Logf("crc32: 0x%x", zf.CRC32)

			if entry.Comment != zf.Comment ||
				entry.IsDir() != zf.Mode().IsDir() ||
				entry.Flags != zf.Flags ||
				entry.Method != zf.Method ||
				!entry.Modified.Equal(zf.Modified) ||
				entry.CRC32 != zf.CRC32 ||
				//bytes.Compare(entry.Extra, zf.Extra) != 0 || // local file header's extra data may be not same as central directory header's extra data
				entry.CompressedSize64 != zf.CompressedSize64 ||
				entry.UncompressedSize64 != zf.UncompressedSize64 {
				t.Fatal("some local file header attr is incorrect")
			}
		}
	}
	if z.err != nil {
		t.Fatal("unexpected error: ", z.err)
	}

	if len(fileMap) != 0 {
		t.Fatal("the resolved entry count is incorrect")
	}

}

func generateBigZip() {
	buf := make([]byte, 1024*7)
	_, err := io.ReadFull(rand.Reader, buf)
	if err != nil {
		panic(err)
	}
	buf = bytes.Repeat(buf, 1024) // 7MB
	f, err := os.Create("testdata/big.zip")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	w, err := zw.Create("big.bin")
	if err != nil {
		panic(err)
	}
	for i := 0; i < 1024; i++ { // 7GB
		if _, err = w.Write(buf); err != nil {
			panic(err)
		}
	}
	_, err = w.Write([]byte("\x01\x02\x03\x04\x05\x06\x07\x08\x09")) // 7GB + 9B
	if err != nil {
		panic(err)
	}
	_, err = zw.Create("dir/")
	if err != nil {
		panic(err)
	}

	w, err = zw.Create("dir/small.txt")
	if err != nil {
		panic(err)
	}
	_, err = w.Write([]byte("hello small.txt"))
	if err != nil {
		panic(err)
	}
	err = zw.Close()
	if err != nil {
		panic(err)
	}
	err = f.Sync()
	if err != nil {
		panic(err)
	}
	info, err := f.Stat()
	if err != nil {
		panic(err)
	}
	fmt.Println("zip size: ", info.Size())
}

func TestOpenRaw(t *testing.T) {

	f, err := os.Open("testdata/big.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zr := NewReader(f)

	for zr.Next() {
		e, err := zr.Entry()
		if err != nil {
			t.Fatal(err)
		}
		rc, err := e.OpenRaw()
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, rc)
		if err != nil {
			t.Fatal(err)
		}
		t.Log("file name: ", e.Name)
		t.Log("compressed size: ", e.CompressedSize64)
		t.Log("uncompressed size: ", e.UncompressedSize64)
		t.Log("compress method: ", e.Method)
		t.Logf("crc32: 0x%x", e.CRC32)
		t.Log("is a dir: ", e.IsDir())
		t.Log("has data descriptor: ", e.hasDataDescriptor())
		t.Log("-------------------------------------------")
		err = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if zr.err != nil {
		t.Fatal(zr.err)
	}
}

/*
goos: darwin
goarch: arm64
pkg: github.com/zhyee/zipstream
cpu: Apple M3
BenchmarkOpen
BenchmarkOpen/Open
BenchmarkOpen/Open-8         	       9	1240596616 ns/op	    5675 B/op	      33 allocs/op
BenchmarkOpen/standard_Open
BenchmarkOpen/standard_Open-8         	       8	1336004370 ns/op	   97029 B/op	      59 allocs/op
*/
func BenchmarkOpen(b *testing.B) {
	b.Run("Open", func(b *testing.B) {
		f, err := os.Open("testdata/big.zip")
		if err != nil {
			b.Fatal(err)
		}
		defer f.Close()

		for i := 0; i < b.N; i++ {
			_, err = f.Seek(0, io.SeekStart)
			if err != nil {
				b.Fatal(err)
			}
			zr := NewReader(f)

			for zr.Next() {
				e, err := zr.Entry()
				if err != nil {
					b.Fatal(err)
				}
				rc, err := e.Open()
				if err != nil {
					b.Fatal(err)
				}
				_, err = io.Copy(io.Discard, rc)
				if err != nil {
					b.Fatal(err)
				}
				err = rc.Close()
				if err != nil {
					b.Fatal(err)
				}
			}
			if zr.err != nil {
				b.Fatal(zr.err)
			}
		}
	})

	b.Run("standard_Open", func(b *testing.B) {
		f, err := os.Open("testdata/big.zip")
		if err != nil {
			b.Fatal(err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			b.Fatal(err)
		}
		size := info.Size()

		for i := 0; i < b.N; i++ {
			_, err = f.Seek(0, io.SeekStart)
			if err != nil {
				b.Fatal(err)
			}
			zr, err := zip.NewReader(f, size)
			if err != nil {
				b.Fatal(err)
			}
			for _, e := range zr.File {
				r, err := e.Open()
				if err != nil {
					b.Fatal(err)
				}
				_, err = io.Copy(io.Discard, r)
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}

/*
goos: darwin
goarch: arm64
pkg: github.com/zhyee/zipstream
cpu: Apple M3
BenchmarkOpenRaw
BenchmarkOpenRaw/OpenRaw
BenchmarkOpenRaw/OpenRaw-8         	       2	 886253542 ns/op	  192184 B/op	      67 allocs/op
BenchmarkOpenRaw/standard_OpenRaw
BenchmarkOpenRaw/standard_OpenRaw-8         	     244	   4877145 ns/op	    6736 B/op	      27 allocs/op
*/
func BenchmarkOpenRaw(b *testing.B) {
	b.Run("OpenRaw", func(b *testing.B) {
		f, err := os.Open("testdata/big.zip")
		if err != nil {
			b.Fatal(err)
		}
		defer f.Close()

		for i := 0; i < b.N; i++ {
			_, err = f.Seek(0, io.SeekStart)
			if err != nil {
				b.Fatal(err)
			}
			zr := NewReader(f)

			for zr.Next() {
				e, err := zr.Entry()
				if err != nil {
					b.Fatal(err)
				}
				rc, err := e.OpenRaw()
				if err != nil {
					b.Fatal(err)
				}
				_, err = io.Copy(io.Discard, rc)
				if err != nil {
					b.Fatal(err)
				}
				err = rc.Close()
				if err != nil {
					b.Fatal(err)
				}
			}
			if zr.err != nil {
				b.Fatal(zr.err)
			}
		}
	})

	b.Run("standard_OpenRaw", func(b *testing.B) {
		f, err := os.Open("testdata/big.zip")
		if err != nil {
			b.Fatal(err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			b.Fatal(err)
		}
		size := info.Size()

		for i := 0; i < b.N; i++ {
			_, err = f.Seek(0, io.SeekStart)
			if err != nil {
				b.Fatal(err)
			}
			zr, err := zip.NewReader(f, size)
			if err != nil {
				b.Fatal(err)
			}
			for _, e := range zr.File {
				r, err := e.OpenRaw()
				if err != nil {
					b.Fatal(err)
				}
				_, err = io.Copy(io.Discard, r)
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}
