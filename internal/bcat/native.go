package bcat

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BuildContainer wraps an operator-supplied payload in the unencrypted BCAT
// envelope. The all-zero signature is intentional: a console must have the
// matching local signature-check patch before it can accept this content.
func BuildContainer(payload []byte) []byte {
	out := make([]byte, 0x120+len(payload))
	copy(out, "bcat")
	out[4] = 1
	copy(out[0x120:], payload)
	return out
}

// NativeFile is a delivery-cache file selected from an operator-owned seed.
type NativeFile struct {
	Directory string
	Name      string
	Data      []byte
}

// ReadNativeSeed loads <root>/<title id>/<directory>/<filename>. Files under
// other titles are never advertised, and symlinks are excluded to prevent an
// operator-facing seed directory from exposing unrelated server files.
func ReadNativeSeed(root, titleID string) ([]NativeFile, error) {
	if root == "" || len(titleID) != 16 || strings.Trim(titleID, "0123456789abcdef") != "" {
		return nil, nil
	}
	base := filepath.Join(root, titleID)
	info, err := os.Lstat(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	dirs, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []NativeFile
	for _, dir := range dirs {
		if !dir.IsDir() || !safeName(dir.Name()) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(base, dir.Name()))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !safeName(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			if info.Size() > 64<<20 {
				return nil, fmt.Errorf("bcat: file %s exceeds 64 MiB", entry.Name())
			}
			data, err := os.ReadFile(filepath.Join(base, dir.Name(), entry.Name()))
			if err != nil {
				return nil, err
			}
			files = append(files, NativeFile{dir.Name(), entry.Name(), data})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Directory != files[j].Directory {
			return files[i].Directory < files[j].Directory
		}
		return files[i].Name < files[j].Name
	})
	return files, nil
}

func safeName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}

// BuildNativeList produces the known fixed fields of the BCAT list format.
// The digest algorithm still needs a hardware capture; keeping it in one
// function makes that uncertainty local to this format, not the HTTP path.
func BuildNativeList(titleID, dataHost string, files []NativeFile) ([]byte, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("bcat: empty seed for title %s", titleID)
	}
	groups := map[string][]NativeFile{}
	for _, file := range files {
		if !safeName(file.Directory) || !safeName(file.Name) {
			return nil, fmt.Errorf("bcat: unsafe seed name")
		}
		groups[file.Directory] = append(groups[file.Directory], file)
	}
	dirs := make([]string, 0, len(groups))
	for dir := range groups {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	w := newMsgpackWriter()
	w.mapLen(5)
	w.str("topic_id")
	w.str("nx_data_" + titleID)
	w.str("service_status")
	w.str("in_service")
	w.str("na_required")
	w.bool(false)
	w.str("required_application_version")
	w.uint(0)
	w.str("directories")
	w.arrayLen(len(dirs))
	for _, dir := range dirs {
		w.mapLen(5)
		w.str("name")
		w.str(dir)
		w.str("mode")
		// Fest and schedule packs must be present before the game consumes the
		// directory. Real Splatoon 2 festival manifests mark fesdata as sync;
		// advertising these operator-seeded packs as async can leave the console
		// with the list cached but no download started for the active event.
		w.str("sync")
		w.str("by_country_group")
		w.bool(false)
		w.str("digest")
		w.str(digestFiles(groups[dir]))
		w.str("data_list")
		w.arrayLen(len(groups[dir]))
		for i, file := range groups[dir] {
			// bcat.nso's list parser requires version.format,
			// version.semantics, and news_id on every entry before it accepts
			// the list. The module validates these as uint8, uint8, and a
			// non-negative integer respectively, but this title's values have
			// not been observed in a successful server response.
			// TODO(capture-needed): compare these three values with a
			// successful Splatoon 3 BCAT list response; change only here.
			w.mapLen(7)
			w.str("data_id")
			w.uint(uint64(i + 1))
			w.str("filename")
			w.str(file.Name)
			w.str("url")
			w.str("https://" + dataHost + "/api/nx/v1/data/" + titleID + "/" + dir + "/" + file.Name)
			w.str("digest")
			w.str(digestBytes(file.Data))
			w.str("size")
			w.uint(uint64(len(file.Data)))
			w.str("version")
			w.mapLen(2)
			w.str("format")
			w.uint(0)
			w.str("semantics")
			w.uint(0)
			w.str("news_id")
			w.uint(0)
		}
	}
	return w.bytes(), nil
}

func digestBytes(b []byte) string { d := md5.Sum(b); return hex.EncodeToString(d[:]) }
func digestFiles(files []NativeFile) string {
	var joined []byte
	for _, f := range files {
		joined = append(joined, f.Data...)
	}
	return digestBytes(joined)
}
