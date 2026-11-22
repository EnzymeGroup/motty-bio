package workbench

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vivaui/viva/gfx"
	"motty/internal/filepreview"
	"motty/internal/sftpclient"
)

func TestRichPreviewFormatsDecodeAndToggleModes(t *testing.T) {
	pdb := []byte("ATOM      1  N   MET A   1      27.340  24.430   2.614  1.00 92.50           N\n")
	d := documentFixture(t)
	d.name = "1ubq.pdb"
	event := decodeFileEvent(d, pdb, nil)
	if event.err != nil {
		t.Fatalf("PDB decode failed: %v", event.err)
	}
	if event.preview == nil || event.preview.Descriptor.Kind != filepreview.KindStructure {
		t.Fatalf("expected structure preview, got %+v", event.preview)
	}
	d.raw = event.data
	d.setPreview(event.preview, nil)
	d.setText(event.decoded, event.err)
	if !d.showingPreview() || d.isReadOnlyPreview() {
		t.Fatalf("PDB should open in editable preview mode")
	}

	// Toggle to source mode, edit, toggle back -> preview updates revision and data.
	revBefore := d.preview.doc.Revision
	d.togglePreviewMode()
	if d.showingPreview() {
		t.Fatal("expected source mode after toggle")
	}
	d.loadEditor(string(pdb) + "ATOM      2  CA  MET A   1      26.266  25.413   2.842  1.00 95.00           C\n")
	if !d.dirty() {
		t.Fatal("expected dirty state after editing PDB source")
	}
	d.togglePreviewMode()
	if !d.showingPreview() {
		t.Fatal("expected preview mode after second toggle")
	}
	if d.preview.doc.Revision <= revBefore || !strings.Contains(d.preview.doc.Summary, "2 atoms") {
		t.Fatalf("preview did not reflect source edit: rev=%d summary=%q", d.preview.doc.Revision, d.preview.doc.Summary)
	}

	// Paint cleanly in headless mode (without WebHost) without panicking.
	var canvas gfx.Canvas
	d.paint(&canvas, gfx.Size{Width: 900, Height: 640})
	d.dispose()
}

func TestReadOnlyPreviewFormatsPDFAndGzippedCIF(t *testing.T) {
	d := documentFixture(t)
	d.name = "paper.pdf"
	pdf := []byte("%PDF-1.7\n1 0 obj\n<< /Type /Page >>\nendobj\n")
	ev := decodeFileEvent(d, pdf, nil)
	if ev.err != nil || ev.preview == nil || ev.preview.Descriptor.Kind != filepreview.KindPDF {
		t.Fatalf("PDF decode failed: err=%v preview=%+v", ev.err, ev.preview)
	}
	d.raw = ev.data
	d.setPreview(ev.preview, nil)
	d.setText(ev.decoded, ev.err)
	if !d.showingPreview() || !d.isReadOnlyPreview() || d.dirty() {
		t.Fatal("PDF should be read-only preview")
	}
	// Toggling source mode on a read-only PDF must stay in preview mode.
	d.togglePreviewMode()
	if !d.showingPreview() {
		t.Fatal("read-only PDF unexpectedly switched to source mode")
	}
	d.dispose()

	// Gzipped mmCIF
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write([]byte("data_1CRN\nloop_\n_atom_site.group_PDB\n_atom_site.auth_asym_id\n_atom_site.auth_seq_id\nATOM A 1\n"))
	_ = w.Close()
	d2 := documentFixture(t)
	d2.name = "model.cif.gz"
	ev2 := decodeFileEvent(d2, gz.Bytes(), nil)
	if ev2.err != nil || ev2.preview == nil || !ev2.preview.Descriptor.Compressed || ev2.preview.Descriptor.TextEditable {
		t.Fatalf("cif.gz decode failed: err=%v preview=%+v", ev2.err, ev2.preview)
	}
	d2.dispose()
}

func TestSVGAndSequenceAndHTMLPreviewBrowserAndExternalApp(t *testing.T) {
	dir := t.TempDir()
	htmlPath := filepath.Join(dir, "report.html")
	cssPath := filepath.Join(dir, "style.css")
	htmlData := []byte(`<!doctype html><link rel="stylesheet" href="style.css"><h1>FastQC</h1>`)
	cssData := []byte(`body{color:#123456}`)
	if err := os.WriteFile(htmlPath, htmlData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cssPath, cssData, 0o600); err != nil {
		t.Fatal(err)
	}

	var openedURL, openedPath string
	d := documentFixture(t)
	d.owner.options.OpenURL = func(u string) error { openedURL = u; return nil }
	d.owner.options.OpenPath = func(p string) error { openedPath = p; return nil }
	d.name = "report.html"
	d.location = htmlPath
	d.local = true

	ev := decodeFileEvent(d, htmlData, nil)
	if ev.err != nil || ev.preview == nil || ev.preview.Descriptor.Kind != filepreview.KindHTML {
		t.Fatalf("HTML decode failed: %v %+v", ev.err, ev.preview)
	}
	d.raw = ev.data
	d.setPreview(ev.preview, nil)
	d.setText(ev.decoded, ev.err)

	// Open in browser via lazy loopback server and fetch both HTML and sibling CSS!
	d.openPreviewInBrowser()
	if openedURL == "" || !strings.HasPrefix(openedURL, "http://127.0.0.1:") {
		t.Fatalf("expected loopback URL, got %q", openedURL)
	}
	resp, err := http.Get(openedURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, htmlData) {
		t.Fatalf("HTML fetch failed: %d %q", resp.StatusCode, string(body))
	}
	cssURL := strings.Replace(openedURL, "report.html", "style.css", 1)
	cssResp, err := http.Get(cssURL)
	if err != nil {
		t.Fatal(err)
	}
	cssBody, _ := io.ReadAll(cssResp.Body)
	cssResp.Body.Close()
	if cssResp.StatusCode != http.StatusOK || !bytes.Equal(cssBody, cssData) {
		t.Fatalf("sibling CSS fetch failed: %d %q", cssResp.StatusCode, string(cssBody))
	}

	// Local external open opens the file directly.
	d.openPreviewExternalApp()
	if openedPath != htmlPath {
		t.Fatalf("expected local external path %q, got %q", htmlPath, openedPath)
	}

	// Remote external open stages a private temp file and cleans it up on dispose.
	d.local = false
	d.name = "af3.cif"
	d.raw = []byte("data_af3\n")
	d.openPreviewExternalApp()
	if openedPath == htmlPath || filepath.Base(openedPath) != "af3.cif" {
		t.Fatalf("expected staged remote file af3.cif, got %q", openedPath)
	}
	if _, err := os.Stat(openedPath); err != nil {
		t.Fatalf("staged external file missing: %v", err)
	}
	stagedDir := d.preview.tempDir
	d.dispose()
	if _, err := os.Stat(stagedDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %q was not cleaned up on dispose", stagedDir)
	}
	d.owner.closeFileWindows()
}

type fakeSiblingRemote struct {
	files map[string][]byte
}

func (f *fakeSiblingRemote) Getwd() (string, error)                { return "/work", nil }
func (f *fakeSiblingRemote) ReadDir(string) ([]os.FileInfo, error) { return nil, nil }
func (f *fakeSiblingRemote) Close() error                          { return nil }
func (f *fakeSiblingRemote) Open(p string) (io.ReadCloser, error) {
	data, ok := f.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func TestRemoteHTMLSiblingAssetLoading(t *testing.T) {
	d := documentFixture(t)
	d.local = false
	d.location = "/work/run1/multiqc_report.html"
	d.name = "multiqc_report.html"
	ev := decodeFileEvent(d, []byte("<html></html>"), nil)
	d.setPreview(ev.preview, func() (sftpclient.Remote, error) {
		return &fakeSiblingRemote{files: map[string][]byte{
			"/work/run1/assets/plot.js": []byte("console.log('ok')"),
		}}, nil
	})
	defer d.dispose()

	got, err := d.loadPreviewSibling(context.Background(), "assets/plot.js")
	if err != nil || string(got) != "console.log('ok')" {
		t.Fatalf("remote sibling fetch failed: %q %v", string(got), err)
	}
	// Second read hits cache even if remote opener is gone.
	d.preview.remoteOpener = nil
	got2, err := d.loadPreviewSibling(context.Background(), "assets/plot.js")
	if err != nil || string(got2) != "console.log('ok')" {
		t.Fatalf("cached remote sibling fetch failed: %q %v", string(got2), err)
	}
}
