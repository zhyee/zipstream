# zipiterator
Package zipiterator is a file iterator for zip archive with no need to supply io.ReaderAt and total size, that is, just only normal io.Reader.

## Implementation
Most code of this package is copied directly from golang standard library [archive/zip](https://pkg.go.dev/archive/zip), and reference .ZIP file format specification
is [here](https://pkware.cachefly.net/webdocs/casestudies/APPNOTE.TXT)

## Usage
> go get github.com/zhyee/zipiterator

## Examples
```go
package main

import (
	"io"
	"log"
	"os"

	"github.com/zhyee/zipiterator"
)

func main() {

	f, err := os.Open("./zipiterator.code.zip")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	zr := zipiterator.NewReader(f)

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

			log.Println(string(content))

			if uint64(len(content)) != e.UncompressedSize64 {
				log.Fatalf("read zip file length not equal with UncompressedSize64")
			}
			if err := rc.Close(); err != nil {
				log.Fatalf("close zip entry reader fail: %s", err)
			}
		}
	}
}

```

## Limitation

- Every file in zip archive can read only once for a new Reader, Repeated read is unsupported.
- Some `central directory header` field is not resolved, such as `version made by`, `internal file attributes`, `external file attributes`, `relative offset of local header`, some `central directory header` field may differ from `local file header`, such as `extra field`. 
- Unable to read multi files concurrently.