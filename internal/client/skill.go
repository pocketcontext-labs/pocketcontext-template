package client

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	assets "github.com/pocketcontext-labs/pocketcontext-template"
)

// marker identifies skill directories written by this command, which may be replaced.
const marker = ".templatecontext"

// skillTargets are the user-level skill directories of supported agents. An
// agent is skipped when its configuration directory does not exist.
func skillTargets(home string) map[string][2]string {
	return map[string][2]string{
		"claude-code": {filepath.Join(home, ".claude"), filepath.Join(home, ".claude", "skills", "templatecontext")},
		"codex":       {filepath.Join(home, ".codex"), filepath.Join(home, ".codex", "skills", "templatecontext")},
		"antigravity": {filepath.Join(home, ".gemini"), filepath.Join(home, ".gemini", "antigravity", "skills", "templatecontext")},
	}
}

// remoteTemplate replaces SKILL.md for a skill that reaches a forwarded server.
const remoteTemplate = "SKILL.remote.md"

// installSkills writes the skill for the local database db, or, when db is
// empty, the remote variant for the forwarded server at url.
func installSkills(db, url, agent, dir string, pretty bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	results := map[string]any{}
	report := func() {
		out := map[string]any{"installed": results, "binary": exe}
		if db != "" {
			out["db"] = db
		} else {
			out["url"] = url
		}
		Print(out, pretty)
	}
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if err := writeSkill(abs, exe, db, url); err != nil {
			return err
		}
		results["custom"] = abs
		report()
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	targets := skillTargets(home)
	agents := []string{"claude-code", "codex", "antigravity"}
	if agent != "" && agent != "all" {
		if _, ok := targets[agent]; !ok {
			return fail(2, "--agent must be claude-code, codex, antigravity or all")
		}
		agents = []string{agent}
	}
	for _, name := range agents {
		target := targets[name]
		if _, err := os.Stat(target[0]); err != nil && (agent == "" || agent == "all") {
			results[name] = "skipped: " + target[0] + " not found"
			continue
		}
		if err := writeSkill(target[1], exe, db, url); err != nil {
			results[name] = "failed: " + err.Error()
			continue
		}
		results[name] = target[1]
	}
	report()
	return nil
}

func writeSkill(dir, exe, db, url string) error {
	if _, err := os.Stat(dir); err == nil {
		if _, err := os.Stat(filepath.Join(dir, marker)); err != nil {
			return fail(1, "%s already contains another skill; remove it first or pass --dir", dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	replacer := strings.NewReplacer("{{BINARY}}", exe, "{{DB}}", db, "{{URL}}", url)
	root := "skills/templatecontext"
	err := fs.WalkDir(assets.Skill, root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(name, root), "/")
		if rel == remoteTemplate || (db == "" && rel == "SKILL.md") {
			return nil
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := assets.Skill.ReadFile(name)
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, ".md") {
			data = []byte(replacer.Replace(string(data)))
		}
		return os.WriteFile(target, data, 0644)
	})
	if err != nil {
		return err
	}
	if db == "" {
		data, err := assets.Skill.ReadFile(root + "/" + remoteTemplate)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(replacer.Replace(string(data))), 0644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, marker), []byte(url+"\n"), 0644)
	}
	return os.WriteFile(filepath.Join(dir, marker), []byte(db+"\n"), 0644)
}
