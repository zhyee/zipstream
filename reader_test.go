package zipstream

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"testing"
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
			t.Log("CompressedSize64: ", entry.CompressedSize64, "UncompressedSize64: ", entry.UncompressedSize64)
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
		}
	}
	if z.err != nil {
		t.Fatal("unexpected error: ", z.err)
	}

	if len(fileMap) != 0 {
		t.Fatal("the resolved entry count is incorrect")
	}

}
