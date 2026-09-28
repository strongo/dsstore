package dsstore

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"sort"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

type freeBlock struct {
	offset uint32
	size   uint32
}

func (s *Store) writeFreeMapCreate() []freeBlock {
	freeBlocks := make([]freeBlock, 0)
	for i := 5; i < 31; i++ {
		pow := uint32(1 << i)
		freeBlocks = append(freeBlocks, freeBlock{pow, pow})
	}
	return freeBlocks
}

func (s *Store) writeFreeMapSort(freeBlocks []freeBlock) {
	sort.SliceStable(freeBlocks, func(i, j int) bool {
		if freeBlocks[i].size < freeBlocks[j].size {
			return true
		}
		if freeBlocks[i].size == freeBlocks[j].size {
			return freeBlocks[i].offset < freeBlocks[j].offset
		}
		return false
	})
}

func (s *Store) writeFreeMapAlloc(freeBlocks []freeBlock, size uint32, capacity uint32) (uint32, []freeBlock) {
	// check capacity
	if capacity < size {
		capacity = size
	}
	// calculate size needed size that powered by 2
	var powCapacity uint32 = 0
	var powSize uint32 = 0
	var powIndex uint32 = 32
	for i := uint32(5); i < 32; i++ {
		powCapacity = uint32(1) << i
		if powCapacity >= size && powIndex >= 32 {
			powIndex = i
			powSize = powCapacity
		}
		if powCapacity >= capacity {
			if powIndex < 32 {
				break
			}
		}
	}
	// sort map
	s.writeFreeMapSort(freeBlocks)
	// find block in partly used blocks
	for i, block := range freeBlocks {
		if block.size != block.offset && block.size == powCapacity {
			freeBlocks = append(freeBlocks[:i], freeBlocks[i+1:]...)
			if block.size > powSize {
				freeBlocks = append(freeBlocks, freeBlock{block.offset + powSize, block.size - powSize})
			}
			return block.offset | powIndex, freeBlocks
		}
	}
	// find block in fully cleared blocks
	for i, block := range freeBlocks {
		if block.size >= powCapacity {
			freeBlocks = append(freeBlocks[:i], freeBlocks[i+1:]...)
			if block.size > powSize {
				freeBlocks = append(freeBlocks, freeBlock{block.offset + powSize, block.size - powSize})
			}
			return block.offset | powIndex, freeBlocks
		}
	}
	return 0, freeBlocks
}

func (s *Store) writeAlignBlock(b *bytes.Buffer, minSize uint32) {
	var lenBytes = uint32(b.Len())
	for i := 0; i < 32; i++ {
		var aligned uint32 = 1 << i
		if lenBytes <= aligned && minSize <= aligned {
			// add dummy bytes
			b.Write(make([]byte, aligned-lenBytes))
			break
		}
	}
}

func (s *Store) writeBlockData(b *bytes.Buffer, records []Record) {
	// nextBlock is always 0. Storing all records in the one block
	_ = binary.Write(b, binary.BigEndian, uint32(0))
	// count of records
	_ = binary.Write(b, binary.BigEndian, uint32(len(records)))
	// records
	for _, r := range records {
		// r.FileName
		n, _, _ := transform.Bytes(unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewEncoder(), []byte(r.FileName))
		_ = binary.Write(b, binary.BigEndian, uint32(len(n)/2))
		b.Write(n)
		// unknown extra 4 bytes
		_ = binary.Write(b, binary.BigEndian, uint32(r.Extra))
		// r.Type (4-bytes string)
		t := make([]byte, 4)
		copy(t, []byte(r.Type))
		b.Write(t)
		// r.DataLen for blob, ustr etc
		if r.DataLen > 0 {
			_ = binary.Write(b, binary.BigEndian, uint32(r.DataLen))
		}
		// r.Data
		b.Write(r.Data)
	}
}

func (s *Store) writeBlockDSDB(b *bytes.Buffer, index uint32) {
	// write data block index
	_ = binary.Write(b, binary.BigEndian, index)
	// levels. always is 0
	_ = binary.Write(b, binary.BigEndian, uint32(0))
	// records
	_ = binary.Write(b, binary.BigEndian, uint32(len(s.Records)))
	// nodes. always is 1 (storing all data in one data block)
	_ = binary.Write(b, binary.BigEndian, uint32(1))
	// dummy 0x1000 value
	_ = binary.Write(b, binary.BigEndian, uint32(0x1000))
	// other unknown data
	b.Write(s.DSDBExtra)
}

func (s *Store) writeOffsets(b *bytes.Buffer, offsetRoot, offsetDBDS, offsetData uint32) {
	// count of offset. always 3 blocks only
	_ = binary.Write(b, binary.BigEndian, uint32(3))
	// dummy 4 bytes
	_ = binary.Write(b, binary.BigEndian, uint32(0))
	// offsets
	_ = binary.Write(b, binary.BigEndian, offsetRoot)
	_ = binary.Write(b, binary.BigEndian, offsetDBDS)
	_ = binary.Write(b, binary.BigEndian, offsetData)
	// dummy 253 zero offsets
	for i := 0; i < 253; i++ {
		_ = binary.Write(b, binary.BigEndian, uint32(0))
	}
}

func (s *Store) writeTopicDBDS(b *bytes.Buffer, index uint32) {
	// always one topic (DBDS)
	_ = binary.Write(b, binary.BigEndian, uint32(1))
	// topic name len. Always is 4
	_ = b.WriteByte(4)
	// topic name
	b.Write([]byte("DSDB"))
	// topic offset
	_ = binary.Write(b, binary.BigEndian, uint32(index))
}

func (s *Store) writeFreeBlocks(b *bytes.Buffer, freeBlocks []freeBlock) {
	// sort blocks by size
	s.writeFreeMapSort(freeBlocks)
	// it is magic
	for i := 0; i < 32; i++ {
		var blockSize = uint32(1) << i
		var blockCount uint32 = 0
		for j := 0; j < len(freeBlocks); j++ {
			power := (freeBlocks[j].size & (freeBlocks[j].size - 1)) == 0
			if freeBlocks[j].size == blockSize || (freeBlocks[j].size > blockSize && !power) {
				blockCount++
				continue
			}
			if freeBlocks[j].size >= blockSize {
				break
			}
		}
		_ = binary.Write(b, binary.BigEndian, uint32(blockCount))
		if blockCount > 0 {
			// writing blocks offsets
			for j := 0; j < len(freeBlocks); j++ {
				power := (freeBlocks[j].size & (freeBlocks[j].size - 1)) == 0
				if freeBlocks[j].size == blockSize || (freeBlocks[j].size > blockSize && !power) {
					_ = binary.Write(b, binary.BigEndian, uint32(freeBlocks[j].offset))
					continue
				}
				if freeBlocks[j].size >= blockSize {
					break
				}
			}
		}
	}
}

func (s *Store) writeBlockRoot(b *bytes.Buffer, offsetRoot, offsetDBDS, offsetData uint32, freeBlocks []freeBlock) {
	// offsets
	s.writeOffsets(b, offsetRoot, offsetDBDS, offsetData)
	// topic. index is always 1
	s.writeTopicDBDS(b, 1)
	// free blocks
	s.writeFreeBlocks(b, freeBlocks)
	// write extra (unknown data)
	b.Write(s.RootExtra)
}

func (s *Store) writeHeader(b *bytes.Buffer, offsetRoot, size uint32) {
	// magic1
	_ = binary.Write(b, binary.BigEndian, headerMagic1)
	// magic2
	_ = binary.Write(b, binary.BigEndian, headerMagic2)
	// offset of root block
	_ = binary.Write(b, binary.BigEndian, offsetRoot)
	// size of root block
	_ = binary.Write(b, binary.BigEndian, size)
	// offset of root block
	_ = binary.Write(b, binary.BigEndian, offsetRoot)
	// write extra
	headerExtra := make([]byte, 16)
	copy(headerExtra, s.HeaderExtra)
	b.Write(headerExtra)
}

// Write writes .DS_Store to io.Writer
func (s *Store) Write(w io.Writer) error {
	// prepare data block
	blockData := new(bytes.Buffer)
	s.writeBlockData(blockData, s.Records)
	// prepare DSDB block (always 2 index)
	blockDSDB := new(bytes.Buffer)
	s.writeBlockDSDB(blockDSDB, 2)
	// prepare Root block
	blockList := s.writeFreeMapCreate()
	blockRoot := new(bytes.Buffer)
	s.writeBlockRoot(blockRoot, 0, 0, 0, blockList)
	// align blocks
	s.writeAlignBlock(blockData, 32)
	s.writeAlignBlock(blockDSDB, 32)
	s.writeAlignBlock(blockRoot, 32)
	// create blocks map
	blockDataOffset, blockList := s.writeFreeMapAlloc(blockList, uint32(blockData.Len()), 0)
	blockDSDBOffset, blockList := s.writeFreeMapAlloc(blockList, uint32(blockDSDB.Len()), 0)
	blockRootOffset, blockList := s.writeFreeMapAlloc(blockList, uint32(blockRoot.Len()), 0)
	// calc real offset
	blockDataOffsetReal := blockOffset(blockDataOffset)
	blockDSDBOffsetReal := blockOffset(blockDSDBOffset)
	blockRootOffsetReal := blockOffset(blockRootOffset)
	// calc end of blocks
	blockDataEnd := blockDataOffsetReal + blockSize(blockDataOffset)
	blockDSDBEnd := blockDSDBOffsetReal + blockSize(blockDSDBOffset)
	blockRootEnd := blockRootOffsetReal + blockSize(blockRootOffset)
	// re-create root block with correct offsets
	blockRoot.Reset()
	s.writeBlockRoot(blockRoot, blockRootOffset, blockDSDBOffset, blockDataOffset, blockList)
	// write header
	blockHeader := new(bytes.Buffer)
	s.writeHeader(blockHeader, blockRootOffsetReal, uint32(blockRoot.Len()))
	// calculate file size
	size := max(uint32(32), blockRootEnd, blockDSDBEnd, blockDataEnd)
	// create full file
	fileData := make([]byte, size+4)
	copy(fileData[0:], blockHeader.Bytes())
	copy(fileData[4+blockRootOffsetReal:], blockRoot.Bytes())
	copy(fileData[4+blockDSDBOffsetReal:], blockDSDB.Bytes())
	copy(fileData[4+blockDataOffsetReal:], blockData.Bytes())
	// write it
	_, err := w.Write(fileData)
	return err
}

// WriteFile writes .DS_Store to the file
func (s *Store) WriteFile(filename string, perm os.FileMode) error {
	buffer := new(bytes.Buffer)
	_ = s.Write(buffer)
	return os.WriteFile(filename, buffer.Bytes(), perm)
}
