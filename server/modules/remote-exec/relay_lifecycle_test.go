package remoteexec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// TestClosedIsOnlyWrittenUnderItsOwnLock is the regression test for a data race
// that -race would have caught and that cannot be caught on this host, because
// the detector needs cgo and there is no C toolchain here.
//
// ActiveTerminalSession.closed is read by WriteToBrowser under s.writeMu. It was
// written by Unregister, which held r.mu -- the relay's map lock, a different
// mutex, and one the reader never touches. Two goroutines reach those lines
// concurrently on the ordinary path: the agent connection calls
// AcceptTerminalData -> WriteToBrowser on terminal output, while the browser
// handler's deferred Unregister fires when the operator closes the tab. Nothing
// ordered those two accesses, so the Go memory model does not guarantee the
// reader ever sees the write.
//
// A torn read of a bool is not observable without the detector, and both
// orderings produce a plausible answer, so the test is not written as "the write
// is seen". What is checkable, and what would have caught the original, is the
// property the race came from: the field has one owning lock. This finds every
// write to `closed` and requires each to sit in a function that takes
// writeMu.Lock() -- which markClosed does and Unregister no longer does. Put the
// assignment back in Unregister and this goes red.
func TestClosedIsOnlyWrittenUnderItsOwnLock(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(thisFile), "relay.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// The lock WriteToBrowser reads closed under, so the lock every write to it
	// has to be held by.
	const readerLock = "writeMu"

	var unguarded []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var writes []token.Pos
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assign.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "closed" {
					writes = append(writes, assign.Pos())
				}
			}
			return true
		})
		if len(writes) > 0 && !takesLock(fn.Body.List, readerLock) {
			for _, pos := range writes {
				unguarded = append(unguarded, fn.Name.Name+" at "+fset.Position(pos).String())
			}
		}
	}

	if len(unguarded) > 0 {
		t.Errorf("closed is written outside a %s critical section at %v, but it is "+
			"read under %s: that is an unsynchronised access", readerLock, unguarded, readerLock)
	}
}

// takesLock reports whether the statement list takes a top-level `x.<lock>.Lock()`.
func takesLock(stmts []ast.Stmt, lock string) bool {
	for _, stmt := range stmts {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Lock" {
			continue
		}
		if recv, ok := sel.X.(*ast.SelectorExpr); ok && recv.Sel.Name == lock {
			return true
		}
	}
	return false
}

// TestUnregisterStopsLaterWritesFromReachingTheSocket is the behavioural half:
// whatever the locking is, an unregistered session must refuse to write.
//
// Register keeps handing out a live pointer, and AcceptTerminalData's map lookup
// can still win the race against the Unregister deleting the entry. The map
// alone therefore cannot be what stops the write; the closed flag is, and this
// is the assertion that it is still consulted.
func TestUnregisterStopsLaterWritesFromReachingTheSocket(t *testing.T) {
	r := NewTerminalRelay()
	sess := r.Register("s1", "dev-1", "user-1", nil)

	// Held across the unregister on purpose: this is the pointer an in-flight
	// AcceptTerminalData is already working from.
	r.Unregister("s1")

	if err := sess.WriteToBrowser("term.data", "late output"); err == nil {
		t.Error("WriteToBrowser succeeded on an unregistered session; the operator's " +
			"socket is closed and this write goes nowhere useful")
	}
	if r.Get("s1") != nil {
		t.Error("Get returned a session after Unregister")
	}
}

// TestConcurrentUnregisterAndWrite drives both goroutines at once. A missing
// lock on a bool would not surface here as a wrong answer, but a lock-ordering
// mistake or a nil dereference would, and these two paths really are concurrent
// in production.
func TestConcurrentUnregisterAndWrite(t *testing.T) {
	r := NewTerminalRelay()
	sess := r.Register("s1", "dev-1", "user-1", nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = sess.WriteToBrowser("term.data", "x")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			r.Unregister("s1")
		}
	}()
	wg.Wait()

	if err := sess.WriteToBrowser("term.data", "after"); err == nil {
		t.Error("the session was never marked closed; Unregister no longer reaches it")
	}
}
