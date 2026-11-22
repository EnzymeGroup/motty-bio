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
	"time"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/gfx"
	"motty/internal/filepreview"
	"motty/internal/sftpclient"
	"motty/internal/term"
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

func TestStructureOverlayFromSFTPSelection(t *testing.T) {
	dir := t.TempDir()
	pdb1 := []byte(
		"ATOM      1  CA  ALA A   1       0.000   0.000   0.000  1.00 92.00           C\n" +
			"ATOM      2  CA  ALA A   2       2.300   1.500   1.500  1.00 92.00           C\n" +
			"ATOM      3  CA  ALA A   3       0.500   3.800   3.000  1.00 92.00           C\n" +
			"ATOM      4  CA  ALA A   4      -1.800   2.200   4.500  1.00 92.00           C\n",
	)
	// Shifted by (+10, -5, +4) -> Kabsch should recover ~0.00 RMSD.
	pdb2 := []byte(
		"ATOM      1  CA  ALA A   1      10.000  -5.000   4.000  1.00 91.00           C\n" +
			"ATOM      2  CA  ALA A   2      12.300  -3.500   5.500  1.00 91.00           C\n" +
			"ATOM      3  CA  ALA A   3      10.500  -1.200   7.000  1.00 91.00           C\n" +
			"ATOM      4  CA  ALA A   4       8.200  -2.800   8.500  1.00 91.00           C\n",
	)
	if err := os.WriteFile(filepath.Join(dir, "ref.pdb"), pdb1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mob.pdb"), pdb2, 0o600); err != nil {
		t.Fatal(err)
	}

	host := &documentTestHost{}
	app.UseApplicationRunner(func(start func(app.Host) error) error { return start(host) })
	t.Cleanup(func() { host.Quit(); app.UseApplicationRunner(nil) })

	if err := app.RunApplication(func(application *app.Application) error {
		state, session, _, _ := openTestSFTP(t)
		state.options.OpenContentWindow = application.OpenContentWindow
		view := session.files
		view.lfolder.path = dir
		view.lfolder.rows = []sftpRow{
			{entry: sftpclient.Entry{Name: "ref.pdb"}, selected: true},
			{entry: sftpclient.Entry{Name: "mob.pdb"}, selected: true},
			{entry: sftpclient.Entry{Name: "notes.txt"}, selected: false},
		}
		if paths := selectedStructurePaths(view, true); len(paths) != 2 {
			t.Fatalf("expected 2 selected structure paths, got %v", paths)
		}
		// Selecting a non-structure file disables overlay.
		view.lfolder.rows[2].selected = true
		if paths := selectedStructurePaths(view, true); paths != nil {
			t.Fatalf("expected nil when non-structure selected, got %v", paths)
		}
		view.lfolder.rows[2].selected = false

		state.openStructureOverlay(view, true)
		deadline := time.Now().Add(3 * time.Second)
		var doc *fileDocument
		for time.Now().Before(deadline) {
			state.Wake()
			for _, candidate := range state.fileWindows.documents {
				if candidate.hasPreview() {
					doc = candidate
					break
				}
			}
			if doc != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if doc == nil {
			t.Fatal("overlay document did not finish preparing")
		}
		if !doc.isReadOnlyPreview() || len(doc.preview.doc.Overlays) != 1 {
			t.Fatalf("unexpected overlay doc state: ro=%v overlays=%d", doc.isReadOnlyPreview(), len(doc.preview.doc.Overlays))
		}
		ov := doc.preview.doc.Overlays[0]
		if !ov.Aligned || ov.RMSD > 0.01 || ov.Pairs != 4 {
			t.Fatalf("unexpected overlay alignment metrics: %+v", ov)
		}
		state.closeFileWindows()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenSiblingMessageFromPreview(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "scores.csv")
	cifPath := filepath.Join(dir, "model_0.cif")
	if err := os.WriteFile(csvPath, []byte("file,iptm\nmodel_0.cif,0.88\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cifPath, []byte("data_M0\nloop_\n_atom_site.group_PDB\n_atom_site.auth_asym_id\n_atom_site.auth_seq_id\nATOM A 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	host := &documentTestHost{}
	app.UseApplicationRunner(func(start func(app.Host) error) error { return start(host) })
	t.Cleanup(func() { host.Quit(); app.UseApplicationRunner(nil) })

	if err := app.RunApplication(func(application *app.Application) error {
		opts := freshOptions(t)
		opts.Invalidate = func() {}
		opts.OpenContentWindow = application.OpenContentWindow
		state := New(opts)
		t.Cleanup(state.Close)
		state.openFileLocation(nil, state.messages.FileWindow.Local, csvPath, true, nil, nil)

		waitDocs := func(want int) {
			t.Helper()
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				state.Wake()
				ready := 0
				for _, doc := range state.fileWindows.documents {
					if doc.hasPreview() {
						ready++
					}
				}
				if ready >= want {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("timed out waiting for %d ready preview documents", want)
		}
		waitDocs(1)

		var csvDoc *fileDocument
		for _, doc := range state.fileWindows.documents {
			csvDoc = doc
		}
		if csvDoc.preview.doc.Descriptor.Kind != filepreview.KindTable {
			t.Fatalf("expected table kind, got %v", csvDoc.preview.doc.Descriptor.Kind)
		}
		csvDoc.handlePreviewMessage(`{"type":"open-sibling","path":"model_0.cif"}`)
		waitDocs(2)

		var foundStruct bool
		for _, doc := range state.fileWindows.documents {
			if doc.name == "model_0.cif" && doc.preview.doc.Descriptor.Kind == filepreview.KindStructure {
				foundStruct = true
			}
		}
		if !foundStruct {
			t.Fatal("sibling model_0.cif did not open as structure preview")
		}
		state.closeFileWindows()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalPreviewClassifyAndOpenLocalAndRemote(t *testing.T) {
	hasKind := func(actions []detected, want actionKind) bool {
		for _, a := range actions {
			if a.kind == want {
				return true
			}
		}
		return false
	}
	for _, previewable := range []string{"./fold_model_0.cif", "runs/metrics.csv", "/tmp/ligands.smi", "~/fig.svg", "out/pae.pdb.gz"} {
		if got := classifyToken(previewable, "alice"); !hasKind(got, actionPreview) {
			t.Errorf("classifyToken(%q) = %+v, want actionPreview", previewable, got)
		}
	}
	if got := classifyToken("src/main.go", "alice"); hasKind(got, actionPreview) {
		t.Errorf("classifyToken(src/main.go) unexpectedly offered actionPreview: %+v", got)
	}

	// Remote path resolution tests
	remoteSess := &Session{
		term: &terminal{reportedDirectory: term.Cwd{Path: "/scratch/af3_run"}},
	}
	if got := resolveRemoteTerminalPath(remoteSess, "outputs/model.cif"); got != "/scratch/af3_run/outputs/model.cif" {
		t.Fatalf("relative remote path mismatch: %q", got)
	}
	if got := resolveRemoteTerminalPath(remoteSess, "/data/ref.pdb"); got != "/data/ref.pdb" {
		t.Fatalf("absolute remote path mismatch: %q", got)
	}
	if got := resolveRemoteTerminalPath(remoteSess, "~/runs/hits.csv"); got != "~/runs/hits.csv" {
		t.Fatalf("home remote path mismatch: %q", got)
	}

	// Local Cmd+Click preview opening
	dir := t.TempDir()
	pdbPath := filepath.Join(dir, "1ubq.pdb")
	if err := os.WriteFile(pdbPath, []byte("ATOM      1  CA  MET A   1       1.000   2.000   3.000  1.00 90.00           C\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	host := &documentTestHost{}
	app.UseApplicationRunner(func(start func(app.Host) error) error { return start(host) })
	t.Cleanup(func() { host.Quit(); app.UseApplicationRunner(nil) })

	if err := app.RunApplication(func(application *app.Application) error {
		opts := freshOptions(t)
		opts.Invalidate = func() {}
		opts.OpenContentWindow = application.OpenContentWindow
		state := New(opts)
		t.Cleanup(state.Close)
		localSess := &Session{
			ID:     1,
			HostID: localHostID,
			term:   &terminal{reportedDirectory: term.Cwd{Path: dir}},
		}
		if !state.openTerminalFilePreview(localSess, "1ubq.pdb") {
			t.Fatal("openTerminalFilePreview returned false for existing local PDB")
		}
		deadline := time.Now().Add(3 * time.Second)
		var opened *fileDocument
		for time.Now().Before(deadline) {
			state.Wake()
			for _, doc := range state.fileWindows.documents {
				if doc.hasPreview() {
					opened = doc
				}
			}
			if opened != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if opened == nil || opened.preview.doc.Descriptor.Kind != filepreview.KindStructure {
			t.Fatalf("expected opened PDB structure preview, got %+v", opened)
		}
		state.closeFileWindows()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
