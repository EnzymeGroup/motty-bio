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
	"sort"
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
	"motty/internal/term"
)

var (
	hitDocumentTogglePreview = tree.ID(documentHits, "toggle-preview")
	hitDocumentOpenBrowser   = tree.ID(documentHits, "open-browser")
	hitDocumentOpenExternal  = tree.ID(documentHits, "open-external")
	hitSFTPOverlayStructures = tree.ID(sftpHits, "sftp-overlay-structures")
	hitSFTPMenuOverlay       = tree.ID(sftpMenuHits, "sftp-menu-overlay")
)

var previewDocSeq atomic.Uint64

const (
	maxPreviewSiblingEntries = 64
	maxPreviewSiblingBytes   = 16 << 20
	maxOverlayStructures     = 12
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
		Theme:        d.previewThemeInfo,
		LoadSibling:  d.loadPreviewSibling,
		ListSiblings: d.listPreviewSiblings,
		MaxBytes:     d.previewLimitBytes(),
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
			if filepreview.NeedsAsync(subPath) {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
	if d.local && len(d.preview.doc.Overlays) == 0 {
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
	if len(data) == 0 {
		data = d.preview.doc.Data
	}
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
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return
	}
	switch msg.Type {
	case "copy":
		if msg.Text != "" && d.owner.options.SetClipboardText != nil {
			_ = d.owner.options.SetClipboardText(msg.Text)
		}
	case "open-sibling":
		d.openSiblingDocument(msg.Path)
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

func (d *fileDocument) openSiblingDocument(target string) {
	target = strings.TrimSpace(target)
	if target == "" || d.owner == nil {
		return
	}
	var resolved string
	if d.local {
		if filepath.IsAbs(target) {
			resolved = filepath.Clean(target)
		} else {
			resolved = filepath.Join(filepath.Dir(d.location), filepath.FromSlash(target))
		}
	} else {
		if strings.HasPrefix(target, "/") {
			resolved = path.Clean(target)
		} else {
			resolved = path.Join(path.Dir(d.location), target)
		}
	}
	var opener func() (sftpclient.Remote, error)
	if d.preview != nil {
		opener = d.preview.remoteOpener
	}
	d.owner.openFileLocation(d.view, d.source, resolved, d.local, opener, d.origin)
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

func (d *fileDocument) listPreviewSiblings(ctx context.Context) ([]string, error) {
	if d.preview == nil || d.location == "" {
		return nil, nil
	}
	var names []string
	if d.local {
		entries, err := os.ReadDir(filepath.Dir(d.location))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
	} else {
		opener := d.preview.remoteOpener
		if opener == nil {
			return nil, errors.New("remote unavailable")
		}
		remote, err := opener()
		if err != nil {
			return nil, err
		}
		defer remote.Close()
		infos, err := remote.ReadDir(path.Dir(d.location))
		if err != nil {
			return nil, err
		}
		for _, info := range infos {
			if !info.IsDir() {
				names = append(names, info.Name())
			}
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	sort.Strings(names)
	return names, nil
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
		data, err = readRemoteBytesBounded(remote, remotePath, limit)
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

func readRemoteBytesBounded(remote sftpclient.Remote, remotePath string, limit int) ([]byte, error) {
	file, err := remote.Open(remotePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	limited := io.LimitReader(file, int64(limit)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, sftpclient.ErrPreviewFileTooLarge
	}
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

// openFileLocation opens an arbitrary local or remote file directly in Motty's
// tabbed file preview window (used by clickable CSV/PAE sibling paths and
// terminal Cmd+Click).
func (state *State) openFileLocation(view *sftpView, sourceLabel, location string, local bool, remoteOpener func() (sftpclient.Remote, error), origin *sftpWindow) {
	if state.options.OpenContentWindow == nil || location == "" {
		return
	}
	if !local && remoteOpener == nil && view != nil {
		remoteOpener = view.openFiles
	}
	if !local && remoteOpener == nil {
		state.fileOpenNotice(origin, state.messages.FileWindow.CannotOpen, state.messages.SFTPUnavailable, components.ToastError)
		return
	}
	scopeID := "direct"
	if view != nil {
		for i := range state.sessions {
			if state.sessions[i].files == view {
				scopeID = strconv.FormatUint(uint64(state.sessions[i].ID), 10)
				if sourceLabel == "" {
					sourceLabel = state.sessions[i].Title + " · " + state.sessions[i].Address
				}
				break
			}
		}
		if view.detached != nil {
			scopeID = strconv.FormatUint(uint64(view.detached.session.ID), 10)
			if sourceLabel == "" {
				sourceLabel = view.detached.session.Title + " · " + view.detached.session.Address
			}
		}
	}
	if local {
		sourceLabel = state.messages.FileWindow.Local
	}
	key := fmt.Sprintf("%s:%t:%s", scopeID, local, location)
	if existing := state.fileWindows.documents[key]; existing != nil {
		state.fileWindows.activate(existing.cacheKey)
		return
	}
	if len(state.fileWindows.documents) >= 8 {
		state.fileOpenNotice(origin, state.messages.FileWindow.CannotOpen, state.messages.FileWindow.TooMany, components.ToastWarning)
		return
	}
	if state.fileWindows.documents == nil {
		state.fileWindows.documents = make(map[string]*fileDocument)
	}
	name := path.Base(location)
	if local {
		name = filepath.Base(location)
	}
	d := &fileDocument{
		owner:     state,
		cacheKey:  key,
		location:  location,
		name:      name,
		source:    sourceLabel,
		local:     local,
		view:      view,
		origin:    origin,
		previewMB: state.filePreviewMB,
		status:    state.messages.FileWindow.Loading,
	}
	if !local && remoteOpener != nil {
		d.preview = &filePreviewState{
			docID:        strconv.FormatUint(previewDocSeq.Add(1), 10),
			remoteOpener: remoteOpener,
		}
	}
	state.fileWindows.documents[key] = d
	state.fileWindows.pending = key
	if local {
		go func() {
			data, err := readLocalDocument(location, d.previewLimitBytes())
			state.postFileEvent(decodeFileEvent(d, data, err))
		}()
		return
	}
	d.openRemoteWorker(remoteOpener)
	if !d.worker.OpenDocument(location, d.previewLimitBytes()) {
		delete(state.fileWindows.documents, key)
		d.worker.Close()
		state.fileOpenNotice(origin, state.messages.FileWindow.CannotOpen, state.messages.SFTPUnavailable, components.ToastError)
	}
}

// selectedStructurePaths returns the paths of selected files when 2..12
// 3D molecular structures are selected on the active side of view.
func selectedStructurePaths(view *sftpView, local bool) []string {
	if view == nil {
		return nil
	}
	folder := &view.sftpFolder
	if local {
		folder = &view.lfolder
	}
	var paths []string
	for i := range folder.rows {
		row := &folder.rows[i]
		if !row.selected {
			continue
		}
		if row.entry.IsDir() || !filepreview.IsStructureName(row.entry.Name) {
			return nil
		}
		if local {
			paths = append(paths, filepath.Join(folder.path, row.entry.Name))
		} else {
			paths = append(paths, path.Join(folder.path, row.entry.Name))
		}
	}
	if len(paths) < 2 || len(paths) > maxOverlayStructures {
		return nil
	}
	return paths
}

// openStructureOverlay loads 2..12 selected 3D structures, runs Kabsch
// superposition against the first structure, and opens them together in Mol*.
func (state *State) openStructureOverlay(view *sftpView, local bool) {
	if state.options.OpenContentWindow == nil || view == nil {
		return
	}
	locations := selectedStructurePaths(view, local)
	if len(locations) < 2 {
		return
	}
	var session *Session
	for i := range state.sessions {
		if state.sessions[i].files == view {
			session = &state.sessions[i]
			break
		}
	}
	if view.detached != nil {
		session = &view.detached.session
	}
	if session == nil {
		return
	}
	var opener func() (sftpclient.Remote, error)
	if !local {
		opener = view.openFiles
		if opener == nil {
			state.fileOpenNotice(view.detached, state.messages.FileWindow.CannotOpen, state.messages.SFTPUnavailable, components.ToastError)
			return
		}
	}
	key := fmt.Sprintf("overlay:%d:%t:%s", session.ID, local, strings.Join(locations, "|"))
	if existing := state.fileWindows.documents[key]; existing != nil {
		state.fileWindows.activate(existing.cacheKey)
		return
	}
	if len(state.fileWindows.documents) >= 8 {
		state.fileOpenNotice(view.detached, state.messages.FileWindow.CannotOpen, state.messages.FileWindow.TooMany, components.ToastWarning)
		return
	}
	if state.fileWindows.documents == nil {
		state.fileWindows.documents = make(map[string]*fileDocument)
	}
	firstBase := path.Base(locations[0])
	source := session.Title + " · " + session.Address
	if local {
		firstBase = filepath.Base(locations[0])
		source = state.messages.FileWindow.Local
	}
	tabName := fmt.Sprintf("%s (+%d)", firstBase, len(locations)-1)
	d := &fileDocument{
		owner:     state,
		cacheKey:  key,
		location:  locations[0],
		name:      tabName,
		source:    source,
		local:     local,
		view:      view,
		origin:    view.detached,
		previewMB: state.filePreviewMB,
		status:    state.messages.FileWindow.Loading,
	}
	state.fileWindows.documents[key] = d
	state.fileWindows.pending = key

	limit := d.previewLimitBytes()
	go func() {
		inputs := make([]filepreview.OverlayInput, 0, len(locations))
		if local {
			for _, loc := range locations {
				data, err := readLocalDocument(loc, limit)
				if err != nil {
					state.postFileEvent(fileDocumentEvent{document: d, err: err})
					return
				}
				inputs = append(inputs, filepreview.OverlayInput{Name: filepath.Base(loc), Data: data})
			}
		} else {
			remote, err := opener()
			if err != nil {
				state.postFileEvent(fileDocumentEvent{document: d, err: err})
				return
			}
			defer remote.Close()
			for _, loc := range locations {
				data, err := readRemoteBytesBounded(remote, loc, limit)
				if err != nil {
					state.postFileEvent(fileDocumentEvent{document: d, err: err})
					return
				}
				inputs = append(inputs, filepreview.OverlayInput{Name: path.Base(loc), Data: data})
			}
		}
		doc, err := filepreview.PrepareOverlay(inputs, true, limit)
		if err != nil {
			state.postFileEvent(fileDocumentEvent{document: d, err: err})
			return
		}
		state.postFileEvent(fileDocumentEvent{
			document: d,
			data:     doc.Data,
			preview:  doc,
		})
	}()
}

// tryOpenTerminalFilePreview checks whether the terminal cell under a Cmd+Click
// holds a previewable file path (PDB/CIF/SDF/FASTA/A3M/CSV/PAE/SMILES/SVG/HTML/PDF/image)
// and opens it directly in Motty's preview window.
func (state *State) tryOpenTerminalFilePreview(session *Session, position term.Position) bool {
	if session == nil || session.term == nil {
		return false
	}
	token := session.term.screen.TokenAt(position)
	if token == "" || strings.Contains(token, "://") {
		return false
	}
	filename := pathLine.ReplaceAllString(token, "")
	if !filepreview.IsPreviewableName(filename) {
		return false
	}
	return state.openTerminalFilePreview(session, filename)
}

// openTerminalFilePreview resolves a terminal file token against the session's
// local or remote working directory and opens it in Motty's file preview window.
func (state *State) openTerminalFilePreview(session *Session, rawPath string) bool {
	if session == nil || state.options.OpenContentWindow == nil {
		return false
	}
	filename := strings.TrimSpace(pathLine.ReplaceAllString(rawPath, ""))
	if filename == "" {
		return false
	}
	if session.isLocal() && session.wslDistro() == "" {
		target, ok := state.localRevealTarget(session, filename)
		if !ok {
			return false
		}
		info, err := os.Stat(target)
		if err != nil || info.IsDir() {
			return false
		}
		state.openFileLocation(session.files, state.messages.FileWindow.Local, target, true, nil, nil)
		return true
	}
	if session.conn == nil || !session.hasServer() {
		return false
	}
	var opener func() (sftpclient.Remote, error)
	if session.files != nil && session.files.openFiles != nil {
		opener = session.files.openFiles
	} else {
		conn := session.conn
		opener = func() (sftpclient.Remote, error) { return openRemoteFiles(conn, transferAuto) }
	}
	resolved := resolveRemoteTerminalPath(session, filename)
	if resolved == "" {
		return false
	}
	source := session.Title + " · " + session.Address
	if strings.HasPrefix(resolved, "~/") {
		// SFTP opens relative paths directly against the remote user's home directory.
		resolved = strings.TrimPrefix(resolved, "~/")
	}
	state.openFileLocation(session.files, source, resolved, false, opener, nil)
	return true
}

func resolveRemoteTerminalPath(session *Session, filename string) string {
	if strings.HasPrefix(filename, "/") || strings.HasPrefix(filename, "~/") {
		return path.Clean(filename)
	}
	dir := ""
	if session.term != nil && session.term.reportedDirectory.Path != "" {
		dir = session.term.reportedDirectory.Path
	} else if session.files != nil && session.files.path != "" {
		dir = session.files.path
	}
	if dir == "" {
		return ""
	}
	return path.Clean(path.Join(dir, filename))
}
