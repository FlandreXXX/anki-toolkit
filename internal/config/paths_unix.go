//go:build !windows

package config

import "path/filepath"

func (r Resolver) defaultPath(kind string) (Path, error) {
	var env, fallback string
	switch kind {
	case "config":
		env, fallback = "XDG_CONFIG_HOME", ".config"
	case "data":
		env, fallback = "XDG_DATA_HOME", ".local/share"
	case "cache":
		env, fallback = "XDG_CACHE_HOME", ".cache"
	case "state":
		env, fallback = "XDG_STATE_HOME", ".local/state"
	}
	base, _ := r.LookupEnv(env)
	if !absolute(base) {
		home, _ := r.LookupEnv("HOME")
		if !absolute(home) {
			return Path{}, &Error{Source: SourceDefault, Message: "Не удалось определить пользовательский каталог из " + env + " или HOME."}
		}
		base = filepath.Join(home, fallback)
	}
	return Path{Value: filepath.Join(base, "anki-toolkit"), Source: SourceDefault}, nil
}

func validAbsolute(string) bool { return true }
func validRelative(string) bool { return true }
