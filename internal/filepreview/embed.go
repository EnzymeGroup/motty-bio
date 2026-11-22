package filepreview

import (
	"embed"
	"io/fs"
	"mime"
	"path"
	"strings"
)

//go:embed all:assets
var embeddedAssets embed.FS

func readEmbeddedAsset(rel string) (string, []byte, bool) {
	cleaned := path.Clean("/" + rel)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || strings.HasPrefix(cleaned, "..") {
		return "", nil, false
	}
	data, err := fs.ReadFile(embeddedAssets, path.Join("assets", cleaned))
	if err != nil {
		return "", nil, false
	}
	return contentTypeForPath(cleaned), data, true
}

// contentTypeForPath returns a stable MIME type for web assets and scientific
// document files without relying on OS MIME registries.
func contentTypeForPath(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".html", ".htm", ".xhtml":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".pdf":
		return "application/pdf"
	case ".wasm":
		return "application/wasm"
	case ".txt", ".pdb", ".ent", ".cif", ".mmcif", ".sdf", ".mol", ".mol2", ".xyz", ".gro", ".fasta", ".fa", ".faa", ".fna", ".a3m", ".sto", ".aln":
		return "text/plain; charset=utf-8"
	}
	if guessed := mime.TypeByExtension(ext); guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}
