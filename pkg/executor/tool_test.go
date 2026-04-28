package executor

import "testing"

func TestExtractCostInfo_OpencodePlaceholder(t *testing.T) {
	info := extractCostInfo("opencode", `{"any":"json"}`, "")
	if info.InputTokens != 0 || info.OutputTokens != 0 || info.CostUSD != 0 {
		t.Errorf("expected zero usage placeholder, got input=%d output=%d cost=%v", info.InputTokens, info.OutputTokens, info.CostUSD)
	}
}

func TestExtractSessionID_OpencodePlaceholder(t *testing.T) {
	if got := extractSessionID("opencode", `{"session":"abc"}`, ""); got != "" {
		t.Errorf("expected empty session placeholder, got %q", got)
	}
}

func TestExtractCostInfo_KilocodePlaceholder(t *testing.T) {
	info := extractCostInfo("kilocode", `{"any":"json"}`, "")
	if info.InputTokens != 0 || info.OutputTokens != 0 || info.CostUSD != 0 {
		t.Errorf("expected zero usage placeholder, got input=%d output=%d cost=%v", info.InputTokens, info.OutputTokens, info.CostUSD)
	}
}

func TestExtractSessionID_KilocodePlaceholder(t *testing.T) {
	if got := extractSessionID("kilocode", `{"session":"abc"}`, ""); got != "" {
		t.Errorf("expected empty session placeholder, got %q", got)
	}
}
