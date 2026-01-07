#!/bin/sh
# Run this INSIDE the Shrinkray container to test different QSV approaches
# Usage: docker exec -it shrinkray /bin/sh
#        Then paste these commands one at a time

# Set your test file path
TEST_FILE="/media/gangs.mkv"

echo "=== QSV Encoding Tests ==="
echo "Testing file: $TEST_FILE"
echo ""

# Test 1: Current v1.4.1 approach (likely fails)
echo ">>> TEST 1: Current v1.4.1 approach (hwupload with format pipe)"
ffmpeg -init_hw_device qsv=hw -filter_hw_device hw -hwaccel qsv -hwaccel_output_format qsv \
  -i "$TEST_FILE" \
  -vf "format=nv12|qsv,hwupload=extra_hw_frames=64,scale_qsv=-2:'min(ih,1080)'" \
  -c:v hevc_qsv -global_quality 27 -preset medium \
  -frames:v 100 -y /tmp/test1.mkv \
  && echo "TEST 1: SUCCESS" || echo "TEST 1: FAILED"
echo ""

# Test 2: vpp_qsv instead of scale_qsv (handles both hw and sw frames)
echo ">>> TEST 2: vpp_qsv (handles both memory types)"
ffmpeg -hwaccel qsv -hwaccel_output_format qsv \
  -i "$TEST_FILE" \
  -vf "vpp_qsv=w=1920:h=1080" \
  -c:v hevc_qsv -global_quality 27 -preset medium \
  -frames:v 100 -y /tmp/test2.mkv \
  && echo "TEST 2: SUCCESS" || echo "TEST 2: FAILED"
echo ""

# Test 3: Full hardware pipeline, no hwupload, just scale_qsv
echo ">>> TEST 3: Full HW pipeline with scale_qsv (no hwupload)"
ffmpeg -hwaccel qsv -hwaccel_output_format qsv \
  -i "$TEST_FILE" \
  -vf "scale_qsv=-2:1080" \
  -c:v hevc_qsv -global_quality 27 -preset medium \
  -frames:v 100 -y /tmp/test3.mkv \
  && echo "TEST 3: SUCCESS" || echo "TEST 3: FAILED"
echo ""

# Test 4: Software decode + hwupload (for sw decode fallback case)
echo ">>> TEST 4: Software decode + hwupload"
ffmpeg -init_hw_device qsv=hw -filter_hw_device hw \
  -i "$TEST_FILE" \
  -vf "hwupload=extra_hw_frames=64,scale_qsv=-2:1080" \
  -c:v hevc_qsv -global_quality 27 -preset medium \
  -frames:v 100 -y /tmp/test4.mkv \
  && echo "TEST 4: SUCCESS" || echo "TEST 4: FAILED"
echo ""

# Test 5: Hybrid VAAPI decode + QSV encode
echo ">>> TEST 5: VAAPI decode + QSV encode (hybrid)"
ffmpeg -hwaccel vaapi -hwaccel_output_format vaapi -vaapi_device /dev/dri/renderD128 \
  -i "$TEST_FILE" \
  -vf "scale_vaapi=w=1920:h=1080,hwmap=derive_device=qsv,format=qsv" \
  -c:v hevc_qsv -global_quality 27 -preset medium \
  -frames:v 100 -y /tmp/test5.mkv \
  && echo "TEST 5: SUCCESS" || echo "TEST 5: FAILED"
echo ""

# Test 6: Just encode without scaling (simplest case)
echo ">>> TEST 6: QSV encode only, no scaling"
ffmpeg -hwaccel qsv -hwaccel_output_format qsv \
  -i "$TEST_FILE" \
  -c:v hevc_qsv -global_quality 27 -preset medium \
  -frames:v 100 -y /tmp/test6.mkv \
  && echo "TEST 6: SUCCESS" || echo "TEST 6: FAILED"
echo ""

echo "=== Tests Complete ==="
echo "Check which tests passed and we'll use that approach."
