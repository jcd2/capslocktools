// Copyright 2024 Google LLC
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd

// Compares packages at two revisions of a git repository.
//
// Usage example:
//
//	capslock-git-diff main mybranch somepath/...
//
// This requires Capslock to be installed:
//
//	go install github.com/google/capslock/cmd/capslock@latest
//
// To compare against the current state of the repository, specify "." as a
// revision:
//
//	capslock-git-diff main . somepath/...
//
// If the environment variable CAPSLOCKTOOLSTMPDIR is set and non-empty, it
// specifies the directory where temporary files are created.  Otherwise the
// system temporary directory is used.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	cpb "github.com/google/capslock/proto"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	verbose     = flag.Bool("v", false, "enable verbose logging")
	granularity = flag.String("granularity", "", "the granularity to use for comparisons")
)

func vlog(format string, a ...any) {
	if !*verbose {
		return
	}
	log.Printf(format, a...)
}

// run executes the specified command and writes its stdout to w.
func run(w io.Writer, command string, args ...string) error {
	vlog("running %q with args %q", command, args)
	cmd := exec.Command(command, args...)
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %q with args %q: %w", command, args, err)
	}
	return nil
}

func AnalyzeAtRevision(rev, pkgname string) (cil *cpb.CapabilityInfoList, err error) {
	if rev == "." {
		return callCapslock(rev, pkgname)
	}
	// Make a temporary directory.
	tmpdir, err := os.MkdirTemp(os.Getenv("CAPSLOCKTOOLSTMPDIR"), "")
	if err != nil {
		return nil, fmt.Errorf("creating temporary directory: %w", err)
	}
	// Get the location of the .git directory, so we can make a temporary clone.
	var b bytes.Buffer
	if err = run(&b, "git", "rev-parse", "--git-dir"); err != nil {
		return nil, err
	}
	gitdir := strings.TrimSuffix(b.String(), "\n")
	b.Reset()
	// Get the relative directory within the git repository.
	if err = run(&b, "git", "rev-parse", "--show-prefix"); err != nil {
		return nil, err
	}
	prefix := strings.TrimSuffix(b.String(), "\n")
	b.Reset()
	// Clone the repo.
	if err = run(nil, "git", "clone", "--shared", "--no-checkout", "--", gitdir, tmpdir); err != nil {
		return nil, err
	}
	// Temporarily switch directory.
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	defer func() {
		// Switch back to the original directory.
		err1 := os.Chdir(wd)
		if err == nil && err1 != nil {
			err = fmt.Errorf("returning to working directory: %w", err1)
		}
	}()
	if err = os.Chdir(tmpdir); err != nil {
		return nil, fmt.Errorf("switching to temporary directory: %w", err)
	}
	// Reset to the requested revision.
	if err = run(nil, "git", "reset", "--hard", rev); err != nil {
		return nil, err
	}
	// Go to the same directory in the clone.
	if err = os.Chdir(filepath.Join(tmpdir, prefix)); err != nil {
		return nil, fmt.Errorf("switching to temporary directory: %w", err)
	}
	return callCapslock(rev, pkgname)
}

func callCapslock(rev, pkgname string) (cil *cpb.CapabilityInfoList, err error) {
	// Call capslock.
	var b bytes.Buffer
	if err = run(&b, "capslock", "-packages="+pkgname, "-output=json", "-granularity="+*granularity); err != nil {
		return nil, err
	}
	if *verbose {
		vlog("At revision %q: %s", rev, string(b.Bytes()))
	}
	// Unmarshal the output.
	cil = new(cpb.CapabilityInfoList)
	if err = protojson.Unmarshal(b.Bytes(), cil); err != nil {
		return nil, fmt.Errorf("Couldn't parse analyzer output: %w", err)
	}
	return cil, nil
}

func main() {
	flag.Parse()
	a := flag.Args()
	if len(a) != 3 {
		panic(fmt.Sprintf("wrong number of arguments: %q", a))
	}
	cil1, err := AnalyzeAtRevision(a[0], a[2])
	if err != nil {
		log.Print(err)
		os.Exit(2)
	}
	cil2, err := AnalyzeAtRevision(a[1], a[2])
	if err != nil {
		log.Print(err)
		os.Exit(2)
	}
	different := diffCapabilityInfoLists(cil1, cil2)
	if different {
		os.Exit(1)
	}
}

type mapKey struct {
	key        string
	capability cpb.Capability
}
type capabilitiesMap map[mapKey]*cpb.CapabilityInfo

func populateMap(cil *cpb.CapabilityInfoList) capabilitiesMap {
	m := make(capabilitiesMap)
	for _, ci := range cil.GetCapabilityInfo() {
		mk := mapKey{capability: ci.GetCapability(), key: ci.GetPackageDir()}
		m[mk] = ci
	}
	return m
}

func diffCapabilityInfoLists(baseline, current *cpb.CapabilityInfoList) (different bool) {
	baselineMap := populateMap(baseline)
	currentMap := populateMap(current)
	var keys []mapKey
	for k := range baselineMap {
		keys = append(keys, k)
	}
	for k := range currentMap {
		if _, ok := baselineMap[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if a, b := keys[i].capability, keys[j].capability; a != b {
			return a < b
		}
		return keys[i].key < keys[j].key
	})
	for _, key := range keys {
		ciBaseline, inBaseline := baselineMap[key]
		ciCurrent, inCurrent := currentMap[key]
		if !inBaseline && inCurrent {
			if different {
				fmt.Println()
			}
			different = true
			fmt.Printf("> Package %s has capability %s:\n", key.key, key.capability)
			printCallPath("> ", ciCurrent.Path)
		}
		if inBaseline && !inCurrent {
			if different {
				fmt.Println()
			}
			different = true
			fmt.Printf("< Package %s has capability %s:\n", key.key, key.capability)
			printCallPath("< ", ciBaseline.Path)
		}
	}
	return different
}

func printCallPath(prefix string, fns []*cpb.Function) {
	tw := tabwriter.NewWriter(
		os.Stdout, // output
		10,        // minwidth
		8,         // tabwidth
		2,         // padding
		' ',       // padchar
		0)         // flags
	for _, f := range fns {
		tw.Write([]byte(prefix))
		if f.Site != nil {
			fmt.Fprint(tw, f.Site.GetFilename(), ":", f.Site.GetLine(), ":", f.Site.GetColumn())
		}
		fmt.Fprint(tw, "\t", f.GetName(), "\n")
	}
	tw.Flush()
}
