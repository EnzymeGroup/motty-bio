package filepreview

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// AlignResult summarizes a Kabsch rigid-body superposition of a mobile
// structure onto a reference structure.
type AlignResult struct {
	AlignedData []byte  // transformed mobile structure bytes (PDB or mmCIF)
	Pairs       int     // number of paired atoms used in the superposition
	RMSD        float64 // post-alignment RMSD in Angstroms
	BeforeRMSD  float64 // pre-alignment RMSD in Angstroms
	TMScore     float64 // approximate TM-score on paired Cα atoms (0..1)
	Aligned     bool    // true when rigid transformation was applied
}

type vec3 [3]float64

type atomCoord struct {
	key string // "chain:resSeq"
	pos vec3
}

// AlignStructure superposes mobile onto ref using Cα Kabsch rigid-body
// alignment (falling back to heavy atoms when Cα coordinates are absent) and
// rewrites mobile's coordinates in place (PDB or mmCIF).
func AlignStructure(refFormat string, refData []byte, mobFormat string, mobData []byte) AlignResult {
	refCA, refAll := extractCoords(refFormat, refData)
	mobCA, mobAll := extractCoords(mobFormat, mobData)

	refPts, mobPts := refCA, mobCA
	isCA := true
	if len(refPts) < 3 || len(mobPts) < 3 {
		refPts, mobPts = refAll, mobAll
		isCA = false
	}
	p, q := pairCoords(refPts, mobPts)
	if len(p) < 3 {
		return AlignResult{AlignedData: mobData}
	}

	R, t, rmsd, beforeRMSD := kabsch(p, q)
	tm := 0.0
	if isCA && len(refCA) > 0 {
		tm = approxTMScore(p, q, R, t, len(refCA))
	}
	transformed, ok := transformStructure(mobFormat, mobData, R, t)
	if !ok {
		return AlignResult{
			AlignedData: mobData,
			Pairs:       len(p),
			RMSD:        rmsd,
			BeforeRMSD:  beforeRMSD,
			TMScore:     tm,
			Aligned:     false,
		}
	}
	return AlignResult{
		AlignedData: transformed,
		Pairs:       len(p),
		RMSD:        rmsd,
		BeforeRMSD:  beforeRMSD,
		TMScore:     tm,
		Aligned:     true,
	}
}

func extractCoords(format string, data []byte) (ca []atomCoord, heavy []atomCoord) {
	switch format {
	case "pdb":
		return extractPDBCoords(data)
	case "cif":
		return extractMMCIFCoords(data)
	default:
		return nil, nil
	}
}

func extractPDBCoords(data []byte) (ca []atomCoord, heavy []atomCoord) {
	seenCA := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ENDMDL") && (len(ca) > 0 || len(heavy) > 0) {
			break
		}
		isAtom := strings.HasPrefix(line, "ATOM  ")
		isHet := strings.HasPrefix(line, "HETATM")
		if (!isAtom && !isHet) || len(line) < 54 {
			continue
		}
		altLoc := line[16]
		if altLoc != ' ' && altLoc != 'A' && altLoc != '1' {
			continue
		}
		x, errX := strconv.ParseFloat(strings.TrimSpace(line[30:38]), 64)
		y, errY := strconv.ParseFloat(strings.TrimSpace(line[38:46]), 64)
		z, errZ := strconv.ParseFloat(strings.TrimSpace(line[46:54]), 64)
		if errX != nil || errY != nil || errZ != nil {
			continue
		}
		atomName := strings.TrimSpace(line[12:16])
		chain := strings.TrimSpace(line[21:22])
		resSeq := strings.TrimSpace(line[22:27])
		key := chain + ":" + resSeq
		pos := vec3{x, y, z}
		if isAtom && atomName == "CA" {
			if _, dup := seenCA[key]; !dup {
				seenCA[key] = struct{}{}
				ca = append(ca, atomCoord{key: key, pos: pos})
			}
		}
		if !strings.HasPrefix(atomName, "H") && len(heavy) < 8000 {
			heavy = append(heavy, atomCoord{key: key + ":" + atomName, pos: pos})
		}
	}
	return ca, heavy
}

func extractMMCIFCoords(data []byte) (ca []atomCoord, heavy []atomCoord) {
	seenCA := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	inLoop, inAtomSite := false, false
	col := 0
	cGroup, cAtom, cAlt, cChain, cSeq, cX, cY, cZ, cModel := -1, -1, -1, -1, -1, -1, -1, -1, -1
	firstModel := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			if inAtomSite && (len(ca) > 0 || len(heavy) > 0) {
				break
			}
			continue
		}
		if line == "loop_" {
			if inAtomSite && (len(ca) > 0 || len(heavy) > 0) {
				break
			}
			inLoop, inAtomSite, col = true, false, 0
			cGroup, cAtom, cAlt, cChain, cSeq, cX, cY, cZ, cModel = -1, -1, -1, -1, -1, -1, -1, -1, -1
			continue
		}
		if strings.HasPrefix(line, "_") {
			if inLoop && strings.HasPrefix(line, "_atom_site.") {
				inAtomSite = true
				switch line {
				case "_atom_site.group_PDB":
					cGroup = col
				case "_atom_site.label_atom_id":
					cAtom = col
				case "_atom_site.auth_atom_id":
					if cAtom < 0 {
						cAtom = col
					}
				case "_atom_site.label_alt_id":
					cAlt = col
				case "_atom_site.auth_asym_id":
					cChain = col
				case "_atom_site.label_asym_id":
					if cChain < 0 {
						cChain = col
					}
				case "_atom_site.auth_seq_id":
					cSeq = col
				case "_atom_site.label_seq_id":
					if cSeq < 0 {
						cSeq = col
					}
				case "_atom_site.Cartn_x":
					cX = col
				case "_atom_site.Cartn_y":
					cY = col
				case "_atom_site.Cartn_z":
					cZ = col
				case "_atom_site.pdbx_PDB_model_num":
					cModel = col
				}
				col++
			} else if inAtomSite && (len(ca) > 0 || len(heavy) > 0) {
				break
			}
			continue
		}
		if !inAtomSite || cX < 0 || cY < 0 || cZ < 0 {
			continue
		}
		fields := splitCIFRow(line)
		need := max(cX, max(cY, cZ))
		if len(fields) <= need {
			continue
		}
		if cModel >= 0 && cModel < len(fields) {
			if firstModel == "" {
				firstModel = fields[cModel]
			} else if fields[cModel] != firstModel {
				continue
			}
		}
		if cAlt >= 0 && cAlt < len(fields) {
			alt := fields[cAlt]
			if alt != "." && alt != "?" && alt != "A" && alt != "1" {
				continue
			}
		}
		x, errX := strconv.ParseFloat(fields[cX], 64)
		y, errY := strconv.ParseFloat(fields[cY], 64)
		z, errZ := strconv.ParseFloat(fields[cZ], 64)
		if errX != nil || errY != nil || errZ != nil {
			continue
		}
		group := "ATOM"
		if cGroup >= 0 && cGroup < len(fields) {
			group = fields[cGroup]
		}
		atomName := ""
		if cAtom >= 0 && cAtom < len(fields) {
			atomName = strings.Trim(fields[cAtom], `"'`)
		}
		chain := ""
		if cChain >= 0 && cChain < len(fields) {
			chain = fields[cChain]
		}
		seq := ""
		if cSeq >= 0 && cSeq < len(fields) {
			seq = fields[cSeq]
		}
		key := chain + ":" + seq
		pos := vec3{x, y, z}
		if group == "ATOM" && atomName == "CA" {
			if _, dup := seenCA[key]; !dup {
				seenCA[key] = struct{}{}
				ca = append(ca, atomCoord{key: key, pos: pos})
			}
		}
		if !strings.HasPrefix(atomName, "H") && len(heavy) < 8000 {
			heavy = append(heavy, atomCoord{key: key + ":" + atomName, pos: pos})
		}
	}
	return ca, heavy
}

func splitCIFRow(line string) []string {
	var out []string
	i, n := 0, len(line)
	for i < n {
		for i < n && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
		if line[i] == '\'' || line[i] == '"' {
			q := line[i]
			start := i
			i++
			for i < n && !(line[i] == q && (i+1 == n || line[i+1] == ' ' || line[i+1] == '\t')) {
				i++
			}
			if i < n {
				i++
			}
			out = append(out, line[start:i])
		} else {
			start := i
			for i < n && line[i] != ' ' && line[i] != '\t' {
				i++
			}
			out = append(out, line[start:i])
		}
	}
	return out
}

func pairCoords(ref, mob []atomCoord) (p, q []vec3) {
	mobMap := make(map[string]vec3, len(mob))
	for _, a := range mob {
		if _, exists := mobMap[a.key]; !exists {
			mobMap[a.key] = a.pos
		}
	}
	for _, a := range ref {
		if mPos, ok := mobMap[a.key]; ok {
			p = append(p, a.pos)
			q = append(q, mPos)
		}
	}
	// If residue keys match at least 30% of the smaller structure (min 3), use
	// exact key matching; otherwise fall back to sequence-order pairing.
	minLen := min(len(ref), len(mob))
	if len(p) >= 3 && len(p)*10 >= minLen*3 {
		return p, q
	}
	p = make([]vec3, minLen)
	q = make([]vec3, minLen)
	for i := range minLen {
		p[i] = ref[i].pos
		q[i] = mob[i].pos
	}
	return p, q
}

type mat3 [3][3]float64

func mat3Mul(a, b mat3) mat3 {
	var c mat3
	for i := range 3 {
		for j := range 3 {
			c[i][j] = a[i][0]*b[0][j] + a[i][1]*b[1][j] + a[i][2]*b[2][j]
		}
	}
	return c
}

func mat3Transpose(a mat3) mat3 {
	return mat3{
		{a[0][0], a[1][0], a[2][0]},
		{a[0][1], a[1][1], a[2][1]},
		{a[0][2], a[1][2], a[2][2]},
	}
}

func mat3MulVec(a mat3, v vec3) vec3 {
	return vec3{
		a[0][0]*v[0] + a[0][1]*v[1] + a[0][2]*v[2],
		a[1][0]*v[0] + a[1][1]*v[1] + a[1][2]*v[2],
		a[2][0]*v[0] + a[2][1]*v[1] + a[2][2]*v[2],
	}
}

func mat3Det(a mat3) float64 {
	return a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) -
		a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) +
		a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
}

func vec3Cross(a, b vec3) vec3 {
	return vec3{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
}

func vec3Norm(a vec3) float64 {
	return math.Sqrt(a[0]*a[0] + a[1]*a[1] + a[2]*a[2])
}

// jacobiSymmetric3 computes eigenvalues and orthonormal eigenvector columns of a
// 3x3 real symmetric matrix A, sorted descending by eigenvalue.
func jacobiSymmetric3(a mat3) (evals [3]float64, V mat3) {
	V = mat3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	for range 30 {
		// Largest off-diagonal entry.
		p, q := 0, 1
		best := math.Abs(a[0][1])
		if v := math.Abs(a[0][2]); v > best {
			p, q, best = 0, 2, v
		}
		if v := math.Abs(a[1][2]); v > best {
			p, q, best = 1, 2, v
		}
		if best < 1e-14 {
			break
		}
		diff := a[q][q] - a[p][p]
		var t float64
		if math.Abs(diff) < 1e-30 {
			t = 1
		} else {
			phi := diff / (2 * a[p][q])
			t = 1 / (math.Abs(phi) + math.Sqrt(1+phi*phi))
			if phi < 0 {
				t = -t
			}
		}
		c := 1 / math.Sqrt(1+t*t)
		s := t * c
		tau := s / (1 + c)
		apq := a[p][q]
		a[p][q], a[q][p] = 0, 0
		a[p][p] -= t * apq
		a[q][q] += t * apq
		r := 3 - p - q
		arp, arq := a[r][p], a[r][q]
		a[r][p] = arp - s*(arq+tau*arp)
		a[p][r] = a[r][p]
		a[r][q] = arq + s*(arp-tau*arq)
		a[q][r] = a[r][q]
		for i := range 3 {
			vip, viq := V[i][p], V[i][q]
			V[i][p] = vip - s*(viq+tau*vip)
			V[i][q] = viq + s*(vip-tau*viq)
		}
	}
	evals = [3]float64{a[0][0], a[1][1], a[2][2]}
	// Sort descending by eigenvalue.
	order := [3]int{0, 1, 2}
	for i := range 2 {
		for j := i + 1; j < 3; j++ {
			if evals[order[j]] > evals[order[i]] {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	var sortedVals [3]float64
	var sortedV mat3
	for k, col := range order {
		sortedVals[k] = evals[col]
		for row := range 3 {
			sortedV[row][k] = V[row][col]
		}
	}
	return sortedVals, sortedV
}

// kabsch finds proper rotation R and translation t minimizing sum ||R*q_i + t - p_i||^2.
func kabsch(p, q []vec3) (R mat3, t vec3, rmsd, beforeRMSD float64) {
	n := float64(len(p))
	var cp, cq vec3
	sumBefore := 0.0
	for i := range p {
		for k := range 3 {
			cp[k] += p[i][k]
			cq[k] += q[i][k]
			d := q[i][k] - p[i][k]
			sumBefore += d * d
		}
	}
	for k := range 3 {
		cp[k] /= n
		cq[k] /= n
	}
	beforeRMSD = math.Sqrt(sumBefore / n)

	// H = Y^T * X where Y = q - cq, X = p - cp
	var H mat3
	for i := range p {
		x := vec3{p[i][0] - cp[0], p[i][1] - cp[1], p[i][2] - cp[2]}
		y := vec3{q[i][0] - cq[0], q[i][1] - cq[1], q[i][2] - cq[2]}
		for r := range 3 {
			for c := range 3 {
				H[r][c] += y[r] * x[c]
			}
		}
	}

	// SVD of H = U * Sigma * V^T via eigendecomposition of H^T * H.
	hth := mat3Mul(mat3Transpose(H), H)
	evals, V := jacobiSymmetric3(hth)
	if mat3Det(V) < 0 {
		for row := range 3 {
			V[row][2] = -V[row][2]
		}
	}
	var U mat3
	var uCols [3]vec3
	validCols := 0
	for k := range 3 {
		sigma := math.Sqrt(max(evals[k], 0))
		if sigma > 1e-10 {
			vk := vec3{V[0][k], V[1][k], V[2][k]}
			hv := mat3MulVec(H, vk)
			uCols[k] = vec3{hv[0] / sigma, hv[1] / sigma, hv[2] / sigma}
			validCols++
		}
	}
	if validCols == 0 {
		R = mat3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	} else {
		if validCols == 1 {
			// Pick an arbitrary vector orthogonal to uCols[0].
			ref := vec3{1, 0, 0}
			if math.Abs(uCols[0][0]) > 0.9 {
				ref = vec3{0, 1, 0}
			}
			u1 := vec3Cross(uCols[0], ref)
			n1 := vec3Norm(u1)
			uCols[1] = vec3{u1[0] / n1, u1[1] / n1, u1[2] / n1}
		}
		if validCols < 3 {
			u2 := vec3Cross(uCols[0], uCols[1])
			n2 := vec3Norm(u2)
			if n2 > 1e-12 {
				uCols[2] = vec3{u2[0] / n2, u2[1] / n2, u2[2] / n2}
			}
		}
		for k := range 3 {
			for row := range 3 {
				U[row][k] = uCols[k][row]
			}
		}
		R = mat3Mul(V, mat3Transpose(U))
		if mat3Det(R) < 0 {
			for row := range 3 {
				U[row][2] = -U[row][2]
			}
			R = mat3Mul(V, mat3Transpose(U))
		}
	}

	rcq := mat3MulVec(R, cq)
	t = vec3{cp[0] - rcq[0], cp[1] - rcq[1], cp[2] - rcq[2]}

	sumAfter := 0.0
	for i := range p {
		rq := mat3MulVec(R, q[i])
		for k := range 3 {
			d := rq[k] + t[k] - p[i][k]
			sumAfter += d * d
		}
	}
	rmsd = math.Sqrt(sumAfter / n)
	return R, t, rmsd, beforeRMSD
}

func approxTMScore(p, q []vec3, R mat3, t vec3, refLen int) float64 {
	if refLen <= 0 || len(p) == 0 {
		return 0
	}
	d0 := 0.5
	if refLen > 15 {
		d0 = math.Max(0.5, 1.24*math.Cbrt(float64(refLen-15))-1.8)
	}
	sum := 0.0
	for i := range p {
		rq := mat3MulVec(R, q[i])
		dx := rq[0] + t[0] - p[i][0]
		dy := rq[1] + t[1] - p[i][1]
		dz := rq[2] + t[2] - p[i][2]
		d2 := dx*dx + dy*dy + dz*dz
		sum += 1.0 / (1.0 + d2/(d0*d0))
	}
	return sum / float64(refLen)
}

func transformStructure(format string, data []byte, R mat3, t vec3) ([]byte, bool) {
	switch format {
	case "pdb":
		return transformPDB(data, R, t), true
	case "cif":
		return transformMMCIF(data, R, t), true
	default:
		return nil, false
	}
}

func transformPDB(data []byte, R mat3, t vec3) []byte {
	var out bytes.Buffer
	out.Grow(len(data))
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if (strings.HasPrefix(line, "ATOM  ") || strings.HasPrefix(line, "HETATM")) && len(line) >= 54 {
			x, errX := strconv.ParseFloat(strings.TrimSpace(line[30:38]), 64)
			y, errY := strconv.ParseFloat(strings.TrimSpace(line[38:46]), 64)
			z, errZ := strconv.ParseFloat(strings.TrimSpace(line[46:54]), 64)
			if errX == nil && errY == nil && errZ == nil {
				v := mat3MulVec(R, vec3{x, y, z})
				out.WriteString(line[:30])
				fmt.Fprintf(&out, "%8.3f%8.3f%8.3f", v[0]+t[0], v[1]+t[1], v[2]+t[2])
				out.WriteString(line[54:])
				out.WriteByte('\n')
				continue
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func transformMMCIF(data []byte, R mat3, t vec3) []byte {
	var out bytes.Buffer
	out.Grow(len(data) + len(data)/16)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	inLoop, inAtomSite := false, false
	col, cX, cY, cZ := 0, -1, -1, -1
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if inAtomSite && col > 0 {
				inLoop, inAtomSite = false, false
			}
			out.WriteString(raw)
			out.WriteByte('\n')
			continue
		}
		if trimmed == "loop_" {
			inLoop, inAtomSite, col = true, false, 0
			cX, cY, cZ = -1, -1, -1
			out.WriteString(raw)
			out.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(trimmed, "_") {
			if inLoop && strings.HasPrefix(trimmed, "_atom_site.") {
				inAtomSite = true
				switch trimmed {
				case "_atom_site.Cartn_x":
					cX = col
				case "_atom_site.Cartn_y":
					cY = col
				case "_atom_site.Cartn_z":
					cZ = col
				}
				col++
			} else if inAtomSite {
				inLoop, inAtomSite = false, false
			}
			out.WriteString(raw)
			out.WriteByte('\n')
			continue
		}
		if inAtomSite && cX >= 0 && cY >= 0 && cZ >= 0 {
			fields := splitCIFRow(trimmed)
			if len(fields) > max(cX, max(cY, cZ)) {
				x, errX := strconv.ParseFloat(fields[cX], 64)
				y, errY := strconv.ParseFloat(fields[cY], 64)
				z, errZ := strconv.ParseFloat(fields[cZ], 64)
				if errX == nil && errY == nil && errZ == nil {
					v := mat3MulVec(R, vec3{x, y, z})
					fields[cX] = strconv.FormatFloat(v[0]+t[0], 'f', 3, 64)
					fields[cY] = strconv.FormatFloat(v[1]+t[1], 'f', 3, 64)
					fields[cZ] = strconv.FormatFloat(v[2]+t[2], 'f', 3, 64)
					out.WriteString(strings.Join(fields, " "))
					out.WriteByte('\n')
					continue
				}
			}
		}
		out.WriteString(raw)
		out.WriteByte('\n')
	}
	return out.Bytes()
}
