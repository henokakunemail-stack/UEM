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
// A third instance of the same class survived the first version of this test,
// because the test only looked for a defer whose callee was named Close:
//
//	backupCtx, stopBackups := context.WithCancel(context.Background())
//	defer stopBackups()
//	go db.StartBackupJob(backupCtx, ...)
//
// That one cancelled the backup job's context microseconds after launching it.
// StartBackupJob opens with a 30-second settle delay selected against
// ctx.Done(), so the cancel always won and the job returned having taken no
// snapshot at all -- on the default configuration, every boot -- while the log
// line two lines below printed "scheduled online database backups". So the
// filter is on what a defer *does* (it ends the lifetime of a thing started
// here), not on what it is called.
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

	// A deferred call that ends a background thing's life. Close is the obvious
	// one; a context cancel is the one that got through, because it is named
	// stop-something and the first version of this filter only matched Close.
	endsALifetime := func(call ast.Expr) (string, bool) {
		switch fn := call.(type) {
		case *ast.SelectorExpr:
			switch fn.Sel.Name {
			case "Close", "Stop", "Shutdown", "Cancel":
				return fn.Sel.Name, true
			}
		case *ast.Ident:
			// context.CancelFunc and friends: an unadorned identifier that is
			// not a builtin. cancel/stopBackups and this shape is how a
			// WithCancel's second return gets deferred.
			switch fn.Name {
			case "cancel", "stop", "close", "shutdown", "stopBackups", "stopBackground":
				return fn.Name, true
			}
		}
		return "", false
	}

	bad := map[string]bool{}
	ast.Inspect(buildServer.Body, func(n ast.Node) bool {
		d, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		// Skip a deferred call that opens no such lifetime: a defer inside a
		// nested closure is scoped to that closure, not to buildServer.
		if name, ok := endsALifetime(d.Call.Fun); ok {
			bad[name] = true
		}
		return true
	})

	for name := range bad {
		t.Errorf("`defer %s` sits inside buildServer, so it runs when buildServer "+
			"returns (at startup) rather than at process shutdown. run() must own "+
			"the thing, or its teardown must be folded into the cleanup func that "+
			"buildServer already returns.", name)
	}
}

// TestTheCleanupClosureStopsEverythingBuildServerStarted is the positive half.
// The fix moves these teardowns into the cleanup func; if that closure ever lost
// one, the same bug would come back with the test still green, because the test
// above only asserts the defer is gone from buildServer.
func TestTheCleanupClosureStopsEverythingBuildServerStarted(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	// Find the local variable each started background thing is bound to, then
	// require the cleanup closure to mention it.
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

	// Names bound by a two-value context.WithCancel: "<x>Ctx, stop<x>".
	//
	// The call is a *ast.CallExpr whose Fun is the selector, so the selector is
	// one level down -- matching on Rhs[0] itself finds nothing, which is how
	// the first version of this test reported a stale premise instead of the
	// thing it was looking for.
	startsWithCancel := map[string]string{}
	ast.Inspect(buildServer.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WithCancel" {
			return true
		}
		stop, ok := as.Lhs[1].(*ast.Ident)
		if !ok {
			return true
		}
		startsWithCancel[stop.Name] = ""
		return true
	})
	if len(startsWithCancel) == 0 {
		t.Fatal("no context.WithCancel in buildServer; the test's premise is stale")
	}

	// Now find the cleanup closure and the identifiers it calls.
	found := false
	mentioned := map[string]bool{}
	ast.Inspect(buildServer.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name != "cleanup" || i >= len(as.Rhs) {
				continue
			}
			lit, ok := as.Rhs[i].(*ast.FuncLit)
			if !ok {
				continue
			}
			found = true
			ast.Inspect(lit.Body, func(inner ast.Node) bool {
				if call, ok := inner.(*ast.Ident); ok {
					mentioned[call.Name] = true
				}
				return true
			})
		}
		return true
	})

	if !found {
		t.Fatal("buildServer has no `cleanup := func() {...}`; this test cannot check it")
	}
	for name := range startsWithCancel {
		if !mentioned[name] {
			t.Errorf("buildServer starts a cancellable background job bound to %s, but "+
				"the cleanup closure never calls it: the job runs for the life of the "+
				"process and nothing can stop it", name)
		}
	}
}
