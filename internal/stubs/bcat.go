package stubs

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/n-popescu/grid0plus/internal/bcat"
)

const bcatListPrefix = "/api/nx/v1/list/nx_data_"
const bcatDataPrefix = "/api/nx/v1/data/"

// nativeBCAT keeps the proven 304 response unless a title has operator-owned
// files. An invalid or absent seed must never turn a previously harmless BCAT
// poll into an HTTP error on the console.
func (h *Handler) nativeBCAT(w http.ResponseWriter, r *http.Request, host string) {
	if r.Method != http.MethodGet || h.cfg.BCATSeedDir == "" {
		h.newsTopics(w, r)
		return
	}
	path := r.URL.Path
	if strings.HasPrefix(host, "bcat-list-") && strings.HasPrefix(path, bcatListPrefix) {
		title := strings.TrimPrefix(path, bcatListPrefix)
		files, err := bcat.ReadNativeSeed(h.cfg.BCATSeedDir, title)
		if err != nil {
			h.log.Error("bcat: read seed", "title", title, "error", err)
			h.newsTopics(w, r)
			return
		}
		if len(files) == 0 {
			h.newsTopics(w, r)
			return
		}
		body, err := bcat.BuildNativeList(title, "bcat-data-lp1.cdn.nintendo.net", files)
		if err != nil {
			h.log.Error("bcat: build list", "title", title, "error", err)
			h.newsTopics(w, r)
			return
		}
		h.writeNativeBCAT(w, r, bcat.BuildContainer(body), "list")
		return
	}
	if strings.HasPrefix(host, "bcat-data-") && strings.HasPrefix(path, bcatDataPrefix) {
		parts := strings.Split(strings.TrimPrefix(path, bcatDataPrefix), "/")
		if len(parts) != 3 {
			h.newsTopics(w, r)
			return
		}
		files, err := bcat.ReadNativeSeed(h.cfg.BCATSeedDir, parts[0])
		if err != nil {
			h.log.Error("bcat: read seed", "title", parts[0], "error", err)
			h.newsTopics(w, r)
			return
		}
		for _, file := range files {
			if file.Directory == parts[1] && file.Name == parts[2] {
				h.writeNativeBCAT(w, r, bcat.BuildContainer(file.Data), "data")
				return
			}
		}
	}
	h.newsTopics(w, r)
}

func (h *Handler) writeNativeBCAT(w http.ResponseWriter, r *http.Request, body []byte, kind string) {
	sum := sha256.Sum256(body)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Connection", "close")
	if h.cfg.BCATRecordDir != "" {
		h.bcat.Observe(r, http.StatusOK, "bcat: native "+kind)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
