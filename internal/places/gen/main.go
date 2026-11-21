// Command gen rebuilds ../cities.tsv.gz from a GeoNames dump: go run ./internal/places/gen cities5000.zip
// The dump is https://download.geonames.org/export/dump/cities5000.zip (CC BY 4.0), every populated place of 5000 people or more.
package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"log"
	"os"
	"strings"
)

// Sections of a city (PPLX) would name a run after a neighbourhood GeoNames happens to list, and the rest are places nobody lives in any more.
var skipped = map[string]bool{"PPLX": true, "PPLH": true, "PPLQ": true, "PPLW": true}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: gen cities5000.zip cities.tsv.gz")
	}
	archive, err := zip.OpenReader(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = archive.Close() }()
	if len(archive.File) != 1 {
		log.Fatalf("expected one file in %s, found %d", os.Args[1], len(archive.File))
	}
	dump, err := archive.File[0].Open()
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = dump.Close() }()

	var out bytes.Buffer
	// No name or timestamp in the header, so the same dump always writes the same bytes.
	zw, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	rows := 0
	lines := bufio.NewScanner(dump)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	for lines.Scan() {
		// GeoNames columns: 1 name, 4 latitude, 5 longitude, 7 feature code, 14 population, 17 timezone.
		f := strings.Split(lines.Text(), "\t")
		if len(f) < 18 {
			log.Fatalf("short row: %q", lines.Text())
		}
		if skipped[f[7]] || f[17] == "" {
			continue
		}
		_, _ = fmt.Fprintf(zw, "%s\t%s\t%s\t%s\t%s\n", f[1], f[4], f[5], f[14], f[17])
		rows++
	}
	if err := lines.Err(); err != nil {
		log.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(os.Args[2], out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %d places, %d bytes", rows, out.Len())
}
