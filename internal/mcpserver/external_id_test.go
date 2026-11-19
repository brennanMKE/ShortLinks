package mcpserver

import (
	"strings"
	"testing"
)

// TestExternalID_Tools covers #0182 through MCP: create_link and update_link
// take external_id, get_link_by_external_id and list_links find it.
func TestExternalID_Tools(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, secret := e.token(uid, "agent", false)
	cs := e.connect(secret)

	made := ok(t, cs, "create_link", map[string]any{"destination_url": "https://example.com/inv", "external_id": "inv/1"})
	if made["external_id"] != "inv/1" {
		t.Fatalf("create_link = %v", made)
	}
	key := made["key"].(string)

	got := ok(t, cs, "get_link_by_external_id", map[string]any{"external_id": "inv/1"})
	if got["key"] != key {
		t.Errorf("get_link_by_external_id = %v, want key %s", got, key)
	}
	list := ok(t, cs, "list_links", map[string]any{"external_id": "inv/1"})
	if links, _ := list["links"].([]any); len(links) != 1 {
		t.Errorf("list_links external_id = %v", list)
	}

	res := call(t, cs, "create_link", map[string]any{"destination_url": "https://example.com/x", "external_id": "inv/1"})
	if !res.IsError || !strings.Contains(textOf(res), "external_id_taken") {
		t.Errorf("reuse = %s", textOf(res))
	}

	upd := ok(t, cs, "update_link", map[string]any{"key": key, "external_id": ""})
	if upd["external_id"] != nil {
		t.Errorf("clear = %v", upd)
	}
	res = call(t, cs, "get_link_by_external_id", map[string]any{"external_id": "inv/1"})
	if !res.IsError || !strings.Contains(textOf(res), "404") {
		t.Errorf("after clear = %s", textOf(res))
	}
}
