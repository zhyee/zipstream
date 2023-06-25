package zipstream

import (
	"archive/zip"
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
	"testing"
)

func TestStreamReader(t *testing.T) {
	resp, err := http.Get("https://github.com/golang/go/archive/refs/tags/go1.16.10.zip")
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	zr := NewReader(resp.Body)

	for {
		e, err := zr.GetNextEntry()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("unable to get next entry: %s", err)
		}

		log.Println("entry name: ", e.Name)
		log.Println("entry comment: ", e.Comment)
		log.Println("entry reader version: ", e.ReaderVersion)
		log.Println("entry modify time: ", e.Modified)
		log.Println("entry compressed size: ", e.CompressedSize64)
		log.Println("entry uncompressed size: ", e.UncompressedSize64)
		log.Println("entry is a dir: ", e.IsDir())

		if !e.IsDir() {
			rc, err := e.Open()
			if err != nil {
				log.Fatalf("unable to open zip file: %s", err)
			}
			content, err := io.ReadAll(rc)
			if err != nil {
				log.Fatalf("read zip file content fail: %s", err)
			}

			log.Println("file length:", len(content))

			if uint64(len(content)) != e.UncompressedSize64 {
				log.Fatalf("read zip file length not equal with UncompressedSize64")
			}
			if err := rc.Close(); err != nil {
				log.Fatalf("close zip entry reader fail: %s", err)
			}
		}
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

	for {
		entry, err := z.GetNextEntry()
		if err == io.EOF {
			// iterator over
			break
		}

		zf, ok := fileMap[entry.Name]
		if !ok {
			t.Fatalf("not expected file: %s", entry.Name)
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

	if len(fileMap) != 0 {
		t.Fatal("the resolved entry count is incorrect")
	}

}
