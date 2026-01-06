package zipstream

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/rand"
	"errors"
	"hash/crc32"
	"io"
	rand2 "math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	flate2 "github.com/klauspost/compress/flate"
)

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

	f, err := os.Open("testdata/example.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zipFile, err := os.ReadFile("testdata/example.zip")
	if err != nil {
		t.Fatal(err)
	}

	az, err := zip.NewReader(f, int64(len(zipFile)))
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
			_ = ziprc.Close()

			t.Log("------------------------------------")
			t.Log("file: ", entry.Name)
			t.Log("CompressedSize64: ", entry.CompressedSize64)
			t.Log("UncompressedSize64: ", entry.UncompressedSize64)
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
		}
	}
	if z.err != nil {
		t.Fatal("unexpected error: ", z.err)
	}

	if len(fileMap) != 0 {
		t.Fatal("the resolved entry count is incorrect")
	}

}

func TestReaderBridge(t *testing.T) {
	out := &bytes.Buffer{}
	fw, err := flate.NewWriter(out, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	uSize, err := io.CopyN(fw, rand.Reader, int64(rand2.Intn(1024*1024)))
	if err != nil {
		t.Fatal(err)
	}
	n, err := fw.Write([]byte(strings.Repeat("hello zipstream", 10000)))
	if err != nil {
		t.Fatal(err)
	}
	uSize += int64(n)
	if err = fw.Close(); err != nil {
		t.Fatal(err)
	}
	cSize := out.Len()

	t.Logf("compressedSize: %d, uncompressedSize: %d", cSize, uSize)
	trailer := "\x01\x02\x03\x04\x05\x06\x07\x08\x09"
	_, err = out.WriteString(trailer)
	if err != nil {
		t.Fatal(err)
	}

	rb := newReaderBridge(out)
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		rc := flate2.NewReader(rb)
		defer rc.Close()
		buf := make([]byte, 4096)
		for {
			_, err := rc.Read(buf)
			if err != nil {
				if errors.Is(err, io.EOF) {
					close(rb.shadow.ch)
					return
				}
				t.Error(err)
			}
		}
	}()

	cBytes, err := io.ReadAll(rb.shadow)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("compressed size:", len(cBytes))
	if len(cBytes) != cSize {
		t.Fatalf("%d compressed bytes expect, got %d", cSize, len(cBytes))
	}
	if out.String() != trailer {
		t.Fatalf("compressed buf %v expect, got %v", []byte(trailer), out.Bytes())
	}
	wg.Wait()
}
