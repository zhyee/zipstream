package zipiterator

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"testing"
)

func TestNewReader(t *testing.T) {

	f, err := os.Open("testData/zipiterator.code.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	z := NewReader(f)

	entryCnt := 0
	for {
		entry, err := z.GetNextEntry()
		if err == io.EOF {
			// iterator over
			break
		}
		entryCnt++

		fmt.Println("Name:", entry.Name)
		fmt.Println("Comment:", entry.Comment)
		fmt.Println("IsDir:", entry.IsDir())
		fmt.Println("ReaderVersion", entry.ReaderVersion)
		fmt.Println("Mtime:", entry.Modified)
		fmt.Println("Method:", entry.Method)
		fmt.Println("hasDataDescriptor: ", entry.hasDataDescriptor())
		fmt.Println("CompressedSize64:", entry.CompressedSize64)
		fmt.Println("UncompressedSize64:", entry.UncompressedSize64)

		if !entry.IsDir() {
			rc, err := entry.Open()
			if err != nil {
				t.Fatalf("open zip file entry err: %s", err)
			}

			entryFile, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read entry file contents fail: %s", err)
			}

			fmt.Println(string(entryFile))

			if err := rc.Close(); err != nil {
				t.Fatalf("close zip file entry reader err: %s", err)
			}
		}

		fmt.Println("------------------------------------------")
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek file fail: %s", err)
	}
	stat, err := os.Stat("testData/zipiterator.code.zip")
	az, err := zip.NewReader(f, stat.Size())
	if len(az.File) != entryCnt {
		t.Fatalf("expected entry count: %d, actual: %d", len(az.File), entryCnt)
	}

}
