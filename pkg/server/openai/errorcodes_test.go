package openai

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The classification every caller builds its retry policy on, spelled out
// independently of errorcodes.go so a silent flip fails here.
var wantRetryable = map[ErrorCode]bool{
	"method_not_allowed":       false,
	"unauthorized":             false,
	"invalid_json":             false,
	"unknown_tool":             false,
	"empty_task":               false,
	"invalid_model":            false,
	"invalid_effort":           false,
	"invalid_work_dir":         false,
	"unsafe_symlink":           false,
	"unsupported_git_worktree": false,
	"unknown_bundle":           false,
	"missing_input":            false,
	"invalid_upload":           false,
	"missing_file":             false,
	"invalid_id":               false,
	"not_found":                false,
	"no_file_store":            false,
	"invalid_callback_url":     false,
	"invalid_callback_headers": false,
	"callback_stream_conflict": false,
	"run_cancelled":            false,

	"concurrency_limit":  true,
	"clone_failed":       true,
	"work_dir_failed":    true,
	"bundle_failed":      true,
	"bundle_list_failed": true,
	"save_failed":        true,
	"server_shutdown":    true,
}

func TestErrorRetryable_MatchesTheShippedClassification(t *testing.T) {
	for code, want := range wantRetryable {
		got, ok := errorRetryable[code]
		if !ok {
			t.Errorf("%s is unclassified", code)
			continue
		}
		if got != want {
			t.Errorf("%s retryable = %v, want %v", code, got, want)
		}
	}
	for code := range errorRetryable {
		if _, ok := wantRetryable[code]; !ok {
			t.Errorf("%s is classified but not in the documented table", code)
		}
	}
}

// Every code the package declares must be classified, and every classification
// must belong to a declared code. A new code added without a decision on its
// retryability fails here rather than defaulting silently at runtime.
func TestErrorRetryable_ClassifiesEveryDeclaredCode(t *testing.T) {
	declared := declaredErrorCodes(t)
	if len(declared) == 0 {
		t.Fatal("no ErrorCode constants found — the source scan is broken, not the codes")
	}

	byValue := make(map[ErrorCode]string, len(declared))
	for name, code := range declared {
		byValue[code] = name
		if _, ok := errorRetryable[code]; !ok {
			t.Errorf("%s (%s) has no entry in errorRetryable", name, code)
		}
	}
	for code := range errorRetryable {
		if _, ok := byValue[code]; !ok {
			t.Errorf("errorRetryable classifies %q, which no ErrorCode constant declares", code)
		}
	}
}

// Error codes must reach NewErrorResponse as declared constants. A bare string
// literal at a call site would slip past the completeness check above, so it is
// rejected here instead.
func TestNewErrorResponse_CallSitesUseDeclaredCodeConstants(t *testing.T) {
	fset, files := parsePackageSources(t)
	declared := declaredErrorCodes(t)
	helpers := codeReturningFuncs(files)

	// The helpers that map an error to a code are call-site arguments too, so
	// their own returns have to be declared constants.
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || !helpers[fn.Name.Name] {
				return true
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ret, ok := n.(*ast.ReturnStmt)
				if !ok || len(ret.Results) != 1 {
					return true
				}
				if id, ok := ret.Results[0].(*ast.Ident); ok && declared[id.Name] != "" {
					return true
				}
				t.Errorf("%s: %s returns an error code that is not a declared constant",
					fset.Position(ret.Pos()), fn.Name.Name)
				return true
			})
			return false
		})
	}

	calls := 0
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isNewErrorResponse(call.Fun) || len(call.Args) != 3 {
				return true
			}
			calls++
			pos := fset.Position(call.Args[2].Pos())
			switch arg := call.Args[2].(type) {
			case *ast.Ident:
				if declared[arg.Name] == "" {
					t.Errorf("%s: code %s is not a declared ErrorCode constant", pos, arg.Name)
				}
			case *ast.CallExpr:
				id, ok := arg.Fun.(*ast.Ident)
				if !ok || !helpers[id.Name] {
					t.Errorf("%s: code comes from a call that does not return ErrorCode", pos)
				}
			default:
				t.Errorf("%s: code must be a declared ErrorCode constant, not an inline value", pos)
			}
			return true
		})
	}
	if calls == 0 {
		t.Fatal("no NewErrorResponse call sites found — the source scan is broken")
	}
}

func TestNewErrorResponse_CarriesRetryability(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want bool
	}{
		{codeInvalidModel, false},
		{codeUnsupportedGitWorktree, false},
		{codeCloneFailed, true},
		{codeConcurrencyLimit, true},
	}
	for _, c := range cases {
		resp := NewErrorResponse("boom", "server_error", c.code)
		if resp.Error.Retryable != c.want {
			t.Errorf("%s retryable = %v, want %v", c.code, resp.Error.Retryable, c.want)
		}
	}

	// The field is always present, including when false: a caller must never
	// have to tell "not retryable" apart from "this server is too old to say".
	body, err := json.Marshal(NewErrorResponse("boom", "invalid_request_error", codeInvalidModel))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"retryable":false`) {
		t.Errorf("error body %s omits retryable", body)
	}
}

// ---------------------------------------------------------------------------
// Source scanning helpers
// ---------------------------------------------------------------------------

// parsePackageSources parses this package's non-test files.
func parsePackageSources(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	return fset, files
}

// declaredErrorCodes returns every constant declared with type ErrorCode,
// keyed by constant name.
func declaredErrorCodes(t *testing.T) map[string]ErrorCode {
	t.Helper()
	_, files := parsePackageSources(t)
	codes := make(map[string]ErrorCode)
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			// A spec without its own type repeats the previous one, so the
			// block's current type is carried forward.
			typed := false
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if vs.Type != nil {
					id, ok := vs.Type.(*ast.Ident)
					typed = ok && id.Name == "ErrorCode"
				}
				if !typed {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s: ErrorCode constants must be string literals", name.Name)
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Errorf("%s: unquote %s: %v", name.Name, lit.Value, err)
						continue
					}
					codes[name.Name] = ErrorCode(value)
				}
			}
		}
	}
	return codes
}

// codeReturningFuncs returns the names of package functions whose sole result
// is an ErrorCode.
func codeReturningFuncs(files []*ast.File) map[string]bool {
	names := make(map[string]bool)
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); ok && id.Name == "ErrorCode" {
				names[fn.Name.Name] = true
			}
		}
	}
	return names
}

func isNewErrorResponse(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == "NewErrorResponse"
	case *ast.SelectorExpr:
		return f.Sel.Name == "NewErrorResponse"
	}
	return false
}
