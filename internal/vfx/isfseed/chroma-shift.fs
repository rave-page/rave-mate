/*{
  "DESCRIPTION": "RGB split / chromatic aberration (amount 0..1 = 0..20 px)",
  "CREDIT": "rave-mate (MIT)",
  "ISFVSN": "2",
  "CATEGORIES": ["Glitch"],
  "INPUTS": [
    {"NAME": "inputImage", "TYPE": "image"},
    {"NAME": "amount", "TYPE": "float", "DEFAULT": 0.3}
  ]
}*/
void main() {
  vec2 off = vec2(amount * 20.0 / RENDERSIZE.x, 0.0);
  vec4 c = IMG_THIS_PIXEL(inputImage);
  float r = IMG_NORM_PIXEL(inputImage, isf_FragNormCoord + off).r;
  float b = IMG_NORM_PIXEL(inputImage, isf_FragNormCoord - off).b;
  gl_FragColor = vec4(r, c.g, b, c.a);
}
