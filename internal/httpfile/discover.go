package httpfile

import (
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// planExtensions are the file extensions folder discovery treats as test plans:
// `.http` (IntelliJ HTTP Client) and `.rest` (VS Code REST Client) — the two
// names for the same format lazyhttp parses.
var planExtensions = map[string]bool{".http": true, ".rest": true}

// skipDirs are directory names folder discovery never descends into: VCS
// metadata and dependency caches that would only bury the overview in noise.
// Dot-prefixed directories are skipped too (see skipDir).
var skipDirs = map[string]bool{"node_modules": true, "vendor": true}

// PlanFile is one test plan discovered while walking a folder tree.
type PlanFile struct {
	Path string // absolute path, opened when the entry is selected
	Rel  string // path relative to the walked root, for display and filtering
	Dir  string // relative parent directory ("" = the root itself), the group heading
	Name string // base file name
}

// PlanIndex is the outcome of walking a folder for test plans: the root that was
// walked, the plans found (sorted by Rel), and any walk error. Like EnvDiscovery
// it keeps the whole outcome so the UI can explain an empty result rather than
// failing silently.
type PlanIndex struct {
	Root  string
	Files []PlanFile
	Err   error
}

// DiscoverPlans walks root recursively and returns every .http/.rest file found,
// sorted by their path relative to root. Dot-prefixed directories (.git and the
// like) and well-known dependency caches are skipped; unreadable entries are
// passed over rather than aborting the walk. A fatal walk error is reported in
// Err alongside whatever was found before it.
func DiscoverPlans(root string) PlanIndex {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	idx := PlanIndex{Root: abs}
	idx.Err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip an unreadable dir/file instead of failing the whole walk
		}
		if d.IsDir() {
			if path != abs && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !planExtensions[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		rel, relErr := filepath.Rel(abs, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel) // display/filter value; keep it `/`-separated on every OS
		idx.Files = append(idx.Files, PlanFile{
			Path: path,
			Rel:  rel,
			Dir:  relDir(rel),
			Name: d.Name(),
		})
		return nil
	})
	// Sort by Rel: this keeps same-directory entries contiguous (Rel shares the
	// Dir prefix), so the UI can emit a group heading whenever Dir changes.
	sort.Slice(idx.Files, func(i, j int) bool {
		return idx.Files[i].Rel < idx.Files[j].Rel
	})
	return idx
}

// skipDir reports whether a directory should be pruned from discovery: anything
// dot-prefixed (.git, .idea, hidden trees) or a known dependency cache.
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || skipDirs[name]
}

// relDir is the parent directory of a relative path, normalised to "" for files
// that sit directly in the root (path.Dir reports "." there). rel is always
// slash-separated (see DiscoverPlans), so use path.Dir to stay `/`-separated on
// every OS rather than filepath.Dir, which would re-introduce `\` on Windows.
func relDir(rel string) string {
	dir := path.Dir(rel)
	if dir == "." {
		return ""
	}
	return dir
}

// CountSteps parses a plan file just far enough to count its steps, for the
// overview's per-file count. It returns -1 on any read/parse error: the count is
// decoration, so a broken file shows no number here rather than an error — the
// real diagnostic surfaces when the file is opened.
func CountSteps(path string) int {
	steps, err := ParseFile(path, nil)
	if err != nil {
		return -1
	}
	return len(steps)
}
