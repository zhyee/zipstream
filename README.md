# zipstream
Package zipstream is an on-the-fly extractor/reader for zip archive similar as Java's `java.util.zip.ZipInputStream`, there is no need to provide `io.ReaderAt` and archive size, that is, just only one `io.Reader` parameter.

## Implementation
Most code in this package is directly copied from Golang standard [archive/zip](https://pkg.go.dev/archive/zip) library, you can read the `ZIP` specification from [here](https://pkware.cachefly.net/webdocs/casestudies/APPNOTE.TXT).

## Usage
> go get github.com/zhyee/zipstream

## Examples

```go
package main

import (
	"io"
	"log"
	"net/http"

	"github.com/zhyee/zipstream"
)

func main() {

	resp, err := http.Get("https://github.com/golang/go/archive/refs/tags/go1.16.10.zip")
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	zr := zipstream.NewReader(resp.Body)

	for zr.Next() {
		e, err := zr.Entry()
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
	if err = zr.Err(); err != nil {
		log.Fatalf("encounter error: %v", err)
    }
}
```

## Limitations

- Repeatable read is not supported, each file in the zip archive can be read only once per `Reader` instance.
- Some `central directory header` fields are unresolved — including `version made by`, `internal file attributes`, `external file attributes` and `relative offset of local header`. Additionally, certain other `central directory header` fields (e.g., `extra field`) may differ from their counterparts in the `local file header`. 
- Concurrent reading of multiple files is not supported.
