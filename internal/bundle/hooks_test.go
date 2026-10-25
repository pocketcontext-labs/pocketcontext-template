package bundle

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestHooksRunNoExternalTools keeps hooks and migrations in-process: no shell
// commands or external programs, which also do not exist on every platform
// (sha256sum, for example, is missing on Windows).
func TestHooksRunNoExternalTools(t *testing.T) {
	external := regexp.MustCompile(`\$os\.[A-Za-z]+`)
	for _, dir := range []string{"pb_hooks", "pb_migrations"} {
		files, err := filepath.Glob(filepath.Join("..", "..", dir, "*.js"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no files in %s: %v", dir, err)
		}
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if loc := external.FindIndex(data); loc != nil {
				t.Errorf("%s uses %s; keep hooks in-process", file, data[loc[0]:loc[1]])
			}
		}
	}
}
