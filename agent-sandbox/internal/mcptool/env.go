package mcptool

import "os"

// resolveEnv looks up each key in the process environment and returns
// "KEY=value" pairs for the keys that are present.
func resolveEnv(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
