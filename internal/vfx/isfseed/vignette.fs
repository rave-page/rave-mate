/*{
  "DESCRIPTION": "Darkened corners; strength sets depth, softness the falloff",
  "CREDIT": "rave-mate (MIT)",
  "ISFVSN": "2",
  "CATEGORIES": ["Stylize"],
  "INPUTS": [
    {"NAME": "inputImage", "TYPE": "image"},
    {"NAME": "strength", "TYPE": "float", "DEFAULT": 0.5},
    {"NAME": "softness", "TYPE": "float", "DEFAULT": 0.5}
  ]
}*/
void main() {
  vec4 c = IMG_THIS_PIXEL(inputImage);
  vec2 p = isf_FragNormCoord - 0.5;
  float d = length(p) * 1.41421356;
  float edge = 1.05 - softness * 0.55;
  float v = 1.0 - strength * smoothstep(edge - 0.6, edge, d);
  gl_FragColor = vec4(c.rgb * v, c.a);
}
