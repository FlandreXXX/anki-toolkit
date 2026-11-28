package config

import (
	"path/filepath"
	"strings"
)

func (r Resolver) defaultPath(kind string) (Path, error) {
	env := "LOCALAPPDATA"
	if kind == "config" {
		env = "APPDATA"
	}
	base, _ := r.LookupEnv(env)
	if !absolute(base) {
		return Path{}, &Error{Source: SourceDefault, Message: "Не удалось определить пользовательский каталог из " + env + "."}
	}
	path := filepath.Join(base, "anki-toolkit")
	if kind != "config" {
		path = filepath.Join(path, kind)
	}
	return Path{Value: path, Source: SourceDefault}, nil
}

func validAbsolute(path string) bool {
	path = filepath.ToSlash(path)
	volume := filepath.ToSlash(filepath.VolumeName(path))
	if strings.HasPrefix(volume, "//") {
		host, share, _ := strings.Cut(volume[2:], "/")
		if !validWindowsComponent(host) || host == "." || host == ".." ||
			!validWindowsComponent(share) || share == "." || share == ".." {
			return false
		}
	} else if len(volume) != 2 || !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", rune(volume[0])) {
		return false
	}
	return validRelative(path[len(volume):])
}

func validRelative(path string) bool {
	for component := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if component != "" && !validWindowsComponent(component) {
			return false
		}
	}
	return true
}

func validWindowsComponent(component string) bool {
	if component == "." || component == ".." {
		return true
	}
	// Версии Windows различаются в обработке имён устройств с расширением.
	stem, _, _ := strings.Cut(component, ".")
	if stem = strings.TrimRight(stem, " "); stem != "" && !filepath.IsLocal(stem) {
		return false
	}
	return filepath.IsLocal(component) && !strings.ContainsAny(component, `<>:"|?*`) &&
		!strings.HasSuffix(component, ".") && !strings.HasSuffix(component, " ")
}
