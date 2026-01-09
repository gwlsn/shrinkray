# FAQ

### Why is CPU usage 10–40% with hardware encoding?

This is normal. Your GPU handles video encoding and decoding (when it supports the source format), but the CPU still handles:
- Demuxing (parsing input container)
- Muxing (writing output container)
- Audio/subtitle stream copying
- FFmpeg process overhead

If you see higher CPU usage, your GPU may not support the source codec and Shrinkray has automatically fallen back to software decoding. The GPU still handles encoding, only decoding moves to CPU. This happens automatically and transparently.

### How can I tell if I'm using my GPU for transcoding?

Open logs for Shrinkray, all the detected encoders are shown and the currently selected encoders have an asterisk. A "HW" or "SW" badge will appear on jobs in your queue to let you know if they are being software or hardware transcoded.

### Intel Quick Sync (QSV) not working on non-Unraid systems?

If you're running Shrinkray on generic Linux (not Unraid), Intel QSV requires proper permissions to access `/dev/dri` devices.

1. **Set PUID/PGID** in your Docker configuration:
   ```yaml
   environment:
     - PUID=1000
     - PGID=1000
   ```

2. **Check device permissions** on your host:
   ```bash
   ls -la /dev/dri
   ```
   Note the group (usually `video` or `render`).

3. **Ensure your user is in the correct group:**
   ```bash
   id
   ```
   If not, add yourself: `sudo usermod -aG video $USER` (and re-login).

4. **Pass through the device:**
   ```yaml
   devices:
     - /dev/dri:/dev/dri
   ```

If issues persist, try running with `PUID=0` temporarily to confirm it's a permissions issue.

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

In MKV mode (default), yes—all subtitle streams are copied unchanged. In MP4 mode, subtitles are stripped because formats like PGS are incompatible with MP4 containers.

### Are multiple audio tracks preserved?

In MKV mode (default), yes—all audio streams are copied unchanged. In MP4 mode, audio is transcoded to AAC stereo for web/direct play compatibility.

### What about HDR content?

Hardware encoders preserve HDR metadata and 10-bit color depth when your GPU supports the source codec. If your GPU can't hardware decode the source (e.g., AV1 on older Intel, or exotic codecs), Shrinkray automatically retries with software decoding. In this fallback path, 10-bit HDR may be converted to 8-bit SDR due to the CPU-to-GPU frame upload. This is a limitation of mixed software decode + hardware encode pipelines, not Shrinkray specifically.

### How does Shrinkray compare to Tdarr/Unmanic?

Shrinkray prioritizes simplicity over features. Tdarr and Unmanic are powerful but complex—Shrinkray is designed for users who want to point at a folder and compress without learning a new system. If you're already comfortable with Tdarr/Unmanic, there's no need to switch.

### Can I use RAM (/dev/shm) for temp files?

Yes. Map `/dev/shm` (or any RAM disk) to the `/temp` container path.

### Can I customize FFmpeg settings or create custom presets?

Shrinkray is intentionally simple, it's not designed for custom FFmpeg workflows. You can adjust quality via the CRF slider, but if you need full control over encoding parameters, FFmpeg directly is the better tool for that.

### Can Shrinkray transcode audio?

In MKV mode (default), no—audio is copied unchanged. In MP4 mode, audio is transcoded to AAC stereo (192 kbps) for web compatibility.

