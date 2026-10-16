// Package profile remembers settings per USB adapter, so the second time you
// plug in the same cable, bauddy already knows the baud rate.
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// Adapter is what bauddy remembers about one adapter.
type Adapter struct {
	Baud    int    `json:"baud"`
	Framing string `json:"framing,omitempty"` // e.g. "7E1"; empty means 8N1
	Name    string `json:"name,omitempty"`    // user label, e.g. "core-sw-01"
	// Rarely needed fixes for the keyboard and screen, off by default.
	BackspaceH bool   `json:"backspaceH,omitempty"` // Backspace sends ^H, not DEL
	AddCR      bool   `json:"addCR,omitempty"`      // show each bare line feed as a new line
	Device     string `json:"device,omitempty"`     // the name the device last gave in its prompt
}

type file struct {
	Adapters map[string]Adapter `json:"adapters"`
}

// Store is a small JSON file in the user's config directory.
type Store struct {
	path     string
	adapters map[string]Adapter
}

// Load reads the store. It always returns a usable Store; on error the
// store is empty and, if the path is unknown, saving does nothing.
func Load() (*Store, error) {
	s := &Store{adapters: map[string]Adapter{}}
	dir, err := configDir()
	if err != nil {
		return s, err
	}
	s.path = filepath.Join(dir, "adapters.json")
	f, err := s.read()
	if err != nil {
		return s, err
	}
	s.adapters = f.Adapters
	return s, nil
}

// Adapter returns what is saved for key, or the zero Adapter.
func (s *Store) Adapter(key string) Adapter { return s.adapters[key] }

// KeyForName returns the key of the adapter saved under name.
func (s *Store) KeyForName(name string) (string, bool) {
	for k, a := range s.adapters {
		if a.Name == name {
			return k, true
		}
	}
	return "", false
}

// RememberLine records a working rate and framing for key and saves.
func (s *Store) RememberLine(key string, baud int, framing string) error {
	if framing == "8N1" {
		framing = ""
	}
	return s.update(key, func(a *Adapter) { a.Baud, a.Framing = baud, framing })
}

// RememberKeys saves the Backspace and line feed fixes for key.
func (s *Store) RememberKeys(key string, backspaceH, addCR bool) error {
	return s.update(key, func(a *Adapter) { a.BackspaceH, a.AddCR = backspaceH, addCR })
}

// RememberDevice saves the name the device on key's cable gave in its
// prompt, replacing the last one: cables move between devices.
func (s *Store) RememberDevice(key, name string) error {
	return s.update(key, func(a *Adapter) { a.Device = name })
}

// RememberName labels the adapter at key and saves. The name is taken from
// any other adapter that had it, so names stay unique.
func (s *Store) RememberName(key, name string) error {
	if old, ok := s.KeyForName(name); ok && old != key {
		if err := s.update(old, func(a *Adapter) { a.Name = "" }); err != nil {
			return err
		}
	}
	return s.update(key, func(a *Adapter) { a.Name = name })
}

// update changes one entry and saves. It re-reads the file first so
// concurrent bauddy instances don't drop each other's entries.
func (s *Store) update(key string, f func(*Adapter)) error {
	if s.path == "" {
		return nil
	}
	file, err := s.read()
	if err != nil {
		return err // don't overwrite a file we couldn't parse
	}
	a := file.Adapters[key]
	f(&a)
	file.Adapters[key] = a
	s.adapters = file.Adapters
	return s.write(file)
}

func (s *Store) read() (file, error) {
	f := file{Adapters: map[string]Adapter{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err != nil {
		return file{Adapters: map[string]Adapter{}}, fmt.Errorf("read %s: %w", s.path, err)
	}
	if f.Adapters == nil {
		f.Adapters = map[string]Adapter{} // the file said "adapters": null
	}
	return f, nil
}

// write replaces the file atomically via a uniquely named temp file.
func (s *Store) write(f file) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "adapters-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// configDir honours XDG_CONFIG_HOME, else ~/.config on Linux and macOS (CLI
// users expect it, not ~/Library/Application Support), and %AppData% on Windows.
func configDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "bauddy"), nil
	}
	if runtime.GOOS == "windows" {
		d, err := os.UserConfigDir()
		return filepath.Join(d, "bauddy"), err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "bauddy"), nil
}
