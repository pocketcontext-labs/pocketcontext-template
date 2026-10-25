package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"time"
)

// renameRetry replaces dst. Sync clients and virus scanners briefly hold files
// open on Windows, which makes an otherwise valid rename fail.
func renameRetry(src, dst string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
