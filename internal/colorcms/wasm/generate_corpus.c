#include <lcms2.h>
#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include <math.h>

static cmsHTRANSFORM toXYZ;
static int sample(const cmsUInt16Number in[], cmsUInt16Number out[], void* channels) {
    cmsUInt16Number rgb[3];
    if ((intptr_t) channels == 4) {
        for (int i=0;i<3;i++) rgb[i]=(cmsUInt16Number)(((uint32_t)(65535-in[i])*(65535-in[3])+32767)/65535);
    } else for (int i=0;i<3;i++) rgb[i]=in[i];
    cmsDoTransform(toXYZ,rgb,out,1);
    return 1;
}

static cmsHPROFILE lut(int channels) {
    cmsHPROFILE p=cmsCreateProfilePlaceholder(NULL);
    cmsSetProfileVersion(p,2.1);
    cmsSetDeviceClass(p,cmsSigInputClass);
    cmsSetColorSpace(p,channels==4?cmsSigCmykData:cmsSigRgbData);
    cmsSetPCS(p,cmsSigXYZData);
    cmsWriteTag(p,cmsSigMediaWhitePointTag,cmsD50_XYZ());
    cmsMLU* desc=cmsMLUalloc(NULL,1);
    cmsMLUsetASCII(desc,"en","US","MidoPress generated test LUT");
    cmsWriteTag(p,cmsSigProfileDescriptionTag,desc); cmsMLUfree(desc);
    cmsPipeline* pipeline=cmsPipelineAlloc(NULL,channels,3);
    cmsPipelineInsertStage(pipeline,cmsAT_END,cmsStageAllocToneCurves(NULL,channels,NULL));
    cmsStage* clut=cmsStageAllocCLut16bit(NULL,3,channels,3,NULL);
    cmsStageSampleCLut16bit(clut,sample,(void*)(intptr_t)channels,0);
    cmsPipelineInsertStage(pipeline,cmsAT_END,clut);
    cmsPipelineInsertStage(pipeline,cmsAT_END,cmsStageAllocToneCurves(NULL,3,NULL));
    cmsWriteTag(p,cmsSigAToB0Tag,pipeline);
    cmsPipelineFree(pipeline);
    return p;
}

static void save(const char* dir,const char* name,cmsHPROFILE p,int channels) {
    char path[1024];
    snprintf(path,sizeof(path),"%s/%s.icc",dir,name);
    if (!cmsSaveProfileToFile(p,path)) exit(2);
    cmsUInt16Number input[8*4],output[8*3];
    for (int i=0;i<8;i++) for(int c=0;c<channels;c++) input[i*channels+c]=(cmsUInt16Number)(((i*59+c*83)%256)*257);
    for (int c=0;c<channels;c++) { input[c]=0; input[channels+c]=65535; }
    cmsHPROFILE srgb=cmsCreate_sRGBProfile();
    cmsHTRANSFORM tr=cmsCreateTransform(p,cmsFormatterForColorspaceOfProfile(p,2,0),srgb,TYPE_RGB_16,INTENT_RELATIVE_COLORIMETRIC,cmsFLAGS_NOOPTIMIZE|cmsFLAGS_NOCACHE);
    if (!tr) exit(3);
    cmsDoTransform(tr,input,output,8);
    snprintf(path,sizeof(path),"%s/%s.input16",dir,name);
    FILE* f=fopen(path,"wb");
    for(int i=0;i<8*channels;i++) { fputc(input[i]&255,f); fputc(input[i]>>8,f); } fclose(f);
    snprintf(path,sizeof(path),"%s/%s.srgb16",dir,name);
    f=fopen(path,"wb");
    for(int i=0;i<8*3;i++) { fputc(output[i]>>8,f); fputc(output[i]&255,f); } fclose(f);
    cmsDeleteTransform(tr); cmsCloseProfile(srgb); cmsCloseProfile(p);
}

int main(int argc,char**argv) {
    if(argc!=2) return 1;
    cmsHPROFILE srgb=cmsCreate_sRGBProfile(),xyz=cmsCreateXYZProfile();
    toXYZ=cmsCreateTransform(srgb,TYPE_RGB_16,xyz,TYPE_XYZ_16,INTENT_RELATIVE_COLORIMETRIC,cmsFLAGS_NOOPTIMIZE|cmsFLAGS_NOCACHE);
    cmsCIExyY d65={0.3127,0.3290,1},d50={0.3457,0.3585,1};
    cmsCIExyYTRIPLE adobe={{.64,.33,1},{.21,.71,1},{.15,.06,1}};
    cmsCIExyYTRIPLE p3={{.68,.32,1},{.265,.69,1},{.15,.06,1}};
    cmsCIExyYTRIPLE pro={{.7347,.2653,1},{.1596,.8404,1},{.0366,.0001,1}};
    cmsToneCurve *gamma=cmsBuildGamma(NULL,2.2),*curves[3]={gamma,gamma,gamma};
    save(argv[1],"adobe-rgb",cmsCreateRGBProfile(&d65,&adobe,curves),3);
    curves[0]=curves[1]=curves[2]=(cmsToneCurve*)cmsReadTag(srgb,cmsSigRedTRCTag);
    save(argv[1],"display-p3",cmsCreateRGBProfile(&d65,&p3,curves),3);
    cmsFreeToneCurve(gamma); gamma=cmsBuildGamma(NULL,1.8);
    curves[0]=curves[1]=curves[2]=gamma;
    save(argv[1],"prophoto",cmsCreateRGBProfile(&d50,&pro,curves),3);
    save(argv[1],"gray18",cmsCreateGrayProfile(&d50,gamma),1);
    save(argv[1],"rgb-lut",lut(3),3);
    save(argv[1],"cmyk-lut",lut(4),4);
    cmsFreeToneCurve(gamma); cmsDeleteTransform(toXYZ); cmsCloseProfile(srgb); cmsCloseProfile(xyz);
    return 0;
}
