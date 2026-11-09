package linkweight

import (
	"debug/elf"
	"fmt"
	"strings"
)

// Sym is a symbol of the binary and the bytes it occupies in the file; Code whether those are
// machine code, which .gopclntab's metadata is in proportion to.
type Sym struct {
	Name string
	Size uint64
	Code bool
}

// Layout is a binary's symbols and the two sizes no symbol carries: Shipped, every byte loaded
// at run time - the file as -ldflags='-s -w' leaves it, give or take headers - and PCLNTab, the
// runtime's per-function table (.gopclntab), a third of a large Go binary and in no symbol.
type Layout struct {
	Symbols          []Sym
	Shipped, PCLNTab uint64
}

// Unattributed is the bucket for a symbol no package owns by its name: string data, the
// funcdata the runtime reads, the GC's liveness maps.
const Unattributed = "(unattributed)"

// ReadLayout is every symbol of the ELF binary at path that takes room in the file, and its
// shipped and .gopclntab sizes. A section with no file contents (.bss, .noptrbss) counts for
// neither: FIPS's DRBG reserves 32 MB there, which is memory at run time and not a byte of the
// binary.
func ReadLayout(path string) (Layout, error) {
	var l Layout
	f, err := elf.Open(path)
	if err != nil {
		return l, fmt.Errorf("reading %s as ELF (linkweight reads Linux binaries; set GOOS=linux): %w", path, err)
	}
	defer func() { _ = f.Close() }() // read-only
	for _, sec := range f.Sections {
		if sec.Flags&elf.SHF_ALLOC == 0 || sec.Type == elf.SHT_NOBITS {
			continue
		}
		l.Shipped += sec.Size
		if sec.Name == ".gopclntab" {
			l.PCLNTab = sec.Size
		}
	}
	syms, err := f.Symbols()
	if err != nil {
		return l, fmt.Errorf("%s has no symbol table: built with -ldflags=-s? %w", path, err)
	}
	for _, s := range syms {
		if s.Size == 0 || s.Section == elf.SHN_UNDEF || int(s.Section) >= len(f.Sections) {
			continue
		}
		sec := f.Sections[s.Section]
		if sec.Type == elf.SHT_NOBITS {
			continue
		}
		l.Symbols = append(l.Symbols, Sym{Name: s.Name, Size: s.Size, Code: sec.Flags&elf.SHF_EXECINSTR != 0})
	}
	return l, nil
}

// PackageOf is the import path of the package a linker symbol belongs to, among pkgs (the
// binary's packages, by import path); Unattributed when its name says no package. main is the
// linker's name for the main package, whose import path is mainPath.
//
// A symbol's name is its package's path, a dot, and the rest, but a path's last element may hold
// dots of its own (gopkg.in/yaml.v3), so the longest prefix ending at a dot that is a package of
// the binary is the package - checked against pkgs and not guessed from the shape.
func PackageOf(sym string, pkgs map[string]bool, mainPath string) string {
	name := sym
	for _, p := range []string{"type:.eq.", "type:.namedata.", "type:", "go:itab.", "go:link."} {
		if rest, ok := strings.CutPrefix(name, p); ok {
			name = rest
			break
		}
	}
	name = strings.TrimLeft(name, "*")
	// A generic's type arguments and a method's receiver come after the package; either may hold
	// a slash or a dot of another package.
	if i := strings.IndexAny(name, "[("); i >= 0 { // mutate-exempt: no linker name starts with ( or [, so i is never 0
		name = name[:i]
	}
	if name == "main" || strings.HasPrefix(name, "main.") {
		return mainPath
	}
	slash := strings.LastIndex(name, "/")
	best := ""
	for i := slash + 1; i < len(name); i++ { // mutate-exempt: a dot right after the slash would end an empty path element, which no package has
		if name[i] == '.' && pkgs[name[:i]] {
			best = name[:i]
		}
	}
	if best == "" {
		return Unattributed
	}
	return best
}
