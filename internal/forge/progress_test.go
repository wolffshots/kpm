package forge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDownloadProgress: WithProgress gets 0 first, then a growing count that
// ends at the size of the body.
func TestDownloadProgress(t *testing.T) {
	body := strings.Repeat("x", 100000)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := &Client{hc: srv.Client()}
	f, err := os.Create(filepath.Join(t.TempDir(), "a"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var seen []int64
	ctx := WithProgress(context.Background(), func(done int64) { seen = append(seen, done) })
	if _, err := c.Download(ctx, srv.URL+"/a", f); err != nil {
		t.Fatal(err)
	}
	if len(seen) < 2 || seen[0] != 0 || seen[len(seen)-1] != int64(len(body)) {
		t.Fatalf("progress = %v, want 0 first and %d last", seen, len(body))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("progress went back: %v", seen)
		}
	}
}
