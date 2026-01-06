# FAQ

### Why is CPU usage 10–40% with hardware encoding?

This is normal. The GPU handles video encoding/decoding, but the CPU still handles:
- Demuxing (parsing input container)
- Muxing (writing output container)
- Audio/subtitle stream copying
- FFmpeg process overhead

### Why did Shrinkray skip some files?

Files are automatically skipped if:
- Already encoded in the target codec (HEVC/AV1)
- Already at or below the target resolution (1080p/720p presets)

### What happens if the transcoded file is larger?

You can choose if you want to keep files, that are transcoded to larger files. Larger transcoded files will be discarded by default.

### Are subtitles preserved?

Yes. All subtitle streams are copied to the output file unchanged (`-c:s copy`).

### Are multiple audio tracks preserved?

Yes. All audio streams are copied to the output file unchanged (`-c:a copy`). If your source has multiple audio tracks (different languages, commentary, etc.), they will all be retained.

### What about HDR content?

Hardware encoders generally preserve HDR metadata and 10-bit color depth. Software encoders may require additional configuration for HDR preservation.

### How does Shrinkray compare to Tdarr/Unmanic?

Shrinkray prioritizes simplicity over features. Tdarr and Unmanic are powerful but complex—Shrinkray is designed for users who want to point at a folder and compress without learning a new system. If you're already comfortable with Tdarr/Unmanic, there's no need to switch.

### Can I use RAM (/dev/shm) for temp files?

Yes. Map `/dev/shm` (or any RAM disk) to the `/temp` container path.

### Can I customize FFmpeg settings or create custom presets?

Shrinkray is intentionally simple, it's not designed for custom FFmpeg workflows. You can adjust quality via the CRF slider, but if you need full control over encoding parameters, FFmpeg directly is the better tool for that.

### Can Shrinkray transcode audio?

No. Audio streams are copied unchanged (`-c:a copy`) to preserve quality and compatibility.
