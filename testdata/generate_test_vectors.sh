#!/bin/bash
# Generate comprehensive test video files for codec detection testing
#
# This script creates synthetic test videos covering all codec/profile/bit-depth
# combinations needed to test the proactive codec detection and software decode
# fallback logic in Shrinkray.
#
# Usage: ./generate_test_vectors.sh
#
# Requirements: ffmpeg with libx264, libx265, libsvtav1, libvpx-vp9

set -e

TESTDATA_DIR="$(dirname "$0")"
cd "$TESTDATA_DIR"

echo "=== Generating Test Vectors for Codec Detection ==="
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

echo "--- H.264 Test Files ---"

# 1. H.264 8-bit High (standard case - should hardware decode)
generate "test_h264_8bit_high.mp4" "8-bit High profile" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=5" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high \
    -crf 23 -preset ultrafast \
    test_h264_8bit_high.mp4

# 2. H.264 10-bit High 10 (CRITICAL: proactive software decode - Issue #56)
generate "test_h264_10bit_high10.mkv" "10-bit High10 profile (Issue #56)" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=5" \
    -c:v libx264 -pix_fmt yuv420p10le -profile:v high10 \
    -crf 23 -preset ultrafast \
    test_h264_10bit_high10.mkv

# 3. Very short file (1 second - frame count edge case)
generate "test_h264_short.mp4" "1-second short file" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=1" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high \
    -crf 23 -preset ultrafast \
    test_h264_short.mp4

# 4. High framerate 60fps
generate "test_h264_60fps.mp4" "60fps high framerate" \
    -y -f lavfi -i "testsrc2=s=1280x720:r=60:d=5" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high \
    -crf 23 -preset ultrafast \
    test_h264_60fps.mp4

# 5. 4K resolution
generate "test_h264_4k.mp4" "4K resolution" \
    -y -f lavfi -i "testsrc2=s=3840x2160:d=3" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high \
    -crf 28 -preset ultrafast \
    test_h264_4k.mp4

# 6. Low bitrate (may produce larger output after transcode)
generate "test_h264_lowbitrate.mp4" "low bitrate source" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=5" \
    -c:v libx264 -pix_fmt yuv420p -profile:v high \
    -crf 40 -preset ultrafast \
    test_h264_lowbitrate.mp4

echo ""
echo "--- HEVC Test Files ---"

# 7. HEVC 8-bit Main (should be skipped - already HEVC)
generate "test_hevc_8bit_main.mkv" "8-bit Main profile" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=5" \
    -c:v libx265 -pix_fmt yuv420p -profile:v main \
    -crf 28 -preset ultrafast -x265-params log-level=error \
    test_hevc_8bit_main.mkv

# 8. HEVC 10-bit Main 10 (should be skipped - already HEVC)
generate "test_hevc_10bit_main10.mkv" "10-bit Main10 profile" \
    -y -f lavfi -i "testsrc2=s=1280x720:d=5" \
    -c:v libx265 -pix_fmt yuv420p10le -profile:v main10 \
    -crf 28 -preset ultrafast -x265-params log-level=error \
    test_hevc_10bit_main10.mkv

echo ""
echo "--- AV1 Test Files ---"

# 9. AV1 8-bit (should be skipped if AV1 preset, else transcode)
if ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libsvtav1; then
    generate "test_av1_8bit.mkv" "8-bit SVT-AV1" \
        -y -f lavfi -i "testsrc2=s=1280x720:d=5" \
        -c:v libsvtav1 -pix_fmt yuv420p \
        -crf 40 -preset 8 \
        test_av1_8bit.mkv
else
    echo "  SKIP: libsvtav1 not available"
fi

echo ""
echo "--- VP9 Test Files ---"

# 10. VP9 8-bit (hardware decode, transcode to HEVC)
if ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libvpx-vp9; then
    generate "test_vp9_8bit.webm" "8-bit VP9" \
        -y -f lavfi -i "testsrc2=s=1280x720:d=5" \
        -c:v libvpx-vp9 -pix_fmt yuv420p \
        -crf 35 -b:v 0 -cpu-used 8 \
        test_vp9_8bit.webm

    # 11. VP9 10-bit Profile 2 (hardware decode supported)
    generate "test_vp9_10bit.webm" "10-bit VP9 Profile 2" \
        -y -f lavfi -i "testsrc2=s=1280x720:d=5" \
        -c:v libvpx-vp9 -pix_fmt yuv420p10le -profile:v 2 \
        -crf 35 -b:v 0 -cpu-used 8 \
        test_vp9_10bit.webm
else
    echo "  SKIP: libvpx-vp9 not available"
fi

echo ""
echo "--- MPEG-4 Test Files ---"

# 12. MPEG-4 Simple Profile (hardware decode on some platforms)
generate "test_mpeg4_simple.avi" "Simple Profile (XviD)" \
    -y -f lavfi -i "testsrc2=s=640x480:d=5" \
    -c:v mpeg4 -vtag xvid -q:v 5 \
    test_mpeg4_simple.avi

echo ""
echo "=== Test Vector Generation Complete ==="
echo ""
echo "Generated files:"
ls -lah test_*.{mp4,mkv,webm,avi} 2>/dev/null | awk '{print "  " $9 " (" $5 ")"}'
echo ""
echo "To run tests:"
echo "  go test ./internal/jobs/... -v -run CodecDetection -timeout 10m"
