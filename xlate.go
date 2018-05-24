// Copyright 2018 Paolo Machiavelli. All rights reserved.
// Use of this source code is governed by the BSD 3-Clause
// license that can be found in the LICENSE file.

package main

import (
	"go/ast"
	"go/scanner"
	"go/token"
	"strconv"
	"strings"
)

// xlateFile translates file in place. file may contain colon-prefixed
// identifiers, and must not contain any token.DEFINE (:=).
func xlateFile(fset *token.FileSet, file *ast.File) error {
	var x = xlate{fset: fset}
	ast.Walk(&visitor{x: &x}, file)
	if x.elist.Len() > 0 {
		x.elist.Sort()
		return x.elist
	}
	var tc = 0
	for _, c := range x.clist {
		c.apply(&tc)
	}
	return nil
}

// xlate contains data relative to a specific xlateFile call,
// that is shared with all of its derived visitors.
type xlate struct {
	clist []*change
	elist scanner.ErrorList
	fset  *token.FileSet
}

type visitor struct {
	x    *xlate
	comm ast.Stmt
	init bool
	list *[]ast.Stmt

	// innermost and outermost LabeledStmt
	ilabel *ast.LabeledStmt
	olabel *ast.LabeledStmt
}

// Visit implements the ast.Visitor interface.
// It fixes things that don't require replacing or adding nodes,
// and fills v.x.clist with the remaining changes to do.
func (v *visitor) Visit(n ast.Node) ast.Visitor {
	var v2 = &visitor{x: v.x}
	switch n := n.(type) {
	case nil:
		return nil
	case *ast.AssignStmt:
		v.assignStmt(n)
	case *ast.BlockStmt:
		v2.list = &n.List
	case *ast.CaseClause:
		v2.list = &n.Body
	case *ast.CommClause:
		v2.list = &n.Body
		v2.comm = n.Comm
	case *ast.Ident:
		v.ident(n)
	case *ast.LabeledStmt:
		// labeled statements may be nested
		v2.list = v.list
		v2.ilabel = n
		if v.olabel != nil {
			v2.olabel = v.olabel
		} else {
			v2.olabel = n
		}
	case *ast.RangeStmt:
		v.rangeStmt(n)
	case *ast.ForStmt,
		*ast.IfStmt,
		*ast.SwitchStmt,
		*ast.TypeSwitchStmt:
		v2.init = true
	}
	return v2
}

func (v *visitor) assignStmt(a *ast.AssignStmt) {
	var decl, assign, kind = processLhs(a.Lhs...)
	if decl == 0 {
		return
	}
	if v.init || v.comm == a {
		if assign == 0 {
			a.Tok = token.DEFINE
		} else {
			v.x.elist.Add(v.x.fset.Position(a.Pos()),
				"mixed assignment in init statement")
		}
		return
	}
	var c = &change{assign: a, list: v.list}
	if v.ilabel != nil {
		// a is v.ilabel's child, and v.list contains v.olabel
		c.ptr = &v.ilabel.Stmt
		c.ref = v.olabel
	} else {
		// v.list contains a
		c.ref = a
	}
	if assign > 0 {
		// mixed
		c.kind = kind
	}
	v.x.clist = append(v.x.clist, c)
}

func (v *visitor) ident(i *ast.Ident) {
	// we already removed valid colon-prefixes with processLhs
	if strings.HasPrefix(i.Name, ":") {
		v.x.elist.Add(v.x.fset.Position(i.Pos()),
			"unexpected colon-prefix")
	}
}

func (v *visitor) rangeStmt(r *ast.RangeStmt) {
	var decl, assign, _ = processLhs(r.Key, r.Value)
	if decl == 0 {
	} else if assign == 0 {
		r.Tok = token.DEFINE
	} else {
		v.x.elist.Add(v.x.fset.Position(r.Pos()),
			"mixed assignment in range")
	}
}

type change struct {
	assign *ast.AssignStmt
	kind   []token.Token // mixed if not nil
	list   *[]ast.Stmt
	ptr    *ast.Stmt // non-mixed only
	ref    ast.Stmt
}

// apply makes the change c. tc must point to a counter
// that is used to generate identifiers.
func (c *change) apply(tc *int) {
	if c.kind == nil {
		c.applyNonMixed()
	} else {
		c.applyMixed(tc)
	}
}

// applyNonMixed replaces c.assign with a var declaration.
func (c *change) applyNonMixed() {
	var idents = make([]*ast.Ident, len(c.assign.Lhs))
	for i, expr := range c.assign.Lhs {
		idents[i] = expr.(*ast.Ident)
	}
	var decl = makeDecl(idents, c.assign.Rhs)
	if c.ptr != nil {
		*c.ptr = decl
		return
	}
	(*c.list)[indexStmt(*c.list, c.assign)] = decl
}

// applyMixed breaks c.assign into multiple statements,
// using temporary variables.
func (c *change) applyMixed(tc *int) {
	c.assign.Tok = token.DEFINE
	var lhs = c.assign.Lhs
	c.assign.Lhs = make([]ast.Expr, len(lhs))
	var after = make([]ast.Stmt, 0, len(lhs))
	for i, expr := range lhs {
		if c.kind[i] == token.ILLEGAL {
			c.assign.Lhs[i] = expr
			continue
		}
		var temp = &ast.Ident{Name: tempTag + strconv.Itoa(*tc)}
		(*tc)++
		c.assign.Lhs[i] = temp
		var stmt ast.Stmt
		if c.kind[i] == token.VAR {
			stmt = makeDecl([]*ast.Ident{expr.(*ast.Ident)},
				[]ast.Expr{temp})
		} else {
			stmt = &ast.AssignStmt{
				Lhs: []ast.Expr{expr},
				Tok: token.ASSIGN,
				Rhs: []ast.Expr{temp},
			}
		}
		after = append(after, stmt)
	}
	var list = make([]ast.Stmt, 0, len(*c.list)+len(after))
	var pos = indexStmt(*c.list, c.ref)
	pos++
	list = append(list, (*c.list)[:pos]...)
	list = append(list, after...)
	list = append(list, (*c.list)[pos:]...)
	*c.list = list
}

// processLhs takes a list of expressions and returns two counters
// and the "type" of each expression: ILLEGAL for "_" or nil, VAR
// for colon-prefixed identifiers (the colon is removed), ASSIGN
// for anything else.
func processLhs(lhs ...ast.Expr) (int, int, []token.Token) {
	var kind = make([]token.Token, len(lhs))
	var decl, assign = 0, 0
	for i, expr := range lhs {
		kind[i] = token.ILLEGAL
		if expr == nil {
			continue
		}
		var ident, ok = expr.(*ast.Ident)
		if !ok {
			kind[i] = token.ASSIGN
			assign++
			continue
		}
		if strings.HasPrefix(ident.Name, ":") {
			ident.Name = ident.Name[1:]
			kind[i] = token.VAR
			decl++
			continue
		}
		if ident.Name != "_" {
			kind[i] = token.ASSIGN
			assign++
		}
	}
	return decl, assign, kind
}

func makeDecl(names []*ast.Ident, values []ast.Expr) *ast.DeclStmt {
	return &ast.DeclStmt{
		Decl: &ast.GenDecl{
			Tok: token.VAR,
			Specs: []ast.Spec{&ast.ValueSpec{
				Names:  names,
				Values: values,
			}},
		},
	}
}

func indexStmt(list []ast.Stmt, stmt ast.Stmt) int {
	for i, v := range list {
		if stmt == v {
			return i
		}
	}
	panic("indexStmt")
}
