// Command sbom writes and checks the CycloneDX SBOMs of what a spin-stack repository ships
// (package sbom). Each kind reads what the build read and writes a BOM whose subject is -name at
// -version; merge nests BOMs under the file they describe; check fails on a component no scanner
// could match. SOURCE_DATE_EPOCH, when set, is the BOM's time.
//
//	go tool sbom go    -name spin-controlplane -version v20261013.1 -out cp.cdx.json bin/controlplane bin/runner
//	go tool sbom apko  -name rootfs -version 20261013 -out rootfs.cdx.json image/ami/apko.lock.json
//	go tool sbom dpkg  -name base -version v1 -distro ubuntu -out base.cdx.json packages.txt
//	go tool sbom apk   -name qemu -version 11.1.2 -distro alpine -only glib-static,musl -out q.cdx.json installed
//	go tool sbom pnpm  -name dashboard -version v1 -out js.cdx.json dashboard/pnpm-lock.yaml
//	go tool sbom pins  -name inputs -version v1 -out pins.cdx.json kernel qemu
//	go tool sbom file  -name bios.bin -version 11.1.2 -out bios.cdx.json bios.bin
//	go tool sbom merge -name f.tar.gz -version v1 -subject f.tar.gz -out f.cdx.json a.cdx.json b.cdx.json
//	go tool sbom check f.cdx.json
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	cdx "github.com/CycloneDX/cyclonedx-go"

	"github.com/spin-stack/go-tools/sbom"
	"github.com/spin-stack/go-tools/versions"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) // mutate-exempt: the process's own arguments, past its name; run is what the tests drive
}

// run answers the exit status: 0 written or whole, 1 a BOM check refuses, 2 no answer.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		say(stderr, "sbom: say go, apko, dpkg, apk, pnpm, pins, file, merge or check")
		return 2
	}
	kind, args := args[0], args[1:]
	if kind == "check" {
		return check(args, stdout, stderr)
	}
	fs := flag.NewFlagSet("sbom "+kind, flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "the BOM's subject")
	version := fs.String("version", "", "the subject's version")
	out := fs.String("out", "", "where to write the BOM")
	distro := fs.String("distro", "", "the distribution the packages are (dpkg, apk)")
	only := fs.String("only", "", "the packages shipped, comma-separated (apk)")
	pins := fs.String("versions", versions.File, "the versions.yaml the pins are in (pins)")
	subjectFile := fs.String("subject", "", "the file the merged BOM describes, by its hash (merge)")
	patches := map[string]string{}
	fs.Func("patches", "<name>=<dir>: the entry was built with dir's *.patch applied, in name order (pins)", func(v string) error {
		n, dir, ok := strings.Cut(v, "=")
		if !ok || n == "" || dir == "" {
			return fmt.Errorf("%q is not <name>=<dir>", v)
		}
		patches[n] = dir
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" || *version == "" || *out == "" {
		say(stderr, "sbom: -name, -version and -out are required")
		return 2
	}
	epoch, err := sbom.Epoch(getenv)
	if err != nil {
		say(stderr, err)
		return 2
	}
	// A subject is known by its name and version, and a merge's by its file's hash too.
	subject := cdx.Component{Type: cdx.ComponentTypeApplication, Name: *name, Version: *version,
		PackageURL: "pkg:generic/" + *name + "@" + *version}
	build, ok := kinds[kind]
	if !ok {
		say(stderr, "sbom: no kind", kind)
		return 2
	}
	b, err := build(request{subject: subject, epoch: epoch, files: fs.Args(), distro: *distro, only: *only, pins: *pins, using: *subjectFile, patches: patches})
	if err != nil {
		say(stderr, err)
		return 2
	}
	if err := sbom.Write(*out, b); err != nil {
		say(stderr, err)
		return 2
	}
	return 0
}

// request is what a run asks a kind for.
type request struct {
	subject                   cdx.Component
	epoch                     time.Time
	files                     []string
	distro, only, pins, using string
	patches                   map[string]string
}

// one is the request's one file, read.
func (r request) one() ([]byte, error) {
	if len(r.files) != 1 {
		return nil, errors.New("sbom: this kind reads one file")
	}
	return os.ReadFile(r.files[0])
}

// of is the BOM of the request's subject made of components, built with formulation.
func (r request) of(components, formulation []cdx.Component, err error) (*cdx.BOM, error) {
	if err != nil {
		return nil, err
	}
	return sbom.New(r.subject, components, formulation, r.epoch), nil
}

// kinds are what each kind makes of a request.
var kinds = map[string]func(r request) (*cdx.BOM, error){
	"go": func(r request) (*cdx.BOM, error) {
		var cs []cdx.Component
		for _, f := range r.files {
			c, err := sbom.Go(r.subject.Version, f)
			if err != nil {
				return nil, err
			}
			cs = append(cs, c)
		}
		return r.of(cs, nil, nil)
	},
	"apko": func(r request) (*cdx.BOM, error) {
		raw, err := r.one()
		if err != nil {
			return nil, err
		}
		cs, err := sbom.Apko(raw)
		return r.of(cs, nil, err)
	},
	"dpkg": func(r request) (*cdx.BOM, error) {
		raw, err := r.one()
		if err != nil {
			return nil, err
		}
		if r.distro == "" {
			return nil, errors.New("sbom: dpkg needs -distro")
		}
		cs, err := sbom.Dpkg(raw, r.distro)
		return r.of(cs, nil, err)
	},
	"apk": func(r request) (*cdx.BOM, error) {
		raw, err := r.one()
		if err != nil {
			return nil, err
		}
		if r.distro == "" || r.only == "" {
			return nil, errors.New("sbom: apk needs -distro, and -only: what is shipped, as against what built it")
		}
		return r.of(sbom.Apk(raw, r.distro, strings.Split(r.only, ",")))
	},
	"pnpm": func(r request) (*cdx.BOM, error) {
		raw, err := r.one()
		if err != nil {
			return nil, err
		}
		cs, err := sbom.Pnpm(raw)
		return r.of(cs, nil, err)
	},
	"pins": func(r request) (*cdx.BOM, error) {
		v, err := versions.Load(r.pins)
		if err != nil {
			return nil, err
		}
		cs, err := sbom.Pins(v, r.files...)
		if err != nil {
			return nil, err
		}
		for i, c := range cs {
			dir, ok := r.patches[c.Name]
			if !ok {
				continue
			}
			found, err := filepath.Glob(filepath.Join(dir, "*.patch"))
			if err != nil || len(found) == 0 {
				return nil, fmt.Errorf("sbom: %s has no *.patch in %s", c.Name, dir)
			}
			slices.Sort(found)
			if cs[i], err = sbom.Patched(c, found...); err != nil {
				return nil, err
			}
		}
		return r.of(cs, nil, nil)
	},
	"file": func(r request) (*cdx.BOM, error) {
		if len(r.files) != 1 {
			return nil, errors.New("sbom: file reads one file")
		}
		c, err := sbom.File(r.subject.Name, r.subject.Version, r.files[0])
		if err != nil {
			return nil, err
		}
		return sbom.New(c, nil, nil, r.epoch), nil
	},
	"merge": func(r request) (*cdx.BOM, error) {
		if r.using == "" {
			return nil, errors.New("sbom: merge needs -subject: the file the BOM describes")
		}
		f, err := sbom.File(r.subject.Name, r.subject.Version, r.using)
		if err != nil {
			return nil, err
		}
		r.subject.Hashes = f.Hashes
		var boms []*cdx.BOM
		for _, p := range r.files {
			b, err := sbom.Read(p)
			if err != nil {
				return nil, err
			}
			boms = append(boms, b)
		}
		return sbom.Merge(r.subject, r.epoch, boms...)
	},
}

// check is each BOM read and held to sbom.Check.
func check(files []string, stdout, stderr io.Writer) int {
	if len(files) == 0 {
		say(stderr, "sbom: check takes the BOMs to check")
		return 2
	}
	status := 0
	for _, f := range files {
		b, err := sbom.Read(f)
		if err != nil {
			say(stderr, err)
			return 2
		}
		if err := sbom.Check(b); err != nil {
			say(stdout, f+":", err)
			status = 1
		}
	}
	if status == 0 { // mutate-exempt: whether the line says OK; the exit status is the answer
		say(stdout, "sbom: OK - every component has a version and a package URL or a hash")
	}
	return status
}

// say writes one line. A line that cannot be written changes no exit status, which is the answer.
func say(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
