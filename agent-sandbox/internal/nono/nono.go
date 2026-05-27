package nono

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type extensionConfig struct {
	base      string
	denyWrite []string
}

func extractExtension(v map[string]any) extensionConfig {
	raw := v["extension"]
	delete(v, "extension")
	ext, ok := raw.(map[string]any)
	if !ok {
		return extensionConfig{}
	}
	var cfg extensionConfig
	if base, ok := ext["base"].(string); ok {
		cfg.base = base
	}
	raw2, ok := ext["deny_write"].([]any)
	if !ok {
		return cfg
	}
	result := make([]string, 0, len(raw2))
	for _, item := range raw2 {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	cfg.denyWrite = result
	return cfg
}

func scanDir(dir string, denyWrite []string) ([]string, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	deny := make(map[string]struct{}, len(denyWrite))
	for _, name := range denyWrite {
		deny[name] = struct{}{}
	}
	var dirs, files []string
	for _, e := range entries {
		if _, skip := deny[e.Name()]; skip {
			continue
		}
		name := "./" + e.Name()
		if e.IsDir() {
			dirs = append(dirs, name)
		} else {
			files = append(files, name)
		}
	}
	return dirs, files, nil
}

func mergeIntoFilesystem(v map[string]any, dirs, files []string) {
	fs, ok := v["filesystem"].(map[string]any)
	if !ok {
		fs = map[string]any{}
		v["filesystem"] = fs
	}
	if len(dirs) > 0 {
		allow, _ := fs["allow"].([]any)
		for _, d := range dirs {
			allow = append(allow, d)
		}
		fs["allow"] = allow
	}
	if len(files) > 0 {
		allowFile, _ := fs["allow_file"].([]any)
		for _, f := range files {
			allowFile = append(allowFile, f)
		}
		fs["allow_file"] = allowFile
	}
}

func setWorkdirAccess(v map[string]any) {
	workdir, ok := v["workdir"].(map[string]any)
	if !ok {
		workdir = map[string]any{}
		v["workdir"] = workdir
	}
	workdir["access"] = "read"
}

func deepMerge(base, child map[string]any) map[string]any {
	result := make(map[string]any, len(base))
	for k, v := range base {
		result[k] = v
	}
	for k, cv := range child {
		bv, exists := result[k]
		if !exists {
			result[k] = cv
			continue
		}
		bMap, bIsMap := bv.(map[string]any)
		cMap, cIsMap := cv.(map[string]any)
		if bIsMap && cIsMap {
			result[k] = deepMerge(bMap, cMap)
			continue
		}
		bArr, bIsArr := bv.([]any)
		cArr, cIsArr := cv.([]any)
		if bIsArr && cIsArr {
			seen := make(map[string]struct{}, len(bArr))
			merged := make([]any, 0, len(bArr)+len(cArr))
			for _, v := range bArr {
				sk := fmt.Sprintf("%v", v)
				if _, ok := seen[sk]; !ok {
					seen[sk] = struct{}{}
					merged = append(merged, v)
				}
			}
			for _, v := range cArr {
				sk := fmt.Sprintf("%v", v)
				if _, ok := seen[sk]; !ok {
					seen[sk] = struct{}{}
					merged = append(merged, v)
				}
			}
			result[k] = merged
			continue
		}
		result[k] = cv
	}
	return result
}

// GenerateProfile reads tomlPath, resolves extension.base, scans dir,
// merges filesystem paths, and returns the JSON nono profile.
func GenerateProfile(tomlPath, dir string) ([]byte, error) {
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return nil, fmt.Errorf("nono: %w", err)
	}

	var v map[string]any
	if _, err := toml.Decode(string(data), &v); err != nil {
		return nil, fmt.Errorf("nono: %w", err)
	}

	ext := extractExtension(v)

	if ext.base != "" {
		basePath := filepath.Join(filepath.Dir(tomlPath), ext.base)
		baseData, err := os.ReadFile(basePath)
		if err != nil {
			return nil, fmt.Errorf("nono: %w", err)
		}
		var baseMap map[string]any
		if _, err := toml.Decode(string(baseData), &baseMap); err != nil {
			return nil, fmt.Errorf("nono: %w", err)
		}
		delete(baseMap, "extension")
		v = deepMerge(baseMap, v)
	}

	scanDirs, scanFiles, err := scanDir(dir, ext.denyWrite)
	if err != nil {
		return nil, fmt.Errorf("nono: %w", err)
	}

	mergeIntoFilesystem(v, scanDirs, scanFiles)
	setWorkdirAccess(v)

	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("nono: %w", err)
	}
	return out, nil
}
