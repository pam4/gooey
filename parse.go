// Copyright 2018 Paolo Machiavelli. All rights reserved.
// Use of this source code is governed by the BSD 3-Clause
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"
)

// parseFile parses src and returns the corresponding ast.File node.
//
// First we use the scanner to make some token modifications, then
// we parse it with the standard Go parser (expecting no errors),
// and finally we traverse the AST to revert the changes.
//
// Token modifications:
//
// When we encounter a colon-prefixed identifier we remove the
// colon and encode it into the identifier name.
// To avoid colons that are part of slicing expressions we look for
// the sequence: COLON IDENT (ASSIGN | COMMA)
// Remaining false-positive colons are:
// - between keys and values in composite literals
// - after labels
// - after switch/select cases
// For these cases we mandate the presence of some whitespace after
// the colon (which is customary, and we make sure that parsing
// will fail if the requirement is not met) so that we can ignore
// colons that are not contiguous to the identifier.
//
// At this point we should have parsable code, except for "=" in
// type switch guards. To fix those we replace "=" with ":=" in
// COLON IDENT ASSIGN sequences, unless they are preceded by a
// COMMA (without the comma exception we may end up with
// non-identifiers on the left side of a ":=").
func parseFile(fset *token.FileSet, name string, src []byte) (*ast.File,
	error) {
	var fset2 = token.NewFileSet()
	var base = fset2.Base()
	var file = fset2.AddFile(name, base, len(src))
	var s scanner.Scanner
	var elist scanner.ErrorList
	var errFunc = func(pos token.Position, msg string) {
		elist.Add(pos, msg)
	}
	// 0 -> skip comments so that they don't interfere
	s.Init(file, src, errFunc, 0)
	var buf bytes.Buffer
	var low, high int
	var last4 [4]struct {
		pos token.Pos
		tok token.Token
		lit string
	}
	for i := 0; ; i++ {
		var tok = &last4[i%4]
		tok.pos, tok.tok, tok.lit = s.Scan()
		if tok.tok == token.EOF {
			break
		}
		if tok.tok == token.DEFINE {
			elist.Add(fset2.Position(tok.pos), `evil token: ":="`)
			continue
		}
		if i < 2 || tok.tok != token.ASSIGN && tok.tok != token.COMMA {
			continue
		}
		var ident = &last4[(i-1)%4]
		var colon = &last4[(i-2)%4]
		if ident.tok != token.IDENT || colon.tok != token.COLON ||
			ident.lit == "_" || colon.pos+1 != ident.pos {
			continue
		}
		high = int(colon.pos) - base
		buf.Write(src[low:high])
		buf.WriteString(" " + cprefTag)
		low, high = high+1, int(tok.pos)-base
		buf.Write(src[low:high])
		low = high
		if tok.tok == token.ASSIGN &&
			(i < 3 || last4[(i-3)%4].tok != token.COMMA) {
			buf.WriteString(":")
		}
	}
	buf.Write(src[low:])
	if elist.Len() > 0 {
		return nil, elist
	}
	var tree, err = parser.ParseFile(fset, name, &buf, parser.ParseComments)
	if err != nil {
		return tree, err
	}
	// revert the changes
	ast.Inspect(tree, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				n.Tok = token.ASSIGN
			}
		case *ast.RangeStmt:
			if n.Tok == token.DEFINE {
				n.Tok = token.ASSIGN
			}
		case *ast.Ident:
			if strings.HasPrefix(n.Name, cprefTag) {
				n.Name = ":" + n.Name[len(cprefTag):]
			}
		}
		return n != nil
	})
	return tree, nil
}
