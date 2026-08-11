/*{
  "DESCRIPTION": "CRT-style horizontal scanlines",
  "CREDIT": "rave-mate (MIT)",
  "ISFVSN": "2",
  "CATEGORIES": ["Stylize"],
  "INPUTS": [
    {"NAME": "inputImage", "TYPE": "image"},
    {"NAME": "intensity", "TYPE": "float", "DEFAULT": 0.4},
    {"NAME": "density", "TYPE": "float", "DEFAULT": 0.5}
  ]
}*/
void main() {
  vec4 c = IMG_THIS_PIXEL(inputImage);
  float lines = 100.0 + density * 400.0;
  float s = 0.5 + 0.5 * sin(isf_FragNormCoord.y * lines * 6.2831853);
  gl_FragColor = vec4(c.rgb * mix(1.0, s, intensity), c.a);
}
