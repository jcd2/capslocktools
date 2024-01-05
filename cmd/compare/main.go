// Copyright 2024 Google LLC
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd

// Compare uses the `-output=compare` option of Capslock to compare the
// capabilities of two versions of a package.  This requires Capslock to be
// installed:
//
//	go install github.com/google/capslock/cmd/capslock@latest
//
// The user does not need to download the source with `go get` first;
// compare creates a temporary workspace for each version and gets the source
// itself.
//
// If the environment variable CAPSLOCKTOOLSTMPDIR is set and non-empty, it
// specifies the directory where temporary files are created.  Otherwise the
// system temporary directory is used.
//
// Usage examples:
//
//	compare some.package/name/foo v1.1 v1.2
//	compare some.package/name/... v1.1 v1.2
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
)

var verbose = flag.Bool("v", false, "enable verbose logging")

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

func MakeWorkspace(pkgname string) error {
	tmpdir, err := os.MkdirTemp(os.Getenv("CAPSLOCKTOOLSTMPDIR"), "")
	if err != nil {
		return fmt.Errorf("creating temporary directory: %w", err)
	}
	if err = os.Chdir(tmpdir); err != nil {
		return fmt.Errorf("switching to temporary directory: %w", err)
	}
	if err = run(nil, "go", "mod", "init", "capslockworkspace"); err != nil {
		return err
	}
	if err := run(nil, "go", "get", pkgname); err != nil {
		return err
	}
	return nil
}

func CreateCapabilitiesFile(pkgname string) (filename string, err error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	f, err := os.Create("capslock.json")
	if err != nil {
		return "", fmt.Errorf("creating temporary file for writing capabilities: %w", err)
	}
	filename = path.Join(dir, f.Name())
	if err = run(f, "capslock", "-packages="+pkgname, "-output=json"); err != nil {
		return filename, err
	}
	return filename, f.Close()
}

func ComparePackages(pkgname, version1, version2 string) (ranComparison bool, err error) {
	create := func(pkg string) error {
		vlog("Creating workspace for %q", pkg)
		if err := MakeWorkspace(pkg); err != nil {
			return fmt.Errorf("creating temporary workspace for analyzing %q: %w", pkg, err)
		}
		return nil
	}
	v1, v2 := pkgname+"@"+version1, pkgname+"@"+version2
	if err := create(v1); err != nil {
		return false, err
	}
	filename, err := CreateCapabilitiesFile(pkgname)
	if err != nil {
		return false, err
	}
	if err := create(v2); err != nil {
		return false, err
	}
	return true, run(os.Stdout, "capslock", "-packages="+pkgname, "-output=compare", filename)
}

func main() {
	flag.Parse()
	a := flag.Args()
	if len(a) != 3 {
		panic(fmt.Sprintf("wrong number of arguments: %q", a))
	}
	ranComparison, err := ComparePackages(a[0], a[1], a[2])
	if err != nil {
		var e *exec.ExitError
		if ranComparison && errors.As(err, &e) && e.ProcessState != nil {
			// If `capslock -output=compare` returns a non-zero exit code, this could
			// just indicate it found differences between the versions.  Return with
			// the same exit code instead of doing additional logging.
			os.Exit(e.ExitCode())
		}
		log.Fatal("Error: ", err)
	}
}
