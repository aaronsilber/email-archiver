package main

import (
	"testing"
	"time"
)

func TestParseBefore(t *testing.T) {
	// A bare date means local midnight: "before 2023-01-01" should mean the
	// same thing to the tool as it does to the person typing it.
	got, err := parseBefore("2023-01-01")
	if err != nil {
		t.Fatalf("parseBefore: %v", err)
	}
	want := time.Date(2023, 1, 1, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("parseBefore(\"2023-01-01\") = %s, want %s", got, want)
	}

	// A full timestamp is taken at face value, zone and all.
	got, err = parseBefore("2023-06-15T08:30:00-04:00")
	if err != nil {
		t.Fatalf("parseBefore: %v", err)
	}
	if want := time.Date(2023, 6, 15, 12, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("parseBefore RFC3339 = %s, want %s", got.UTC(), want)
	}

	for _, bad := range []string{"", "yesterday", "01/01/2023", "2023-13-45"} {
		if _, err := parseBefore(bad); err == nil {
			t.Errorf("parseBefore(%q) succeeded, want an error", bad)
		}
	}
}

func TestParseFlags(t *testing.T) {
	o, code, err := parseFlags([]string{"--before", "2023-01-01", "--from", "Inbox", "--from", "Sent", "--dry-run"})
	if err != nil {
		t.Fatalf("parseFlags: %v (code %d)", err, code)
	}
	if o.before != "2023-01-01" || !o.dryRun {
		t.Errorf("options = %+v", o)
	}
	if len(o.from) != 2 || o.from[0] != "Inbox" || o.from[1] != "Sent" {
		t.Errorf("--from = %v, want [Inbox Sent]", o.from)
	}
	if o.batch != 500 {
		t.Errorf("default batch = %d, want 500", o.batch)
	}
}

func TestParseFlagsRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing --before", []string{"--dry-run"}},
		{"zero batch", []string{"--before", "2023-01-01", "--batch", "0"}},
		{"stray argument", []string{"--before", "2023-01-01", "extra"}},
		{"empty mailbox", []string{"--before", "2023-01-01", "--from", "  "}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, code, err := parseFlags(tc.args)
			if err == nil {
				t.Fatal("parseFlags succeeded, want an error")
			}
			if code != exitUsage {
				t.Errorf("exit code = %d, want %d", code, exitUsage)
			}
		})
	}
}

// TestNoTokenFlagExists is a standing guard: a credential passed as an
// argument would land in shell history and in `ps` output.
func TestNoTokenFlagExists(t *testing.T) {
	for _, arg := range []string{"--token", "--api-token", "--password"} {
		if _, _, err := parseFlags([]string{"--before", "2023-01-01", arg, "secret"}); err == nil {
			t.Errorf("%s was accepted; credentials must never come from a flag", arg)
		}
	}
}
