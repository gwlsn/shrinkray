# Shrinkray

Video transcoding app: Go backend, embedded web UI, FFmpeg processing. Supports hardware acceleration (NVENC, Quick Sync, VAAPI, VideoToolbox) and VMAF-guided SmartShrink presets.

## Commands

```bash
go build -o shrinkray ./cmd/shrinkray          # Build
./shrinkray -media /path/to/media -port 8080   # Run (requires FFmpeg)
go test ./...                                   # Test
go test -race ./...                             # Test with race detector
golangci-lint run                              # Lint
```

## Docker (GPU passthrough)

```bash
docker run -d --name shrinkray-test \
  -p 8080:8080 \
  -v /tmp/claude:/media \
  -v /tmp/claude/shrinkray-config:/config \
  --device /dev/dri:/dev/dri \
  -e PUID=$(id -u) \
  -e PGID=993 \
  shrinkray:test
```

Note: PGID=993 is the render group for /dev/dri access.

## FFmpeg Errors (MANDATORY)

Before diagnosing ANY FFmpeg error, invoke `Skill(ffmpeg-debugging)` FIRST. Issue #77 taught us: get full stderr, check if HW and SW fail identically (means NOT an encoder issue), isolate which stream fails, then diagnose.

## Resources
- You can always find test and sample video files in my media library which is shared as a read-only samba share called "data" at 10.0.0.9. Username is smbuser, password is bubbles99

## Git Workflow

```
main       ●──────────────●────── (stable releases only, tagged)
            \            /
develop     ●──●──●──●──● ────── (integration branch)
             \   / \   /
feature/*     ●─●   ●─●          (short-lived branches)
```

- **`main`** — Always stable/releasable. Only updated via merge from `develop` when cutting a release. Tags trigger CI Docker builds.
- **`develop`** — Working integration branch. Features and fixes merge here.
- **`feature/*` / `fix/*`** — Branch from `develop`, merge back to `develop`. Use worktrees (`.worktrees/`) for isolation.

**Day-to-day:** Branch from develop → work → merge to develop → delete branch.
**Releases:** Merge develop into main → tag → CI builds Docker image. Use `/release` slash command.

## Constraints
- Never use goto
- See `docs/` for architecture details, API reference, and package structure
- All code generated and implementations must be submitted to Codex via our MCP for review
