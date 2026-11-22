// Package filepreview detects, prepares, and serves rich offline previews for
// scientific, tabular, chemical, vector, HTML, and PDF files inside Motty's
// file window (via Viva's native WebView and custom URL scheme) as well as in
// an external browser via a lazily started loopback HTTP server.
package filepreview

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Kind identifies which built-in previewer handles a document.
type Kind uint8

const (
	KindNone Kind = iota
	KindStructure
	KindSequence
	KindSVG
	KindHTML
	KindPDF
	KindConfidence
	KindTable
	KindSMILES
)

// ErrDecompressedTooLarge reports a compressed file whose inflated payload
// exceeds the preview size budget.
var ErrDecompressedTooLarge = errors.New("compressed file exceeds preview size limit")

// Descriptor describes a recognized previewable format.
type Descriptor struct {
	Kind         Kind
	Format       string // canonical format id, e.g. "pdb", "cif", "sdf", "mol2", "fasta", "a3m", "svg", "html", "pdf", "confidence", "csv", "tsv", "jsonl", "smiles"
	Label        string // short human-readable badge
	Compressed   bool   // true when the source bytes are gzip-compressed (.gz / .svgz)
	TextEditable bool   // true when the user may also toggle to Motty's built-in text editor
}

// OverlayEntry is an additional structure superposed onto the primary structure
// when comparing multiple files in Mol*.
type OverlayEntry struct {
	Name       string  `json:"name"`
	Format     string  `json:"format"`
	Color      string  `json:"color"`
	Data       []byte  `json:"-"`
	RMSD       float64 `json:"rmsd"`
	BeforeRMSD float64 `json:"beforeRmsd"`
	TMScore    float64 `json:"tmScore"`
	Pairs      int     `json:"pairs"`
	Aligned    bool    `json:"aligned"`
}

// OverlayInput is one raw file passed to PrepareOverlay.
type OverlayInput struct {
	Name string
	Data []byte
}

// Document holds the prepared preview payload and metadata for one open file.
type Document struct {
	Name       string
	Descriptor Descriptor
	Data       []byte // ready-to-render bytes (inflated when Compressed is true)
	Summary    string // concise status-bar summary
	Revision   uint64
	Overlays   []OverlayEntry // optional extra superposed structures (KindStructure)
}

type extSpec struct {
	kind         Kind
	format       string
	label        string
	allowGzip    bool
	textEditable bool
}

var registry = map[string]extSpec{
	// Proteins & nucleic acids (3D)
	".pdb":   {KindStructure, "pdb", "PDB", true, true},
	".ent":   {KindStructure, "pdb", "PDB", true, true},
	".cif":   {KindStructure, "cif", "mmCIF", true, true},
	".mmcif": {KindStructure, "cif", "mmCIF", true, true},

	// Small molecules, ligands & coordinate trajectories (3D)
	".sdf":  {KindStructure, "sdf", "SDF", true, true},
	".mol":  {KindStructure, "mol", "MOL", true, true},
	".mol2": {KindStructure, "mol2", "MOL2", true, true},
	".xyz":  {KindStructure, "xyz", "XYZ", true, true},
	".gro":  {KindStructure, "gro", "GRO", true, true},

	// Small molecules 2D (SMILES)
	".smi":    {KindSMILES, "smiles", "SMILES", true, true},
	".smiles": {KindSMILES, "smiles", "SMILES", true, true},
	".ism":    {KindSMILES, "smiles", "SMILES", true, true},
	".can":    {KindSMILES, "smiles", "SMILES", true, true},

	// Biological sequences & MSAs
	".fasta":     {KindSequence, "fasta", "FASTA", true, true},
	".fa":        {KindSequence, "fasta", "FASTA", true, true},
	".faa":       {KindSequence, "fasta", "FASTA", true, true},
	".fna":       {KindSequence, "fasta", "FASTA", true, true},
	".ffn":       {KindSequence, "fasta", "FASTA", true, true},
	".frn":       {KindSequence, "fasta", "FASTA", true, true},
	".a3m":       {KindSequence, "a3m", "A3M", true, true},
	".sto":       {KindSequence, "stockholm", "Stockholm", true, true},
	".stockholm": {KindSequence, "stockholm", "Stockholm", true, true},
	".aln":       {KindSequence, "clustal", "Clustal", true, true},
	".clustal":   {KindSequence, "clustal", "Clustal", true, true},

	// Scientific tables & metrics logs
	".csv":    {KindTable, "csv", "CSV", true, true},
	".tsv":    {KindTable, "tsv", "TSV", true, true},
	".jsonl":  {KindTable, "jsonl", "JSONL", true, true},
	".ndjson": {KindTable, "jsonl", "JSONL", true, true},

	// Vector graphics, interactive HTML reports & PDF documents
	".svg":   {KindSVG, "svg", "SVG", true, true},
	".html":  {KindHTML, "html", "HTML", false, true},
	".htm":   {KindHTML, "html", "HTML", false, true},
	".xhtml": {KindHTML, "html", "HTML", false, true},
	".pdf":   {KindPDF, "pdf", "PDF", false, false},
}

// OverlayPalette holds high-contrast colors assigned to overlaid structures.
var OverlayPalette = []string{
	"#6366f1", // Reference (indigo)
	"#e11d48", // Overlay 1 (rose)
	"#059669", // Overlay 2 (emerald)
	"#d97706", // Overlay 3 (amber)
	"#0284c7", // Overlay 4 (sky)
	"#9333ea", // Overlay 5 (purple)
	"#ea580c", // Overlay 6 (orange)
	"#0d9488", // Overlay 7 (teal)
}

const maxConfidenceEditableBytes = 2 << 20

// IsStructureName reports whether name has a 3D molecular structure extension.
func IsStructureName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	lower = strings.TrimSuffix(lower, ".gz")
	spec, ok := registry[filepath.Ext(lower)]
	return ok && spec.kind == KindStructure
}

// IsPreviewableName reports whether name looks like a rich-previewable or
// image file (used by terminal Cmd+Click and clickable table cells).
func IsPreviewableName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || strings.HasSuffix(lower, "/") {
		return false
	}
	if strings.HasSuffix(lower, ".svgz") {
		return true
	}
	base := strings.TrimSuffix(lower, ".gz")
	ext := filepath.Ext(base)
	if spec, ok := registry[ext]; ok {
		if strings.HasSuffix(lower, ".gz") && !spec.allowGzip {
			return false
		}
		return true
	}
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return !strings.HasSuffix(lower, ".gz")
	case ".json":
		return looksLikeConfidenceFilename(base)
	}
	return false
}

func looksLikeConfidenceFilename(lower string) bool {
	base := filepath.Base(lower)
	return strings.Contains(base, "confidence") ||
		strings.Contains(base, "pae") ||
		strings.Contains(base, "plddt") ||
		strings.HasPrefix(base, "full_data") ||
		strings.HasPrefix(base, "summary_")
}

// looksLikeConfidenceJSON quickly inspects the head and tail of a JSON payload
// for AlphaFold2/3, Protenix, ColabFold, or Boltz confidence keys without
// unmarshaling multi-megabyte matrices.
func looksLikeConfidenceJSON(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return false
	}
	head := trimmed
	var tail []byte
	const window = 64 << 10
	if len(trimmed) > window {
		head = trimmed[:window]
		tailStart := max(len(trimmed)-window, window)
		tail = trimmed[tailStart:]
	}
	has := func(key string) bool {
		needle := []byte(`"` + key + `"`)
		return bytes.Contains(head, needle) || (len(tail) > 0 && bytes.Contains(tail, needle))
	}
	if has("pae") || has("predicted_aligned_error") || has("atom_plddts") ||
		has("chain_pair_iptm") || has("chain_pair_pae_min") ||
		has("complex_plddt") || has("complex_iplddt") || has("pair_chains_iptm") {
		return true
	}
	if has("iptm") && (has("ptm") || has("ranking_score") || has("plddt") || has("confidence_score")) {
		return true
	}
	if has("plddt") && (has("ptm") || has("max_predicted_aligned_error")) {
		return true
	}
	return false
}

// Detect reports whether a file with the given name and bytes is handled by a
// registered rich previewer.
func Detect(name string, data []byte) (Descriptor, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return Descriptor{}, false
	}
	if strings.HasSuffix(lower, ".svgz") {
		return Descriptor{
			Kind:         KindSVG,
			Format:       "svg",
			Label:        "SVG (gzip)",
			Compressed:   true,
			TextEditable: false,
		}, true
	}
	compressed := false
	base := lower
	if strings.HasSuffix(base, ".gz") {
		compressed = true
		base = strings.TrimSuffix(base, ".gz")
	}
	ext := filepath.Ext(base)
	if ext == ".json" {
		if compressed {
			if looksLikeConfidenceFilename(base) {
				return Descriptor{
					Kind:         KindConfidence,
					Format:       "confidence",
					Label:        "PAE/Confidence (gz)",
					Compressed:   true,
					TextEditable: false,
				}, true
			}
			return Descriptor{}, false
		}
		if looksLikeConfidenceJSON(data) {
			return Descriptor{
				Kind:         KindConfidence,
				Format:       "confidence",
				Label:        "PAE/Confidence",
				Compressed:   false,
				TextEditable: len(data) <= maxConfidenceEditableBytes,
			}, true
		}
		return Descriptor{}, false
	}
	spec, ok := registry[ext]
	if !ok {
		return Descriptor{}, false
	}
	if compressed && !spec.allowGzip {
		return Descriptor{}, false
	}
	// Only treat .pdf as PDF when it looks like a PDF or is non-empty; an
	// unrelated extension carrying %PDF- bytes stays rejected as binary.
	if spec.kind == KindPDF && len(data) > 0 && !bytes.HasPrefix(data, []byte("%PDF-")) {
		return Descriptor{}, false
	}
	label := spec.label
	if compressed {
		label += " (gz)"
	}
	return Descriptor{
		Kind:         spec.kind,
		Format:       spec.format,
		Label:        label,
		Compressed:   compressed,
		TextEditable: spec.textEditable && !compressed,
	}, true
}

// Prepare decodes (and if gzip-compressed, safely inflates) raw bytes into a
// Document ready for rendering. maxBytes bounds the inflated payload size.
func Prepare(name string, raw []byte, maxBytes int) (*Document, error) {
	desc, ok := Detect(name, raw)
	if !ok {
		return nil, nil
	}
	if maxBytes <= 0 {
		maxBytes = 20 << 20
	}
	payload := raw
	if desc.Compressed {
		inflated, err := gunzipBounded(raw, maxBytes)
		if err != nil {
			return nil, err
		}
		if desc.Kind == KindConfidence && !looksLikeConfidenceJSON(inflated) {
			return nil, nil
		}
		payload = inflated
	}
	return &Document{
		Name:       name,
		Descriptor: desc,
		Data:       payload,
		Summary:    Summarize(desc, payload),
		Revision:   1,
	}, nil
}

// PrepareOverlay prepares a multi-structure comparison Document where the first
// input is the reference and subsequent inputs are optionally Kabsch-aligned
// onto it.
func PrepareOverlay(inputs []OverlayInput, align bool, maxBytes int) (*Document, error) {
	if len(inputs) == 0 {
		return nil, errors.New("no structures to overlay")
	}
	ref, err := Prepare(inputs[0].Name, inputs[0].Data, maxBytes)
	if err != nil {
		return nil, err
	}
	if ref == nil || ref.Descriptor.Kind != KindStructure {
		return nil, fmt.Errorf("%s is not a supported 3D structure", inputs[0].Name)
	}
	ref.Descriptor.TextEditable = false
	ref.Descriptor.Label = fmt.Sprintf("Overlay (%d)", len(inputs))
	minRMSD, maxRMSD := 0.0, 0.0
	hasRMSD := false
	for i := 1; i < len(inputs) && i < 12; i++ {
		mob, err := Prepare(inputs[i].Name, inputs[i].Data, maxBytes)
		if err != nil || mob == nil || mob.Descriptor.Kind != KindStructure {
			continue
		}
		entry := OverlayEntry{
			Name:   mob.Name,
			Format: mob.Descriptor.Format,
			Color:  OverlayPalette[i%len(OverlayPalette)],
			Data:   mob.Data,
		}
		if align {
			res := AlignStructure(ref.Descriptor.Format, ref.Data, mob.Descriptor.Format, mob.Data)
			entry.Data = res.AlignedData
			entry.RMSD = res.RMSD
			entry.BeforeRMSD = res.BeforeRMSD
			entry.TMScore = res.TMScore
			entry.Pairs = res.Pairs
			entry.Aligned = res.Aligned
			if res.Pairs >= 3 {
				if !hasRMSD || res.RMSD < minRMSD {
					minRMSD = res.RMSD
				}
				if !hasRMSD || res.RMSD > maxRMSD {
					maxRMSD = res.RMSD
				}
				hasRMSD = true
			}
		}
		ref.Overlays = append(ref.Overlays, entry)
	}
	total := 1 + len(ref.Overlays)
	if hasRMSD {
		if len(ref.Overlays) == 1 {
			ref.Summary = fmt.Sprintf("Overlay · %d structures · Cα RMSD %.2f Å", total, minRMSD)
		} else {
			ref.Summary = fmt.Sprintf("Overlay · %d structures · Cα RMSD %.2f–%.2f Å", total, minRMSD, maxRMSD)
		}
	} else {
		ref.Summary = fmt.Sprintf("Overlay · %d structures", total)
	}
	return ref, nil
}

// UpdateText replaces the preview payload with edited text from the source
// editor and recomputes the summary.
func (d *Document) UpdateText(text string) {
	if d == nil || !d.Descriptor.TextEditable {
		return
	}
	d.Data = []byte(text)
	d.Summary = Summarize(d.Descriptor, d.Data)
	d.Revision++
}

// EntryPath returns the relative URL path beneath a document's root
// (e.g. "molstar/index.html" or "site/report.html").
func (d *Document) EntryPath() string {
	if d == nil {
		return ""
	}
	switch d.Descriptor.Kind {
	case KindStructure:
		return "molstar/index.html"
	case KindSequence:
		return "sequence/index.html"
	case KindSVG:
		return "svg/index.html"
	case KindPDF:
		return "pdf/" + safeSegment(d.Name, "document.pdf")
	case KindHTML:
		return "site/" + safeSegment(d.Name, "index.html")
	case KindConfidence:
		return "confidence/index.html"
	case KindTable:
		return "table/index.html"
	case KindSMILES:
		return "smiles/index.html"
	default:
		return ""
	}
}

func safeSegment(name, fallback string) string {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if base == "" || base == "." || base == "/" {
		return fallback
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}

func gunzipBounded(raw []byte, maxBytes int) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decompress %w", err)
	}
	defer reader.Close()
	limited := io.LimitReader(reader, int64(maxBytes)+1)
	out, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("decompress %w", err)
	}
	if len(out) > maxBytes {
		return nil, ErrDecompressedTooLarge
	}
	return out, nil
}
