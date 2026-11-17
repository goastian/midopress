#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <jxl/cms.h>
#include <jxl/decode.h>
#include <jxl/encode.h>

#define LIMIT_BYTES (128u << 20)
#define LIMIT_FRAMES 256u

static uint8_t *finish_encoder(JxlEncoder *enc, uint32_t *size) {
  size_t cap = 4096, used = 0;
  uint8_t *out = malloc(cap);
  if (!out) return NULL;
  for (;;) {
    uint8_t *next = out + used;
    size_t avail = cap - used;
    JxlEncoderStatus st = JxlEncoderProcessOutput(enc, &next, &avail);
    used = next - out;
    if (st == JXL_ENC_SUCCESS) { *size = used; return out; }
    if (st != JXL_ENC_NEED_MORE_OUTPUT || cap >= LIMIT_BYTES) break;
    cap *= 2;
    uint8_t *larger = realloc(out, cap);
    if (!larger) break;
    out = larger;
  }
  free(out);
  return NULL;
}

uint8_t *mp_recompress(const uint8_t *in, uint32_t size, uint32_t *out_size) {
  JxlEncoder *enc = JxlEncoderCreate(NULL);
  if (!enc) return NULL;
  uint8_t *out = NULL;
  JxlEncoderFrameSettings *fs = JxlEncoderFrameSettingsCreate(enc, NULL);
  if (fs && JxlEncoderStoreJPEGMetadata(enc, JXL_TRUE) == JXL_ENC_SUCCESS &&
      JxlEncoderFrameSettingsSetOption(fs, JXL_ENC_FRAME_SETTING_EFFORT, 7) == JXL_ENC_SUCCESS &&
      JxlEncoderAddJPEGFrame(fs, in, size) == JXL_ENC_SUCCESS) {
    JxlEncoderCloseInput(enc);
    out = finish_encoder(enc, out_size);
  }
  JxlEncoderDestroy(enc);
  return out;
}

uint8_t *mp_reconstruct(const uint8_t *in, uint32_t size, uint32_t *out_size) {
  JxlDecoder *dec = JxlDecoderCreate(NULL);
  if (!dec) return NULL;
  uint8_t *out = NULL;
  size_t cap = 4096, used = 0;
  int active = 0;
  JxlDecoderSubscribeEvents(dec, JXL_DEC_JPEG_RECONSTRUCTION | JXL_DEC_FULL_IMAGE);
  JxlDecoderSetInput(dec, in, size);
  JxlDecoderCloseInput(dec);
  for (;;) {
    JxlDecoderStatus st = JxlDecoderProcessInput(dec);
    if (st == JXL_DEC_JPEG_RECONSTRUCTION) {
      out = malloc(cap);
      if (!out || JxlDecoderSetJPEGBuffer(dec, out, cap) != JXL_DEC_SUCCESS) break;
      active = 1;
    } else if (st == JXL_DEC_JPEG_NEED_MORE_OUTPUT) {
      used = cap - JxlDecoderReleaseJPEGBuffer(dec);
      if (cap >= LIMIT_BYTES) break;
      cap *= 2;
      uint8_t *larger = realloc(out, cap);
      if (!larger) break;
      out = larger;
      if (JxlDecoderSetJPEGBuffer(dec, out + used, cap - used) != JXL_DEC_SUCCESS) break;
    } else if (st == JXL_DEC_SUCCESS && active) {
      *out_size = cap - JxlDecoderReleaseJPEGBuffer(dec);
      JxlDecoderDestroy(dec);
      return out;
    } else if (st != JXL_DEC_FULL_IMAGE) break;
  }
  JxlDecoderDestroy(dec);
  free(out);
  return NULL;
}

static int basic(const JxlBasicInfo *info, uint32_t *words, uint32_t max_pixels) {
  if (!info->xsize || !info->ysize || info->xsize > max_pixels / info->ysize) return 0;
  words[0] = info->xsize; words[1] = info->ysize;
  words[2] = info->orientation; words[3] = info->alpha_bits;
  words[4] = info->bits_per_sample; words[5] = info->exponent_bits_per_sample;
  words[6] = info->have_animation; words[7] = info->animation.num_loops;
  words[8] = info->animation.tps_numerator; words[9] = info->animation.tps_denominator;
  memcpy(words + 10, &info->intensity_target, 4);
  words[11] = info->num_extra_channels;
  return 1;
}

int mp_inspect(const uint8_t *in, uint32_t size, uint32_t *words, uint32_t max_pixels) {
  JxlDecoder *dec = JxlDecoderCreate(NULL);
  if (!dec) return 0;
  JxlDecoderSetKeepOrientation(dec, JXL_TRUE);
  JxlDecoderSubscribeEvents(dec, JXL_DEC_BASIC_INFO | JXL_DEC_COLOR_ENCODING | JXL_DEC_FRAME);
  JxlDecoderSetInput(dec, in, size);
  JxlDecoderCloseInput(dec);
  JxlBasicInfo info;
  uint32_t frames = 0;
  int have_basic = 0;
  for (;;) {
    JxlDecoderStatus st = JxlDecoderProcessInput(dec);
    if (st == JXL_DEC_BASIC_INFO) {
      if (JxlDecoderGetBasicInfo(dec, &info) != JXL_DEC_SUCCESS || !basic(&info, words, max_pixels)) break;
      have_basic = 1;
    } else if (st == JXL_DEC_COLOR_ENCODING) {
      size_t icc_size = 0;
      if (JxlDecoderGetICCProfileSize(dec, JXL_COLOR_PROFILE_TARGET_ORIGINAL, &icc_size) != JXL_DEC_SUCCESS || icc_size > (4u << 20)) break;
      uint8_t *icc = malloc(icc_size);
      if (icc_size && (!icc || JxlDecoderGetColorAsICCProfile(dec, JXL_COLOR_PROFILE_TARGET_ORIGINAL, icc, icc_size) != JXL_DEC_SUCCESS)) { free(icc); break; }
      words[12] = (uint32_t)(uintptr_t)icc; words[13] = icc_size;
      JxlColorEncoding color;
      if (JxlDecoderGetColorAsEncodedProfile(dec, JXL_COLOR_PROFILE_TARGET_ORIGINAL, &color) == JXL_DEC_SUCCESS) {
        words[16] = color.color_space; words[17] = color.primaries; words[18] = color.transfer_function; words[19] = color.white_point;
      }
    } else if (st == JXL_DEC_FRAME) {
      if (!have_basic || frames >= LIMIT_FRAMES ||
          (uint64_t)(frames + 1) * info.xsize * info.ysize > max_pixels) break;
      JxlFrameHeader frame;
      if (JxlDecoderGetFrameHeader(dec, &frame) != JXL_DEC_SUCCESS) break;
      words[24 + frames++] = frame.duration;
      JxlDecoderSkipCurrentFrame(dec);
    } else if (st == JXL_DEC_SUCCESS && have_basic && frames) {
      words[14] = frames;
      JxlDecoderDestroy(dec);
      return 1;
    } else break;
  }
  free((void *)(uintptr_t)words[12]); words[12] = 0; words[13] = 0;
  JxlDecoderDestroy(dec);
  return 0;
}

uint8_t *mp_decode(const uint8_t *in, uint32_t size, uint32_t *words, uint32_t max_pixels, uint32_t all) {
  JxlDecoder *dec = JxlDecoderCreate(NULL);
  if (!dec) return NULL;
  JxlDecoderSetCms(dec, *JxlGetDefaultCms());
  JxlDecoderSetUnpremultiplyAlpha(dec, JXL_TRUE);
  JxlDecoderSetDesiredIntensityTarget(dec, 203.0f);
  JxlDecoderSubscribeEvents(dec, JXL_DEC_BASIC_INFO | JXL_DEC_COLOR_ENCODING | JXL_DEC_FRAME | JXL_DEC_FULL_IMAGE);
  JxlDecoderSetInput(dec, in, size);
  JxlDecoderCloseInput(dec);
  JxlBasicInfo info;
  JxlPixelFormat format = {4, JXL_TYPE_UINT16, JXL_BIG_ENDIAN, 0};
  uint8_t *pixels = NULL;
  size_t frame_bytes = 0;
  uint32_t frames = 0;
  for (;;) {
    JxlDecoderStatus st = JxlDecoderProcessInput(dec);
    if (st == JXL_DEC_BASIC_INFO) {
      if (JxlDecoderGetBasicInfo(dec, &info) != JXL_DEC_SUCCESS || !basic(&info, words, max_pixels)) break;
      if (info.num_extra_channels > (info.alpha_bits ? 1u : 0u)) break;
    } else if (st == JXL_DEC_COLOR_ENCODING) {
      JxlColorEncoding srgb;
      JxlColorEncodingSetToSRGB(&srgb, JXL_FALSE);
      if (JxlDecoderSetOutputColorProfile(dec, &srgb, NULL, 0) != JXL_DEC_SUCCESS) break;
    } else if (st == JXL_DEC_FRAME) {
      if (frames >= LIMIT_FRAMES) break;
      JxlFrameHeader frame;
      if (JxlDecoderGetFrameHeader(dec, &frame) != JXL_DEC_SUCCESS) break;
      words[24 + frames] = frame.duration;
    } else if (st == JXL_DEC_NEED_IMAGE_OUT_BUFFER) {
      size_t need;
      if (JxlDecoderImageOutBufferSize(dec, &format, &need) != JXL_DEC_SUCCESS || need > LIMIT_BYTES ||
          (uint64_t)(frames + 1) * info.xsize * info.ysize > max_pixels || (uint64_t)(frames + 1) * need > LIMIT_BYTES) break;
      frame_bytes = need;
      uint8_t *larger = realloc(pixels, (frames + 1) * frame_bytes);
      if (!larger) break;
      pixels = larger;
      if (JxlDecoderSetImageOutBuffer(dec, &format, pixels + frames * frame_bytes, frame_bytes) != JXL_DEC_SUCCESS) break;
    } else if (st == JXL_DEC_FULL_IMAGE) {
      frames++;
      if (!all) { words[14] = frames; words[15] = frame_bytes * frames; JxlDecoderDestroy(dec); return pixels; }
    } else if (st == JXL_DEC_SUCCESS && frames) {
      words[14] = frames; words[15] = frame_bytes * frames;
      JxlDecoderDestroy(dec);
      return pixels;
    } else break;
  }
  free(pixels);
  JxlDecoderDestroy(dec);
  return NULL;
}

uint8_t *mp_encode(const uint8_t *pixels, uint32_t width, uint32_t height, uint32_t depth,
                   uint32_t quality, uint32_t effort, uint32_t count, const uint32_t *delays,
                   uint32_t loops, uint32_t orientation, const uint8_t *icc, uint32_t icc_size,
                   uint32_t primaries, uint32_t transfer, uint32_t *out_size) {
  if (!width || !height || !count || count > LIMIT_FRAMES || (depth != 8 && depth != 16)) return NULL;
  JxlEncoder *enc = JxlEncoderCreate(NULL);
  if (!enc) return NULL;
  uint8_t *out = NULL;
  JxlBasicInfo info;
  JxlEncoderInitBasicInfo(&info);
  info.xsize = width; info.ysize = height; info.bits_per_sample = depth;
  info.alpha_bits = depth; info.num_extra_channels = 1;
  info.orientation = orientation ? orientation : JXL_ORIENT_IDENTITY;
  info.uses_original_profile = quality == 100;
  info.have_animation = count > 1;
  info.animation.tps_numerator = 1000; info.animation.tps_denominator = 1; info.animation.num_loops = loops;
  if (transfer == JXL_TRANSFER_FUNCTION_PQ || transfer == JXL_TRANSFER_FUNCTION_HLG) info.intensity_target = 1000;
  if (JxlEncoderSetBasicInfo(enc, &info) != JXL_ENC_SUCCESS) goto done;
  JxlColorEncoding color;
  JxlColorEncodingSetToSRGB(&color, JXL_FALSE);
  if (primaries) color.primaries = primaries;
  if (transfer) color.transfer_function = transfer;
  if (icc_size) {
    if (JxlEncoderSetICCProfile(enc, icc, icc_size) != JXL_ENC_SUCCESS) goto done;
  } else if (JxlEncoderSetColorEncoding(enc, &color) != JXL_ENC_SUCCESS) goto done;
  JxlEncoderFrameSettings *fs = JxlEncoderFrameSettingsCreate(enc, NULL);
  if (!fs || JxlEncoderSetFrameDistance(fs, JxlEncoderDistanceFromQuality(quality)) != JXL_ENC_SUCCESS ||
      JxlEncoderFrameSettingsSetOption(fs, JXL_ENC_FRAME_SETTING_EFFORT, effort) != JXL_ENC_SUCCESS) goto done;
  if (quality == 100 && JxlEncoderSetFrameLossless(fs, JXL_TRUE) != JXL_ENC_SUCCESS) goto done;
  JxlPixelFormat format = {4, depth == 16 ? JXL_TYPE_UINT16 : JXL_TYPE_UINT8,
                           depth == 16 ? JXL_BIG_ENDIAN : JXL_NATIVE_ENDIAN, 0};
  size_t stride = (size_t)width * height * 4 * (depth / 8);
  for (uint32_t i = 0; i < count; i++) {
    if (count > 1) {
      JxlFrameHeader frame;
      JxlEncoderInitFrameHeader(&frame);
      frame.duration = delays[i];
      if (JxlEncoderSetFrameHeader(fs, &frame) != JXL_ENC_SUCCESS) goto done;
    }
    if (JxlEncoderAddImageFrame(fs, &format, pixels + i * stride, stride) != JXL_ENC_SUCCESS) goto done;
  }
  JxlEncoderCloseInput(enc);
  out = finish_encoder(enc, out_size);
done:
  JxlEncoderDestroy(enc);
  return out;
}
