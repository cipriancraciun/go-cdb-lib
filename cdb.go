/*
Package cdb provides a native implementation of cdb, a constant key/value
database with some very nice properties.

For more information on cdb, see the original design doc at http://cr.yp.to/cdb.html.
*/
package cdb

import (
	"bytes"
	"encoding/binary"
	"hash"
	"io"
	"os"
)

const indexSize = 256 * 8

type index [256]table

// CDB represents an open CDB database. It can only be used for reads; to
// create a database, use Writer.
type CDB struct {
	reader io.ReaderAt
	readerBytes []byte
	hasher func ([]byte) (uint32)
	index  index
}

type table struct {
	offset uint32
	length uint32
}

// Open opens an existing CDB database at the given path.
func Open(path string) (*CDB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	return New(f, nil)
}

// New opens a new CDB instance for the given io.ReaderAt. It can only be used
// for reads; to create a database, use Writer.
//
// If hasher is nil, it will default to the CDB hash function. If a database
// was created with a particular hash function, that same hash function must be
// passed to New, or the database will return incorrect results.
func New(reader io.ReaderAt, hasher hash.Hash32) (*CDB, error) {
	return NewFromReaderWithHasher(reader, adaptHash32 (hasher))
}

func NewFromReaderWithHasher(reader io.ReaderAt, hasher func ([]byte) (uint32)) (*CDB, error) {
	cdb := &CDB{reader: reader}
	return cdb.initialize(hasher)
}

func NewFromBufferWithHasher(buffer []byte, hasher func ([]byte) (uint32)) (*CDB, error) {
	cdb := &CDB{readerBytes: buffer}
	return cdb.initialize(hasher)
}

func adaptHash32(hasher hash.Hash32) (func ([]byte) (uint32)) {
	var hasherFunc func ([]byte) (uint32)
	if hasher == nil {
		hasherFunc = nil
	} else {
		hasherFunc = func (data []byte) (uint32) {
			hasher.Reset()
			hasher.Write(data)
			return hasher.Sum32()
		}
	}
	return hasherFunc
}

func (cdb *CDB) initialize (hasher func ([]byte) (uint32)) (*CDB, error) {
	if hasher == nil {
		hasher = CDBHashSum32
	}
	cdb.hasher = hasher
	err := cdb.readIndex()
	if err != nil {
		return nil, err
	}
	return cdb, nil
}

// Get returns the value for a given key, or nil if it can't be found.
func (cdb *CDB) Get(key []byte) ([]byte, error) {
	hash := cdb.hasher(key)
	return cdb.GetWithHash(key, hash)
}

func (cdb *CDB) GetWithCdbHash(key []byte) ([]byte, error) {
	hash := CDBHashSum32(key)
	return cdb.GetWithHash(key, hash)
}

func (cdb *CDB) GetWithHash(key []byte, hash uint32) ([]byte, error) {

	table := cdb.index[hash&0xff]
	if table.length == 0 {
		return nil, nil
	}

	// Probe the given hash table, starting at the given slot.
	startingSlot := (hash >> 8) % table.length
	slot := startingSlot

	for {
		slotOffset := table.offset + (8 * slot)
		slotHash, offset, err := cdb.readTuple(slotOffset)
		if err != nil {
			return nil, err
		}

		// An empty slot means the key doesn't exist.
		if slotHash == 0 {
			break
		} else if slotHash == hash {
			value, err := cdb.getValueAt(offset, key)
			if err != nil {
				return nil, err
			} else if value != nil {
				return value, nil
			}
		}

		slot = (slot + 1) % table.length
		if slot == startingSlot {
			break
		}
	}

	return nil, nil
}

// Close closes the database to further reads.
func (cdb *CDB) Close() error {
	if cdb.reader == nil {
		return nil
	}
	if closer, ok := cdb.reader.(io.Closer); ok {
		return closer.Close()
	} else {
		return nil
	}
}

func (cdb *CDB) readIndex() error {

	buf, err := cdb.readAt(0, indexSize)
	if err != nil {
		return err
	}

	for i := 0; i < 256; i++ {
		off := i * 8
		cdb.index[i] = table{
			offset: binary.LittleEndian.Uint32(buf[off : off+4]),
			length: binary.LittleEndian.Uint32(buf[off+4 : off+8]),
		}
	}

	return nil
}

func (cdb *CDB) getValueAt(offset uint32, expectedKey []byte) ([]byte, error) {
	keyLength, valueLength, err := cdb.readTuple(offset)
	if err != nil {
		return nil, err
	}

	// We can compare key lengths before reading the key at all.
	if int(keyLength) != len(expectedKey) {
		return nil, nil
	}

	var buf []byte
	buf, err = cdb.readAt(offset+8, keyLength+valueLength)
	if err != nil {
		return nil, err
	}

	// If they keys don't match, this isn't it.
	if bytes.Compare(buf[:keyLength], expectedKey) != 0 {
		return nil, nil
	}

	return buf[keyLength:], nil
}
