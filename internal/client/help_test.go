package client

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// dispatchCommands returns the keys of the `spec` map literal in run, read
// from the source so the test follows the dispatch table without a copy of it.
func dispatchCommands(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "commands.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		if id, ok := assign.Lhs[0].(*ast.Ident); !ok || id.Name != "spec" {
			return true
		}
		literal, ok := assign.Rhs[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, element := range literal.Elts {
			kv := element.(*ast.KeyValueExpr)
			key, err := strconv.Unquote(kv.Key.(*ast.BasicLit).Value)
			if err != nil {
				t.Fatal(err)
			}
			keys[key] = true
		}
		return false
	})
	if len(keys) < 20 {
		t.Fatalf("found only %d commands in the spec map of commands.go", len(keys))
	}
	return keys
}

func TestEveryCommandHasHelp(t *testing.T) {
	commands := dispatchCommands(t)
	for command := range commands {
		h, ok := commandHelp[command]
		if !ok {
			t.Errorf("command %q has no help entry", command)
			continue
		}
		if !strings.HasPrefix(h.usage, command) || h.text == "" || !strings.HasPrefix(h.example, "templatecontext ") && !strings.Contains(h.example, "| templatecontext ") {
			t.Errorf("help of %q needs a usage line starting with the command, a description and an example", command)
		}
	}
	// help and version are handled before the dispatch table.
	outside := map[string]bool{"help": true, "version": true}
	for command := range commandHelp {
		if !commands[command] && !outside[command] {
			t.Errorf("help entry %q names no command", command)
		}
	}
}

func TestDataHelpDocumentsShapes(t *testing.T) {
	for command, wants := range map[string][]string{
		"add":    {`"id"`, `"message"`, `"created"`, "500"},
		"list":   {`"message"`, "500"},
		"search": {`"generation"`, `"greetings"`, `"nextOffset"`, "exit 4"},
	} {
		for _, want := range wants {
			if !strings.Contains(commandHelp[command].text, want) {
				t.Errorf("%s help lacks %s", command, want)
			}
		}
	}
}

func TestWantsHelp(t *testing.T) {
	cases := map[string]bool{"--help": true, "x -h": true, "--title x --help": true, "-- --help": false, "x": false}
	for in, want := range cases {
		if got := wantsHelp(strings.Fields(in)); got != want {
			t.Errorf("wantsHelp(%q) = %v", in, got)
		}
	}
}
