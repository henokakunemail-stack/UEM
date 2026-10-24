package remotecontrol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The address of a Go variable must reach Windows in the argument list of
// syscall.SyscallN or LazyProc.Call itself, where the compiler keeps that
// memory in place for the call. Through comCall it was converted to a uintptr
// first; comCall's prologue could then grow the goroutine's stack, Go moved
// the stack, and Windows wrote its result to the old one. In a real session
// that was a Map that reported success with a nil surface pointer, and the
// agent panicked. Stack moves cannot be forced from a test, so this checks the
// source: no comCall may be handed an address.
func TestNoCOMHelperCallIsHandedAGoAddress(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "dxgi_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "comCall" {
			return true
		}
		calls++
		for _, arg := range call.Args {
			ast.Inspect(arg, func(m ast.Node) bool {
				if u, ok := m.(*ast.UnaryExpr); ok && u.Op == token.AND {
					t.Errorf("%s: comCall is handed the address of a Go value; call syscall.SyscallN directly",
						fset.Position(call.Pos()))
				}
				return true
			})
		}
		return true
	})
	if calls == 0 {
		t.Fatal("no comCall found: the check is looking at the wrong file")
	}
}
