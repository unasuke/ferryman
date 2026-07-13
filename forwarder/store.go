package forwarder

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DefaultConfigPath returns the per-user config file location, e.g.
// %AppData%\ferryman\config.json (Windows) or
// ~/Library/Application Support/ferryman/config.json (macOS).
func DefaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "ferryman", "config.json")
}

// LoadProfile reads a Profile. A missing file yields an empty Profile.
func LoadProfile(path string) (Profile, error) {
	var p Profile
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return p, err
	}
	err = json.Unmarshal(data, &p)
	return p, err
}

// SaveProfile writes a Profile (file 0600, dir 0700).
func SaveProfile(path string, p Profile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
