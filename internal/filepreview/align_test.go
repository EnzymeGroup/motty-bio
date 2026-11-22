package filepreview

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func rotXYZ(ax, ay, az float64) mat3 {
	cx, sx := math.Cos(ax), math.Sin(ax)
	cy, sy := math.Cos(ay), math.Sin(ay)
	cz, sz := math.Cos(az), math.Sin(az)
	rx := mat3{{1, 0, 0}, {0, cx, -sx}, {0, sx, cx}}
	ry := mat3{{cy, 0, sy}, {0, 1, 0}, {-sy, 0, cy}}
	rz := mat3{{cz, -sz, 0}, {sz, cz, 0}, {0, 0, 1}}
	return mat3Mul(rz, mat3Mul(ry, rx))
}

func samplePDB(R mat3, shift vec3) []byte {
	// Six non-coplanar Cα + N atoms forming a small helical segment.
	base := [][3]float64{
		{0.0, 0.0, 0.0},
		{2.3, 1.5, 1.5},
		{0.5, 3.8, 3.0},
		{-1.8, 2.2, 4.5},
		{-0.1, -0.1, 6.0},
		{2.2, 1.4, 7.5},
	}
	var b strings.Builder
	serial := 1
	for i, c := range base {
		for _, spec := range []struct {
			name string
			off  [3]float64
		}{
			{"N", [3]float64{-0.9, -0.5, -0.3}},
			{"CA", [3]float64{0, 0, 0}},
		} {
			p := vec3{c[0] + spec.off[0], c[1] + spec.off[1], c[2] + spec.off[2]}
			v := mat3MulVec(R, p)
			fmt.Fprintf(&b, "ATOM  %5d  %-3s ALA A%4d    %8.3f%8.3f%8.3f  1.00 92.50           C\n",
				serial, spec.name, i+1, v[0]+shift[0], v[1]+shift[1], v[2]+shift[2])
			serial++
		}
	}
	b.WriteString("END\n")
	return []byte(b.String())
}

func sampleCIF(R mat3, shift vec3) []byte {
	base := [][3]float64{
		{0.0, 0.0, 0.0},
		{2.3, 1.5, 1.5},
		{0.5, 3.8, 3.0},
		{-1.8, 2.2, 4.5},
		{-0.1, -0.1, 6.0},
		{2.2, 1.4, 7.5},
	}
	var b strings.Builder
	b.WriteString("data_TEST\nloop_\n")
	b.WriteString("_atom_site.group_PDB\n")
	b.WriteString("_atom_site.id\n")
	b.WriteString("_atom_site.label_atom_id\n")
	b.WriteString("_atom_site.auth_asym_id\n")
	b.WriteString("_atom_site.auth_seq_id\n")
	b.WriteString("_atom_site.Cartn_x\n")
	b.WriteString("_atom_site.Cartn_y\n")
	b.WriteString("_atom_site.Cartn_z\n")
	for i, c := range base {
		v := mat3MulVec(R, vec3{c[0], c[1], c[2]})
		fmt.Fprintf(&b, "ATOM %d CA A %d %.3f %.3f %.3f\n",
			i+1, i+1, v[0]+shift[0], v[1]+shift[1], v[2]+shift[2])
	}
	return []byte(b.String())
}

func TestKabschRecoversExactRigidMotionAndForbidsReflection(t *testing.T) {
	ref := []vec3{
		{1.2, -0.4, 2.5},
		{4.1, 1.8, -1.2},
		{-2.3, 3.5, 0.9},
		{0.7, -2.9, -3.1},
		{3.3, -1.1, 4.4},
		{-1.5, 0.6, 5.2},
	}
	rot := rotXYZ(0.9, -1.3, 2.1)
	shift := vec3{14.5, -22.0, 9.75}
	mob := make([]vec3, len(ref))
	for i, p := range ref {
		r := mat3MulVec(rot, p)
		mob[i] = vec3{r[0] + shift[0], r[1] + shift[1], r[2] + shift[2]}
	}

	R, tr, rmsd, before := kabsch(ref, mob)
	if before < 10.0 {
		t.Fatalf("expected large beforeRMSD, got %.4f", before)
	}
	if rmsd > 1e-10 {
		t.Fatalf("expected near-zero post-alignment RMSD, got %g", rmsd)
	}
	if det := mat3Det(R); math.Abs(det-1.0) > 1e-9 {
		t.Fatalf("expected proper rotation det=+1, got %.12f", det)
	}
	rtr := mat3Mul(mat3Transpose(R), R)
	for i := range 3 {
		for j := range 3 {
			want := 0.0
			if i == j {
				want = 1.0
			}
			if math.Abs(rtr[i][j]-want) > 1e-9 {
				t.Fatalf("R is not orthogonal at (%d,%d): got %.12f", i, j, rtr[i][j])
			}
		}
	}
	for i := range ref {
		got := mat3MulVec(R, mob[i])
		for k := range 3 {
			if math.Abs(got[k]+tr[k]-ref[i][k]) > 1e-9 {
				t.Fatalf("point %d axis %d mismatch: got %.9f want %.9f", i, k, got[k]+tr[k], ref[i][k])
			}
		}
	}

	// Mirror-reflected point cloud must still yield det(R) == +1 (proper rotation).
	mirrored := make([]vec3, len(ref))
	for i, p := range ref {
		mirrored[i] = vec3{-p[0] + 5, p[1] - 2, p[2] + 3}
	}
	Rm, _, _, _ := kabsch(ref, mirrored)
	if det := mat3Det(Rm); math.Abs(det-1.0) > 1e-9 {
		t.Fatalf("kabsch returned reflection det=%.12f", det)
	}
}

func TestAlignStructurePDBAndMMCIFAndPrepareOverlay(t *testing.T) {
	id := mat3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	refPDB := samplePDB(id, vec3{0, 0, 0})
	mobPDB := samplePDB(rotXYZ(1.1, 0.7, -0.8), vec3{25.0, -18.0, 12.0})
	mobCIF := sampleCIF(rotXYZ(-0.6, 1.4, 0.5), vec3{-15.0, 30.0, -9.0})

	resPDB := AlignStructure("pdb", refPDB, "pdb", mobPDB)
	if !resPDB.Aligned || resPDB.Pairs != 6 || resPDB.RMSD > 0.01 || resPDB.BeforeRMSD < 20 || resPDB.TMScore < 0.99 {
		t.Fatalf("unexpected PDB->PDB alignment: %+v", resPDB)
	}
	// Re-checking alignment on the rewritten PDB bytes must yield ~0 beforeRMSD.
	checkPDB := AlignStructure("pdb", refPDB, "pdb", resPDB.AlignedData)
	if checkPDB.BeforeRMSD > 0.01 {
		t.Fatalf("rewritten PDB coordinates not superposed: beforeRMSD=%.4f", checkPDB.BeforeRMSD)
	}

	// Cross-format PDB reference with mmCIF mobile.
	resCIF := AlignStructure("pdb", refPDB, "cif", mobCIF)
	if !resCIF.Aligned || resCIF.Pairs != 6 || resCIF.RMSD > 0.01 || resCIF.TMScore < 0.99 {
		t.Fatalf("unexpected PDB->CIF alignment: %+v", resCIF)
	}
	checkCIF := AlignStructure("pdb", refPDB, "cif", resCIF.AlignedData)
	if checkCIF.BeforeRMSD > 0.01 {
		t.Fatalf("rewritten mmCIF coordinates not superposed: beforeRMSD=%.4f", checkCIF.BeforeRMSD)
	}

	// PrepareOverlay builds a read-only multi-structure document.
	doc, err := PrepareOverlay([]OverlayInput{
		{Name: "ref.pdb", Data: refPDB},
		{Name: "model1.pdb", Data: mobPDB},
		{Name: "model2.cif", Data: mobCIF},
	}, true, 1<<20)
	if err != nil {
		t.Fatalf("PrepareOverlay failed: %v", err)
	}
	if doc.Descriptor.Kind != KindStructure || doc.Descriptor.TextEditable {
		t.Fatalf("unexpected overlay descriptor: %+v", doc.Descriptor)
	}
	if len(doc.Overlays) != 2 {
		t.Fatalf("expected 2 overlays, got %d", len(doc.Overlays))
	}
	if !strings.Contains(doc.Summary, "3 structures") || !strings.Contains(doc.Summary, "RMSD") {
		t.Fatalf("unexpected overlay summary: %q", doc.Summary)
	}
	for i, ov := range doc.Overlays {
		if !ov.Aligned || ov.RMSD > 0.01 || ov.Pairs != 6 {
			t.Fatalf("overlay[%d] bad metrics: %+v", i, ov)
		}
	}
}
