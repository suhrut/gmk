// Package dag provides topological ordering with cycle detection for
// target dependency graphs.
//
// The package has no dependency on ir or any other gmk package — it takes
// a name->deps map and returns an execution order. This keeps the
// algorithm reusable across stages: Stage 2 uses it for target deps,
// Stage 5 will reuse it for lazy-var dependency chains, and Stage 8's
// `gmk deps why` reuses the same edges for reverse traversal.
//
// Determinism: ties in topological order (independent nodes at the same
// level) are broken alphabetically so the same graph always produces the
// same execution order. This matters for reproducible builds and for
// test stability.
package dag

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrCycle indicates a dependency cycle was found during ordering.
// The error wrap chain includes the cycle's path for diagnostic output.
var ErrCycle = errors.New("dependency cycle")

// ErrMissingNode indicates a node depends on a name that doesn't exist
// in the graph. The error wrap chain identifies which node referenced
// what missing name.
var ErrMissingNode = errors.New("missing dependency target")

// TopoSort returns the nodes in execution order: each node appears after
// all of its transitive dependencies.
//
// Parameters:
//   - nodes: map of node-name -> list of names this node depends on. Every
//     node that can be requested must appear as a key, even if its deps
//     list is empty.
//   - roots: nodes whose subgraphs should be scheduled. If nil or empty,
//     every node in the graph is scheduled. Otherwise only roots and
//     everything transitively reachable from them are scheduled.
//
// Returns:
//   - An ordered slice of node names. Stable across runs.
//   - ErrCycle (wrapped with cycle path) on cyclic graphs.
//   - ErrMissingNode (wrapped with referrer and missing name) on dangling refs.
//
// Stage 5 may add a sibling TopoLevels that returns layered groups for
// parallel scheduling; TopoSort's sequential output remains.
func TopoSort(nodes map[string][]string, roots []string) ([]string, error) {
	if len(nodes) == 0 {
		return []string{}, nil
	}

	// Determine which subset of nodes to actually schedule.
	scheduled, err := reachable(nodes, roots)
	if err != nil {
		return nil, err
	}

	// Color-marking DFS (Cormen-style): white=unvisited, gray=in-progress,
	// black=finished. Gray-on-gray means cycle.
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(scheduled))
	var order []string
	var stack []string // current DFS path, used for cycle reporting

	var visit func(name string) error
	visit = func(name string) error {
		switch color[name] {
		case black:
			return nil
		case gray:
			// Cycle found. Reconstruct the cycle path from the DFS stack.
			cycleStart := -1
			for i, s := range stack {
				if s == name {
					cycleStart = i
					break
				}
			}
			path := append([]string{}, stack[cycleStart:]...)
			path = append(path, name)
			return fmt.Errorf("%w: %s", ErrCycle, strings.Join(path, " -> "))
		}

		color[name] = gray
		stack = append(stack, name)

		// Visit deps in sorted order for deterministic output.
		deps := append([]string(nil), nodes[name]...)
		sort.Strings(deps)
		for _, d := range deps {
			if _, ok := nodes[d]; !ok {
				return fmt.Errorf("%w: target %q depends on undefined target %q", ErrMissingNode, name, d)
			}
			if !scheduled[d] {
				// A dep outside the scheduled subset shouldn't happen because
				// reachable() expands roots transitively, but guard defensively.
				scheduled[d] = true
			}
			if err := visit(d); err != nil {
				return err
			}
		}

		stack = stack[:len(stack)-1]
		color[name] = black
		order = append(order, name)
		return nil
	}

	// Visit scheduled roots in sorted order for deterministic output.
	roots2 := make([]string, 0, len(scheduled))
	for n := range scheduled {
		roots2 = append(roots2, n)
	}
	sort.Strings(roots2)
	for _, n := range roots2 {
		if err := visit(n); err != nil {
			return nil, err
		}
	}

	return order, nil
}

// reachable returns the set of nodes reachable from roots (transitively).
// If roots is empty, every node in the graph is reachable.
// Returns ErrMissingNode if a root is not in the graph.
func reachable(nodes map[string][]string, roots []string) (map[string]bool, error) {
	out := make(map[string]bool, len(nodes))

	if len(roots) == 0 {
		for k := range nodes {
			out[k] = true
		}
		return out, nil
	}

	for _, r := range roots {
		if _, ok := nodes[r]; !ok {
			return nil, fmt.Errorf("%w: requested target %q does not exist", ErrMissingNode, r)
		}
	}

	var visit func(name string)
	visit = func(name string) {
		if out[name] {
			return
		}
		out[name] = true
		for _, d := range nodes[name] {
			// Tolerate missing deps here; TopoSort will report them with
			// the proper referrer context during its DFS.
			if _, ok := nodes[d]; ok {
				visit(d)
			}
		}
	}
	for _, r := range roots {
		visit(r)
	}
	return out, nil
}
