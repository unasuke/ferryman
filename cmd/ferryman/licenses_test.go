package main

import (
	"strings"
	"testing"
)

// The notices are embedded from a generated file, which two mistakes could
// quietly hollow out: a failed generation (empty text shipped to users) and a
// CRLF checkout on Windows (carriage returns embedded verbatim, drawn as boxes).
// .gitattributes pins the file to LF; this is the check that it stayed pinned.
func TestNotices(t *testing.T) {
	if len(notices) < 10_000 {
		t.Fatalf("notices look truncated: %d bytes", len(notices))
	}
	if strings.Contains(notices, "\r") {
		t.Error("notices contain CR; cmd/ferryman/NOTICES.txt must be checked out with LF endings")
	}
	// One anchor per section the generator writes, so a silently emptied
	// section is caught too.
	for _, want := range []string{
		"MIT License",           // Ferryman itself
		"fyne.io/fyne/v2",       // the module list
		"SIL OPEN FONT LICENSE", // the fonts Fyne embeds
	} {
		if !strings.Contains(notices, want) {
			t.Errorf("notices missing %q", want)
		}
	}
}
