package tools

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEqual(t *testing.T) {
	schema1 := json.RawMessage(`{"type":"object","properties":{"FOO":{"type":"string"}}}`)
	schema2 := json.RawMessage(`{"type":"object","properties":{"BAR":{"type":"string"}}}`)

	mk := func(name, desc string, schema json.RawMessage) *RegisteredTool {
		return &RegisteredTool{
			Tool:        mcp.Tool{Name: name, Description: desc, InputSchema: schema},
			schemaBytes: schema,
		}
	}

	tests := []struct {
		name string
		a, b *RegisteredTool
		want bool
	}{
		{
			name: "identical",
			a:    mk("greet", "Say hello", schema1),
			b:    mk("greet", "Say hello", schema1),
			want: true,
		},
		{
			name: "different name",
			a:    mk("greet", "Say hello", schema1),
			b:    mk("build", "Say hello", schema1),
			want: false,
		},
		{
			name: "different description",
			a:    mk("greet", "Say hello", schema1),
			b:    mk("greet", "Say goodbye", schema1),
			want: false,
		},
		{
			name: "different schema",
			a:    mk("greet", "Say hello", schema1),
			b:    mk("greet", "Say hello", schema2),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Equal(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("Equal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiff(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	mk := func(name, desc string) RegisteredTool {
		return RegisteredTool{
			Tool:        mcp.Tool{Name: name, Description: desc, InputSchema: schema},
			schemaBytes: schema,
		}
	}

	t.Run("empty to populated", func(t *testing.T) {
		desired := map[string]RegisteredTool{"greet": mk("greet", "Say hello")}
		stale, added := Diff(nil, desired)
		if len(stale) != 0 {
			t.Errorf("stale = %v, want empty", stale)
		}
		if !slices.Equal(added, []string{"greet"}) {
			t.Errorf("added = %v, want [greet]", added)
		}
	})

	t.Run("populated to empty", func(t *testing.T) {
		old := map[string]RegisteredTool{"greet": mk("greet", "Say hello")}
		stale, added := Diff(old, nil)
		if !slices.Equal(stale, []string{"greet"}) {
			t.Errorf("stale = %v, want [greet]", stale)
		}
		if len(added) != 0 {
			t.Errorf("added = %v, want empty", added)
		}
	})

	t.Run("unchanged", func(t *testing.T) {
		tools := map[string]RegisteredTool{"greet": mk("greet", "Say hello")}
		stale, added := Diff(tools, tools)
		if len(stale) != 0 {
			t.Errorf("stale = %v, want empty", stale)
		}
		if len(added) != 0 {
			t.Errorf("added = %v, want empty", added)
		}
	})

	t.Run("changed description", func(t *testing.T) {
		old := map[string]RegisteredTool{"greet": mk("greet", "Say hello")}
		desired := map[string]RegisteredTool{"greet": mk("greet", "Say goodbye")}
		stale, added := Diff(old, desired)
		if !slices.Equal(stale, []string{"greet"}) {
			t.Errorf("stale = %v, want [greet]", stale)
		}
		if !slices.Equal(added, []string{"greet"}) {
			t.Errorf("added = %v, want [greet]", added)
		}
	})
}
