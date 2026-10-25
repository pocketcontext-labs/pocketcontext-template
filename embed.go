// Package assets embeds the application files that the single
// templatecontext binary unpacks into its private cache before serving.
package assets

import "embed"

// Assets holds the hooks, migrations, SQL configuration and built reader.
// Build the reader (ui/dist) before compiling.
//
//go:embed pb_hooks pb_migrations server.json ui/dist
var Assets embed.FS

// Skill is the agent skill template that `templatecontext skill` installs with this
// computer's absolute paths.
//
//go:embed skills/templatecontext
var Skill embed.FS
