package filepreview

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
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

// SiblingLoader loads a relative sibling asset for an HTML document (e.g.
// "style.css" or "images/plot.png"). relPath is already normalized and verified
// to stay inside the HTML document's directory.
type SiblingLoader func(ctx context.Context, relPath string) ([]byte, error)

// Source binds one previewable Document to its live theme and optional sibling
// asset loader (used by KindHTML).
type Source struct {
	Doc         func() *Document
	Theme       func() ThemeInfo
	LoadSibling SiblingLoader
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

// Resolve serves one document-scoped path (the portion after "doc/<docID>/").
// When a sibling asset needs asynchronous I/O, Resolve invokes loadSibling.
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
		return resolveMolstar(doc, source.Theme, strings.TrimPrefix(clean, "molstar/"))
	case strings.HasPrefix(clean, "sequence/"):
		return resolveSequence(doc, source.Theme, strings.TrimPrefix(clean, "sequence/"))
	case strings.HasPrefix(clean, "svg/"):
		return resolveSVG(doc, source.Theme, strings.TrimPrefix(clean, "svg/"))
	case strings.HasPrefix(clean, "pdf/"):
		return resolvePDF(doc)
	case strings.HasPrefix(clean, "site/"):
		return resolveHTMLSite(ctx, doc, source.LoadSibling, strings.TrimPrefix(clean, "site/"))
	default:
		return notFound()
	}
}

func resolveMolstar(doc *Document, themeFn func() ThemeInfo, rel string) Response {
	switch rel {
	case "index.html":
		return embeddedResponse("molstar/molstar-host.html")
	case "meta.json":
		return metaResponse(doc, themeFn)
	case "payload":
		return Response{
			Status: http.StatusOK,
			MIME:   "text/plain; charset=utf-8",
			Data:   doc.Data,
		}
	default:
		return embeddedResponse(path.Join("molstar", rel))
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
// and rejects absolute paths or parent-directory escapes ("..").
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
		Name       string `json:"name"`
		Format     string `json:"format"`
		Label      string `json:"label"`
		Summary    string `json:"summary"`
		Compressed bool   `json:"compressed"`
		Revision   uint64 `json:"revision"`
		Dark       bool   `json:"dark"`
		Background int    `json:"background"`
	}{
		Name:       doc.Name,
		Format:     doc.Descriptor.Format,
		Label:      doc.Descriptor.Label,
		Summary:    doc.Summary,
		Compressed: doc.Descriptor.Compressed,
		Revision:   doc.Revision,
		Dark:       theme.Dark,
		Background: theme.Background,
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
