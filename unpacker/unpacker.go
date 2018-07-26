package unpacker

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/mpolden/sfv"
	"github.com/nwaples/rardecode"
	"github.com/pkg/errors"
)

var rarPartRE = regexp.MustCompile(`\.part0*(\d+)\.rar$`)

type unpacker struct {
	SFV  *sfv.SFV
	Dir  string
	Name string
}

func New(dir string) (*unpacker, error) {
	sfv, err := sfv.Find(dir)
	if err != nil {
		return nil, err
	}
	rar, err := findFirstRAR(sfv)
	if err != nil {
		return nil, err
	}
	return &unpacker{
		SFV:  sfv,
		Dir:  dir,
		Name: rar,
	}, nil
}

func isRAR(name string) bool { return filepath.Ext(name) == ".rar" }

func isFirstRAR(name string) bool {
	m := rarPartRE.FindStringSubmatch(name)
	if len(m) == 2 {
		return m[1] == "1"
	}
	return isRAR(name)
}

func findFirstRAR(s *sfv.SFV) (string, error) {
	for _, c := range s.Checksums {
		if isFirstRAR(c.Path) {
			return c.Path, nil
		}
	}
	return "", errors.Errorf("no rar file found in %s", s.Path)
}

func chtimes(name string, header *rardecode.FileHeader) error {
	if header.ModificationTime.IsZero() {
		return nil
	}
	return os.Chtimes(name, header.ModificationTime, header.ModificationTime)
}

func (u *unpacker) unpack(name string) error {
	r, err := rardecode.OpenReader(name, "")
	if err != nil {
		return errors.Wrapf(err, "failed to open %s", name)
	}
	for {
		header, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Join(u.Dir, header.Name)
		// If entry is a directory, create it and set correct ctime
		if header.IsDir {
			if err := os.MkdirAll(name, 0755); err != nil {
				return err
			}
			if err := chtimes(name, header); err != nil {
				return err
			}
			continue
		}
		// Files can come before their containing folders, ensure that parent is created
		parent := filepath.Dir(name)
		if err := os.MkdirAll(parent, 0755); err != nil {
			return err
		}
		if err := chtimes(parent, header); err != nil {
			return err
		}
		// Unpack file
		f, err := os.Create(name)
		if err != nil {
			return errors.Wrapf(err, "failed to create file: %s", name)
		}
		if _, err = io.Copy(f, r); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		// Set correct ctime of unpacked file
		if err := chtimes(name, header); err != nil {
			return err
		}
		// Unpack recursively if unpacked file is also a RAR
		if isRAR(name) {
			if err := u.unpack(name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (u *unpacker) remove() error {
	for _, c := range u.SFV.Checksums {
		if err := os.Remove(c.Path); err != nil {
			return err
		}
	}
	return os.Remove(u.SFV.Path)
}

func (u *unpacker) fileCount() (int, int) {
	exists := 0
	for _, c := range u.SFV.Checksums {
		if c.IsExist() {
			exists++
		}
	}
	return exists, len(u.SFV.Checksums)
}

func (u *unpacker) verify() error {
	for _, c := range u.SFV.Checksums {
		ok, err := c.Verify()
		if err != nil {
			return err
		}
		if !ok {
			return errors.Errorf("%s: failed checksum: %s", u.SFV.Path, c.Filename)
		}
	}
	return nil
}

func (u *unpacker) Run(removeRARs bool) error {
	if exists, total := u.fileCount(); exists != total {
		return errors.Errorf("%s is incomplete: %d/%d files", u.Dir, exists, total)
	}
	if err := u.verify(); err != nil {
		return errors.Wrapf(err, "verification of %s failed", u.Dir)
	}
	if err := u.unpack(u.Name); err != nil {
		return errors.Wrapf(err, "unpacking %s failed", u.Dir)
	}
	if removeRARs {
		if err := u.remove(); err != nil {
			return errors.Wrapf(err, "cleaning up %s failed", u.Dir)
		}
	}
	return nil
}

func postProcess(u *unpacker, command string) error {
	if command == "" {
		return nil
	}
	values := cmdValues{
		Name: u.Name,
		Base: filepath.Base(u.Dir),
		Dir:  u.Dir,
	}
	cmd, err := newCmd(command, values)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return errors.Errorf("%s: %s", err, stderr.String())
	}
	return nil
}

func OnFile(name, postCommand string, remove bool) error {
	u, err := New(filepath.Dir(name))
	if err != nil {
		return errors.Wrap(err, "failed to initialize unpacker")
	}
	if err := u.Run(remove); err != nil {
		return err
	}
	if err := postProcess(u, postCommand); err != nil {
		return errors.Wrap(err, "post-process command failed")
	}
	return nil
}
