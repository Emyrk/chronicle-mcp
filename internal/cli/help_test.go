package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelp(t *testing.T) {
	for _, args := range [][]string{
		nil, {"help"}, {"--help"}, {"-h"},
		{"help", "serve"}, {"serve", "--help"}, {"start", "-h"},
		{"status", "--help"}, {"stop", "--help"},
		{"auth", "--help"}, {"auth", "headers", "--help"},
		{"help", "auth", "headers"},
		{"stop", "--state-dir", "/nonexistent-state", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			if err := Run(context.Background(), args, &out, &diagnostics); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Usage: chronicle-mcp") {
				t.Fatalf("missing help: %s", out.String())
			}
			if diagnostics.Len() != 0 {
				t.Fatalf("help wrote to stderr: %s", diagnostics.String())
			}
			if strings.Contains(out.String(), "Bearer ") {
				t.Fatal("help must not emit credentials")
			}
			if len(args) > 0 && (args[0] == "stop" || (args[0] == "help" && len(args) > 1 && args[1] == "stop")) && !strings.Contains(out.String(), "--force") {
				t.Fatal("missing stop flag")
			}
		})
	}
}

func TestInvalidHelpTopic(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if err := Run(context.Background(), []string{"help", "unknown"}, &out, &diagnostics); err == nil {
		t.Fatal("unknown help topic accepted")
	}
}
