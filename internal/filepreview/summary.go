package filepreview

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Summarize returns a concise, human-readable status bar description of a
// prepared preview document.
func Summarize(desc Descriptor, data []byte) string {
	switch desc.Kind {
	case KindStructure:
		return summarizeStructure(desc, data)
	case KindSequence:
		return summarizeSequence(desc, data)
	case KindSVG:
		return summarizeSVG(desc, data)
	case KindHTML:
		return fmt.Sprintf("%s · %s", desc.Label, humanBytes(len(data)))
	case KindPDF:
		return summarizePDF(desc, data)
	case KindConfidence:
		return summarizeConfidence(desc.Label, data)
	case KindTable:
		return summarizeTable(desc, data)
	case KindSMILES:
		return summarizeSMILES(desc.Label, data)
	default:
		return desc.Label
	}
}

func summarizeStructure(desc Descriptor, data []byte) string {
	switch desc.Format {
	case "pdb":
		return summarizePDB(desc.Label, data)
	case "cif":
		return summarizeMMCIF(desc.Label, data)
	case "sdf", "mol":
		return summarizeSDF(desc.Label, data)
	case "mol2":
		return summarizeMOL2(desc.Label, data)
	case "xyz":
		return summarizeXYZ(desc.Label, data)
	case "gro":
		return summarizeGRO(desc.Label, data)
	default:
		return fmt.Sprintf("%s · %s", desc.Label, humanBytes(len(data)))
	}
}

func summarizePDB(label string, data []byte) string {
	chains := make(map[byte]struct{})
	residues := make(map[string]struct{})
	atoms := 0
	hetAtoms := 0
	models := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "MODEL "):
			models++
		case strings.HasPrefix(line, "ATOM  "):
			atoms++
			if len(line) >= 27 {
				chain := line[21]
				if chain != ' ' {
					chains[chain] = struct{}{}
				}
				residues[string(chain)+":"+strings.TrimSpace(line[22:27])] = struct{}{}
			}
		case strings.HasPrefix(line, "HETATM"):
			hetAtoms++
		}
	}
	parts := []string{label}
	if len(chains) > 0 {
		parts = append(parts, plural(len(chains), "chain", "chains"))
	}
	if len(residues) > 0 {
		parts = append(parts, plural(len(residues), "residue", "residues"))
	}
	totalAtoms := atoms + hetAtoms
	if totalAtoms > 0 {
		parts = append(parts, plural(totalAtoms, "atom", "atoms"))
	}
	if models > 1 {
		parts = append(parts, plural(models, "model", "models"))
	}
	return strings.Join(parts, " · ")
}

func summarizeMMCIF(label string, data []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	inLoop := false
	inAtomSite := false
	colIndex := 0
	colGroup, colChain, colSeq := -1, -1, -1
	chains := make(map[string]struct{})
	residues := make(map[string]struct{})
	atoms := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			if inAtomSite && atoms > 0 {
				break
			}
			continue
		}
		if line == "loop_" {
			if inAtomSite && atoms > 0 {
				break
			}
			inLoop = true
			inAtomSite = false
			colIndex = 0
			colGroup, colChain, colSeq = -1, -1, -1
			continue
		}
		if strings.HasPrefix(line, "_") {
			if inLoop && strings.HasPrefix(line, "_atom_site.") {
				inAtomSite = true
				switch line {
				case "_atom_site.group_PDB":
					colGroup = colIndex
				case "_atom_site.auth_asym_id":
					colChain = colIndex
				case "_atom_site.label_asym_id":
					if colChain < 0 {
						colChain = colIndex
					}
				case "_atom_site.auth_seq_id":
					colSeq = colIndex
				case "_atom_site.label_seq_id":
					if colSeq < 0 {
						colSeq = colIndex
					}
				}
				colIndex++
			} else if inAtomSite && atoms > 0 {
				break
			}
			continue
		}
		if inAtomSite {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			atoms++
			isAtom := colGroup < 0 || (colGroup < len(fields) && fields[colGroup] == "ATOM")
			if isAtom && colChain >= 0 && colChain < len(fields) {
				ch := fields[colChain]
				if ch != "." && ch != "?" {
					chains[ch] = struct{}{}
					if colSeq >= 0 && colSeq < len(fields) {
						residues[ch+":"+fields[colSeq]] = struct{}{}
					}
				}
			}
		}
	}
	parts := []string{label}
	if len(chains) > 0 {
		parts = append(parts, plural(len(chains), "chain", "chains"))
	}
	if len(residues) > 0 {
		parts = append(parts, plural(len(residues), "residue", "residues"))
	}
	if atoms > 0 {
		parts = append(parts, plural(atoms, "atom", "atoms"))
	}
	return strings.Join(parts, " · ")
}

func summarizeSDF(label string, data []byte) string {
	molecules := bytes.Count(data, []byte("\n$$$$"))
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("$$$$")) {
		molecules++
	}
	if molecules == 0 && len(bytes.TrimSpace(data)) > 0 {
		molecules = 1
	}
	lines := strings.SplitN(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n", 6)
	atoms, bonds := 0, 0
	if len(lines) >= 4 {
		counts := lines[3]
		if len(counts) >= 6 {
			a, errA := strconv.Atoi(strings.TrimSpace(counts[0:3]))
			b, errB := strconv.Atoi(strings.TrimSpace(counts[3:6]))
			if errA == nil && errB == nil && a >= 0 && b >= 0 {
				atoms, bonds = a, b
			}
		}
	}
	parts := []string{label}
	if molecules > 1 {
		parts = append(parts, plural(molecules, "molecule", "molecules"))
	}
	if atoms > 0 {
		parts = append(parts, plural(atoms, "atom", "atoms"))
	}
	if bonds > 0 {
		parts = append(parts, plural(bonds, "bond", "bonds"))
	}
	return strings.Join(parts, " · ")
}

func summarizeMOL2(label string, data []byte) string {
	molecules := bytes.Count(data, []byte("@<TRIPOS>MOLECULE"))
	atoms, bonds := 0, 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "@<TRIPOS>MOLECULE" {
			if scanner.Scan() && scanner.Scan() {
				fields := strings.Fields(scanner.Text())
				if len(fields) >= 2 {
					atoms, _ = strconv.Atoi(fields[0])
					bonds, _ = strconv.Atoi(fields[1])
				}
			}
			break
		}
	}
	parts := []string{label}
	if molecules > 1 {
		parts = append(parts, plural(molecules, "molecule", "molecules"))
	}
	if atoms > 0 {
		parts = append(parts, plural(atoms, "atom", "atoms"))
	}
	if bonds > 0 {
		parts = append(parts, plural(bonds, "bond", "bonds"))
	}
	return strings.Join(parts, " · ")
}

func summarizeXYZ(label string, data []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	firstAtoms := 0
	frames := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		n, err := strconv.Atoi(line)
		if err != nil || n <= 0 {
			break
		}
		if firstAtoms == 0 {
			firstAtoms = n
		}
		frames++
		// Skip comment line + n atom lines
		if !scanner.Scan() {
			break
		}
		for range n {
			if !scanner.Scan() {
				break
			}
		}
	}
	parts := []string{label}
	if firstAtoms > 0 {
		parts = append(parts, plural(firstAtoms, "atom", "atoms"))
	}
	if frames > 1 {
		parts = append(parts, plural(frames, "frame", "frames"))
	}
	return strings.Join(parts, " · ")
}

func summarizeGRO(label string, data []byte) string {
	lines := strings.SplitN(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n", 4)
	if len(lines) >= 2 {
		if n, err := strconv.Atoi(strings.TrimSpace(lines[1])); err == nil && n > 0 {
			return fmt.Sprintf("%s · %s", label, plural(n, "atom", "atoms"))
		}
	}
	return fmt.Sprintf("%s · %s", label, humanBytes(len(data)))
}

func summarizeSequence(desc Descriptor, data []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	seqs := 0
	maxLen := 0
	curLen := 0
	switch desc.Format {
	case "stockholm":
		lengths := make(map[string]int)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") || line == "//" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				lengths[fields[0]] += len(fields[1])
			}
		}
		seqs = len(lengths)
		for _, n := range lengths {
			if n > maxLen {
				maxLen = n
			}
		}
	case "clustal":
		lengths := make(map[string]int)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(strings.ToUpper(line), "CLUSTAL") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 && !strings.ContainsAny(fields[0], "*:.") {
				lengths[fields[0]] += len(fields[1])
			}
		}
		seqs = len(lengths)
		for _, n := range lengths {
			if n > maxLen {
				maxLen = n
			}
		}
	default: // fasta, a3m
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, ";") {
				continue
			}
			if strings.HasPrefix(line, ">") {
				if curLen > maxLen {
					maxLen = curLen
				}
				seqs++
				curLen = 0
				continue
			}
			if seqs == 0 {
				seqs = 1
			}
			if desc.Format == "a3m" {
				for i := range len(line) {
					c := line[i]
					if c >= 'a' && c <= 'z' || c == ' ' || c == '\t' {
						continue
					}
					curLen++
				}
			} else {
				curLen += len(strings.ReplaceAll(line, " ", ""))
			}
		}
		if curLen > maxLen {
			maxLen = curLen
		}
	}
	parts := []string{desc.Label}
	if seqs > 0 {
		parts = append(parts, plural(seqs, "sequence", "sequences"))
	}
	if maxLen > 0 {
		parts = append(parts, fmt.Sprintf("max %d", maxLen))
	}
	return strings.Join(parts, " · ")
}

var (
	svgViewBoxRe = regexp.MustCompile(`viewBox\s*=\s*["']\s*[-+0-9.]+\s+[-+0-9.]+\s+([0-9.]+)\s+([0-9.]+)\s*["']`)
	svgWidthRe   = regexp.MustCompile(`\bwidth\s*=\s*["']\s*([0-9.]+)(?:px)?\s*["']`)
	svgHeightRe  = regexp.MustCompile(`\bheight\s*=\s*["']\s*([0-9.]+)(?:px)?\s*["']`)
)

func summarizeSVG(desc Descriptor, data []byte) string {
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	if m := svgViewBoxRe.FindSubmatch(head); len(m) == 3 {
		return fmt.Sprintf("%s · %s×%s", desc.Label, m[1], m[2])
	}
	mw := svgWidthRe.FindSubmatch(head)
	mh := svgHeightRe.FindSubmatch(head)
	if len(mw) == 2 && len(mh) == 2 {
		return fmt.Sprintf("%s · %s×%s", desc.Label, mw[1], mh[1])
	}
	return fmt.Sprintf("%s · %s", desc.Label, humanBytes(len(data)))
}

var pdfPageTypeRe = regexp.MustCompile(`/Type\s*/Page\b`)

func summarizePDF(desc Descriptor, data []byte) string {
	pages := len(pdfPageTypeRe.FindAll(data, -1))
	if pages > 0 {
		return fmt.Sprintf("%s · %s · %s", desc.Label, plural(pages, "page", "pages"), humanBytes(len(data)))
	}
	return fmt.Sprintf("%s · %s", desc.Label, humanBytes(len(data)))
}

var (
	jsonScalarNumRe = regexp.MustCompile(`"(ranking_score|iptm|ptm|confidence_score|complex_plddt)"\s*:\s*(-?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)`)
)

func summarizeConfidence(label string, data []byte) string {
	parts := []string{label}
	found := map[string]string{}
	scan := func(chunk []byte) {
		for _, m := range jsonScalarNumRe.FindAllSubmatch(chunk, 16) {
			k := string(m[1])
			if _, ok := found[k]; !ok {
				if v, err := strconv.ParseFloat(string(m[2]), 64); err == nil {
					found[k] = fmt.Sprintf("%.2f", v)
				}
			}
		}
	}
	if len(data) <= 128<<10 {
		scan(data)
	} else {
		scan(data[:64<<10])
		scan(data[len(data)-64<<10:])
	}
	for _, item := range [][2]string{
		{"ranking_score", "score"},
		{"confidence_score", "conf"},
		{"iptm", "ipTM"},
		{"ptm", "pTM"},
		{"complex_plddt", "pLDDT"},
	} {
		if val, ok := found[item[0]]; ok {
			parts = append(parts, item[1]+" "+val)
		}
	}
	parts = append(parts, humanBytes(len(data)))
	return strings.Join(parts, " · ")
}

func summarizeTable(desc Descriptor, data []byte) string {
	lines := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lines++
	}
	if desc.Format == "jsonl" {
		return fmt.Sprintf("%s · %s · %s", desc.Label, plural(lines, "record", "records"), humanBytes(len(data)))
	}
	cols := 0
	if idx := bytes.IndexByte(data, '\n'); idx > 0 {
		header := string(bytes.TrimRight(data[:idx], "\r"))
		sep := ','
		if desc.Format == "tsv" {
			sep = '\t'
		}
		cols = countCSVFields(header, sep)
	}
	rows := max(lines-1, 0)
	if cols > 0 {
		return fmt.Sprintf("%s · %s × %s", desc.Label, plural(rows, "row", "rows"), plural(cols, "col", "cols"))
	}
	return fmt.Sprintf("%s · %s", desc.Label, humanBytes(len(data)))
}

func countCSVFields(line string, sep rune) int {
	if line == "" {
		return 0
	}
	count := 1
	inQuote := false
	for _, r := range line {
		if r == '"' {
			inQuote = !inQuote
		} else if r == sep && !inQuote {
			count++
		}
	}
	return count
}

func summarizeSMILES(label string, data []byte) string {
	count := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if count == 0 && strings.HasPrefix(strings.ToLower(line), "smiles") {
			continue
		}
		count++
	}
	if count > 0 {
		return fmt.Sprintf("%s · %s", label, plural(count, "molecule", "molecules"))
	}
	return fmt.Sprintf("%s · %s", label, humanBytes(len(data)))
}

func plural(n int, singular, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
