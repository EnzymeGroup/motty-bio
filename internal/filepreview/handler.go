package filepreview

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// SchemeName is the custom Viva WebView scheme used for embedded previews.
const SchemeName = "motty-view"

// SchemeHost is the authority component paired with SchemeName.
const SchemeHost = "app"

// ThemeInfo carries Motty's active color polarity into the viewer host page.
type ThemeInfo struct {
	Dark       bool
	Background int // 0xRRGGBB
}

// SiblingLoader loads a relative sibling asset from the document's directory
// (e.g. "style.css" or "other_model.cif"). relPath is already normalized and
// verified to stay inside the document's directory.
type SiblingLoader func(ctx context.Context, relPath string) ([]byte, error)

// SiblingLister lists entry names in the document's directory.
type SiblingLister func(ctx context.Context) ([]string, error)

// Source binds one previewable Document to its live theme and optional sibling
// loaders (used by KindHTML, KindStructure overlay, and KindConfidence pairing).
type Source struct {
	Doc          func() *Document
	Theme        func() ThemeInfo
	LoadSibling  SiblingLoader
	ListSiblings SiblingLister
	MaxBytes     int
}

// Response is a protocol-neutral HTTP/scheme response.
type Response struct {
	Status  int
	MIME    string
	Headers map[string]string
	Data    []byte
}

// DocumentURL builds the custom-scheme entry URL for a document id and entry
// path (e.g. "motty-view://app/doc/1/molstar/index.html").
func DocumentURL(docID string, entryPath string) string {
	return SchemeName + "://" + SchemeHost + "/doc/" + docID + "/" + strings.TrimPrefix(entryPath, "/")
}

// NeedsAsync reports whether subPath may invoke local disk or remote SFTP I/O.
func NeedsAsync(subPath string) bool {
	clean := strings.TrimPrefix(path.Clean("/"+subPath), "/")
	return strings.HasPrefix(clean, "site/") ||
		clean == "molstar/siblings.json" ||
		strings.HasPrefix(clean, "molstar/overlay/") ||
		clean == "confidence/siblings.json"
}

// Resolve serves one document-scoped path (the portion after "doc/<docID>/").
func Resolve(ctx context.Context, source Source, subPath string) Response {
	doc := (*Document)(nil)
	if source.Doc != nil {
		doc = source.Doc()
	}
	if doc == nil {
		return notFound()
	}
	clean := strings.TrimPrefix(path.Clean("/"+subPath), "/")
	if clean == "" || strings.HasPrefix(clean, "..") {
		return notFound()
	}

	switch {
	case strings.HasPrefix(clean, "molstar/"):
		return resolveMolstar(ctx, doc, source, strings.TrimPrefix(clean, "molstar/"))
	case strings.HasPrefix(clean, "sequence/"):
		return resolveSequence(doc, source.Theme, strings.TrimPrefix(clean, "sequence/"))
	case strings.HasPrefix(clean, "svg/"):
		return resolveSVG(doc, source.Theme, strings.TrimPrefix(clean, "svg/"))
	case strings.HasPrefix(clean, "pdf/"):
		return resolvePDF(doc)
	case strings.HasPrefix(clean, "site/"):
		return resolveHTMLSite(ctx, doc, source.LoadSibling, strings.TrimPrefix(clean, "site/"))
	case strings.HasPrefix(clean, "confidence/"):
		return resolveConfidence(ctx, doc, source, strings.TrimPrefix(clean, "confidence/"))
	case strings.HasPrefix(clean, "table/"):
		return resolveTable(doc, source.Theme, strings.TrimPrefix(clean, "table/"))
	case strings.HasPrefix(clean, "smiles/"):
		return resolveSMILES(doc, source.Theme, strings.TrimPrefix(clean, "smiles/"))
	default:
		return notFound()
	}
}

func resolveMolstar(ctx context.Context, doc *Document, source Source, rel string) Response {
	switch {
	case rel == "index.html":
		return embeddedResponse("molstar/molstar-host.html")
	case rel == "meta.json":
		return metaResponse(doc, source.Theme)
	case rel == "payload":
		return Response{
			Status: http.StatusOK,
			MIME:   "text/plain; charset=utf-8",
			Data:   doc.Data,
		}
	case rel == "overlays.json":
		payload, _ := json.Marshal(doc.Overlays)
		return Response{
			Status: http.StatusOK,
			MIME:   "application/json; charset=utf-8",
			Data:   payload,
		}
	case strings.HasPrefix(rel, "overlay-item/"):
		idx, err := strconv.Atoi(strings.TrimPrefix(rel, "overlay-item/"))
		if err != nil || idx < 0 || idx >= len(doc.Overlays) {
			return notFound()
		}
		return Response{
			Status: http.StatusOK,
			MIME:   "text/plain; charset=utf-8",
			Data:   doc.Overlays[idx].Data,
		}
	case rel == "siblings.json":
		return listStructureSiblings(ctx, doc.Name, source.ListSiblings)
	case strings.HasPrefix(rel, "overlay/aligned/"):
		return resolveDynamicOverlay(ctx, doc, source, strings.TrimPrefix(rel, "overlay/aligned/"), true)
	case strings.HasPrefix(rel, "overlay/raw/"):
		return resolveDynamicOverlay(ctx, doc, source, strings.TrimPrefix(rel, "overlay/raw/"), false)
	default:
		return embeddedResponse(path.Join("molstar", rel))
	}
}

func listStructureSiblings(ctx context.Context, selfName string, lister SiblingLister) Response {
	var names []string
	if lister != nil {
		if entries, err := lister(ctx); err == nil {
			for _, name := range entries {
				if name != selfName && IsStructureName(name) {
					names = append(names, name)
				}
			}
		}
	}
	if names == nil {
		names = []string{}
	}
	payload, _ := json.Marshal(names)
	return Response{
		Status: http.StatusOK,
		MIME:   "application/json; charset=utf-8",
		Data:   payload,
	}
}

func resolveDynamicOverlay(ctx context.Context, doc *Document, source Source, relName string, align bool) Response {
	if source.LoadSibling == nil {
		return notFound()
	}
	safeName, ok := SafeRelativePath(relName)
	if !ok || !IsStructureName(safeName) {
		return Response{Status: http.StatusForbidden, MIME: "text/plain; charset=utf-8", Data: []byte("forbidden")}
	}
	raw, err := source.LoadSibling(ctx, safeName)
	if err != nil {
		return notFound()
	}
	maxBytes := source.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 20 << 20
	}
	mob, err := Prepare(safeName, raw, maxBytes)
	if err != nil || mob == nil || mob.Descriptor.Kind != KindStructure {
		return notFound()
	}
	result := struct {
		Name       string  `json:"name"`
		Format     string  `json:"format"`
		Content    string  `json:"content"`
		RMSD       float64 `json:"rmsd"`
		BeforeRMSD float64 `json:"beforeRmsd"`
		TMScore    float64 `json:"tmScore"`
		Pairs      int     `json:"pairs"`
		Aligned    bool    `json:"aligned"`
	}{
		Name:    mob.Name,
		Format:  mob.Descriptor.Format,
		Content: string(mob.Data),
	}
	if align {
		res := AlignStructure(doc.Descriptor.Format, doc.Data, mob.Descriptor.Format, mob.Data)
		result.Content = string(res.AlignedData)
		result.RMSD = res.RMSD
		result.BeforeRMSD = res.BeforeRMSD
		result.TMScore = res.TMScore
		result.Pairs = res.Pairs
		result.Aligned = res.Aligned
	}
	payload, _ := json.Marshal(result)
	return Response{
		Status: http.StatusOK,
		MIME:   "application/json; charset=utf-8",
		Data:   payload,
	}
}

func resolveConfidence(ctx context.Context, doc *Document, source Source, rel string) Response {
	switch rel {
	case "index.html":
		return embeddedResponse("confidence-viewer.html")
	case "meta.json":
		return metaResponse(doc, source.Theme)
	case "payload":
		return Response{
			Status: http.StatusOK,
			MIME:   "application/json; charset=utf-8",
			Data:   doc.Data,
		}
	case "siblings.json":
		return listStructureSiblings(ctx, doc.Name, source.ListSiblings)
	default:
		return notFound()
	}
}

func resolveTable(doc *Document, themeFn func() ThemeInfo, rel string) Response {
	switch rel {
	case "index.html":
		return embeddedResponse("table-viewer.html")
	case "meta.json":
		return metaResponse(doc, themeFn)
	case "payload":
		return Response{
			Status: http.StatusOK,
			MIME:   "text/plain; charset=utf-8",
			Data:   doc.Data,
		}
	case "vendor/smiles-drawer/smiles-drawer.min.js":
		return embeddedResponse("vendor/smiles-drawer/smiles-drawer.min.js")
	default:
		return notFound()
	}
}

func resolveSMILES(doc *Document, themeFn func() ThemeInfo, rel string) Response {
	switch rel {
	case "index.html":
		return embeddedResponse("smiles-viewer.html")
	case "meta.json":
		return metaResponse(doc, themeFn)
	case "payload":
		return Response{
			Status: http.StatusOK,
			MIME:   "text/plain; charset=utf-8",
			Data:   doc.Data,
		}
	case "vendor/smiles-drawer/smiles-drawer.min.js":
		return embeddedResponse("vendor/smiles-drawer/smiles-drawer.min.js")
	default:
		return notFound()
	}
}

func resolveSequence(doc *Document, themeFn func() ThemeInfo, rel string) Response {
	switch rel {
	case "index.html":
		return embeddedResponse("sequence-viewer.html")
	case "meta.json":
		return metaResponse(doc, themeFn)
	case "payload":
		return Response{
			Status: http.StatusOK,
			MIME:   "text/plain; charset=utf-8",
			Data:   doc.Data,
		}
	default:
		return notFound()
	}
}

func resolveSVG(doc *Document, themeFn func() ThemeInfo, rel string) Response {
	switch rel {
	case "index.html":
		return embeddedResponse("svg-viewer.html")
	case "meta.json":
		return metaResponse(doc, themeFn)
	case "payload.svg":
		return Response{
			Status: http.StatusOK,
			MIME:   "image/svg+xml",
			Headers: map[string]string{
				"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; img-src data:",
			},
			Data: doc.Data,
		}
	default:
		return notFound()
	}
}

func resolvePDF(doc *Document) Response {
	return Response{
		Status: http.StatusOK,
		MIME:   "application/pdf",
		Headers: map[string]string{
			"Content-Disposition": "inline",
		},
		Data: doc.Data,
	}
}

func resolveHTMLSite(ctx context.Context, doc *Document, loadSibling SiblingLoader, rel string) Response {
	entryFile := safeSegment(doc.Name, "index.html")
	if rel == "" || rel == entryFile || rel == doc.Name {
		return Response{
			Status: http.StatusOK,
			MIME:   "text/html; charset=utf-8",
			Data:   doc.Data,
		}
	}
	if loadSibling == nil {
		return notFound()
	}
	normalized, ok := SafeRelativePath(rel)
	if !ok {
		return Response{Status: http.StatusForbidden, MIME: "text/plain; charset=utf-8", Data: []byte("forbidden")}
	}
	data, err := loadSibling(ctx, normalized)
	if err != nil {
		return notFound()
	}
	return Response{
		Status: http.StatusOK,
		MIME:   contentTypeForPath(normalized),
		Data:   data,
	}
}

// SafeRelativePath normalizes a relative asset path requested by an HTML page
// or overlay loader and rejects absolute paths or parent-directory escapes.
func SafeRelativePath(rel string) (string, bool) {
	if rel == "" || strings.ContainsRune(rel, '\x00') || strings.ContainsRune(rel, '\\') {
		return "", false
	}
	if strings.HasPrefix(rel, "/") {
		return "", false
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	return cleaned, true
}

func embeddedResponse(assetPath string) Response {
	mimeType, data, ok := readEmbeddedAsset(assetPath)
	if !ok {
		return notFound()
	}
	return Response{
		Status: http.StatusOK,
		MIME:   mimeType,
		Data:   data,
	}
}

func metaResponse(doc *Document, themeFn func() ThemeInfo) Response {
	theme := ThemeInfo{Dark: false, Background: 0xf6f7f9}
	if themeFn != nil {
		theme = themeFn()
	}
	payload, _ := json.Marshal(struct {
		Name         string `json:"name"`
		Format       string `json:"format"`
		Label        string `json:"label"`
		Summary      string `json:"summary"`
		Compressed   bool   `json:"compressed"`
		Revision     uint64 `json:"revision"`
		Dark         bool   `json:"dark"`
		Background   int    `json:"background"`
		OverlayCount int    `json:"overlayCount"`
	}{
		Name:         doc.Name,
		Format:       doc.Descriptor.Format,
		Label:        doc.Descriptor.Label,
		Summary:      doc.Summary,
		Compressed:   doc.Descriptor.Compressed,
		Revision:     doc.Revision,
		Dark:         theme.Dark,
		Background:   theme.Background,
		OverlayCount: len(doc.Overlays),
	})
	return Response{
		Status: http.StatusOK,
		MIME:   "application/json; charset=utf-8",
		Data:   payload,
	}
}

func notFound() Response {
	return Response{
		Status: http.StatusNotFound,
		MIME:   "text/plain; charset=utf-8",
		Data:   []byte("not found"),
	}
}
