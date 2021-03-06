package main

import (
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"runtime"
	"runtime/pprof"
	// "strings"

	"github.com/TuftsBCB/io/fasta"
	"github.com/TuftsBCB/seq"

	"github.com/ndaniels/cablastp2"
)

// A BLAST database is created on the reference sequence after compression.
// The search program will blast the input query sequences against this
// database (with a relaxed e-value), and expand the hits using links into an
// in memory FASTA file. This FASTA file is passed to the stdin of a
// `makeblastdb` command, which outputs the fine BLAST database. Finally, the
// query sequences are blasted against this new database, and the hits are
// returned unmodified.

var (
	// A default configuration.
	argDBConf = cablastp.DefaultDBConf.DeepCopy()
	// Flags that affect the operation of search.
	// Flags that control algorithmic parameters are stored in `queryDBConf`.
	flagMakeBlastDB    = "makeblastdb"
	flagBlastx         = "blastx"
	flagBlastn         = "blastn"
	flagGoMaxProcs     = runtime.NumCPU()
	flagQuiet          = false
	flagCpuProfile     = ""
	flagMemProfile     = ""
	flagCoarseEval     = 1000.0
  flagCoarseBitScore = 0.0
	flagNoCleanup      = false
	flagCompressQuery  = false
	flagBatchQueries   = false
	flagIterativeQuery = false
  flagShortQueries   = true
  flagQueryChunkSize = 100
)

// blastArgs are all the arguments after "--blast-args".
var blastArgs []string

func init() {
	log.SetFlags(0)

	// Regular cablastp-xsearch options

	flag.StringVar(&flagMakeBlastDB, "makeblastdb",
		flagMakeBlastDB,
		"The location of the 'makeblastdb' executable.")
	flag.StringVar(&flagBlastx, "blastx",
		flagBlastx,
		"The location of the 'blastx' executable.")
	flag.StringVar(&flagBlastn, "blastn",
		flagBlastn,
		"The location of the 'blastn' executable.")
	flag.Float64Var(&flagCoarseEval, "coarse-eval", flagCoarseEval,
		"The e-value threshold for the coarse search. This will NOT\n"+
			"\tbe used on the fine search. The fine search e-value threshold\n"+
			"\tcan be set in the 'blast-args' argument.")
	flag.Float64Var(&flagCoarseBitScore, "coarse-bitscore", flagCoarseBitScore,
		"The bit score threshold for the coarse search. This will NOT\n"+
			"\tbe used on the fine search.")
	flag.BoolVar(&flagNoCleanup, "no-cleanup", flagNoCleanup,
		"When set, the temporary fine BLAST database that is created\n"+
			"\twill NOT be deleted.")

	flag.IntVar(&flagGoMaxProcs, "p", flagGoMaxProcs,
		"The maximum number of CPUs that can be executing simultaneously.")
	flag.BoolVar(&flagQuiet, "quiet", flagQuiet,
		"When set, the only outputs will be errors echoed to stderr.")
	flag.StringVar(&flagCpuProfile, "cpuprofile", flagCpuProfile,
		"When set, a CPU profile will be written to the file specified.")
	flag.StringVar(&flagMemProfile, "memprofile", flagMemProfile,
		"When set, a memory profile will be written to the file specified.")
	flag.BoolVar(&flagIterativeQuery, "iterative-queries", flagIterativeQuery,
		"When set, will process queries in chunks instead of as a batch.")
	flag.IntVar(&flagQueryChunkSize, "l", flagQueryChunkSize,
		"How many sequences to perform coarse search on at a time.")
	flag.BoolVar(&flagCompressQuery, "compress-query", flagCompressQuery,
		"When set, will process compress queries before search.")
	flag.BoolVar(&flagShortQueries, "short-queries", flagShortQueries,
		"When set, will assume query sequences are short, adjusting blast args.")

	// compress options

	flag.IntVar(&argDBConf.MinMatchLen, "min-match-len",
		argDBConf.MinMatchLen,
		"The minimum size of a match.")
	flag.IntVar(&argDBConf.MatchKmerSize, "match-kmer-size",
		argDBConf.MatchKmerSize,
		"The size of kmer fragments to match in ungapped extension.")
	flag.IntVar(&argDBConf.ExtSeqIdThreshold, "ext-seq-id-threshold",
		argDBConf.ExtSeqIdThreshold,
		"The sequence identity threshold of [un]gapped extension. \n"+
			"\t(An integer in the inclusive range from 0 to 100.)")
	flag.IntVar(&argDBConf.MatchSeqIdThreshold, "match-seq-id-threshold",
		argDBConf.MatchSeqIdThreshold,
		"The sequence identity threshold of an entire match.")
	flag.IntVar(&argDBConf.MatchExtend, "match-extend",
		argDBConf.MatchExtend,
		"The maximum number of residues to blindly extend a \n"+
			"\tmatch without regard to sequence identity. This is \n"+
			"\tto avoid small sequences in the coarse database.")
	flag.IntVar(&argDBConf.GappedWindowSize, "gapped-window-size",
		argDBConf.GappedWindowSize,
		"The size of the gapped match window.")
	flag.IntVar(&argDBConf.UngappedWindowSize, "ungapped-window-size",
		argDBConf.UngappedWindowSize,
		"The size of the ungapped match window.")
	flag.IntVar(&argDBConf.MapSeedSize, "map-seed-size",
		argDBConf.MapSeedSize,
		"The size of a seed in the K-mer map. This size combined with\n"+
			"\t'ext-seed-size' forms the total seed size.")

	flag.IntVar(&argDBConf.LowComplexity, "low-complexity",
		argDBConf.LowComplexity,
		"The window size used to detect regions of low complexity.\n"+
			"\tLow complexity regions are repetitions of a single amino\n"+
			"\tacid residue. Low complexity regions are skipped when\n"+
			"\ttrying to extend a match.")
	flag.IntVar(&argDBConf.SeedLowComplexity, "seed-low-complexity",
		argDBConf.SeedLowComplexity,
		"The seed window size used to detect regions of low complexity.\n"+
			"\tLow complexity regions are repetitions of a single amino\n"+
			"\tacid residue. Low complexity regions matching this window\n"+
			"\tsize are not included in the seeds table.")
	// flag.Float64Var(&flagMaxSeedsGB, "max-seeds", flagMaxSeedsGB,
	// 	"When set, the in memory seeds table will be completely erased\n"+
	// 		"\twhen the memory used by seeds exceeds the specified number,\n"+
	// 		"\tin gigabytes.\n"+
	// 		"\tEach seed corresponds to 16 bytes of memory.\n"+
	// 		"\tSetting to zero disables this behavior.")

	// find '--blast-args' and chop off the remainder before letting the flag
	// package have its way.
	for i, arg := range os.Args {
		if arg == "--blast-args" {
			blastArgs = os.Args[i+1:]
			os.Args = os.Args[:i]
		}
	}

	flag.Usage = usage
	flag.Parse()

	runtime.GOMAXPROCS(flagGoMaxProcs)

}

func main() {

	searchBuf := new(bytes.Buffer) // might need more than 1 buffer

	if flag.NArg() != 2 {
		flag.Usage()
	}

	// If the quiet flag isn't set, enable verbose output.
	if !flagQuiet {
		cablastp.Verbose = true
	}

	queryDBConf := argDBConf.DeepCopy() // deep copy of the default DBConf 
                                      // updated by the args
	inputFastaQueryName := flag.Arg(1)
	db, err := cablastp.NewReadDB(flag.Arg(0))
	if err != nil {
		fatalf("Could not open '%s' database: %s\n", flag.Arg(0), err)
	}
	// For query-compression mode, we first run compression on the query file
	// then coarse-coarse search, decompress both, fine-fine search.
	// otherwise, just coarse search, decompress results, fine search.
	// iterate over the query sequences in the input fasta
	// initially, only implement standard search.

	if flagCompressQuery {

		processCompressedQueries(db, queryDBConf, inputFastaQueryName, searchBuf)

	} else {

		queryBuf := new(bytes.Buffer) // might need more than 1 buffer
		inputFastaQuery, err := getInputFasta(inputFastaQueryName)
		handleFatalError("Could not read input fasta query", err)

		f := fasta.NewWriter(queryBuf)
		reader := fasta.NewReader(inputFastaQuery)

		for i := 0; true; i++ {
      
      if flagIterativeQuery {

        for j := 0; j < flagQueryChunkSize; j++ {
    			translateQueries(reader, f)
        }
				
				transQueries := bytes.NewReader(queryBuf.Bytes())
				processQueries(db, transQueries, searchBuf)
				queryBuf.Reset()
      
      } else {
        translateQueries(reader, f)
      }
			
		}

		if !flagIterativeQuery {
			cablastp.Vprintln("\nProcessing Queries in one batch...")
			f.Flush()
			transQueries := bytes.NewReader(queryBuf.Bytes())
			processQueries(db, transQueries, searchBuf)
		}
	}

	cleanup(db)
}

func translateQueries(reader *fasta.Reader, f *fasta.Writer) error {
  
	sequence, err := reader.Read()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		fatalf("Could not read input fasta query: %s\n", err)
	}

	origSeq := sequence.Bytes()
	n := sequence.Name
	// generate 6 ORFs
	transSeqs := cablastp.Translate(origSeq)

	for _, s := range transSeqs {
		// reduce each one
		result := seq.NewSequenceString(n, string(cablastp.Reduce(s)))

		f.Write(result)

	}
  f.Flush()
  return nil
}

func processQueries(
	db *cablastp.DB, transQueries *bytes.Reader, searchBuf *bytes.Buffer) error {
	// now we will read from queryBuf!
	// I think we create a NewReader from queryBuf?
	// this now needs to become the replacement for inputFastaQuery
	// so must use a different buffer for that.
	// we need a buffer for the query trans/reduce
	// and a buffer for coarse blast results

	cablastp.Vprintln("\nBlasting query on coarse database...")
	err := blastCoarse(db, transQueries, searchBuf)
	handleFatalError("Error blasting coarse database", err)

	cablastp.Vprintln("Decompressing blast hits...")
	expandedSequences, err := expandBlastHits(db, searchBuf)
	handleFatalError("Error decompressing blast hits", err)
  if len(expandedSequences) == 0 {
    cablastp.Vprintln("No results from coarse search")
  } else {
  
  	// Write the contents of the expanded sequences to a fasta file.
  	// It is then indexed using makeblastdb.
  	searchBuf.Reset()
  	err = writeFasta(expandedSequences, searchBuf)
  	handleFatalError("Could not create FASTA input from coarse hits", err)

  	// Create the fine blast db in a temporary directory
  	cablastp.Vprintln("Building fine BLAST database...")
  	tmpDir, err := makeFineBlastDB(db, searchBuf)
  	handleFatalError("Could not create fine database to search on", err)

  	// retrieve the cluster members for the original representative query seq

  	// pass them to blastx on the expanded (fine) db

  	// Finally, run the query against the fine fasta database and pass on the
  	// stdout and stderr...
  	cablastp.Vprintln("Blasting query on fine database...")
  	_, err = transQueries.Seek(0, 0) // First 0 is amount to offset, Second 0 
                                     // is code for absolute
  	handleFatalError("Could not seek to start of query fasta input", err)

  	err = blastFine(db, tmpDir, transQueries)
  	handleFatalError("Error blasting fine database", err)
  	// Delete the temporary fine database.
  	if !flagNoCleanup {
  		err := os.RemoveAll(tmpDir)
  		handleFatalError("Could not delete fine BLAST database", err)
  	}
  }
	return nil
}

func processCompressedQueries(db *cablastp.DB, queryDBConf *cablastp.DBConf, inputQueryFilename string, searchBuf *bytes.Buffer) error {
	cablastp.Vprintln("Compressing queries into a database...")
	dbDirLoc, err := ioutil.TempDir("", "cablastp-tmp-query-db")
	if err != nil {
		return fmt.Errorf("Could not create temporary directory: %s\n", err)
	}
	qDBDirLoc, err := compressQueries(inputQueryFilename, queryDBConf, dbDirLoc)
	handleFatalError("Error compressing queries", err)
	cablastp.Vprintln("Opening DB for reading")
	qDB, err := cablastp.NewReadDB(qDBDirLoc)
	handleFatalError("Error opening query database", err)
	cablastp.Vprintln("Opening compressed queries for search...")
	compQueryFilename := qDB.CoarseFastaLocation()
	compQueries, err := getInputFasta(compQueryFilename)
	handleFatalError("Error opening compressed query file", err)

	queryBuf := new(bytes.Buffer)
	f := fasta.NewWriter(queryBuf)
	reader := fasta.NewReader(compQueries)

	for origSeqID := 0; true; origSeqID++ {

		sequence, err := reader.Read()
		if err == io.EOF {
			break
		}

		if err != nil {
			fatalf("Could not read input fasta query: %s\n", err)
		}

		origSeq := sequence.Bytes()
		n := sequence.Name
		// generate 6 ORFs
		transSeqs := cablastp.Translate(origSeq)
		for _, s := range transSeqs {
			// reduce each one
			result := seq.NewSequenceString(n, string(cablastp.Reduce(s)))

			f.Write(result)

		}

		f.Flush()
		transCoarseQueries := bytes.NewReader(queryBuf.Bytes())

		cablastp.Vprintln("\nBlasting query on coarse database...")
		err = blastCoarse(db, transCoarseQueries, searchBuf)
		handleFatalError("Error blasting coarse database", err)

		cablastp.Vprintln("Decompressing coarse blast hits...")
		expandedSequences, err := expandBlastHits(db, searchBuf)
		handleFatalError("Error decompressing coarse blast hits", err)
    if len(expandedSequences) == 0 {
      cablastp.Vprintln("No results from coarse search")
    } else {
  		cablastp.Vprintln("Making FASTA from coarse blast hits...")
  		searchBuf.Reset()
  		err = writeFasta(expandedSequences, searchBuf)
  		handleFatalError("Could not create FASTA input from coarse hits", err)

  		cablastp.Vprintln("Expanding coarse query...")
  		expQuery, err := expandCoarseSequence(qDB, origSeqID, &sequence)
  		handleFatalError("Could not expand coarse queries", err)

  		fineQueryBuf := new(bytes.Buffer)
  		fineWriter := fasta.NewWriter(fineQueryBuf)
  		for _, fineQuery := range expQuery {
  			fineQueryBytes := fineQuery.FastaSeq().Bytes() // <- Is This the same as fineQuery.Residues()?
  			fineName := fineQuery.Name
  			writeSeq := seq.NewSequenceString(fineName, string(fineQueryBytes))
  			fineWriter.Write(writeSeq)
  		}
  		fineWriter.Flush()
  		transFineQueries := bytes.NewReader(fineQueryBuf.Bytes())

  		cablastp.Vprintln("Building fine BLAST target database...")
  		targetTmpDir, err := makeFineBlastDB(db, searchBuf)
  		handleFatalError("Could not create fine database to search on", err)

  		cablastp.Vprintln("Blasting original query on fine database...")
  		err = blastFine(db, targetTmpDir, transFineQueries)
  		handleFatalError("Error blasting fine database", err)
    	if !flagNoCleanup {
    		err := os.RemoveAll(targetTmpDir)
    		handleFatalError("Could not delete fine database", err)
    	}
    }
		queryBuf.Reset()
	}
	cablastp.Vprintln("Cleaning up...")
	if !flagNoCleanup {
		err = os.RemoveAll(dbDirLoc)
		handleFatalError("Could not delete fine database", err)
	}
	return nil
}

func s(i int) string {
	return fmt.Sprintf("%d", i)
}

func su(i uint64) string {
	return fmt.Sprintf("%d", i)
}

func sf(f float64) string {
  return fmt.Sprintf("%.2f", f)
}

func compressQueries(queryFileName string, queryDBConf *cablastp.DBConf, dbDirLoc string) (string, error) {
	cablastp.Vprintln("")

	db, err := cablastp.NewWriteDB(queryDBConf, dbDirLoc)
	handleFatalError("Failed to open new db", err)
	pool := cablastp.StartCompressReducedWorkers(db)
	seqId := db.ComDB.NumSequences()
	mainQuit := make(chan struct{}, 0)

	seqChan, err := cablastp.ReadOriginalSeqs(queryFileName, []byte{})
	handleFatalError("Could not read query sequences", err)

	for readSeq := range seqChan {
		// Do a non-blocking receive to see if main needs to quit.
		select {
		case <-mainQuit:
			<-mainQuit // wait for cleanup to finish before exiting main.
			return "", nil
		default:
		}

		handleFatalError("Failed to read sequence", readSeq.Err)

		queryDBConf.BlastDBSize += uint64(readSeq.Seq.Len())
		redReadSeq := &cablastp.ReducedSeq{
			&cablastp.Sequence{
				Name:     readSeq.Seq.Name,
				Residues: readSeq.Seq.Residues,
				Offset:   readSeq.Seq.Offset,
				Id:       readSeq.Seq.Id,
			},
		}
		seqId = pool.CompressReduced(seqId, redReadSeq)
	}
	cablastp.CleanupDB(db, &pool)
	cablastp.Vprintln("")
	return dbDirLoc, nil
}

// queryDBName, err := ioutil.TempDir(".", "tmp_compressed_"+queryFileName)
// handleFatalError("Failed to create temporary directory for database", err)
// queryDBName := "tmp_compressed_" + queryFileName
// flags := []string{
// 	"--map-seed-size=10",
// 	queryDBName,
// 	queryFileName,
// }
// cmd := exec.Command("cablastp-compress", flags...)
// cmd.Stdout = os.Stdout
// cmd.Stderr = os.Stderr
// err := cablastp.Exec(cmd)
// handleFatalError("Error while compressing database", err)

// db, err := cablastp.NewReadDB(queryDBName)
// if err != nil {
// 	return nil, err
// }

// return db, nil

func blastFine(
	db *cablastp.DB, blastFineDir string, stdin *bytes.Reader) error {

	// We pass our own "-db" flag to blastp, but the rest come from user
	// defined flags.
	flags := []string{
		"-db", path.Join(blastFineDir, cablastp.FileBlastFine),
		"-dbsize", su(db.BlastDBSize),
		"-num_threads", s(flagGoMaxProcs),
	}
	flags = append(flags, blastArgs...)

	cmd := exec.Command(flagBlastx, flags...)
	cmd.Stdin = stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cablastp.Exec(cmd)
}

func makeFineBlastDB(db *cablastp.DB, stdin *bytes.Buffer) (string, error) {
	tmpDir, err := ioutil.TempDir("", "cablastp-fine-search-db")
	if err != nil {
		return "", fmt.Errorf("Could not create temporary directory: %s\n", err)
	}

	cmd := exec.Command(
		flagMakeBlastDB, "-dbtype", "prot",
		"-title", cablastp.FileBlastFine,
		"-in", "-",
		"-out", path.Join(tmpDir, cablastp.FileBlastFine))
	cmd.Stdin = stdin

	cablastp.Vprintf("Created temporary fine BLAST database in %s\n", tmpDir)

	return tmpDir, cablastp.Exec(cmd)
}

func writeFasta(oseqs []cablastp.OriginalSeq, buf *bytes.Buffer) error {
	for _, oseq := range oseqs {
		_, err := fmt.Fprintf(buf, "> %s\n%s\n",
			oseq.Name, string(oseq.Residues))
		if err != nil {
			return fmt.Errorf("Could not write to buffer: %s", err)
		}
	}
	return nil
}

func expandBlastHits(
	db *cablastp.DB, blastOut *bytes.Buffer) ([]cablastp.OriginalSeq, error) {

	results := blast{}
	if err := xml.NewDecoder(blastOut).Decode(&results); err != nil {
		return nil, fmt.Errorf("Could not parse BLAST search results: %s", err)
	}

	used := make(map[int]bool, 100) // prevent original sequence duplicates
	oseqs := make([]cablastp.OriginalSeq, 0, 100)
	for _, hit := range results.Hits {

		for _, hsp := range hit.Hsps {
			someOseqs, err := db.CoarseDB.Expand(db.ComDB,
				hit.Accession, hsp.HitFrom, hsp.HitTo)
			if err != nil {
				errorf("Could not decompress coarse sequence %d (%d, %d): %s\n",
					hit.Accession, hsp.HitFrom, hsp.HitTo, err)
				continue
			}

			// Make sure this hit is above the coarse bit score threshold.
			if hsp.BitScore < flagCoarseBitScore {
				continue
			}

			for _, oseq := range someOseqs {
				if used[oseq.Id] {
					continue
				}
				used[oseq.Id] = true
				oseqs = append(oseqs, oseq)
			}
		}
	}
	// if len(oseqs) == 0 {
	// 	return nil, fmt.Errorf("No hits from coarse search\n")
	// }
	return oseqs, nil
}

func expandCoarseSequence(db *cablastp.DB, seqId int, coarseSequence *seq.Sequence) ([]cablastp.OriginalSeq, error) {
	originalSeqs, err := db.CoarseDB.Expand(db.ComDB, seqId, 0, coarseSequence.Len())
	if err != nil {
		return nil, err
	}
	// var redSeqs [originalSeqs]cablastp.ReducedSeq
	// for _, oSeq := range originalSeqs {
	// 	redSeq := &cablastp.ReducedSeq{
	// 		&cablastp.Sequence{
	// 			Name:     readSeq.Seq.Name,
	// 			Residues: readSeq.Seq.Residues,
	// 			Offset:   readSeq.Seq.Offset,
	// 			Id:       readSeq.Seq.Id,
	// 		},
	// 	}
	// }

	return originalSeqs, nil
}

func blastCoarse(
	db *cablastp.DB, stdin *bytes.Reader, stdout *bytes.Buffer) error {
  var cmd *exec.Cmd

  if flagShortQueries {
  	cmd = exec.Command(
  		flagBlastn,
  		"-db", path.Join(db.Path, cablastp.FileBlastCoarse),
  		"-num_threads", s(flagGoMaxProcs),
      "-max_target_seqs", "100000",
      "-task", "blastn-short", "-evalue", sf(flagCoarseEval), "-penalty", "-1",
  		"-outfmt", "5", "-dbsize", su(db.BlastDBSize))
  } else {
  	cmd = exec.Command(
  		flagBlastn,
  		"-db", path.Join(db.Path, cablastp.FileBlastCoarse),
  		"-num_threads", s(flagGoMaxProcs),
      "-max_target_seqs", "100000",
      "-evalue", sf(flagCoarseEval),
  		"-outfmt", "5", "-dbsize", su(db.BlastDBSize))
  }
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	return cablastp.Exec(cmd)
}

func getInputFasta(inputFilename string) (*bytes.Reader, error) {
	queryFasta, err := os.Open(inputFilename)
	if err != nil {
		return nil, fmt.Errorf("Could not open '%s': %s.", flag.Arg(1), err)
	}
	bs, err := ioutil.ReadAll(queryFasta)
	if err != nil {
		return nil, fmt.Errorf("Could not read input fasta query: %s", err)
	}
	return bytes.NewReader(bs), nil
}

func cleanup(db *cablastp.DB) {
	if len(flagCpuProfile) > 0 {
		pprof.StopCPUProfile()
	}
	if len(flagMemProfile) > 0 {
		writeMemProfile(fmt.Sprintf("%s.last", flagMemProfile))
	}
	db.ReadClose()
}

func fatalf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, format, v...)
	os.Exit(1)
}

func errorf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, format, v...)
}

func writeMemProfile(name string) {
	f, err := os.Create(name)
	if err != nil {
		fatalf("%s\n", err)
	}
	pprof.WriteHeapProfile(f)
	f.Close()
}

func usage() {
	fmt.Fprintf(os.Stderr,
		"\nUsage: %s [flags] database-directory query-fasta-file "+
			"[--blast-args BLASTP_ARGUMENTS]\n",
		path.Base(os.Args[0]))
	cablastp.PrintFlagDefaults()
	os.Exit(1)
}

func handleFatalError(msg string, err error) error {
	if err != nil {
		fatalf(msg+": %s\n", err)
	}
	return nil
}
