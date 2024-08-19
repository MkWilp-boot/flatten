package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
)

var (
	outputDirectory = flag.String("x", "output", "output directory")
	namePrefix      = flag.String("prefix", "", "prefix all entries with the provided value")
	maxNumCores     = flag.Int("c", runtime.NumCPU(), "set's the maximum number of cores for use")
	timeExecution   = flag.Bool("time", false, "time program execution")
	helpFlag        = flag.Bool("h", false, "display available flags and usage")

	semaphore chan struct{}
)

var pathReplacer = regexp.MustCompile(`[\\\/]`)

func init() {
	flag.Parse()

	if *helpFlag {
		flag.PrintDefaults()
		os.Exit(0)
	}

	semaphore = make(chan struct{}, *maxNumCores)

	if *namePrefix != "" && !strings.HasSuffix(*namePrefix, "_") {
		*namePrefix += "_"
	}

	totalCoresAvailable := runtime.GOMAXPROCS(*maxNumCores)
	log.Printf("[INFO] Using '%d' cores for processing, maximum available is '%d'\n", *maxNumCores, totalCoresAvailable)
}

func main() {
	if *timeExecution {
		timeNow := time.Now()
		log.Println("[INFO] Requested timed execution")

		defer func(timeNow time.Time) {
			log.Printf("\n[INFO] finished execution, time elapsed: %.2fs\n", time.Since(timeNow).Seconds())
		}(timeNow)
	}

	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	entries, err := os.ReadDir(wd)
	if err != nil {
		log.Fatal(err)
	}

	// since we're on the root folder, pass "" as it's parent path
	totalItems := scoutDirectory(&entries, "")
	log.Printf("[INFO] Found: '%d' nested items to copy\n", totalItems)

	outputDirEntry, err := os.Stat(*outputDirectory)
	if outputDirEntry != nil && err != nil {
		log.Fatal(err)
	}

	/*
		https://stackoverflow.com/questions/14249467/os-mkdir-and-os-mkdirall-permissions
		Hope you don't mind @Shannon Matthews
		+-----+---+--------------------------+
		| rwx | 7 | Read, write and execute  |
		| rw- | 6 | Read, write              |
		| r-x | 5 | Read, and execute        |
		| r-- | 4 | Read,                    |
		| -wx | 3 | Write and execute        |
		| -w- | 2 | Write                    |
		| --x | 1 | Execute                  |
		| --- | 0 | no permissions           |
		+------------------------------------+

		+------------+------+-------+
		| Permission | Octal| Field |
		+------------+------+-------+
		| rwx------  | 0700 | User  |
		| ---rwx---  | 0070 | Group |
		| ------rwx  | 0007 | Other |
		+------------+------+-------+
	*/
	if outputDirEntry == nil {
		err = os.Mkdir(*outputDirectory, 0666)
		if err != nil {
			log.Println(err)
			os.Exit(1)
		}
	}

	bar := progressbar.Default(int64(totalItems))

	var wg sync.WaitGroup
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != *outputDirectory {
			wg.Add(1)
			go expandDirectory(bar, &wg, entry.Name())
		}
	}
	wg.Wait()
}

func scoutDirectory(dir *[]fs.DirEntry, parentPath string) (total uint) {
	total = 0
	for i := 0; i < len(*dir); i++ {
		currentDirEntryName := filepath.Join(parentPath, (*dir)[i].Name())
		if currentDirEntryName == *outputDirectory {
			continue
		}
		dirs, err := os.ReadDir(currentDirEntryName)
		if err != nil {
			log.Printf("[ERROR] Could not read entry %q, skipping...\n", currentDirEntryName)
			continue
		}

		dirsOnly := make([]fs.DirEntry, 0, len(dirs))

		for _, entry := range dirs {
			if entry.IsDir() {
				dirsOnly = append(dirsOnly, entry)
			} else {
				total++
			}
		}

		total += scoutDirectory(&dirsOnly, currentDirEntryName)
	}
	return
}

func expandDirectory(bar *progressbar.ProgressBar, wg *sync.WaitGroup, dirName string) {
	defer wg.Done()

	dirEntries, err := os.ReadDir(dirName)
	if err != nil {
		log.Println(err)
		return
	}

	for _, entry := range dirEntries {
		if entry.IsDir() {
			wg.Add(1)
			go expandDirectory(bar, wg, filepath.Join(dirName, entry.Name()))
		} else {
			wg.Add(1)
			go copyFilesFromSource(bar, wg, dirName, entry.Name())
		}
	}
}

func copyFilesFromSource(bar *progressbar.ProgressBar, wg *sync.WaitGroup, fullPath, copyingFileName string) {
	defer wg.Done()
	defer bar.Add(1)

	// Acquire a "slot" in the semaphore
	semaphore <- struct{}{}
	defer func() { <-semaphore }() // Release the "slot" when done

	destName := filepath.Join(*outputDirectory, fmt.Sprintf("%s%s_%s", *namePrefix, pathReplacer.ReplaceAllString(fullPath, "_"), copyingFileName))
	destFile, err := os.Create(destName)
	if err != nil {
		log.Println(err)
		return
	}
	defer destFile.Close()

	srcFile, err := os.Open(filepath.Join(fullPath, copyingFileName))
	if err != nil {
		log.Println(err)
		return
	}
	defer srcFile.Close()

	if _, err := io.Copy(destFile, srcFile); err != nil {
		log.Printf("Error copying file %s: %v\n", copyingFileName, err)
	}
}
