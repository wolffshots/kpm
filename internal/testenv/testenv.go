// Package testenv gates the host-capability skips in kpm's test suite.
//
// Several of kpm's safety guards can only be exercised on a host that supports
// unprivileged symlinks and unix permission bits — the symlinked-parent refusal
// in internal/uninstall, internal/apptree and the config editor above all. On
// the Windows development host those tests SKIP, and a skipped test is
// indistinguishable from a passing one at a glance: deleting the entire
// SymlinkedInstallParent call from apptree.checkWritable left
// `go test ./internal/apptree/` green here. There is no CI, so nothing else
// notices.
//
// SkipUnsupported keeps the skip (the suite has to run on the dev host) but
// makes it a hard failure when KPM_REQUIRE_SYMLINK_TESTS=1, so "are these guards
// actually covered anywhere?" is a question with a runnable answer:
//
//	GOOS=linux GOARCH=amd64 go test -c ./internal/apptree/   # then, under WSL:
//	KPM_REQUIRE_SYMLINK_TESTS=1 ./apptree.test
package testenv

import (
	"fmt"
	"os"
	"testing"
)

// RequireVar turns every host-capability skip into a failure when set to "1".
const RequireVar = "KPM_REQUIRE_SYMLINK_TESTS"

// SkipUnsupported skips t because this HOST cannot do what the test needs — or
// fails it, when the caller has declared that the host must be able to.
func SkipUnsupported(t testing.TB, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if os.Getenv(RequireVar) == "1" {
		t.Fatalf("%s — but %s=1 says this host must run it", msg, RequireVar)
	}
	t.Skip(msg)
}
