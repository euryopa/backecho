package store

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// IgnoreLines are the machine-specific files under the data dir.
func IgnoreLines(dataRel string) []string {
	dataRel = strings.Trim(filepath.ToSlash(dataRel), "/")
	return []string{dataRel + "/local.md", dataRel + "/local.json", dataRel + "/state.json"}
}

// EnsureGitignore appends the lines a repo's .gitignore does not already
// have, once. It never creates a .gitignore: missing reports true instead.
func EnsureGitignore(root string, lines []string) (added []string, missing bool, err error) {
	path := filepath.Join(root, ".gitignore")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	have := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		have[l] = true
		have[strings.TrimPrefix(l, "/")] = true
	}
	for _, l := range lines {
		dir := l[:strings.LastIndexByte(l, '/')+1]
		if have[l] || have["/"+l] || have[dir] || have[strings.TrimSuffix(dir, "/")] || have[dir+"*"] || have[dir+"local.*"] && strings.Contains(l, "/local.") {
			continue
		}
		added = append(added, l)
	}
	if len(added) == 0 {
		return nil, false, nil
	}
	var buf strings.Builder
	buf.Write(b)
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		buf.WriteByte('\n')
	}
	buf.WriteString("# backecho: machine-specific notes and parse cursor\n")
	for _, l := range added {
		buf.WriteString(l + "\n")
	}
	return added, false, WriteFileAtomic(path, []byte(buf.String()))
}
