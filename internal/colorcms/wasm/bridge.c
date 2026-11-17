#include <lcms2.h>
#include <math.h>

unsigned cms_rgb_profile(const double* c, unsigned transfer, double gamma, void* output, unsigned capacity) {
    cmsCIExyY white={c[0],c[1],1};
    cmsCIExyYTRIPLE primaries={{c[2],c[3],1},{c[4],c[5],1},{c[6],c[7],1}};
    cmsUInt16Number samples[4096];
    for(int i=0;i<4096;i++) {
        double x=(double)i/4095,y;
        if(transfer==13) y=x<=.04045?x/12.92:pow((x+.055)/1.055,2.4);
        else if(transfer==1 || transfer==6) y=x<.081?x/4.5:pow((x+.099)/1.099,1/.45);
        else if(transfer==8) y=x;
        else y=pow(x,gamma);
        samples[i]=(cmsUInt16Number)(y*65535+.5);
    }
    cmsToneCurve* curve=cmsBuildTabulatedToneCurve16(NULL,4096,samples);
    if(!curve) return 0;
    cmsToneCurve* curves[3]={curve,curve,curve};
    cmsHPROFILE profile=cmsCreateRGBProfile(&white,&primaries,curves);
    cmsFreeToneCurve(curve);
    if(!profile) return 0;
    cmsUInt32Number size=capacity;
    int ok=cmsSaveProfileToMem(profile,output,&size);
    cmsCloseProfile(profile);
    return ok?size:0;
}

int cms_convert(const void* profile, unsigned profile_size,
                const void* input, void* output, unsigned pixels,
                unsigned channels) {
    cmsHPROFILE src = cmsOpenProfileFromMem(profile, profile_size);
    if (!src) return 1;
    cmsColorSpaceSignature space = cmsGetColorSpace(src);
    if (!((space == cmsSigRgbData && channels == 3) ||
          (space == cmsSigGrayData && channels == 1) ||
          (space == cmsSigCmykData && channels == 4))) {
        cmsCloseProfile(src);
        return 2;
    }
    cmsHPROFILE dst = cmsCreate_sRGBProfile();
    if (!dst) { cmsCloseProfile(src); return 3; }
    cmsUInt32Number format = cmsFormatterForColorspaceOfProfile(src, 2, 0);
    cmsHTRANSFORM tr = cmsCreateTransform(src, format, dst, TYPE_RGB_16,
        INTENT_RELATIVE_COLORIMETRIC, cmsFLAGS_NOOPTIMIZE | cmsFLAGS_NOCACHE);
    if (!tr) { cmsCloseProfile(dst); cmsCloseProfile(src); return 4; }
    cmsDoTransform(tr, input, output, pixels);
    cmsDeleteTransform(tr);
    cmsCloseProfile(dst);
    cmsCloseProfile(src);
    return 0;
}
