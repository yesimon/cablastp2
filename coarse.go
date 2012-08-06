package cablastp

import (
	"os"
	"sync"
)

const (
	FileCoarseFasta      = "coarse.fasta"
	FileCoarseLinks      = "coarse.links"
	FileCoarsePlainLinks = "coarse.links.plain"
	FileCoarseSeeds      = "coarse.seeds"
	FileCoarsePlainSeeds = "coarse.seeds.plain"
)

// CoarseDB represents a set of unique sequences that comprise the "coarse"
// database. Sequences in the ReferenceDB are use to re-create the original
// sequences.
type CoarseDB struct {
	Seqs     []*CoarseSeq
	seqsRead int
	Seeds    Seeds

	FileFasta *os.File
	FileSeeds *os.File
	FileLinks *os.File

	seqLock *sync.RWMutex

	readOnly   bool
	plain      bool
	plainLinks *os.File
	plainSeeds *os.File
}

// NewCoarseDB takes a list of initial original sequences, and adds each
// sequence to the reference database unchanged. Seeds are also generated for
// each K-mer in each original sequence.
func NewWriteCoarseDB(appnd bool, db *DB) (*CoarseDB, error) {
	var err error

	coarsedb := &CoarseDB{
		Seqs:       make([]*CoarseSeq, 0, 10000000),
		seqsRead:   0,
		Seeds:      NewSeeds(db.MapSeedSize),
		FileFasta:  nil,
		FileSeeds:  nil,
		FileLinks:  nil,
		seqLock:    &sync.RWMutex{},
		readOnly:   db.ReadOnly,
		plain:      db.SavePlain,
		plainSeeds: nil,
	}
	coarsedb.FileFasta, err = db.openWriteFile(appnd, FileCoarseFasta)
	if err != nil {
		return nil, err
	}
	coarsedb.FileSeeds, err = db.openWriteFile(false, FileCoarseSeeds)
	if err != nil {
		return nil, err
	}
	coarsedb.FileLinks, err = db.openWriteFile(false, FileCoarseLinks)
	if err != nil {
		return nil, err
	}

	if coarsedb.plain {
		coarsedb.plainLinks, err = db.openWriteFile(false, FileCoarsePlainLinks)
		if err != nil {
			return nil, err
		}
		coarsedb.plainSeeds, err = db.openWriteFile(false, FileCoarsePlainSeeds)
		if err != nil {
			return nil, err
		}
	}

	if appnd {
		if err := coarsedb.load(); err != nil {
			return nil, err
		}
	}
	return coarsedb, nil
}

func NewReadCoarseDB(db *DB) (*CoarseDB, error) {
	var err error

	coarsedb := &CoarseDB{
		Seqs:      make([]*CoarseSeq, 0, 10000000),
		Seeds:     NewSeeds(db.MapSeedSize),
		FileFasta: nil,
		FileSeeds: nil,
		FileLinks: nil,
		seqLock:   nil,
		readOnly:  false,
		plain:     db.SavePlain,
	}
	coarsedb.FileFasta, err = db.openReadFile(FileCoarseFasta)
	if err != nil {
		return nil, err
	}
	coarsedb.FileLinks, err = db.openReadFile(FileCoarseLinks)
	if err != nil {
		return nil, err
	}

	if err := coarsedb.load(); err != nil {
		return nil, err
	}
	return coarsedb, nil
}

// Add takes an original sequence, converts it to a reference sequence, and
// adds it as a new reference sequence to the reference database. Seeds are
// also generated for each K-mer in the sequence. The resulting reference
// sequence is returned.
func (coarsedb *CoarseDB) Add(oseq []byte) (int, *CoarseSeq) {
	coarsedb.seqLock.Lock()
	id := len(coarsedb.Seqs)
	corSeq := NewCoarseSeq(id, "", oseq)
	coarsedb.Seqs = append(coarsedb.Seqs, corSeq)
	coarsedb.seqLock.Unlock()

	coarsedb.Seeds.Add(id, corSeq)

	return id, corSeq
}

func (coarsedb *CoarseDB) CoarseSeqGet(i int) *CoarseSeq {
	coarsedb.seqLock.RLock()
	seq := coarsedb.Seqs[i]
	coarsedb.seqLock.RUnlock()

	return seq
}

func (coarsedb *CoarseDB) ReadClose() {
	coarsedb.FileFasta.Close()
	coarsedb.FileLinks.Close()
}

func (coarsedb *CoarseDB) WriteClose() {
	coarsedb.FileFasta.Close()
	coarsedb.FileSeeds.Close()
	coarsedb.FileLinks.Close()
	if coarsedb.plain {
		coarsedb.plainLinks.Close()
		coarsedb.plainSeeds.Close()
	}
}

func (coarsedb *CoarseDB) load() error {
	if err := coarsedb.readFasta(); err != nil {
		return err
	}
	if err := coarsedb.readSeeds(); err != nil {
		return err
	}
	if err := coarsedb.readLinks(); err != nil {
		return err
	}
	return nil
}

// Save will save the reference database as a coarse FASTA file and a binary
// encoding of all reference links.
func (coarsedb *CoarseDB) Save() error {
	coarsedb.seqLock.RLock()
	defer coarsedb.seqLock.RUnlock()

	if err := coarsedb.saveFasta(); err != nil {
		return err
	}
	if err := coarsedb.saveLinks(); err != nil {
		return err
	}
	if !coarsedb.readOnly {
		if err := coarsedb.saveSeeds(); err != nil {
			return err
		}
	}
	if coarsedb.plain {
		if err := coarsedb.saveLinksPlain(); err != nil {
			return err
		}
		if !coarsedb.readOnly {
			if err := coarsedb.saveSeedsPlain(); err != nil {
				return err
			}
		}
	}
	return nil
}
