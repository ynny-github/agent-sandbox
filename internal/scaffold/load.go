package scaffold

import (
	"fmt"
	"io/fs"
)

// Fetched is one asset's content, ready to validate and write.
type Fetched struct {
	Asset Asset
	Body  []byte
}

// Load reads every asset from fsys (the binary's embedded files), keyed by
// each Asset's Source path.
func Load(fsys fs.FS) ([]Fetched, error) {
	out := make([]Fetched, 0, len(Assets))
	for _, a := range Assets {
		body, err := fs.ReadFile(fsys, a.Source)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.Source, err)
		}
		out = append(out, Fetched{Asset: a, Body: body})
	}
	return out, nil
}
