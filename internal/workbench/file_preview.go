package workbench

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/components"
	"github.com/vivaui/viva/gfx"
	"motty/internal/filepreview"
	"motty/internal/sftpclient"
)

var (
	hitDocumentTogglePreview = tree.ID(documentHits, "toggle-preview")
	hitDocumentOpenBrowser   = tree.ID(documentHits, "open-browser")
	hitDocumentOpenExternal  = tree.ID(documentHits, "open-external")
)

var previewDocSeq atomic.Uint64

const (
	maxPreviewSiblingEntries = 64
	maxPreviewSiblingBytes   = 16 << 20
)

type filePreviewState struct {
	doc          *filepreview.Document
	sourceMode   bool
	docID        string
	web          *app.WebView
	webError     string
	loadedRev    uint64
	lastDark     bool
	lastDarkSet  bool
	loopbackID   string
	tempDir      string
	remoteOpener func() (sftpclient.Remote, error)

	siblingMu    sync.Mutex
	siblingCache map[string][]byte
	siblingBytes int
}

func (d *fileDocument) hasPreview() bool {
	return d != nil && d.preview != nil && d.preview.doc != nil
}

func (d *fileDocument) showingPreview() bool {
	return d.hasPreview() && !d.preview.sourceMode
}

func (d *fileDocument) isReadOnlyPreview() bool {
	return d.hasPreview() && !d.preview.doc.Descriptor.TextEditable
}

func (d *fileDocument) isNonTextDocument() bool {
	return d.image != nil || d.isReadOnlyPreview()
}

func (m *fileWindowManager) ensureWebHost() *app.WebHost {
	web := &app.WebHost{}
	web.OnReady = func() {
		if m.web != web {
			return
		}
		m.webReady = true
		if m.window != nil {
			m.window.Invalidate()
		}
	}
	m.web = web
	m.webReady = false
	return web
}

func (m *fileWindowManager) resetWebHost() {
	m.web = nil
	m.webReady = false
}

func (m *fileWindowManager) closePreviewServer() {
	if m.previewServer != nil {
		m.previewServer.Close()
		m.previewServer = nil
	}
}

func (d *fileDocument) setPreview(doc *filepreview.Document, remoteOpener func() (sftpclient.Remote, error)) {
	if doc == nil {
		d.disposePreview()
		return
	}
	if d.preview == nil {
		d.preview = &filePreviewState{
			docID:        strconv.FormatUint(previewDocSeq.Add(1), 10),
			remoteOpener: remoteOpener,
		}
	} else {
		if remoteOpener != nil {
			d.preview.remoteOpener = remoteOpener
		}
		if d.preview.doc != nil && doc.Revision <= d.preview.doc.Revision {
			doc.Revision = d.preview.doc.Revision + 1
		}
		d.preview.clearSiblingCache()
	}
	d.preview.doc = doc
	if !doc.Descriptor.TextEditable {
		d.preview.sourceMode = false
	}
}

func (p *filePreviewState) clearSiblingCache() {
	p.siblingMu.Lock()
	clear(p.siblingCache)
	p.siblingBytes = 0
	p.siblingMu.Unlock()
}

func (d *fileDocument) previewThemeInfo() filepreview.ThemeInfo {
	bg := d.owner.Theme().Background
	r := clampByte(bg.Red)
	g := clampByte(bg.Green)
	b := clampByte(bg.Blue)
	lum := 0.2126*float64(bg.Red) + 0.7152*float64(bg.Green) + 0.0722*float64(bg.Blue)
	return filepreview.ThemeInfo{
		Dark:       lum < 0.5,
		Background: int(r)<<16 | int(g)<<8 | int(b),
	}
}

func clampByte(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

func (d *fileDocument) previewSource() filepreview.Source {
	return filepreview.Source{
		Doc: func() *filepreview.Document {
			if d.preview == nil {
				return nil
			}
			return d.preview.doc
		},
		Theme:       d.previewThemeInfo,
		LoadSibling: d.loadPreviewSibling,
	}
}

func (d *fileDocument) previewVersionedID() string {
	return fmt.Sprintf("%s-r%d", d.preview.docID, d.preview.doc.Revision)
}

func (d *fileDocument) previewURL() string {
	return filepreview.DocumentURL(d.previewVersionedID(), d.preview.doc.EntryPath())
}

func (d *fileDocument) previewScheme() *app.WebScheme {
	source := d.previewSource()
	return &app.WebScheme{
		Name: filepreview.SchemeName,
		ServeAsync: func(request app.WebRequest, respond func(app.WebResponse)) {
			subPath, ok := extractDocSubPath(request.Path)
			if !ok {
				respond(app.WebResponse{Status: http.StatusNotFound, MIME: "text/plain; charset=utf-8", Data: []byte("not found")})
				return
			}
			// Sibling assets under HTML "site/" may perform local disk or SFTP I/O,
			// so handle them off the UI thread; everything else is in-memory.
			if strings.HasPrefix(subPath, "site/") && d.preview != nil && d.preview.doc != nil && d.preview.doc.Descriptor.Kind == filepreview.KindHTML {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					resp := filepreview.Resolve(ctx, source, subPath)
					respond(app.WebResponse{Status: resp.Status, MIME: resp.MIME, Headers: resp.Headers, Data: resp.Data})
				}()
				return
			}
			resp := filepreview.Resolve(context.Background(), source, subPath)
			respond(app.WebResponse{Status: resp.Status, MIME: resp.MIME, Headers: resp.Headers, Data: resp.Data})
		},
	}
}

// extractDocSubPath strips the leading "doc/<id>/" prefix from a scheme request
// path (e.g. "doc/3-r1/molstar/index.html" -> "molstar/index.html").
func extractDocSubPath(reqPath string) (string, bool) {
	trimmed := strings.TrimPrefix(reqPath, "/")
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 3 || parts[0] != "doc" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[2], true
}

func (d *fileDocument) ensurePreviewWebView() {
	if !d.showingPreview() {
		return
	}
	m := &d.owner.fileWindows
	if !m.painting || m.web == nil || !m.webReady {
		return
	}
	p := d.preview
	if p.web != nil && p.web.IsCrashed() {
		p.web.Close()
		p.web = nil
	}
	if p.web != nil || p.webError != "" {
		return
	}
	info := d.previewThemeInfo()
	p.lastDark = info.Dark
	p.lastDarkSet = true
	p.loadedRev = p.doc.Revision
	url := d.previewURL()
	view, err := m.web.Open(app.WebViewConfig{
		URL:                 url,
		Scheme:              d.previewScheme(),
		Ephemeral:           true,
		BridgeMainFrameOnly: true,
		OnMessage:           d.handlePreviewMessage,
		Navigate: func(nav app.WebNavigation) bool {
			lower := strings.ToLower(nav.URL)
			if strings.HasPrefix(lower, filepreview.SchemeName+"://") ||
				strings.HasPrefix(lower, "about:") ||
				strings.HasPrefix(lower, "blob:") ||
				strings.HasPrefix(lower, "data:") {
				return true
			}
			if nav.UserGesture && (strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")) {
				if d.owner.options.OpenURL != nil {
					_ = d.owner.options.OpenURL(nav.URL)
				}
			}
			return false
		},
		NewWindow: func(target string) bool {
			lower := strings.ToLower(target)
			if (strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")) && d.owner.options.OpenURL != nil {
				_ = d.owner.options.OpenURL(target)
			}
			return false
		},
		OnProcessFailed: func(app.WebProcessFailure) {
			p.webError = d.owner.messages.FileWindow.PreviewUnavailable
			d.invalidateWindows()
		},
	})
	if err != nil {
		p.webError = d.owner.messages.FileWindow.PreviewUnavailable
		return
	}
	p.web = view
}

func (d *fileDocument) paintPreview(canvas *gfx.Canvas, theme gfx.Theme, bounds gfx.Rect) {
	if !d.showingPreview() || bounds.Width <= 0 || bounds.Height <= 0 {
		return
	}
	canvas.Rounded(bounds, radius.control, theme.Card)
	canvas.RoundedBorder(bounds, radius.control, 1, theme.Border, theme.Background)

	inner := gfx.Rect{
		X:      bounds.X + 1,
		Y:      bounds.Y + 1,
		Width:  max(bounds.Width-2, 0),
		Height: max(bounds.Height-2, 0),
	}
	d.ensurePreviewWebView()
	p := d.preview
	if p.web != nil && !p.web.IsCrashed() {
		info := d.previewThemeInfo()
		if !p.lastDarkSet || p.lastDark != info.Dark {
			p.lastDark = info.Dark
			p.lastDarkSet = true
			payload, _ := json.Marshal(struct {
				Type       string `json:"type"`
				Dark       bool   `json:"dark"`
				Background int    `json:"background"`
			}{Type: "theme", Dark: info.Dark, Background: info.Background})
			p.web.PostMessage(string(payload))
		}
		if p.loadedRev != p.doc.Revision {
			p.loadedRev = p.doc.Revision
			p.web.Load(d.previewURL())
		}
		canvas.NativeView(p.web.ID(), inner)
		return
	}
	msg := d.owner.messages.FileWindow.PreviewLoading
	if p.webError != "" {
		msg = p.webError
	}
	summary := p.doc.Summary
	if summary != "" {
		canvas.LabelCenter(summary, gfx.Rect{X: inner.X + 16, Y: inner.Y + inner.Height/2 - 22, Width: max(inner.Width-32, 1), Height: 22}, gfx.TextStyle{Size: theme.Metrics.FontSizeM, Color: theme.Foreground})
	}
	canvas.LabelCenter(msg, gfx.Rect{X: inner.X + 16, Y: inner.Y + inner.Height/2 + 2, Width: max(inner.Width-32, 1), Height: 22}, gfx.TextStyle{Size: theme.Metrics.FontSizeS, Color: theme.MutedForeground})
}

func (d *fileDocument) togglePreviewMode() {
	if !d.hasPreview() || !d.preview.doc.Descriptor.TextEditable {
		return
	}
	if d.preview.sourceMode {
		// Switching back to Preview: reflect any unsaved source edits immediately.
		if d.ready {
			current := d.editor.String()
			if current != string(d.preview.doc.Data) {
				d.preview.doc.UpdateText(current)
			}
		}
		d.preview.sourceMode = false
		d.preview.webError = ""
	} else {
		d.preview.sourceMode = true
	}
	d.invalidateWindows()
}

func (d *fileDocument) openPreviewInBrowser() {
	if !d.hasPreview() || d.owner.options.OpenURL == nil {
		return
	}
	if d.preview.sourceMode && d.ready && d.preview.doc.Descriptor.TextEditable {
		current := d.editor.String()
		if current != string(d.preview.doc.Data) {
			d.preview.doc.UpdateText(current)
		}
	}
	m := &d.owner.fileWindows
	if m.previewServer == nil {
		srv, err := filepreview.NewLoopbackServer()
		if err != nil {
			d.notices.show(d.name, err.Error(), components.ToastError)
			return
		}
		m.previewServer = srv
	}
	if d.preview.loopbackID == "" {
		d.preview.loopbackID = m.previewServer.Register(d.previewSource())
	}
	target := m.previewServer.URL(d.preview.loopbackID, d.preview.doc.EntryPath())
	if err := d.owner.options.OpenURL(target); err != nil {
		d.notices.show(d.name, err.Error(), components.ToastError)
	}
}

func (d *fileDocument) openPreviewExternalApp() {
	if !d.hasPreview() || d.owner.options.OpenPath == nil {
		return
	}
	if d.local {
		if err := d.owner.options.OpenPath(d.location); err != nil {
			d.notices.show(d.name, err.Error(), components.ToastError)
		}
		return
	}
	if d.preview.tempDir == "" {
		dir, err := os.MkdirTemp("", "motty-preview-*")
		if err != nil {
			d.notices.show(d.name, err.Error(), components.ToastError)
			return
		}
		d.preview.tempDir = dir
	}
	localPath := filepath.Join(d.preview.tempDir, safeLocalName(d.name))
	data := d.raw
	if d.preview.doc.Descriptor.TextEditable && d.ready && d.dirty() {
		data = []byte(d.editor.String())
	}
	if err := os.WriteFile(localPath, data, 0o600); err != nil {
		d.notices.show(d.name, err.Error(), components.ToastError)
		return
	}
	if err := d.owner.options.OpenPath(localPath); err != nil {
		d.notices.show(d.name, err.Error(), components.ToastError)
	}
}

func (d *fileDocument) handlePreviewMessage(raw string) {
	var msg struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Name string `json:"name"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return
	}
	switch msg.Type {
	case "copy":
		if msg.Text != "" && d.owner.options.SetClipboardText != nil {
			_ = d.owner.options.SetClipboardText(msg.Text)
		}
	case "snapshot":
		const prefix = "data:image/png;base64,"
		if !strings.HasPrefix(msg.Data, prefix) {
			return
		}
		pngBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(msg.Data, prefix))
		if err != nil || len(pngBytes) == 0 {
			return
		}
		saved, err := savePreviewSnapshot(msg.Name, pngBytes)
		if err != nil {
			d.notices.show(d.name, err.Error(), components.ToastError)
			return
		}
		d.notices.show(d.name, fmt.Sprintf(d.owner.messages.FileWindow.SnapshotSavedFmt, saved), components.ToastSuccess)
	}
}

func savePreviewSnapshot(suggested string, pngBytes []byte) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Downloads")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = home
	}
	base := safeLocalName(suggested)
	if base == "" || !strings.HasSuffix(strings.ToLower(base), ".png") {
		base = "structure-snapshot.png"
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	target := filepath.Join(dir, base)
	for i := 1; i < 1000; i++ {
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			break
		}
		target = filepath.Join(dir, fmt.Sprintf("%s-%d.png", stem, i))
	}
	if err := os.WriteFile(target, pngBytes, 0o644); err != nil {
		return "", err
	}
	return target, nil
}

func (d *fileDocument) loadPreviewSibling(ctx context.Context, relPath string) ([]byte, error) {
	if d.preview == nil {
		return nil, errors.New("preview closed")
	}
	p := d.preview
	p.siblingMu.Lock()
	if cached, ok := p.siblingCache[relPath]; ok {
		p.siblingMu.Unlock()
		return cached, nil
	}
	p.siblingMu.Unlock()

	limit := d.previewLimitBytes()
	var data []byte
	var err error
	if d.local {
		baseDir := filepath.Dir(d.location)
		target := filepath.Join(baseDir, filepath.FromSlash(relPath))
		rel, relErr := filepath.Rel(baseDir, target)
		if relErr != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return nil, errors.New("forbidden sibling path")
		}
		data, err = readLocalDocument(target, limit)
	} else {
		opener := p.remoteOpener
		if opener == nil {
			return nil, errors.New("remote unavailable")
		}
		remote, openErr := opener()
		if openErr != nil {
			return nil, openErr
		}
		defer remote.Close()
		remotePath := path.Join(path.Dir(d.location), relPath)
		file, fileErr := remote.Open(remotePath)
		if fileErr != nil {
			return nil, fileErr
		}
		defer file.Close()
		limited := io.LimitReader(file, int64(limit)+1)
		data, err = io.ReadAll(limited)
		if err == nil && len(data) > limit {
			err = sftpclient.ErrPreviewFileTooLarge
		}
	}
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	p.siblingMu.Lock()
	if len(p.siblingCache) < maxPreviewSiblingEntries && p.siblingBytes+len(data) <= maxPreviewSiblingBytes {
		if p.siblingCache == nil {
			p.siblingCache = make(map[string][]byte)
		}
		p.siblingCache[relPath] = data
		p.siblingBytes += len(data)
	}
	p.siblingMu.Unlock()
	return data, nil
}

func (d *fileDocument) disposePreview() {
	if d == nil || d.preview == nil {
		return
	}
	p := d.preview
	d.preview = nil
	if p.web != nil {
		p.web.Close()
		p.web = nil
	}
	if p.loopbackID != "" && d.owner != nil && d.owner.fileWindows.previewServer != nil {
		d.owner.fileWindows.previewServer.Unregister(p.loopbackID)
	}
	if p.tempDir != "" {
		_ = os.RemoveAll(p.tempDir)
	}
	p.remoteOpener = nil
}
