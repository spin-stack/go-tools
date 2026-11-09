// Package linkweight reads what a Go binary links and why: the linker's own reachability graph
// (`go build -ldflags=-dumpdep`) and the binary's symbol table. From those it answers how many
// bytes each module puts in the binary, whether the linker could drop unused methods, which import
// a refactor would cut to drop the most, and what the binary can reach - exec, listen, mount.
//
// The linker's graph and not a static call graph: it is what the binary has, after inlining,
// build tags and the linker's own dead-code pass, so a symbol in it is linked and one absent is not.
package linkweight

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Graph is the linker's reachability graph: an edge from a symbol to each symbol it keeps alive.
// Every symbol in it is linked; the linker prints only what its dead-code pass marked.
type Graph struct {
	names []string
	ids   map[string]int
	succ  [][]int
	pred  [][]int
	// reflectMethod are the symbols the linker marked <ReflectMethod>: a call to
	// reflect.Value.Method or MethodByName with a name it cannot see, which makes it keep every
	// exported method of every reachable type.
	reflectMethod map[int]bool
}

// ParseDeps reads -dumpdep's output: `from -> to` a line, either side followed by a marker such as
// <ReflectMethod> or <UsedInIface>. Lines that are not edges - what go build itself says - are not
// part of the graph and are skipped.
func ParseDeps(r io.Reader) (*Graph, error) {
	g := &Graph{ids: map[string]int{}, reflectMethod: map[int]bool{}}
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for s.Scan() {
		from, to, ok := strings.Cut(s.Text(), " -> ")
		if !ok {
			continue
		}
		from, fromRM := unmark(from)
		to, toRM := unmark(to)
		// No source is the linker's method pass keeping a method of a reachable type, which says
		// no symbol called it; Chain goes through the receiver's type instead. Its roots are "_".
		if from == "" {
			g.id(to)
			if toRM {
				g.reflectMethod[g.ids[to]] = true
			}
			continue
		}
		a, b := g.id(from), g.id(to)
		g.succ[a] = append(g.succ[a], b)
		g.pred[b] = append(g.pred[b], a)
		if fromRM {
			g.reflectMethod[a] = true
		}
		if toRM {
			g.reflectMethod[b] = true
		}
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("reading the linker's graph: %w", err)
	}
	if len(g.names) == 0 {
		return nil, fmt.Errorf("the linker's graph is empty: was the binary built with -ldflags=-dumpdep?")
	}
	return g, nil
}

// unmark is a symbol without the linker's marker, and whether the marker was <ReflectMethod>.
// Only the markers the linker writes are taken off: a string symbol may end in something that
// looks like one (go:string."<nil>").
func unmark(s string) (string, bool) {
	for _, m := range []string{" <ReflectMethod>", " <UsedInIface>"} {
		if before, ok := strings.CutSuffix(s, m); ok {
			return before, m == " <ReflectMethod>"
		}
	}
	return s, false
}

func (g *Graph) id(name string) int {
	if i, ok := g.ids[name]; ok {
		return i
	}
	i := len(g.names)
	g.names = append(g.names, name)
	g.ids[name] = i
	g.succ = append(g.succ, nil)
	g.pred = append(g.pred, nil)
	return i
}

// Has is whether the binary links name.
func (g *Graph) Has(name string) bool {
	_, ok := g.ids[name]
	return ok
}

// Symbols is every symbol the graph names, in the order first seen.
func (g *Graph) Symbols() []string { return g.names }

// ReflectMethod is every linked symbol the linker marked <ReflectMethod>.
func (g *Graph) ReflectMethod() []string {
	var out []string
	for i, n := range g.names {
		if g.reflectMethod[i] {
			out = append(out, n)
		}
	}
	return out
}

// Chain is a shortest path the linker followed to name, from its root to name itself; nil when
// name is not linked. viaMethod is whether the path enters a method through its receiver's type
// rather than through a call: the linker kept that method because the type is reachable, either
// for an interface call or because method elimination was already off, and the graph does not
// say which.
func (g *Graph) Chain(name string) (chain []string, viaMethod bool) {
	target, ok := g.ids[name]
	if !ok {
		return nil, false
	}
	// Backwards from the target: the first symbol reached with no predecessor is a root, and the
	// path found is a shortest one to it.
	next := map[int]int{target: -1}
	hop := map[int]bool{}
	queue := []int{target}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		preds := g.pred[n]
		typeHop := false
		if len(preds) == 0 {
			// A method nothing calls by name has no edge into it: the linker kept it because its
			// receiver's type is reachable. The type is the way in, and it has a chain of its own.
			if t, ok := g.ids[receiverType(g.names[n])]; ok {
				preds, typeHop = []int{t}, true
			}
		}
		if len(preds) == 0 {
			for c := n; c != -1; c = next[c] {
				chain = append(chain, g.names[c])
				// A type keeping a method alive, printed by the linker or inferred above: the
				// method is linked because the type is.
				viaMethod = viaMethod || hop[c] ||
					(next[c] != -1 && strings.HasPrefix(g.names[c], "type:") && !strings.HasPrefix(g.names[next[c]], "type:"))
			}
			return chain, viaMethod
		}
		for _, p := range preds {
			if _, seen := next[p]; !seen {
				next[p] = n
				hop[p] = typeHop
				queue = append(queue, p)
			}
		}
	}
	// Every predecessor is reached from the target itself: a cycle with no way in, which the
	// linker does not print. Said as the symbol alone rather than as a path that is not one.
	return []string{name}, false
}

// receiverType is the type symbol of a method's receiver, as the linker names both:
// pkg.(*T).M is a method of type:*pkg.T, pkg.T.M of type:pkg.T. A name that is no method answers
// a type symbol the graph does not have, which is no way in.
func receiverType(name string) string {
	if i := strings.Index(name, ".(*"); i >= 0 {
		if j := strings.Index(name[i:], ")."); j >= 0 {
			return "type:*" + name[:i] + "." + name[i+3:i+j]
		}
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		return "type:" + name[:i]
	}
	return ""
}
