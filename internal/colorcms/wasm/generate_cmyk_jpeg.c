/* Deterministic, redistributable CMYK JPEG used by the engine integration test.
 * cc generate_cmyk_jpeg.c -ljpeg -o generator
 * generator testdata/cmyk-lut.icc ../../img/testdata/cmyk-lut.jpg */
#include <stdio.h>
#include <stdlib.h>
#include <jpeglib.h>

int main(int argc, char** argv) {
    if (argc != 3) return 1;
    FILE* p = fopen(argv[1], "rb");
    if (!p) return 2;
    fseek(p, 0, SEEK_END);
    long size = ftell(p);
    rewind(p);
    if (size <= 0 || size > 65000) return 3;
    unsigned char* marker = malloc(size + 14);
    const unsigned char header[14] = "ICC_PROFILE\0\1\1";
    for (int i = 0; i < 14; i++) marker[i] = header[i];
    if (fread(marker + 14, 1, size, p) != (size_t)size) return 4;
    fclose(p);
    FILE* out = fopen(argv[2], "wb");
    if (!out) return 5;
    struct jpeg_compress_struct c;
    struct jpeg_error_mgr err;
    c.err = jpeg_std_error(&err);
    jpeg_create_compress(&c);
    jpeg_stdio_dest(&c, out);
    c.image_width = 192; c.image_height = 128;
    c.input_components = 4; c.in_color_space = JCS_CMYK;
    jpeg_set_defaults(&c);
    jpeg_set_quality(&c, 100, 1);
    jpeg_start_compress(&c, 1);
    jpeg_write_marker(&c, JPEG_APP0 + 2, marker, size + 14);
    unsigned char row[192 * 4];
    while (c.next_scanline < c.image_height) {
        for (int x = 0; x < 192; x++) {
            int y = c.next_scanline;
            row[x * 4 + 0] = 255 - ((x * 3 + y) % 200);
            row[x * 4 + 1] = 255 - ((x + y * 2) % 200);
            row[x * 4 + 2] = 255 - ((x * 2 + y * 3) % 200);
            row[x * 4 + 3] = 255 - ((x + y) % 64);
        }
        JSAMPROW rows[1] = {row};
        jpeg_write_scanlines(&c, rows, 1);
    }
    jpeg_finish_compress(&c);
    jpeg_destroy_compress(&c);
    fclose(out);
    free(marker);
    return 0;
}
