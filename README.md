# Shrinkray

A simple video transcoding tool for Unraid. Select a folder, pick a preset, and shrink your media library.

## Quick Start (Unraid)

1. Install from Community Applications (search "Shrinkray") or add manually:
   - **Repository**: `ghcr.io/jesposito/shrinkray:latest`
   - **WebUI**: `8080`
   - **Volumes**: `/config` → appdata, `/media` → your media library
   - **Optional**: `/temp` → fast storage for temp file

2. Open the WebUI, browse to a folder, select files, and click **Start Transcode**

## Quick Start (Docker)

```bash
docker run -d \
  --name shrinkray \
  -p 8080:8080 \
  -e PUID=99 \
  -e PGID=100 \
  -v /path/to/config:/config \
  -v /path/to/media:/media \
  ghcr.io/jesposito/shrinkray:latest
```

**Optional**: For better performance, mount fast storage for temp files:

```bash
  -v /path/to/fast/storage:/temp
```

For hardware acceleration, add the appropriate device:

```bash
# Intel QSV / AMD VAAPI
--device /dev/dri:/dev/dri

# NVIDIA (requires Nvidia-Driver plugin on Unraid)
--runtime=nvidia --gpus all
```

## Presets

| Preset | Description |
|--------|-------------|
| **Compress (HEVC)** | Re-encode to H.265 (HEVC) |
| **Compress (AV1)** | Re-encode to AV1 |
| **1080p** | Downscale to 1080p + HEVC |
| **720p** | Downscale to 720p + HEVC |

All presets copy audio, and subtitle streams are copied unless a `mov_text` subtitle needs conversion for MKV output.

## Hardware Acceleration

Automatically detected and used when available:

| Platform | Encoder |
|----------|---------|
| Intel | Quick Sync (QSV) |
| NVIDIA | NVENC |
| AMD (Linux) | VAAPI |
| macOS | VideoToolbox |

Falls back to software encoding if no hardware is available.

## Settings

Access via the gear icon in the WebUI:

- **Original files**: Delete after transcode, or keep as `.old`
- **Concurrent jobs**: 1-6 simultaneous transcodes
- **Pushover notifications**: Get notified when all jobs complete
- **ntfy notifications**: Send notifications to an ntfy topic

## Pushover Notifications

1. Create an app at [pushover.net](https://pushover.net)
2. Enter your **User Key** and **App Token** in Settings
3. Check **"Notify when done"** in the queue header before starting jobs

You'll receive a notification with job counts and total space saved when the queue empties.

## ntfy Notifications

1. Pick an ntfy server (default: `https://ntfy.sh`) and topic
2. Enter the server, topic, and optional token in Settings
3. Check **"Notify when done"** in the queue header before starting jobs

You'll receive a notification with job counts and total space saved when the queue empties.

## Configuration

Config is stored in `/config/shrinkray.yaml`. Most settings are available in the WebUI, but you can also edit the file directly:

| Setting | Default | Description |
|---------|---------|-------------|
| `media_path` | `/media` | Root directory to browse for media files |
| `temp_path` | *(empty)* | Directory for temp files during transcode. If empty, uses same directory as source file |
| `original_handling` | `replace` | What to do with originals: `replace` (delete) or `keep` (rename to `.old`) |
| `subtitle_handling` | `convert` | How to handle unsupported subtitles: `convert` (convert `mov_text` to SRT) or `drop` (remove unsupported subtitles) |
| `workers` | `1` | Number of concurrent transcode jobs (1-6) |
| `ffmpeg_path` | `ffmpeg` | Path to ffmpeg binary |
| `ffprobe_path` | `ffprobe` | Path to ffprobe binary |
| `pushover_user_key` | *(empty)* | Pushover user key for notifications |
| `pushover_app_token` | *(empty)* | Pushover application token for notifications |
| `ntfy_server` | `https://ntfy.sh` | ntfy server URL for notifications |
| `ntfy_topic` | *(empty)* | ntfy topic for notifications |
| `ntfy_token` | *(empty)* | ntfy access token (optional) |
| `auth.enabled` | `false` | Require authentication |
| `auth.provider` | `noop` | Auth provider name (`noop`, `password`) |
| `auth.secret` | *(empty)* | HMAC secret used to sign session cookies |
| `auth.bypass_paths` | *(empty)* | Extra unauthenticated paths (comma-separated in env) |
| `auth.password.hash_algo` | `auto` | Password hash algorithm (`auto`, `bcrypt`, `argon2id`) |
| `auth.password.users` | *(empty)* | Map of usernames to password hashes |
| `auth.oidc.issuer` | *(empty)* | OIDC issuer URL |
| `auth.oidc.client_id` | *(empty)* | OIDC client ID |
| `auth.oidc.client_secret` | *(empty)* | OIDC client secret |
| `auth.oidc.redirect_url` | *(empty)* | Callback URL registered with the IdP |
| `auth.oidc.scopes` | `["openid","profile","email"]` | OIDC scopes (openid is always enforced) |
| `auth.oidc.group_claim` | *(empty)* | Claim containing group membership |
| `auth.oidc.allowed_groups` | *(empty)* | Allowed groups (any match grants access) |

Example:

```yaml
media_path: /media
temp_path: /tmp/shrinkray
original_handling: replace
subtitle_handling: convert
workers: 2
```

Auth example:

```yaml
auth:
  enabled: true
  provider: password
  secret: "change-me"
  password:
    hash_algo: auto
    users:
      admin: "$2b$12$abcdefghijklmnopqrstuv1234567890abcdefghijklmnopqrstuv"
```

OIDC example:

```yaml
auth:
  enabled: true
  provider: oidc
  secret: "change-me"
  oidc:
    issuer: "https://accounts.example.com"
    client_id: "shrinkray"
    client_secret: "super-secret"
    redirect_url: "https://shrinkray.example.com/auth/callback"
    scopes: ["openid", "profile", "email", "groups"]
    group_claim: "groups"
    allowed_groups: ["media-admins", "ops"]
```

Environment overrides:

```bash
SHRINKRAY_AUTH_ENABLED=1
SHRINKRAY_AUTH_PROVIDER=password
SHRINKRAY_AUTH_SECRET=change-me
SHRINKRAY_AUTH_USERS='admin:$2b$12$abcdefghijklmnopqrstuv1234567890abcdefghijklmnopqrstuv'
```

OIDC overrides:

```bash
SHRINKRAY_AUTH_ENABLED=1
SHRINKRAY_AUTH_PROVIDER=oidc
SHRINKRAY_AUTH_SECRET=change-me
SHRINKRAY_AUTH_OIDC_ISSUER=https://accounts.example.com
SHRINKRAY_AUTH_OIDC_CLIENT_ID=shrinkray
SHRINKRAY_AUTH_OIDC_CLIENT_SECRET=super-secret
SHRINKRAY_AUTH_OIDC_REDIRECT_URL=https://shrinkray.example.com/auth/callback
SHRINKRAY_AUTH_OIDC_SCOPES="openid,profile,email,groups"
SHRINKRAY_AUTH_OIDC_GROUP_CLAIM=groups
SHRINKRAY_AUTH_OIDC_ALLOWED_GROUPS="media-admins,ops"
```

### OIDC IdP Settings

Configure your identity provider with:

- **Redirect/Callback URL:** `https://<your-host>/auth/callback`
- **Grant type:** Authorization Code (OIDC)
- **Scopes:** `openid` plus any additional scopes (e.g., `profile`, `email`, `groups`)
- **Group claim:** Ensure your IdP includes the configured `group_claim` (string or array)

## Building from Source

```bash
go build -o shrinkray ./cmd/shrinkray
./shrinkray -media /path/to/media
```

Requires Go 1.22+ and FFmpeg with HEVC/AV1 support.

## Docker Image Publishing

The Docker image is automatically built and published to GitHub Container Registry (GHCR) when changes are pushed to the `main` branch.

**Image URL**: `ghcr.io/jesposito/shrinkray:latest`

**Available Tags**:
- `latest` - Always points to the most recent build from main
- `<sha>` - Short git commit SHA for specific versions (e.g., `ghcr.io/jesposito/shrinkray:abc1234`)

### Making the Package Public (Required for Unraid)

By default, GHCR packages are private. To allow Unraid to pull the image without authentication:

1. Go to your GitHub profile → **Packages**
2. Click on the `shrinkray` package
3. Click **Package settings** (right sidebar)
4. Scroll to **Danger Zone** → Click **Change visibility**
5. Select **Public** and confirm

### Unraid Setup

Use this exact image name in Unraid:

```
ghcr.io/jesposito/shrinkray:latest
```

Add a new container in Unraid with:
- **Repository**: `ghcr.io/jesposito/shrinkray:latest`
- **WebUI**: `http://[IP]:[PORT:8080]`
- **Port**: `8080` → `8080`
- **Path**: `/config` → `/mnt/user/appdata/shrinkray`
- **Path**: `/media` → `/mnt/user/` (or your media location)

For hardware acceleration, add the appropriate device mapping:
- Intel/AMD: `/dev/dri` → `/dev/dri`
- NVIDIA: Enable `--runtime=nvidia` with the Nvidia-Driver plugin
