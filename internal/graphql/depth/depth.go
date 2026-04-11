// Package depth provides a pre-execution query depth guard for
// GraphQL requests. graphql-go does not expose a validation rule
// hook comparable to graphql-js, so we parse the document once,
// walk its selection sets, and reject anything deeper than a
// configured bound before handing the query to graphql.Do.
//
// The goal is DoS protection: a query like
//
//	{ a { a { a { a { a { a { ... } } } } } } }
//
// can fit inside a 1 MiB body cap and still cost significant CPU
// to plan and resolve. Bounding depth at ~15 is comfortably above
// anything the lexicons produce in practice and well below the
// pathological cases.
package depth

import (
	"errors"
	"fmt"

	"github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/graphql/language/parser"
	"github.com/graphql-go/graphql/language/source"
)

// ErrTooDeep is returned by Check when a query exceeds the limit.
var ErrTooDeep = errors.New("graphql query exceeds maximum selection depth")

// Check parses the given GraphQL query string and returns an error
// if any operation's selection tree is deeper than maxDepth. A
// non-positive maxDepth is treated as "no limit".
//
// Fragments are inlined during the walk so fragment spreads cannot
// be used to bypass the cap. A fragment cycle detector prevents
// infinite recursion on malformed documents (graphql.Do would
// reject them anyway, but we must not hang first).
func Check(query string, maxDepth int) error {
	if maxDepth <= 0 || query == "" {
		return nil
	}
	doc, err := parser.Parse(parser.ParseParams{
		Source: source.NewSource(&source.Source{Body: []byte(query)}),
	})
	if err != nil {
		// Let graphql.Do surface parse errors in its own format.
		return nil //nolint:nilerr // parse errors are reported by graphql.Do
	}

	// Collect all fragment definitions so the walker can resolve
	// spreads without a second pass.
	frags := make(map[string]*ast.FragmentDefinition)
	for _, def := range doc.Definitions {
		if fd, ok := def.(*ast.FragmentDefinition); ok && fd.Name != nil {
			frags[fd.Name.Value] = fd
		}
	}

	for _, def := range doc.Definitions {
		op, ok := def.(*ast.OperationDefinition)
		if !ok || op.SelectionSet == nil {
			continue
		}
		d := selectionDepth(op.SelectionSet, frags, make(map[string]bool), 0)
		if d > maxDepth {
			return fmt.Errorf("%w: depth=%d limit=%d", ErrTooDeep, d, maxDepth)
		}
	}
	return nil
}

// selectionDepth walks a SelectionSet and returns the maximum nested
// selection depth starting at `current`. Fragment spreads are
// inlined; a seen-set guards against fragment cycles.
func selectionDepth(set *ast.SelectionSet, frags map[string]*ast.FragmentDefinition, seen map[string]bool, current int) int {
	if set == nil {
		return current
	}
	depth := current
	next := current + 1
	for _, sel := range set.Selections {
		switch s := sel.(type) {
		case *ast.Field:
			if d := selectionDepth(s.SelectionSet, frags, seen, next); d > depth {
				depth = d
			} else if next > depth {
				depth = next
			}
		case *ast.InlineFragment:
			if d := selectionDepth(s.SelectionSet, frags, seen, current); d > depth {
				depth = d
			}
		case *ast.FragmentSpread:
			if s.Name == nil {
				continue
			}
			name := s.Name.Value
			if seen[name] {
				// cycle — halt descent for this branch
				continue
			}
			frag, ok := frags[name]
			if !ok || frag.SelectionSet == nil {
				continue
			}
			seen[name] = true
			if d := selectionDepth(frag.SelectionSet, frags, seen, current); d > depth {
				depth = d
			}
			delete(seen, name)
		}
	}
	return depth
}
