/*{
  "DESCRIPTION": "Mosaic blocks (amount 0..1 = 1..64 px cells)",
  "CREDIT": "rave-mate (MIT)",
  "ISFVSN": "2",
  "CATEGORIES": ["Stylize"],
  "INPUTS": [
    {"NAME": "inputImage", "TYPE": "image"},
    {"NAME": "amount", "TYPE": "float", "DEFAULT": 0.25}
  ]
}*/
void main() {
  float px = 1.0 + amount * 63.0;
  vec2 cell = (floor(isf_FragNormCoord * RENDERSIZE / px) + 0.5) * px;
  gl_FragColor = IMG_NORM_PIXEL(inputImage, cell / RENDERSIZE);
}
