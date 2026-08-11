/*{
  "DESCRIPTION": "Reduce colors to discrete steps (levels 0..1 = 2..16 steps)",
  "CREDIT": "rave-mate (MIT)",
  "ISFVSN": "2",
  "CATEGORIES": ["Stylize"],
  "INPUTS": [
    {"NAME": "inputImage", "TYPE": "image"},
    {"NAME": "levels", "TYPE": "float", "DEFAULT": 0.35}
  ]
}*/
void main() {
  vec4 c = IMG_THIS_PIXEL(inputImage);
  float steps = 2.0 + floor(levels * 14.0);
  c.rgb = floor(c.rgb * steps + 0.5) / steps;
  gl_FragColor = c;
}
