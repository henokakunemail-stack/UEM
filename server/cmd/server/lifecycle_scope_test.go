package main

// Regression test for a resource-lifetime bug caused by scope, not by logic.
//
// loginH.Close() and wsH.Close() are deferred INSIDE buildServer, so they run
// when buildServer returns -- at startup, immediately after the router is
// wired -- not when the process shuts down. Two consequences, both real:
//
//   - wsH.Close() stops the HeartbeatFlusher loop. From the first served request
//     on, handleHeartbeat keeps calling flusher.Record, which appends to an
//     in-memory map that nothing drains any more: last_seen_at is never written
//     and the pending map grows by one entry per device for the life of the
//     process.
//   - loginH.Close() stops the IPRateLimiter cleanup ticker, so records is never
//     pruned again; every source IP that ever failed a login stays resident.
//
// The assertion is structural on purpose: a timing probe of the flusher would
// need an accessor into private state, and a probe of the limiter needs the
// handler that buildServer does not return. Reading the AST is deterministic
// and requires no production change.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestBuildServer_DoesNotDeferCloseInsideIt(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	var buildServer *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "buildServer" {
			buildServer = fn
			break
		}
	}
	if buildServer == nil {
		t.Fatal("buildServer not found in main.go")
	}

	bad := map[string]bool{}
	ast.Inspect(buildServer.Body, func(n ast.Node) bool {
		d, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		call, ok := d.Call.Fun.(*ast.SelectorExpr)
		if !ok || call.Sel.Name != "Close" {
			return true
		}
		if recv, ok := call.X.(*ast.Ident); ok {
			bad[recv.Name+".Close()"] = true
		}
		return true
	})

	for name := range bad {
		t.Errorf("`defer %s` sits inside buildServer, so it runs when buildServer "+
			"returns (at startup) rather than at process shutdown. run() must own "+
			"the object, or its Close must be folded into the cleanup func that "+
			"buildServer already returns.", name)
	}
}
