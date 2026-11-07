package opstore

import (
	"errors"
	"os"
)

func (s *Store) replace(r *os.Root, from, to string) error {
	if err := r.Rename(from, to); err != nil {
		return err
	}
	return syncDirectory(r)
}

func syncDirectory(r *os.Root) error {
	dir, err := r.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
