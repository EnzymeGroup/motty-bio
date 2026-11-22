// Package filepreview detects, prepares, and serves rich offline previews for
// scientific, vector, HTML, and PDF files inside Motty's file window (via Viva's
// native WebView and custom URL scheme) as well as in an external browser via a
// lazily started loopback HTTP server.
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
)

// ErrDecompressedTooLarge reports a compressed file whose inflated payload
// exceeds the preview size budget.
var ErrDecompressedTooLarge = errors.New("compressed file exceeds preview size limit")

// Descriptor describes a recognized previewable format.
type Descriptor struct {
	Kind         Kind
	Format       string // canonical format id, e.g. "pdb", "cif", "sdf", "mol2", "fasta", "a3m", "svg", "html", "pdf"
	Label        string // short human-readable badge, e.g. "PDB", "mmCIF", "A3M", "PDF"
	Compressed   bool   // true when the source bytes are gzip-compressed (.gz / .svgz)
	TextEditable bool   // true when the user may also toggle to Motty's built-in text editor
}

// Document holds the prepared preview payload and metadata for one open file.
type Document struct {
	Name       string
	Descriptor Descriptor
	Data       []byte // ready-to-render bytes (inflated when Compressed is true)
	Summary    string // concise status-bar summary
	Revision   uint64
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

	// Vector graphics, interactive HTML reports & PDF documents
	".svg":   {KindSVG, "svg", "SVG", true, true},
	".html":  {KindHTML, "html", "HTML", false, true},
	".htm":   {KindHTML, "html", "HTML", false, true},
	".xhtml": {KindHTML, "html", "HTML", false, true},
	".pdf":   {KindPDF, "pdf", "PDF", false, false},
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
	// Allow gzip streams that start with gzip magic bytes; if a .pdb.gz was
	// already stored uncompressed, surface a clear error rather than panicking.
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
