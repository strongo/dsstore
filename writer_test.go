package dsstore

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"testing"
)

func TestWriteFile(t *testing.T) {
	testdata := filepath.Join(".", "testdata", "00.DS_Store")
	var s Store
	err := s.ReadFile(testdata)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "test.DS_Store")

	err = s.WriteFile(tempFile, 0644)
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var s2 Store
	err = s2.ReadFile(tempFile)
	if err != nil {
		t.Fatalf("ReadFile of written file failed: %v", err)
	}

	if len(s.Records) != len(s2.Records) {
		t.Errorf("expected %d records, got %d", len(s.Records), len(s2.Records))
	}
}

func TestRecordTypes(t *testing.T) {
	types := []string{"bool", "type", "long", "shor", "comp", "dutc", "blob", "ustr"}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			s := &Store{}
			r := Record{
				FileName: "test",
				Type:     typ,
				Data:     []byte{0, 0, 0, 0, 0, 0, 0, 0},
			}
			switch typ {
			case "bool":
				r.Data = []byte{1}
			case "type", "long", "shor":
				r.Data = []byte{0, 0, 0, 1}
			case "blob", "ustr":
				r.DataLen = 1
				if typ == "ustr" {
					r.Data = []byte{0, 65} // 'A' in UTF16-BE
				} else {
					r.Data = []byte{0x42}
				}
			}
			s.Records = append(s.Records, r)

			buf := new(bytes.Buffer)
			err := s.Write(buf)
			if err != nil {
				t.Fatalf("Write failed: %v", err)
			}

			var s2 Store
			err = s2.Read(buf)
			if err != nil {
				t.Fatalf("Read failed: %v", err)
			}

			if len(s2.Records) != 1 {
				t.Fatalf("expected 1 record, got %d", len(s2.Records))
			}
			if s2.Records[0].Type != typ {
				t.Errorf("expected type %s, got %s", typ, s2.Records[0].Type)
			}
		})
	}
}

func TestWriteFileError(t *testing.T) {
	var s Store
	err := s.WriteFile("/non/existent/path/file", 0644)
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestManyRecords(t *testing.T) {
	s := &Store{}
	for i := 0; i < 2000; i++ {
		s.Records = append(s.Records, Record{
			FileName: fmt.Sprintf("file%04d", i),
			Type:     "long",
			Data:     []byte{0, 0, 0, byte(i % 256)},
		})
	}
	buf := new(bytes.Buffer)
	err := s.Write(buf)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var s2 Store
	err = s2.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if len(s2.Records) != 2000 {
		t.Errorf("expected 2000 records, got %d", len(s2.Records))
	}
}

func TestWriteFreeBlocksMultiple(t *testing.T) {
	s := &Store{}
	buf := new(bytes.Buffer)
	freeBlocks := make([]freeBlock, 0)
	for i := uint32(0); i < 40; i++ {
		freeBlocks = append(freeBlocks, freeBlock{offset: i * 1024, size: 1024})
	}
	s.writeFreeBlocks(buf, freeBlocks)
}

func TestWriteAlignBlock(t *testing.T) {
	s := &Store{}
	buf := new(bytes.Buffer)
	buf.WriteByte(1)
	s.writeAlignBlock(buf, 8)
	if buf.Len() != 8 {
		t.Errorf("expected length 8, got %d", buf.Len())
	}
}

type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) {
	return 0, io.ErrShortWrite
}

func TestWriteError(t *testing.T) {
	s := &Store{}
	if err := s.Write(failWriter{}); err == nil {
		t.Fatal("expected error on failing writer")
	}
}

func TestWriteFreeMapAlloc(t *testing.T) {
	s := &Store{}
	// Empty slice returns 0
	val, _ := s.writeFreeMapAlloc(nil, 64, 64)
	if val != 0 {
		t.Fatalf("expected 0 for empty free blocks, got %d", val)
	}

	// Partly used block matching powCapacity where block.size > powSize
	fb := []freeBlock{{offset: 128, size: 64}}
	allocated, remaining := s.writeFreeMapAlloc(fb, 32, 64)
	if allocated == 0 {
		t.Fatal("expected successful allocation")
	}
	if len(remaining) != 1 || remaining[0].size != 32 {
		t.Fatalf("expected remaining block of size 32, got %+v", remaining)
	}

	// Partly used block matching powCapacity where block.size == powSize
	fb2 := []freeBlock{{offset: 256, size: 64}}
	allocated2, remaining2 := s.writeFreeMapAlloc(fb2, 64, 64)
	if allocated2 == 0 {
		t.Fatal("expected successful allocation")
	}
	if len(remaining2) != 0 {
		t.Fatalf("expected 0 remaining blocks, got %+v", remaining2)
	}

	// writeFreeMapSort equality branch
	sortBlocks := []freeBlock{{offset: 200, size: 100}, {offset: 100, size: 100}}
	s.writeFreeMapSort(sortBlocks)
	if sortBlocks[0].offset != 100 {
		t.Fatalf("expected offset 100 first, got %d", sortBlocks[0].offset)
	}
}

