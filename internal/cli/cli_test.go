package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	t.Setenv("OTEL_SDK_DISABLED", "true")
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), args, &out, &errOut)
	return out.String() + errOut.String(), code
}

func TestParseInts(t *testing.T) {
	got, err := parseInts[int64]("500k, 1M,10_000")
	if err != nil || len(got) != 3 || got[0] != 500_000 || got[1] != 1_000_000 || got[2] != 10_000 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := parseInts[int32]("abc"); err == nil {
		t.Fatal("accepted abc")
	}
}

func TestCommands(t *testing.T) {
	out, code := run(t, "scenarios")
	if code != 0 || !strings.Contains(out, "R6") || !strings.Contains(out, "Masowy import") {
		t.Fatalf("scenarios: %d\n%s", code, out)
	}
	out, code = run(t, "gen", "--tickets", "20k")
	if code != 0 || !strings.Contains(out, "checksum") {
		t.Fatalf("gen: %d\n%s", code, out)
	}
	out, code = run(t, "plan", "--sizes", "500k", "--stores", "valkey")
	if code != 0 || !strings.Contains(out, "valkey/500000/10-contend") {
		t.Fatalf("plan: %d\n%s", code, out)
	}
	if _, code = run(t, "plan", "--stores", "oracle"); code == 0 {
		t.Fatal("unknown store accepted")
	}
	if _, code = run(t, "--runner", "127.0.0.1:1", "ping"); code == 0 {
		t.Fatal("ping to a closed port succeeded")
	}
}

func BenchmarkGenLayout(b *testing.B) {
	b.Setenv("OTEL_SDK_DISABLED", "true")
	for b.Loop() {
		var out bytes.Buffer
		Execute(context.Background(), []string{"plan"}, &out, &out)
	}
}
