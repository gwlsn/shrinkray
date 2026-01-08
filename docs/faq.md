# FAQ

### Why is CPU usage 10–40% with hardware encoding?

This is normal. Your GPU handles video encoding and decoding (when it supports the source format), but the CPU still handles:
- Demuxing (parsing input container)
- Muxing (writing output container)
- Audio/subtitle stream copying
- FFmpeg process overhead

If you see higher CPU usage, your GPU may not support the source codec and Shrinkray has automatically fallen back to software decoding. The GPU still handles encoding—only decoding moves to CPU. This happens automatically and transparently.

### Why does my AMD GPU show 0% usage during hardware encoding?

Standard GPU monitoring tools may show 0% usage even when hardware encoding is working correctly. AMD GPUs use a dedicated video engine (UVD/VCN) that isn't always reported by generic monitoring tools. To verify AMD hardware encoding is active, use `radeontop` which shows UVD/VCN utilization separately. If you see UVD at 100% while encoding, hardware acceleration is working as expected.

### Why did Shrinkray skip some files?

Files are automatically skipped if:
- Already encoded in the target codec (HEVC/AV1)
- Already at or below the target resolution (1080p/720p presets)

### What happens if the transcoded file is larger?

By default, Shrinkray rejects files where the output is larger than the original. The original file is kept unchanged.

If you want to keep larger files anyway (e.g., for codec consistency across your library), set `keep_larger_files: true` in your config.

### Are subtitles preserved?

Yes. All subtitle streams are copied to the output file unchanged (`-c:s copy`).

### Are multiple audio tracks preserved?

Yes. All audio streams are copied to the output file unchanged (`-c:a copy`). If your source has multiple audio tracks (different languages, commentary, etc.), they will all be retained.

### What about HDR content?

Hardware encoders preserve HDR metadata and 10-bit color depth when your GPU supports the source codec. If your GPU can't hardware decode the source (e.g., AV1 on older Intel, or exotic codecs), Shrinkray automatically retries with software decoding. In this fallback path, 10-bit HDR may be converted to 8-bit SDR due to the CPU-to-GPU frame upload. This is a limitation of mixed software decode + hardware encode pipelines, not Shrinkray specifically.

### How does Shrinkray compare to Tdarr/Unmanic?

Shrinkray prioritizes simplicity over features. Tdarr and Unmanic are powerful but complex—Shrinkray is designed for users who want to point at a folder and compress without learning a new system. If you're already comfortable with Tdarr/Unmanic, there's no need to switch.

### Can I use RAM (/dev/shm) for temp files?

Yes. Map `/dev/shm` (or any RAM disk) to the `/temp` container path.

### Can I customize FFmpeg settings or create custom presets?

Shrinkray is intentionally simple, it's not designed for custom FFmpeg workflows. You can adjust quality via the CRF slider, but if you need full control over encoding parameters, FFmpeg directly is the better tool for that.

### Can Shrinkray transcode audio?

No. Audio streams are copied unchanged (`-c:a copy`) to preserve quality and compatibility.
