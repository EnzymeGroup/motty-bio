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
	pdbBytes := []byte("HEADER    EXPERIMENTAL TEST STRUCTURE\n" +
		"EXPDTA    X-RAY DIFFRACTION\n" +
		"ATOM      1  N   ALA A   1       0.000   0.000   0.000  1.00 18.20           N\n" +
		"ATOM      2  CA  ALA A   1       1.458   0.000   0.000  1.00 17.50           C\n" +
		"ATOM      3  C   ALA A   1       2.009   1.420   0.000  1.00 19.10           C\n" +
		"ATOM      4  O   ALA A   1       1.251   2.390   0.000  1.00 20.40           O\n" +
		"ATOM      5  N   GLY A   2       3.330   1.530   0.000  1.00 16.80           N\n" +
		"ATOM      6  CA  GLY A   2       4.000   2.820   0.000  1.00 15.90           C\n" +
		"ATOM      7  C   GLY A   2       5.510   2.660   0.000  1.00 16.40           C\n" +
		"ATOM      8  O   GLY A   2       6.040   1.550   0.000  1.00 18.00           O\n" +
		"ATOM      9  N   SER A   3       6.190   3.800   0.000  1.00 21.30           N\n" +
		"ATOM     10  CA  SER A   3       7.640   3.850   0.000  1.00 22.10           C\n" +
		"ATOM     11  C   SER A   3       8.180   5.270   0.000  1.00 23.00           C\n" +
		"ATOM     12  O   SER A   3       7.420   6.240   0.000  1.00 24.50           O\n" +
		"ATOM     13  N   VAL A   4       9.500   5.380   0.000  1.00 19.80           N\n" +
		"ATOM     14  CA  VAL A   4      10.170   6.670   0.000  1.00 18.70           C\n" +
		"ATOM     15  C   VAL A   4      11.680   6.510   0.000  1.00 20.20           C\n" +
		"ATOM     16  O   VAL A   4      12.210   5.400   0.000  1.00 21.10           O\n" +
		"END\n")
	cifBytes := []byte(`data_1CRN
#
_entry.id 1CRN
_exptl.entry_id 1CRN
_exptl.method 'X-RAY DIFFRACTION'
#
loop_
_entity.id
_entity.type
1 polymer
#
loop_
_entity_poly.entity_id
_entity_poly.type
1 polypeptide(L)
#
loop_
_atom_site.group_PDB
_atom_site.id
_atom_site.type_symbol
_atom_site.label_atom_id
_atom_site.label_alt_id
_atom_site.label_comp_id
_atom_site.label_asym_id
_atom_site.label_entity_id
_atom_site.label_seq_id
_atom_site.pdbx_PDB_ins_code
_atom_site.Cartn_x
_atom_site.Cartn_y
_atom_site.Cartn_z
_atom_site.occupancy
_atom_site.B_iso_or_equiv
_atom_site.auth_seq_id
_atom_site.auth_asym_id
_atom_site.pdbx_PDB_model_num
ATOM 1  N N  . THR A 1 1 ? 0.000  0.000 0.000 1.00 12.50 1 A 1
ATOM 2  C CA . THR A 1 1 ? 1.458  0.000 0.000 1.00 11.80 1 A 1
ATOM 3  C C  . THR A 1 1 ? 2.009  1.420 0.000 1.00 12.10 1 A 1
ATOM 4  O O  . THR A 1 1 ? 1.251  2.390 0.000 1.00 13.40 1 A 1
ATOM 5  N N  . THR A 1 2 ? 3.330  1.530 0.000 1.00 10.90 2 A 1
ATOM 6  C CA . THR A 1 2 ? 4.000  2.820 0.000 1.00 10.40 2 A 1
ATOM 7  C C  . THR A 1 2 ? 5.510  2.660 0.000 1.00 11.00 2 A 1
ATOM 8  O O  . THR A 1 2 ? 6.040  1.550 0.000 1.00 11.90 2 A 1
ATOM 9  N N  . CYS A 1 3 ? 6.190  3.800 0.000 1.00 14.20 3 A 1
ATOM 10 C CA . CYS A 1 3 ? 7.640  3.850 0.000 1.00 14.80 3 A 1
ATOM 11 C C  . CYS A 1 3 ? 8.180  5.270 0.000 1.00 15.10 3 A 1
ATOM 12 O O  . CYS A 1 3 ? 7.420  6.240 0.000 1.00 16.00 3 A 1
ATOM 13 N N  . CYS A 1 4 ? 9.500  5.380 0.000 1.00 13.50 4 A 1
ATOM 14 C CA . CYS A 1 4 ? 10.170 6.670 0.000 1.00 13.10 4 A 1
ATOM 15 C C  . CYS A 1 4 ? 11.680 6.510 0.000 1.00 14.00 4 A 1
ATOM 16 O O  . CYS A 1 4 ? 12.210 5.400 0.000 1.00 14.90 4 A 1
#
`)
	sdfBytes := []byte("ethanol\n  motty-test\n\n" +
		"  3  2  0  0  0  0  0  0  0  0999 V2000\n" +
		"    0.0000    0.0000    0.0000 C   0  0  0  0  0  0  0  0  0  0  0  0\n" +
		"    1.5400    0.0000    0.0000 C   0  0  0  0  0  0  0  0  0  0  0  0\n" +
		"    2.0400    1.3400    0.0000 O   0  0  0  0  0  0  0  0  0  0  0  0\n" +
		"  1  2  1  0  0  0  0\n" +
		"  2  3  1  0  0  0  0\n" +
		"M  END\n$$$$\n")

	// Minimal valid 1-page PDF 1.4.
	pdfBytes := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"2 0 obj<</Type/Pages/Count 1/Kids[3 0 R]>>endobj\n" +
		"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>endobj\n" +
		"xref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000056 00000 n \n0000000111 00000 n \n" +
		"trailer<</Size 4/Root 1 0 R>>\nstartxref\n178\n%%EOF\n")

	type probeCase struct {
		name            string
		fileName        string
		data            []byte
		doc             *Document // optional pre-prepared Document (e.g. PrepareOverlay)
		siblings        map[string][]byte
		siblingNames    []string
		waitReady       bool // true for Mol* ready message; false uses Eval probe after WebLoadFinished
		wantMinOverlays int
		probeJS         string
	}
	shiftedPDB := transformPDB(pdbBytes, rotXYZ(0.6, -0.4, 0.9), vec3{18.0, -12.0, 9.0})
	overlayDoc, err := PrepareOverlay([]OverlayInput{
		{Name: "1UBQ_ref.pdb", Data: pdbBytes},
		{Name: "1UBQ_mob.pdb", Data: shiftedPDB},
	}, true, 20<<20)
	if err != nil {
		t.Fatalf("PrepareOverlay: %v", err)
	}
	af3CIF := []byte(`data_elof
#
_entry.id elof
#
loop_
_entity.id
_entity.type
1 polymer
2 non-polymer
#
loop_
_entity_poly.entity_id
_entity_poly.type
1 polypeptide(L)
#
loop_
_atom_site.group_PDB
_atom_site.id
_atom_site.type_symbol
_atom_site.label_atom_id
_atom_site.label_alt_id
_atom_site.label_comp_id
_atom_site.label_asym_id
_atom_site.label_entity_id
_atom_site.label_seq_id
_atom_site.pdbx_PDB_ins_code
_atom_site.Cartn_x
_atom_site.Cartn_y
_atom_site.Cartn_z
_atom_site.occupancy
_atom_site.B_iso_or_equiv
_atom_site.auth_seq_id
_atom_site.auth_asym_id
_atom_site.pdbx_PDB_model_num
ATOM   1  N N   . ALA A 1 1 ? 0.000  0.000 0.000 1.00 96.50 1 A 1
ATOM   2  C CA  . ALA A 1 1 ? 1.458  0.000 0.000 1.00 96.50 1 A 1
ATOM   3  C C   . ALA A 1 1 ? 2.009  1.420 0.000 1.00 96.50 1 A 1
ATOM   4  O O   . ALA A 1 1 ? 1.251  2.390 0.000 1.00 96.50 1 A 1
ATOM   5  N N   . GLY A 1 2 ? 3.330  1.530 0.000 1.00 81.20 2 A 1
ATOM   6  C CA  . GLY A 1 2 ? 4.000  2.820 0.000 1.00 81.20 2 A 1
ATOM   7  C C   . GLY A 1 2 ? 5.510  2.660 0.000 1.00 81.20 2 A 1
ATOM   8  O O   . GLY A 1 2 ? 6.040  1.550 0.000 1.00 81.20 2 A 1
ATOM   9  N N   . SER A 1 3 ? 6.190  3.800 0.000 1.00 61.40 3 A 1
ATOM   10 C CA  . SER A 1 3 ? 7.640  3.850 0.000 1.00 61.40 3 A 1
ATOM   11 C C   . SER A 1 3 ? 8.180  5.270 0.000 1.00 61.40 3 A 1
ATOM   12 O O   . SER A 1 3 ? 7.420  6.240 0.000 1.00 61.40 3 A 1
ATOM   13 N N   . VAL A 1 4 ? 9.500  5.380 0.000 1.00 34.80 4 A 1
ATOM   14 C CA  . VAL A 1 4 ? 10.170 6.670 0.000 1.00 34.80 4 A 1
ATOM   15 C C   . VAL A 1 4 ? 11.680 6.510 0.000 1.00 34.80 4 A 1
ATOM   16 O O   . VAL A 1 4 ? 12.210 5.400 0.000 1.00 34.80 4 A 1
HETATM 17 C C1  . LIG B 2 . ? 15.000 2.000 0.000 1.00 88.00 1 B 1
HETATM 18 O O1  . LIG B 2 . ? 16.250 2.000 0.000 1.00 42.00 1 B 1
#
`)
	esmfoldPDB := []byte(
		"ATOM      1  N   ALA A   1       0.000   0.000   0.000  1.00  0.95           N\n" +
			"ATOM      2  CA  ALA A   1       1.458   0.000   0.000  1.00  0.95           C\n" +
			"ATOM      3  C   ALA A   1       2.009   1.420   0.000  1.00  0.95           C\n" +
			"ATOM      4  O   ALA A   1       1.251   2.390   0.000  1.00  0.95           O\n" +
			"ATOM      5  N   GLY A   2       3.330   1.530   0.000  1.00  0.78           N\n" +
			"ATOM      6  CA  GLY A   2       4.000   2.820   0.000  1.00  0.78           C\n" +
			"ATOM      7  C   GLY A   2       5.510   2.660   0.000  1.00  0.78           C\n" +
			"ATOM      8  O   GLY A   2       6.040   1.550   0.000  1.00  0.78           O\n" +
			"ATOM      9  N   SER A   3       6.190   3.800   0.000  1.00  0.58           N\n" +
			"ATOM     10  CA  SER A   3       7.640   3.850   0.000  1.00  0.58           C\n" +
			"ATOM     11  C   SER A   3       8.180   5.270   0.000  1.00  0.58           C\n" +
			"ATOM     12  O   SER A   3       7.420   6.240   0.000  1.00  0.58           O\n" +
			"ATOM     13  N   VAL A   4       9.500   5.380   0.000  1.00  0.32           N\n" +
			"ATOM     14  CA  VAL A   4      10.170   6.670   0.000  1.00  0.32           C\n" +
			"ATOM     15  C   VAL A   4      11.680   6.510   0.000  1.00  0.32           C\n" +
			"ATOM     16  O   VAL A   4      12.210   5.400   0.000  1.00  0.32           O\n" +
			"END\n")
	maQACIF := []byte(`data_AF_P00001
#
_entry.id AF_P00001
#
loop_
_entity.id
_entity.type
1 polymer
#
loop_
_entity_poly.entity_id
_entity_poly.type
1 polypeptide(L)
#
loop_
_ma_qa_metric.id
_ma_qa_metric.name
_ma_qa_metric.type
_ma_qa_metric.mode
_ma_qa_metric.software_group_id
1 pLDDT pLDDT local 1
#
loop_
_ma_qa_metric_local.ordinal_id
_ma_qa_metric_local.model_id
_ma_qa_metric_local.label_asym_id
_ma_qa_metric_local.label_seq_id
_ma_qa_metric_local.label_comp_id
_ma_qa_metric_local.metric_id
_ma_qa_metric_local.metric_value
1 1 A 1 ALA 1 95.0
2 1 A 2 GLY 1 75.0
3 1 A 3 SER 1 55.0
4 1 A 4 VAL 1 40.0
#
loop_
_atom_site.group_PDB
_atom_site.id
_atom_site.type_symbol
_atom_site.label_atom_id
_atom_site.label_alt_id
_atom_site.label_comp_id
_atom_site.label_asym_id
_atom_site.label_entity_id
_atom_site.label_seq_id
_atom_site.pdbx_PDB_ins_code
_atom_site.Cartn_x
_atom_site.Cartn_y
_atom_site.Cartn_z
_atom_site.occupancy
_atom_site.B_iso_or_equiv
_atom_site.auth_seq_id
_atom_site.auth_asym_id
_atom_site.pdbx_PDB_model_num
ATOM 1  N N  . ALA A 1 1 ? 0.000  0.000 0.000 1.00 0.00 1 A 1
ATOM 2  C CA . ALA A 1 1 ? 1.458  0.000 0.000 1.00 0.00 1 A 1
ATOM 3  C C  . ALA A 1 1 ? 2.009  1.420 0.000 1.00 0.00 1 A 1
ATOM 4  O O  . ALA A 1 1 ? 1.251  2.390 0.000 1.00 0.00 1 A 1
ATOM 5  N N  . GLY A 1 2 ? 3.330  1.530 0.000 1.00 0.00 2 A 1
ATOM 6  C CA . GLY A 1 2 ? 4.000  2.820 0.000 1.00 0.00 2 A 1
ATOM 7  C C  . GLY A 1 2 ? 5.510  2.660 0.000 1.00 0.00 2 A 1
ATOM 8  O O  . GLY A 1 2 ? 6.040  1.550 0.000 1.00 0.00 2 A 1
ATOM 9  N N  . SER A 1 3 ? 6.190  3.800 0.000 1.00 0.00 3 A 1
ATOM 10 C CA . SER A 1 3 ? 7.640  3.850 0.000 1.00 0.00 3 A 1
ATOM 11 C C  . SER A 1 3 ? 8.180  5.270 0.000 1.00 0.00 3 A 1
ATOM 12 O O  . SER A 1 3 ? 7.420  6.240 0.000 1.00 0.00 3 A 1
ATOM 13 N N  . VAL A 1 4 ? 9.500  5.380 0.000 1.00 0.00 4 A 1
ATOM 14 C CA . VAL A 1 4 ? 10.170 6.670 0.000 1.00 0.00 4 A 1
ATOM 15 C C  . VAL A 1 4 ? 11.680 6.510 0.000 1.00 0.00 4 A 1
ATOM 16 O O  . VAL A 1 4 ? 12.210 5.400 0.000 1.00 0.00 4 A 1
#
`)
	plddtBandProbeJS := `setTimeout(() => {
		try {
			const w = document.getElementById('viewer').contentWindow;
			const state = w.eval('({ plugin, representation, residueIndex, ligandRep, ligandComponent, molstar, palette: baseStyle.palette })');
			const cellObj = (sel) => sel ? state.plugin.state.data.cells.get(sel.ref)?.obj : null;
			const reg = state.plugin.representation.structure.themes.colorThemeRegistry;
			const hasBuiltin = reg.get('plddt-confidence').name === 'plddt-confidence';
			const hasMotty = reg.get('motty-plddt').name === 'motty-plddt';
			const cell = state.representation?.cell || (state.representation ? state.plugin.state.data.cells.get(state.representation.ref) : null);
			const theme = cell?.obj?.data?.repr?.theme?.color;
			if (!theme) {
				const diag = w.eval("(() => {" +
					"const layer = layers.get('Whole structure');" +
					"const repCell = representation ? plugin.state.data.cells.get(representation.ref) : null;" +
					"const compCell = layer?.component ? plugin.state.data.cells.get(layer.component.ref) : null;" +
					"const ligCell = ligandRep ? plugin.state.data.cells.get(ligandRep.ref) : null;" +
					"return JSON.stringify({" +
					"cellsSize: plugin.state.data.cells.size," +
					"transformsSize: plugin.state.data.tree.transforms.size," +
					"compRef: layer?.component?.ref," +
					"compStatus: compCell?.status," +
					"repRef: representation?.ref," +
					"repStatus: repCell?.status," +
					"repErr: repCell?.errorText," +
					"repHasObj: !!repCell?.obj," +
					"repDataKeys: repCell?.obj?.data ? Object.keys(repCell.obj.data) : []," +
					"reprKeys: repCell?.obj?.data?.repr ? Object.keys(repCell.obj.data.repr) : []," +
					"ligRef: ligandRep?.ref," +
					"ligStatus: ligCell?.status," +
					"ligErr: ligCell?.errorText," +
					"logs: plugin.log.entries.toArray().map(e => e.type + ':' + e.message)" +
					"});" +
					"})()");
				window.viva.postMessage(JSON.stringify({ type: 'probe', ok: false, detail: diag }));
				return;
			}
			const S = state.molstar.lib.structure;
			const hex = (c) => '#' + Number(c).toString(16).padStart(6, '0');
			const colors = ['A:1', 'A:2', 'A:3', 'A:4'].map(k => {
				const loci = state.residueIndex.get(k);
				const loc = loci ? S.StructureElement.Loci.getFirstLocation(loci) : null;
				return loc ? hex(theme.color(loc)) : 'missing';
			});
			let ligColors = [];
			const ligTheme = cellObj(state.ligandRep)?.data?.repr?.theme?.color;
			if (ligTheme && state.ligandComponent?.obj?.data) {
				const lStruct = state.ligandComponent.obj.data;
				for (const u of lStruct.units) {
					for (const el of u.elements) {
						ligColors.push(hex(ligTheme.color(S.StructureElement.Location.create(lStruct, u, el))));
					}
				}
			}
			const legendVis = document.getElementById('plddt-legend')?.classList.contains('visible');
			const meanTxt = document.getElementById('plddt-mean')?.textContent || '';
			const want = ['#0053d6', '#65cbf3', '#ffdb13', '#ff7d45'];
			const polyOK = colors.join(',') === want.join(',');
			const ligOK = ligColors.length === 0 || ligColors.join(',') === '#65cbf3,#ff7d45';
			const ok = state.palette === 'plddt' && hasBuiltin && hasMotty && polyOK && ligOK && legendVis && meanTxt.includes('Mean');
			window.viva.postMessage(JSON.stringify({ type: 'probe', ok, detail: 'palette=' + state.palette + ' builtin=' + hasBuiltin + ' colors=' + colors.join(',') + ' lig=' + ligColors.join(',') + ' mean=' + meanTxt }));
		} catch (e) {
			window.viva.postMessage(JSON.stringify({ type: 'probe', ok: false, detail: 'err=' + String(e) }));
		}
	}, 250);`
	cases := []probeCase{
		{
			name:      "AF3-mmCIF-pLDDT",
			fileName:  "elof_model.cif",
			data:      af3CIF,
			waitReady: true,
			probeJS:   plddtBandProbeJS,
		},
		{
			name:      "ESMFold-01-pLDDT",
			fileName:  "esmfold_pred.pdb",
			data:      esmfoldPDB,
			waitReady: true,
			probeJS:   plddtBandProbeJS,
		},
		{
			name:      "MA-QA-mmCIF-pLDDT",
			fileName:  "AF-P00001-F1-model_v4.cif",
			data:      maQACIF,
			waitReady: true,
			probeJS:   plddtBandProbeJS,
		},
		{
			name:      "PDB-MolStar",
			fileName:  "1UBQ.pdb",
			data:      pdbBytes,
			waitReady: true,
		},
		{
			name:            "Overlay-MolStar",
			doc:             overlayDoc,
			waitReady:       true,
			wantMinOverlays: 2,
		},
		{
			name:      "mmCIF-MolStar",
			fileName:  "1CRN.cif",
			data:      cifBytes,
			waitReady: true,
			probeJS: `setTimeout(() => {
				try {
					const sel = document.getElementById('palette');
					const initPalette = sel.value;
					sel.value = 'plddt';
					sel.dispatchEvent(new Event('change'));
					setTimeout(() => {
						const w = document.getElementById('viewer').contentWindow;
						const state = w.eval('({ palette: baseStyle.palette, theme: plugin.state.data.cells.get(representation?.ref)?.obj?.data?.repr?.theme?.color })');
						const S = w.molstar.lib.structure;
						const loci = w.eval('residueIndex.values().next().value');
						const loc = loci ? S.StructureElement.Loci.getFirstLocation(loci) : null;
						const col = loc && state.theme ? '#' + Number(state.theme.color(loc)).toString(16).padStart(6, '0') : 'none';
						const ok = initPalette === 'secondary' && state.palette === 'plddt' && ['#0053d6','#65cbf3','#ffdb13','#ff7d45'].includes(col);
						window.viva.postMessage(JSON.stringify({ type: 'probe', ok, detail: 'init=' + initPalette + ' switched=' + state.palette + ' sample=' + col }));
					}, 350);
				} catch (e) {
					window.viva.postMessage(JSON.stringify({ type: 'probe', ok: false, detail: 'err=' + String(e) }));
				}
			}, 200);`,
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
			name:     "AF3-Confidence",
			fileName: "fold_confidences_0.json",
			data: []byte(`{
				"ranking_score": 0.87,
				"iptm": 0.84,
				"ptm": 0.91,
				"fraction_disordered": 0.04,
				"has_clash": 0,
				"token_chain_ids": ["A","A","A","B","B","B"],
				"atom_plddts": [94.2, 91.8, 88.5, 76.4, 82.1, 90.0],
				"chain_pair_iptm": [[0.92, 0.84], [0.84, 0.89]],
				"pae": [
					[0.4, 1.2, 2.5, 9.8, 11.2, 12.0],
					[1.1, 0.3, 1.4, 8.9, 10.1, 11.5],
					[2.3, 1.3, 0.5, 7.6, 8.4, 9.2],
					[9.5, 8.7, 7.4, 0.5, 1.6, 2.8],
					[11.0, 10.0, 8.5, 1.5, 0.4, 1.7],
					[12.1, 11.4, 9.1, 2.7, 1.6, 0.5]
				]
			}`),
			siblingNames: []string{"fold_confidences_0.json", "fold_model_0.cif"},
			probeJS: `setTimeout(() => {
				const kpis = document.querySelectorAll('.kpi').length;
				const canvases = document.querySelectorAll('canvas').length;
				const paired = document.getElementById('paired-select')?.value || '';
				const ok = kpis >= 4 && canvases >= 2 && paired === 'fold_model_0.cif';
				window.viva.postMessage(JSON.stringify({ type: 'probe', ok, detail: 'kpis=' + kpis + ' canvases=' + canvases + ' paired=' + paired }));
			}, 350);`,
		},
		{
			name:     "CSV-Table",
			fileName: "scores.csv",
			data:     []byte("id,iptm,rmsd,smiles,structure\nm1,0.89,1.12,CCO,fold_model_0.cif\nm2,0.72,2.85,CC(=O)Oc1ccccc1C(=O)O,fold_model_1.cif\n"),
			probeJS: `setTimeout(() => {
				const rowsBefore = document.querySelectorAll('#tbody tr').length;
				const fileLinks = document.querySelectorAll('.file-link').length;
				const smiLink = document.querySelector('.smi-link');
				if (smiLink) smiLink.click();
				const filter = document.getElementById('filter');
				filter.value = 'iptm > 0.8';
				filter.dispatchEvent(new Event('input'));
				setTimeout(() => {
					const rowsAfter = document.querySelectorAll('#tbody tr').length;
					const svgNodes = document.querySelectorAll('#smi-svg svg path, #smi-svg svg line').length;
					const ok = rowsBefore === 2 && fileLinks === 2 && rowsAfter === 1 && svgNodes > 0;
					window.viva.postMessage(JSON.stringify({ type: 'probe', ok, detail: 'before=' + rowsBefore + ' after=' + rowsAfter + ' links=' + fileLinks + ' smiSvgNodes=' + svgNodes }));
				}, 200);
			}, 300);`,
		},
		{
			name:     "SMILES-2D",
			fileName: "ligands.smi",
			data: []byte("SMILES Name\n" +
				"CC(=O)Oc1ccccc1C(=O)O Aspirin\n" +
				"CN1C=NC2=C1C(=O)N(C(=O)N2C)C Caffeine\n" +
				"CC(C)Cc1ccc(cc1)C(C)C(=O)O Ibuprofen\n" +
				"CCO.CC(=O)O>>CCOC(=O)C.O Esterification\n"),
			probeJS: `setTimeout(() => {
				const cards = [...document.querySelectorAll('.card')];
				const drawn = cards.filter(c => c.querySelectorAll('svg path, svg line').length > 0).length;
				const formulas = cards.map(c => c.querySelector('.mol-formula')?.textContent || '').filter(Boolean);
				const ok = cards.length === 4 && drawn === 4 && formulas.length >= 3;
				window.viva.postMessage(JSON.stringify({ type: 'probe', ok, detail: 'cards=' + cards.length + ' drawn=' + drawn + ' formulas=' + formulas.join(',') }));
			}, 450);`,
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
		doc := tc.doc
		var err error
		if doc == nil {
			doc, err = Prepare(tc.fileName, tc.data, 20<<20)
			if err != nil || doc == nil {
				finish(fmt.Sprintf("%s: Prepare failed: %v", tc.name, err))
				return
			}
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
			ListSiblings: func(context.Context) ([]string, error) {
				return tc.siblingNames, nil
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
					Type         string `json:"type"`
					Atoms        int    `json:"atoms"`
					Mode         string `json:"mode"`
					Palette      string `json:"palette"`
					OverlayCount int    `json:"overlayCount"`
					Message      string `json:"message"`
					OK           bool   `json:"ok"`
					Detail       string `json:"detail"`
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
						if tc.wantMinOverlays > 0 && msg.OverlayCount < tc.wantMinOverlays {
							finish(fmt.Sprintf("%s: expected >=%d overlays, got %d", tc.name, tc.wantMinOverlays, msg.OverlayCount))
							return
						}
						if tc.probeJS != "" {
							page.Eval(tc.probeJS)
							return
						}
						results = append(results, fmt.Sprintf("%s: OK (atoms=%d mode=%s palette=%s overlays=%d)", tc.name, msg.Atoms, msg.Mode, msg.Palette, msg.OverlayCount))
						idx++
						startNext()
						return
					}
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
			time.Sleep(40 * time.Second)
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
