// Package statefile reads and writes small JSON state files that only the
// user can read.
package statefile

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

const maxRead = 32 << 20

// Write replaces path atomically with v encoded as JSON, mode 0600.
func Write(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Read decodes path into v. A missing file leaves v untouched.
func Read(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxRead))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
