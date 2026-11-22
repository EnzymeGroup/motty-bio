package filepreview

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDetectSupportedAndRejectedFormats(t *testing.T) {
	cases := []struct {
		name         string
		data         []byte
		wantOK       bool
		wantKind     Kind
		wantFormat   string
		wantGzip     bool
		wantEditable bool
	}{
		{"1ubq.pdb", []byte("ATOM"), true, KindStructure, "pdb", false, true},
		{"1ubq.PDB.gz", []byte{0x1f, 0x8b}, true, KindStructure, "pdb", true, false},
		{"af3_model.cif", []byte("data_1"), true, KindStructure, "cif", false, true},
		{"complex.mmcif.gz", []byte{0x1f, 0x8b}, true, KindStructure, "cif", true, false},
		{"ligands.sdf", []byte("mol"), true, KindStructure, "sdf", false, true},
		{"drug.mol2", []byte("@<TRIPOS>MOLECULE"), true, KindStructure, "mol2", false, true},
		{"traj.xyz", []byte("3\n"), true, KindStructure, "xyz", false, true},
		{"system.gro", []byte("gro\n"), true, KindStructure, "gro", false, true},
		{"hits.a3m", []byte(">q\nACDE\n"), true, KindSequence, "a3m", false, true},
		{"hits.a3m.gz", []byte{0x1f, 0x8b}, true, KindSequence, "a3m", true, false},
		{"seqs.fasta", []byte(">s1\nATGC\n"), true, KindSequence, "fasta", false, true},
		{"msa.sto", []byte("# STOCKHOLM 1.0\n"), true, KindSequence, "stockholm", false, true},
		{"msa.aln", []byte("CLUSTAL\n"), true, KindSequence, "clustal", false, true},
		{"plot.svg", []byte("<svg/>"), true, KindSVG, "svg", false, true},
		{"plot.svgz", []byte{0x1f, 0x8b}, true, KindSVG, "svg", true, false},
		{"report.html", []byte("<html></html>"), true, KindHTML, "html", false, true},
		{"paper.pdf", []byte("%PDF-1.7\n"), true, KindPDF, "pdf", false, false},
		// Rejected cases:
		{"fake.txt", []byte("%PDF-1.7\n"), false, KindNone, "", false, false},
		{"corrupt.pdf", []byte("not a pdf"), false, KindNone, "", false, false},
		{"report.html.gz", []byte{0x1f, 0x8b}, false, KindNone, "", false, false},
		{"archive.tar.gz", []byte{0x1f, 0x8b}, false, KindNone, "", false, false},
		{"photo.png", []byte{0x89, 'P', 'N', 'G'}, false, KindNone, "", false, false},
		{"main.go", []byte("package main"), false, KindNone, "", false, false},
	}
	for _, tc := range cases {
		desc, ok := Detect(tc.name, tc.data)
		if ok != tc.wantOK {
			t.Errorf("Detect(%q): ok=%v, want %v (desc=%+v)", tc.name, ok, tc.wantOK, desc)
			continue
		}
		if !ok {
			continue
		}
		if desc.Kind != tc.wantKind || desc.Format != tc.wantFormat || desc.Compressed != tc.wantGzip || desc.TextEditable != tc.wantEditable {
			t.Errorf("Detect(%q): got %+v, want kind=%v format=%s gz=%v edit=%v", tc.name, desc, tc.wantKind, tc.wantFormat, tc.wantGzip, tc.wantEditable)
		}
	}
}

func TestPrepareGzipAndZipBombLimit(t *testing.T) {
	pdb := []byte("ATOM      1  N   MET A   1      27.340  24.430   2.614  1.00 92.50           N\n" +
		"ATOM      2  CA  MET A   1      26.266  25.413   2.842  1.00 94.10           C\n" +
		"ATOM      3  N   GLN A   2      25.000  25.000   2.000  1.00 88.00           N\n")
	gz := gzipBytes(t, pdb)
	doc, err := Prepare("1ubq.pdb.gz", gz, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(doc.Data, pdb) {
		t.Fatal("decompressed PDB mismatch")
	}
	if !strings.Contains(doc.Summary, "1 chain") || !strings.Contains(doc.Summary, "2 residues") || !strings.Contains(doc.Summary, "3 atoms") {
		t.Fatalf("unexpected PDB summary: %q", doc.Summary)
	}

	huge := bytes.Repeat([]byte("A"), 4096)
	hugeGZ := gzipBytes(t, huge)
	if _, err := Prepare("bomb.fasta.gz", hugeGZ, 1024); !errors.Is(err, ErrDecompressedTooLarge) {
		t.Fatalf("expected ErrDecompressedTooLarge, got %v", err)
	}
}

func TestSummariesAcrossFormats(t *testing.T) {
	cif := []byte("data_1CRN\nloop_\n_atom_site.group_PDB\n_atom_site.auth_asym_id\n_atom_site.auth_seq_id\nATOM A 1\nATOM A 2\nATOM B 1\n")
	doc, err := Prepare("1crn.cif", cif, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Summary, "2 chains") || !strings.Contains(doc.Summary, "3 residues") || !strings.Contains(doc.Summary, "3 atoms") {
		t.Fatalf("unexpected mmCIF summary: %q", doc.Summary)
	}

	sdf := []byte("ethanol\n  prog\n\n  3  2  0  0  0  0\nC\nC\nO\nM  END\n$$$$\nwater\n  prog\n\n  1  0  0  0  0  0\nO\nM  END\n$$$$\n")
	doc, err = Prepare("ligands.sdf", sdf, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Summary, "2 molecules") || !strings.Contains(doc.Summary, "3 atoms") || !strings.Contains(doc.Summary, "2 bonds") {
		t.Fatalf("unexpected SDF summary: %q", doc.Summary)
	}

	a3m := []byte(">query\nACDEFG\n>hit1\nACdeEFG\n")
	doc, err = Prepare("msa.a3m", a3m, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Summary, "2 sequences") || !strings.Contains(doc.Summary, "max 6") {
		t.Fatalf("unexpected A3M summary: %q", doc.Summary)
	}

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 600"><circle r="10"/></svg>`)
	doc, err = Prepare("fig.svg", svg, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Summary, "800×600") {
		t.Fatalf("unexpected SVG summary: %q", doc.Summary)
	}
}

func TestResolveRoutingAndHTMLSiblingSafety(t *testing.T) {
	pdbDoc, _ := Prepare("1ubq.pdb", []byte("ATOM 1"), 1<<20)
	src := Source{
		Doc:   func() *Document { return pdbDoc },
		Theme: func() ThemeInfo { return ThemeInfo{Dark: true, Background: 0x14161a} },
	}
	if resp := Resolve(context.Background(), src, "molstar/index.html"); resp.Status != http.StatusOK || !strings.Contains(string(resp.Data), "protein-viewer.html") {
		t.Fatalf("molstar host page not served: status=%d", resp.Status)
	}
	if resp := Resolve(context.Background(), src, "molstar/vendor/molstar/molstar.js"); resp.Status != http.StatusOK || len(resp.Data) < 100000 {
		t.Fatalf("embedded molstar.js missing: status=%d len=%d", resp.Status, len(resp.Data))
	}
	metaResp := Resolve(context.Background(), src, "molstar/meta.json")
	var meta map[string]any
	if err := json.Unmarshal(metaResp.Data, &meta); err != nil || meta["dark"] != true || meta["format"] != "pdb" {
		t.Fatalf("unexpected meta.json: %s (%v)", string(metaResp.Data), err)
	}

	htmlDoc, _ := Prepare("report.html", []byte("<h1>Hi</h1>"), 1<<20)
	var requested []string
	htmlSrc := Source{
		Doc: func() *Document { return htmlDoc },
		LoadSibling: func(_ context.Context, rel string) ([]byte, error) {
			requested = append(requested, rel)
			if rel == "assets/app.css" {
				return []byte("body{color:red}"), nil
			}
			return nil, errors.New("missing")
		},
	}
	if resp := Resolve(context.Background(), htmlSrc, "site/assets/app.css"); resp.Status != http.StatusOK || resp.MIME != "text/css; charset=utf-8" {
		t.Fatalf("HTML sibling CSS failed: %+v", resp)
	}
	if _, ok := SafeRelativePath("../secret.txt"); ok {
		t.Fatal("parent traversal was allowed")
	}
	if _, ok := SafeRelativePath("/etc/passwd"); ok {
		t.Fatal("absolute path was allowed")
	}
	if resp := Resolve(context.Background(), htmlSrc, "site/../../etc/passwd"); resp.Status == http.StatusOK {
		t.Fatalf("traversal served status %d", resp.Status)
	}
	if len(requested) != 1 || requested[0] != "assets/app.css" {
		t.Fatalf("unexpected sibling requests: %v", requested)
	}
}

func TestLoopbackServerAuthAndServing(t *testing.T) {
	srv, err := NewLoopbackServer()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	doc, _ := Prepare("fig.svg", []byte(`<svg viewBox="0 0 10 10"></svg>`), 1<<20)
	id := srv.Register(Source{Doc: func() *Document { return doc }})
	defer srv.Unregister(id)

	url := srv.URL(id, doc.EntryPath())
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "SVG Viewer") {
		t.Fatalf("loopback serve failed: %d", resp.StatusCode)
	}

	// Wrong token must be rejected with 403.
	badURL := strings.Replace(url, "/t/"+srv.token+"/", "/t/deadbeef/", 1)
	badResp, err := http.Get(badURL)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for bad token, got %d", badResp.StatusCode)
	}
}
