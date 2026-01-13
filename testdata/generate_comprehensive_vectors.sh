#!/bin/bash
# Generate COMPREHENSIVE test video files covering all codec/bitdepth/track permutations
#
# This script creates an exhaustive set of test videos to verify:
# 1. All codec types (H.264, H.265, AV1, VP9, MPEG-4)
# 2. All bit depths (8-bit, 10-bit)
# 3. Multi-track files (multiple audio, subtitles)
# 4. Cover art / attached pictures (tests -map 0:v:0 fix)
# 5. Long duration files (catch late-stage failures like -38 error)
# 6. Edge cases (very short, 4K, 60fps, interlaced)
#
# Usage: ./generate_comprehensive_vectors.sh
#
# Requirements: ffmpeg with libx264, libx265, libsvtav1, libvpx-vp9

set -e

TESTDATA_DIR="$(dirname "$0")"
cd "$TESTDATA_DIR"

# Configuration
SHORT_DURATION=5    # seconds - quick tests
MEDIUM_DURATION=30  # seconds - catch early failures
LONG_DURATION=120   # seconds - catch late-stage failures (2 minutes)

echo "=== Generating Comprehensive Test Vectors ==="
echo ""
echo "This will generate test files with varying durations:"
echo "  Short:  ${SHORT_DURATION}s   - Quick regression tests"
echo "  Medium: ${MEDIUM_DURATION}s  - Catch early failures"
echo "  Long:   ${LONG_DURATION}s   - Catch late-stage failures (Issue #56)"
echo ""

# Check for ffmpeg
if ! command -v ffmpeg &> /dev/null; then
    echo "ERROR: ffmpeg not found in PATH"
    exit 1
fi

echo "Using ffmpeg: $(which ffmpeg)"
echo ""

# Function to generate a test file with error handling
generate() {
    local name="$1"
    local description="$2"
    shift 2

    echo -n "  Generating $name ($description)... "
    if ffmpeg -hide_banner -loglevel error "$@" 2>&1; then
        echo "OK"
    else
        echo "FAILED"
        return 1
    fi
}

# Create a small cover art image (100x100 red square)
create_cover_art() {
    if [ ! -f "cover_art.jpg" ]; then
        echo -n "  Creating cover art image... "
        ffmpeg -hide_banner -loglevel error -y \
            -f lavfi -i "color=red:s=100x100:d=0.1" \
            -frames:v 1 -update 1 \
            cover_art.jpg 2>&1 && echo "OK" || echo "FAILED"
    fi
}

# Create a sample subtitle file
create_subtitles() {
    if [ ! -f "test_subtitles.srt" ]; then
        echo -n "  Creating subtitle file... "
        cat > test_subtitles.srt << 'EOF'
1
00:00:00,000 --> 00:00:05,000
Test subtitle line 1

2
00:00:05,000 --> 00:00:10,000
Test subtitle line 2

3
00:00:10,000 --> 00:00:15,000
Test subtitle line 3
EOF
        echo "OK"
    fi
}

echo "--- Prerequisites ---"
create_cover_art
create_subtitles
echo ""

# ============================================================================
# H.264 TEST FILES - Most comprehensive since it's the most common source
# ============================================================================
echo "--- H.264 Test Files (most common source format) ---"

# H.264 8-bit - Standard case (hardware decode everywhere)
generate "comp_h264_8bit_short.mp4" "8-bit High profile, ${SHORT_DURATION}s" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 23 -preset ultrafast \
    -c:a aac -b:a 128k \
    comp_h264_8bit_short.mp4

# H.264 8-bit - Medium duration
generate "comp_h264_8bit_medium.mp4" "8-bit High profile, ${MEDIUM_DURATION}s" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$MEDIUM_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$MEDIUM_DURATION" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 23 -preset ultrafast \
    -c:a aac -b:a 128k \
    comp_h264_8bit_medium.mp4

# H.264 8-bit - LONG DURATION (catch -38 errors near EOF)
generate "comp_h264_8bit_long.mp4" "8-bit High profile, ${LONG_DURATION}s (late-stage test)" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$LONG_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$LONG_DURATION" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 23 -preset ultrafast \
    -c:a aac -b:a 128k \
    comp_h264_8bit_long.mp4

# H.264 10-bit High10 (CRITICAL: Issue #56 - no hardware decode support)
generate "comp_h264_10bit_short.mkv" "10-bit High10 profile, ${SHORT_DURATION}s (Issue #56)" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v libx264 -pix_fmt yuv420p10le -profile:v high10 -crf 23 -preset ultrafast \
    -c:a aac -b:a 128k \
    comp_h264_10bit_short.mkv

# H.264 10-bit - LONG DURATION (the real Issue #56 test)
generate "comp_h264_10bit_long.mkv" "10-bit High10 profile, ${LONG_DURATION}s (Issue #56 CRITICAL)" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$LONG_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$LONG_DURATION" \
    -c:v libx264 -pix_fmt yuv420p10le -profile:v high10 -crf 23 -preset ultrafast \
    -c:a aac -b:a 128k \
    comp_h264_10bit_long.mkv

# H.264 with cover art (test -map 0:v:0 fix for issue #40)
# Create base video first, then add cover art
echo -n "  Generating comp_h264_coverart.mkv (8-bit with cover art (Issue #40))... "
ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v libx264 -pix_fmt yuv420p -crf 23 -preset ultrafast \
    -c:a aac -b:a 128k \
    comp_h264_coverart_temp.mkv 2>&1 && \
ffmpeg -hide_banner -loglevel error -y \
    -i comp_h264_coverart_temp.mkv \
    -i cover_art.jpg \
    -map 0 -map 1 \
    -c copy \
    -disposition:v:1 attached_pic \
    comp_h264_coverart.mkv 2>&1 && \
rm -f comp_h264_coverart_temp.mkv && echo "OK" || echo "FAILED"

# H.264 with multiple audio tracks
generate "comp_h264_multiaudio.mkv" "8-bit with multiple audio tracks" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -f lavfi -i "sine=f=880:d=$SHORT_DURATION" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 23 -preset ultrafast \
    -c:a aac -b:a 128k \
    comp_h264_multiaudio.mkv

# H.264 with subtitles
generate "comp_h264_subtitles.mkv" "8-bit with SRT subtitles" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -i test_subtitles.srt \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 23 -preset ultrafast \
    -c:a aac -b:a 128k \
    -c:s srt \
    comp_h264_subtitles.mkv

# H.264 60fps
generate "comp_h264_60fps.mp4" "8-bit 60fps" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=60" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 23 -preset ultrafast \
    -c:a aac -b:a 128k \
    comp_h264_60fps.mp4

# H.264 4K
generate "comp_h264_4k.mp4" "4K resolution" \
    -y -f lavfi -i "testsrc2=s=3840x2160:d=3:r=30" \
    -f lavfi -i "sine=f=440:d=3" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 28 -preset ultrafast \
    -c:a aac -b:a 128k \
    comp_h264_4k.mp4

# H.264 interlaced (edge case)
generate "comp_h264_interlaced.mp4" "interlaced content" \
    -y -f lavfi -i "testsrc2=s=1920x1080:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 23 -preset ultrafast \
    -flags +ilme+ildct -top 1 \
    -c:a aac -b:a 128k \
    comp_h264_interlaced.mp4

# H.264 very short (1 second - edge case)
generate "comp_h264_1sec.mp4" "1-second file" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=1:r=30" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 23 -preset ultrafast \
    comp_h264_1sec.mp4

echo ""

# ============================================================================
# HEVC TEST FILES - Target codec tests (should be skipped)
# ============================================================================
echo "--- HEVC Test Files (target codec - should be skipped) ---"

generate "comp_hevc_8bit_short.mkv" "8-bit Main profile, ${SHORT_DURATION}s" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v libx265 -pix_fmt yuv420p -profile:v main -crf 28 -preset ultrafast -x265-params log-level=error \
    -c:a aac -b:a 128k \
    comp_hevc_8bit_short.mkv

generate "comp_hevc_10bit_short.mkv" "10-bit Main10 profile, ${SHORT_DURATION}s" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v libx265 -pix_fmt yuv420p10le -profile:v main10 -crf 28 -preset ultrafast -x265-params log-level=error \
    -c:a aac -b:a 128k \
    comp_hevc_10bit_short.mkv

# HEVC with cover art
# Create base video first, then add cover art
echo -n "  Generating comp_hevc_coverart.mkv (8-bit with cover art)... "
ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v libx265 -pix_fmt yuv420p -crf 28 -preset ultrafast -x265-params log-level=error \
    -c:a aac -b:a 128k \
    comp_hevc_coverart_temp.mkv 2>&1 && \
ffmpeg -hide_banner -loglevel error -y \
    -i comp_hevc_coverart_temp.mkv \
    -i cover_art.jpg \
    -map 0 -map 1 \
    -c copy \
    -disposition:v:1 attached_pic \
    comp_hevc_coverart.mkv 2>&1 && \
rm -f comp_hevc_coverart_temp.mkv && echo "OK" || echo "FAILED"

echo ""

# ============================================================================
# AV1 TEST FILES - Modern efficient codec
# ============================================================================
echo "--- AV1 Test Files ---"

if ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libsvtav1; then
    generate "comp_av1_8bit_short.mkv" "8-bit SVT-AV1, ${SHORT_DURATION}s" \
        -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
        -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
        -c:v libsvtav1 -pix_fmt yuv420p -crf 40 -preset 8 \
        -c:a aac -b:a 128k \
        comp_av1_8bit_short.mkv

    generate "comp_av1_10bit_short.mkv" "10-bit SVT-AV1, ${SHORT_DURATION}s" \
        -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
        -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
        -c:v libsvtav1 -pix_fmt yuv420p10le -crf 40 -preset 8 \
        -c:a aac -b:a 128k \
        comp_av1_10bit_short.mkv
else
    echo "  SKIP: libsvtav1 not available"
fi

echo ""

# ============================================================================
# VP9 TEST FILES - WebM container
# ============================================================================
echo "--- VP9 Test Files ---"

if ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libvpx-vp9; then
    generate "comp_vp9_8bit_short.webm" "8-bit VP9, ${SHORT_DURATION}s" \
        -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
        -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
        -c:v libvpx-vp9 -pix_fmt yuv420p -crf 35 -b:v 0 -cpu-used 8 \
        -c:a libopus -b:a 128k \
        comp_vp9_8bit_short.webm

    generate "comp_vp9_10bit_short.webm" "10-bit VP9 Profile 2, ${SHORT_DURATION}s" \
        -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
        -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
        -c:v libvpx-vp9 -pix_fmt yuv420p10le -profile:v 2 -crf 35 -b:v 0 -cpu-used 8 \
        -c:a libopus -b:a 128k \
        comp_vp9_10bit_short.webm
else
    echo "  SKIP: libvpx-vp9 not available"
fi

echo ""

# ============================================================================
# MPEG-4 TEST FILES - Legacy format
# ============================================================================
echo "--- MPEG-4 Test Files (legacy format) ---"

generate "comp_mpeg4_short.avi" "MPEG-4 Simple Profile, ${SHORT_DURATION}s" \
    -y -f lavfi -i "testsrc2=s=640x480:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v mpeg4 -vtag xvid -q:v 5 \
    -c:a mp3 -b:a 128k \
    comp_mpeg4_short.avi

echo ""

# ============================================================================
# VC-1/WMV TEST FILES (Issue #32 - Windows VC1 Support)
# ============================================================================
echo "--- VC-1/WMV Test Files (Issue #32) ---"

# WMV2 codec in WMV container - commonly fails on hardware decode
# Note: True VC-1 requires wmv3dmo which isn't available on Linux
# Using wmv2 as the closest available codec
generate "comp_wmv2_short.wmv" "WMV2 ${SHORT_DURATION}s (Issue #32)" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v wmv2 -b:v 5M \
    -c:a wmav2 -b:a 128k \
    comp_wmv2_short.wmv

echo ""

# ============================================================================
# EDGE CASE TEST FILES
# ============================================================================
echo "--- Edge Case Test Files ---"

# Video-only (no audio)
generate "comp_videoonly.mp4" "Video only, no audio" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 23 -preset ultrafast \
    -an \
    comp_videoonly.mp4

# Very low resolution (may trigger minimum bitrate)
generate "comp_lowres.mp4" "Very low resolution 320x240" \
    -y -f lavfi -i "testsrc2=s=320x240:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v libx264 -pix_fmt yuv420p -profile:v baseline -crf 23 -preset ultrafast \
    -c:a aac -b:a 64k \
    comp_lowres.mp4

# Very high bitrate source
generate "comp_highbitrate.mp4" "High bitrate source (50Mbps)" \
    -y -f lavfi -i "testsrc2=s=1920x1080:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -b:v 50M -preset ultrafast \
    -c:a aac -b:a 192k \
    comp_highbitrate.mp4

# Non-standard resolution (odd dimensions)
generate "comp_oddres.mp4" "Odd resolution 1279x719" \
    -y -f lavfi -i "testsrc2=s=1279x719:d=$SHORT_DURATION:r=30" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high -crf 23 -preset ultrafast \
    comp_oddres.mp4

echo ""

# ============================================================================
# COMPLEX MULTI-TRACK TEST FILES
# ============================================================================
echo "--- Complex Multi-Track Test Files ---"

# Kitchen sink: video + cover art + multiple audio + subtitles
# Create base video first, then add cover art
echo -n "  Generating comp_kitchensink.mkv (Video + cover + 2 audio + subtitles)... "
ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "testsrc2=s=1280x720:d=$SHORT_DURATION:r=30" \
    -f lavfi -i "sine=f=440:d=$SHORT_DURATION" \
    -f lavfi -i "sine=f=880:d=$SHORT_DURATION" \
    -i test_subtitles.srt \
    -map 0:v -map 1:a -map 2:a -map 3:s \
    -c:v libx264 -pix_fmt yuv420p -crf 23 -preset ultrafast \
    -c:a aac -b:a 128k \
    -c:s srt \
    -metadata:s:a:0 language=eng -metadata:s:a:0 title="English" \
    -metadata:s:a:1 language=spa -metadata:s:a:1 title="Spanish" \
    -metadata:s:s:0 language=eng -metadata:s:s:0 title="English Subtitles" \
    comp_kitchensink_temp.mkv 2>&1 && \
ffmpeg -hide_banner -loglevel error -y \
    -i comp_kitchensink_temp.mkv \
    -i cover_art.jpg \
    -map 0 -map 1 \
    -c copy \
    -disposition:v:1 attached_pic \
    comp_kitchensink.mkv 2>&1 && \
rm -f comp_kitchensink_temp.mkv && echo "OK" || echo "FAILED"

echo ""
echo "=== Test Vector Generation Complete ==="
echo ""
echo "Generated files:"
ls -lah comp_*.{mp4,mkv,webm,avi} 2>/dev/null | awk '{print "  " $9 " (" $5 ")"}'
echo ""
echo "Total comprehensive test files:"
ls -1 comp_*.{mp4,mkv,webm,avi} 2>/dev/null | wc -l
echo ""
echo "To run comprehensive tests:"
echo "  go test ./internal/jobs/... -v -run Comprehensive -timeout 30m"
echo ""
