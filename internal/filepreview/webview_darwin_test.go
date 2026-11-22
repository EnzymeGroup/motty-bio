//go:build darwin && webviewtest

package filepreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/gfx"
)

// TestLiveWKWebViewAllPreviewKinds opens a real macOS WKWebView via Viva and
// verifies Mol* (PDB/CIF/SDF), Sequence/A3M, SVG, HTML+sibling CSS, and PDF
// end-to-end under the motty-view:// custom URL scheme.
// Run manually with: go test -tags webviewtest -v ./internal/filepreview
func TestLiveWKWebViewAllPreviewKinds(t *testing.T) {
	pdbBytes, err := os.ReadFile("/Users/jxtang/Desktop/CodeProjects/MyKit/components/molstar-viewer/typescript/test/fixtures/1UBQ.pdb")
	if err != nil {
		pdbBytes = []byte("ATOM      1  N   MET A   1      27.340  24.430   2.614  1.00 92.50           N\n" +
			"ATOM      2  CA  MET A   1      26.266  25.413   2.842  1.00 94.10           C\n" +
			"ATOM      3  C   MET A   1      26.854  26.824   2.921  1.00 93.20           C\n" +
			"ATOM      4  O   MET A   1      27.886  27.090   2.301  1.00 91.00           O\n")
	}
	cifBytes, err := os.ReadFile("/Users/jxtang/Desktop/CodeProjects/MyKit/components/molstar-viewer/typescript/test/fixtures/1CRN.cif")
	if err != nil {
		t.Fatalf("read 1CRN.cif: %v", err)
	}
	sdfBytes, err := os.ReadFile("/Users/jxtang/Desktop/CodeProjects/MyKit/components/molstar-viewer/typescript/test/fixtures/ethanol.sdf")
	if err != nil {
		t.Fatalf("read ethanol.sdf: %v", err)
	}

	// Minimal valid 1-page PDF 1.4.
	pdfBytes := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"2 0 obj<</Type/Pages/Count 1/Kids[3 0 R]>>endobj\n" +
		"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>endobj\n" +
		"xref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000056 00000 n \n0000000111 00000 n \n" +
		"trailer<</Size 4/Root 1 0 R>>\nstartxref\n178\n%%EOF\n")

	type probeCase struct {
		name      string
		fileName  string
		data      []byte
		siblings  map[string][]byte
		waitReady bool // true for Mol* ready message; false uses Eval probe after WebLoadFinished
		probeJS   string
	}
	cases := []probeCase{
		{
			name:      "PDB-MolStar",
			fileName:  "1UBQ.pdb",
			data:      pdbBytes,
			waitReady: true,
		},
		{
			name:      "mmCIF-MolStar",
			fileName:  "1CRN.cif",
			data:      cifBytes,
			waitReady: true,
		},
		{
			name:      "SDF-MolStar",
			fileName:  "ethanol.sdf",
			data:      sdfBytes,
			waitReady: true,
		},
		{
			name:     "MOL2-MolStar",
			fileName: "water.mol2",
			data: []byte("@<TRIPOS>MOLECULE\nWATER\n 3 2 0 0 0\nSMALL\nNO_CHARGES\n\n@<TRIPOS>ATOM\n" +
				"      1 O1          0.0000    0.0000    0.0000 O.3     1  HOH        0.0000\n" +
				"      2 H1          0.7570    0.5860    0.0000 H       1  HOH        0.0000\n" +
				"      3 H2         -0.7570    0.5860    0.0000 H       1  HOH        0.0000\n" +
				"@<TRIPOS>BOND\n     1    1    2 1\n     2    1    3 1\n"),
			waitReady: true,
		},
		{
			name:      "XYZ-MolStar",
			fileName:  "water.xyz",
			data:      []byte("3\nwater molecule\nO 0.000 0.000 0.000\nH 0.757 0.586 0.000\nH -0.757 0.586 0.000\n"),
			waitReady: true,
		},
		{
			name:     "A3M-Sequence",
			fileName: "hits.a3m",
			data:     []byte(">query\nACDEFGHIKLMNPQRSTVWY\n>hit1\nACdeEFGHIKLMNPQRSTVWY\n"),
			probeJS: `setTimeout(() => {
				const text = document.getElementById('stats')?.textContent || '';
				window.viva.postMessage(JSON.stringify({ type: 'probe', ok: text.includes('2 seqs'), detail: text }));
			}, 250);`,
		},
		{
			name:     "SVG-Vector",
			fileName: "plot.svg",
			data:     []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 180" width="320" height="180"><rect width="320" height="180" fill="#eef"/><text x="20" y="90" font-size="24">BioPlot</text></svg>`),
			probeJS: `setTimeout(() => {
				const img = document.getElementById('graphic');
				window.viva.postMessage(JSON.stringify({ type: 'probe', ok: Boolean(img && img.complete && img.naturalWidth === 320), detail: img ? (img.naturalWidth + 'x' + img.naturalHeight) : 'no-img' }));
			}, 300);`,
		},
		{
			name:     "HTML-With-Sibling-CSS",
			fileName: "report.html",
			data:     []byte(`<!doctype html><html><head><link rel="stylesheet" href="assets/theme.css"></head><body><h1 id="t">MultiQC</h1></body></html>`),
			siblings: map[string][]byte{"assets/theme.css": []byte(`body { background-color: rgb(12, 34, 56); }`)},
			probeJS: `setTimeout(() => {
				const bg = getComputedStyle(document.body).backgroundColor;
				window.viva.postMessage(JSON.stringify({ type: 'probe', ok: bg === 'rgb(12, 34, 56)', detail: bg }));
			}, 250);`,
		},
		{
			name:     "PDF-Native",
			fileName: "paper.pdf",
			data:     pdfBytes,
		},
	}

	var (
		web     = &app.WebHost{}
		win     *app.Window
		current *app.WebView
		idx     int
		results []string
		failed  string
		done    bool
	)

	finish := func(errMsg string) {
		if done {
			return
		}
		done = true
		failed = errMsg
		if current != nil {
			current.Close()
			current = nil
		}
		if win != nil {
			win.Close()
		}
	}

	var startNext func()
	startNext = func() {
		if current != nil {
			current.Close()
			current = nil
		}
		if idx >= len(cases) {
			finish("")
			return
		}
		tc := cases[idx]
		doc, err := Prepare(tc.fileName, tc.data, 20<<20)
		if err != nil || doc == nil {
			finish(fmt.Sprintf("%s: Prepare failed: %v", tc.name, err))
			return
		}
		source := Source{
			Doc:   func() *Document { return doc },
			Theme: func() ThemeInfo { return ThemeInfo{Dark: false, Background: 0xf6f7f9} },
			LoadSibling: func(_ context.Context, rel string) ([]byte, error) {
				if b, ok := tc.siblings[rel]; ok {
					return b, nil
				}
				return nil, os.ErrNotExist
			},
		}
		scheme := &app.WebScheme{
			Name: SchemeName,
			ServeAsync: func(req app.WebRequest, respond func(app.WebResponse)) {
				parts := strings.SplitN(strings.TrimPrefix(req.Path, "/"), "/", 3)
				if len(parts) < 3 || parts[0] != "doc" {
					respond(app.WebResponse{Status: http.StatusNotFound})
					return
				}
				resp := Resolve(context.Background(), source, parts[2])
				respond(app.WebResponse{Status: resp.Status, MIME: resp.MIME, Headers: resp.Headers, Data: resp.Data})
			},
		}
		docID := fmt.Sprintf("live-%d", idx)
		url := DocumentURL(docID, doc.EntryPath())
		var page *app.WebView
		page, err = web.Open(app.WebViewConfig{
			URL:                 url,
			Scheme:              scheme,
			Ephemeral:           true,
			BridgeMainFrameOnly: true,
			OnMessage: func(raw string) {
				var msg struct {
					Type    string `json:"type"`
					Atoms   int    `json:"atoms"`
					Mode    string `json:"mode"`
					Palette string `json:"palette"`
					Message string `json:"message"`
					OK      bool   `json:"ok"`
					Detail  string `json:"detail"`
				}
				if err := json.Unmarshal([]byte(raw), &msg); err != nil {
					return
				}
				if tc.waitReady {
					if msg.Type == "error" {
						finish(fmt.Sprintf("%s: viewer error: %s", tc.name, msg.Message))
						return
					}
					if msg.Type == "ready" {
						if msg.Atoms <= 0 {
							finish(fmt.Sprintf("%s: ready with 0 atoms", tc.name))
							return
						}
						results = append(results, fmt.Sprintf("%s: OK (atoms=%d mode=%s palette=%s)", tc.name, msg.Atoms, msg.Mode, msg.Palette))
						idx++
						startNext()
					}
					return
				}
				if msg.Type == "probe" {
					if !msg.OK {
						finish(fmt.Sprintf("%s: probe failed: %s", tc.name, msg.Detail))
						return
					}
					results = append(results, fmt.Sprintf("%s: OK (%s)", tc.name, msg.Detail))
					idx++
					startNext()
				}
			},
			OnLoad: func(load app.WebLoad) {
				if load.Phase != app.WebLoadFinished {
					return
				}
				if load.Code != app.WebLoadNoError {
					finish(fmt.Sprintf("%s: load failed: code=%v desc=%s", tc.name, load.Code, load.Description))
					return
				}
				if tc.waitReady {
					return
				}
				if tc.probeJS != "" {
					page.Eval(tc.probeJS)
					return
				}
				// PDF direct load succeeded!
				results = append(results, fmt.Sprintf("%s: OK (url=%s)", tc.name, load.URL))
				idx++
				startNext()
			},
		})
		if err != nil {
			finish(fmt.Sprintf("%s: web.Open failed: %v", tc.name, err))
			return
		}
		current = page
	}

	var inv app.Invalidator
	web.OnReady = func() {
		startNext()
		go func() {
			time.Sleep(25 * time.Second)
			inv.Post(func() {
				if !done {
					finish(fmt.Sprintf("timeout at case %d (%s); completed=%v", idx, cases[min(idx, len(cases)-1)].name, results))
				}
			})
		}()
	}

	err = app.RunApplication(func(application *app.Application) error {
		var openErr error
		win, openErr = application.OpenWindow(app.Config{
			Title:       "Motty Live Preview Smoke Test",
			Width:       720,
			Height:      540,
			Web:         web,
			Invalidator: &inv,
			Paint: func(canvas *gfx.Canvas, size gfx.Size) {
				canvas.Fill(gfx.Rect{Width: size.Width, Height: size.Height}, gfx.LightTheme().Background)
				if current != nil {
					canvas.NativeView(current.ID(), gfx.Rect{Width: size.Width, Height: size.Height})
				}
			},
		}, nil)
		return openErr
	})
	if err != nil {
		t.Fatalf("app.Run failed: %v", err)
	}
	for _, r := range results {
		t.Log(r)
	}
	if failed != "" {
		t.Fatal(failed)
	}
}
